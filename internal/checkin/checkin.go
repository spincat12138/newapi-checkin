package checkin

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newapi-checkin/internal/config"
)

const quotaPerUSD = 500000.0

// Run performs check-in for a single site configuration.
func Run(ctx context.Context, site config.Site) (result Result) {
	result.Site = site.Name
	defer func() {
		result.CheckedAt = time.Now()
	}()

	if !supportsCheckin(site.Platform) {
		result.Error = fmt.Sprintf("unsupported platform: %s", site.Platform)
		return result
	}

	credential := authCredential{
		Type:  site.CredentialType,
		Value: site.AccessToken,
	}
	userID := site.UserID

	switch site.CredentialType {
	case config.CredentialSessionCookie:
		credential.Value = site.SessionCookie
	case config.CredentialUsernamePassword:
		loggedInCredential, uid, err := login(ctx, site)
		if err != nil {
			result.Error = fmt.Sprintf("login failed: %v", err)
			return result
		}
		credential = loggedInCredential
		if userID <= 0 {
			userID = uid
		}
	}

	if strings.TrimSpace(credential.Value) == "" {
		result.Error = "credential is required"
		return result
	}

	// Many NewAPI deployments require New-Api-User and the value must match
	// the logged-in account. Prefer config value, otherwise try lightweight discovery.
	if userID <= 0 {
		if discovered, err := discoverUserID(ctx, site, credential); err == nil && discovered > 0 {
			userID = discovered
		}
	}

	if userID <= 0 {
		// Confirm whether this site requires the header before failing hard.
		if requires, sample := siteRequiresUserIDHeader(ctx, site, credential); requires {
			result.Error = fmt.Sprintf(
				"站点要求 New-Api-User 请求头，但未能自动识别用户 ID（%s）。请在 config.yaml 填写 user_id。获取方法：浏览器登录站点后打开 F12 → Network，查看任意 /api/user/* 请求头中的 New-Api-User 值",
				sample,
			)
			return result
		}
	}

	checkinResult, usedUserID, err := checkinSite(ctx, site, credential, userID)
	if err != nil {
		populateTotalBalance(ctx, site, credential, userID, &result)
		if isUserIDHeaderError(err, nil) {
			result.Error = fmt.Sprintf(
				"%v；请检查 config.yaml 中的 user_id 是否与当前凭证对应账号一致（浏览器 F12 请求头 New-Api-User）",
				err,
			)
			return result
		}
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.Message = checkinResult.Message
	if usedUserID > 0 {
		if result.Message == "" {
			result.Message = "checkin success"
		}
		result.Message = fmt.Sprintf("%s (user_id=%d)", result.Message, usedUserID)
	}
	result.Reward = checkinResult.Reward
	if rewardUSD, ok := quotaValueToUSD(checkinResult.Reward); ok {
		result.RewardUSD = float64Ptr(rewardUSD)
	} else if isAlreadyCheckedInMessage(checkinResult.Message) {
		result.RewardUSD = float64Ptr(0)
	}
	populateTotalBalance(ctx, site, credential, firstPositiveInt(usedUserID, userID), &result)
	return result
}

type checkinOK struct {
	Message string
	Reward  string
}

func login(ctx context.Context, site config.Site) (authCredential, int, error) {
	payload, err := requestJSON(ctx, site, http.MethodPost, buildSiteURL(site.BaseURL, "/api/user/login"), map[string]any{
		"username": site.Username,
		"password": site.Password,
	}, nil)
	if err != nil {
		return authCredential{}, 0, err
	}
	if !jsonBool(payload["success"]) {
		msg := firstNonEmptyString(extractResponseMessage(payload), "login failed")
		return authCredential{}, 0, fmt.Errorf("%s", msg)
	}

	credential := extractLoginCredential(payload)
	userID := extractUserID(payload)
	if credential.Value == "" {
		return authCredential{}, userID, fmt.Errorf("login succeeded but no credential returned")
	}
	return credential, userID, nil
}

func extractLoginCredential(payload map[string]any) authCredential {
	candidates := []authCredential{
		{Type: config.CredentialAccessToken, Value: jsonString(nestedValue(payload, "data", "token"))},
		{Type: config.CredentialAccessToken, Value: jsonString(nestedValue(payload, "data", "access_token"))},
		{Type: config.CredentialAccessToken, Value: jsonString(nestedValue(payload, "data", "accessToken"))},
		{Type: config.CredentialSessionCookie, Value: jsonString(nestedValue(payload, "data", "session"))},
		{Type: config.CredentialAccessToken, Value: jsonString(payload["token"])},
		{Type: config.CredentialAccessToken, Value: jsonString(payload["access_token"])},
		{Type: config.CredentialSessionCookie, Value: jsonString(payload["session"])},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.Value) != "" {
			return candidate
		}
	}
	return authCredential{}
}

func checkinSite(ctx context.Context, site config.Site, credential authCredential, userID int) (*checkinOK, int, error) {
	authVariants := buildAuthHeaderVariants(credential)
	if len(authVariants) == 0 {
		return nil, 0, fmt.Errorf("credential is required")
	}

	// Only try configured/discovered user id (+ no-header fallback).
	// Do NOT brute-force large user id ranges; NewAPI will always reject mismatches.
	userIDs := make([]int, 0, 2)
	if userID > 0 {
		userIDs = append(userIDs, userID)
	}
	userIDs = append(userIDs, 0)

	methods := []string{http.MethodPost, http.MethodGet}
	var lastErr error

	for _, auth := range authVariants {
		for _, uid := range userIDs {
			headers := mergeHeaders(auth, managedUserIDHeaders(uid))
			for _, method := range methods {
				result, err := doRequest(ctx, site, method, buildSiteURL(site.BaseURL, "/api/user/checkin"), nil, headers)
				if err != nil {
					lastErr = err
					continue
				}
				if ok, message, reward := interpretCheckinPayload(result.Payload); ok {
					return &checkinOK{Message: message, Reward: reward}, uid, nil
				}
				message := firstNonEmptyString(extractResponseMessage(result.Payload), "checkin failed")
				lastErr = fmt.Errorf("%s", message)
			}
		}
	}

	if lastErr != nil {
		return nil, 0, lastErr
	}
	return nil, 0, fmt.Errorf("checkin failed")
}

func interpretCheckinPayload(payload map[string]any) (bool, string, string) {
	if payload == nil {
		return false, "", ""
	}
	message := firstNonEmptyString(extractResponseMessage(payload), "checkin success")
	if jsonBool(payload["success"]) || isAlreadyCheckedInMessage(message) {
		reward := firstNonEmptyString(
			jsonString(nestedValue(payload, "data", "quota_awarded")),
			jsonString(nestedValue(payload, "data", "quotaAwarded")),
			jsonString(nestedValue(payload, "data", "reward")),
			jsonString(nestedValue(payload, "data", "quota")),
			jsonString(nestedValue(payload, "data", "amount")),
		)
		return true, message, reward
	}
	return false, message, ""
}

func populateTotalBalance(ctx context.Context, site config.Site, credential authCredential, userID int, result *Result) {
	if result == nil || strings.TrimSpace(credential.Value) == "" {
		return
	}

	balance, err := fetchAccountBalance(ctx, site, credential, userID)
	if err != nil {
		result.BalanceError = err.Error()
		return
	}
	result.TotalBalanceUSD = float64Ptr(balance)
}

func fetchAccountBalance(ctx context.Context, site config.Site, credential authCredential, userID int) (float64, error) {
	authVariants := buildAuthHeaderVariants(credential)
	if len(authVariants) == 0 {
		return 0, fmt.Errorf("credential is required")
	}

	userIDs := []int{0}
	if userID > 0 {
		userIDs = []int{userID, 0}
	}

	var lastErr error
	for _, auth := range authVariants {
		for _, uid := range userIDs {
			headers := mergeHeaders(auth, managedUserIDHeaders(uid))
			result, err := doRequest(ctx, site, http.MethodGet, buildSiteURL(site.BaseURL, "/api/user/self"), nil, headers)
			if err != nil {
				lastErr = err
				continue
			}
			if success, exists := result.Payload["success"]; exists && !jsonBool(success) {
				lastErr = fmt.Errorf("%s", firstNonEmptyString(extractResponseMessage(result.Payload), "fetch balance failed"))
				continue
			}

			balance, err := extractBalanceUSD(result.Payload, site.Platform)
			if err == nil {
				return balance, nil
			}
			lastErr = err
		}
	}

	if lastErr != nil {
		return 0, lastErr
	}
	return 0, fmt.Errorf("balance unavailable")
}

func extractBalanceUSD(payload map[string]any, platform string) (float64, error) {
	if payload == nil {
		return 0, fmt.Errorf("balance response is empty")
	}

	data := payload
	if nested, ok := payload["data"].(map[string]any); ok {
		data = nested
	}

	quota, ok := numericValue(data["quota"])
	if !ok {
		return 0, fmt.Errorf("balance response missing numeric quota")
	}

	if platform == PlatformOneAPI {
		if used, exists := data["used_quota"]; exists {
			usedQuota, ok := numericValue(used)
			if !ok {
				return 0, fmt.Errorf("balance response contains invalid used_quota")
			}
			quota -= usedQuota
			if quota < 0 {
				quota = 0
			}
		}
	}

	return quota / quotaPerUSD, nil
}

func quotaValueToUSD(raw string) (float64, bool) {
	quota, ok := numericValue(raw)
	if !ok {
		return 0, false
	}
	return quota / quotaPerUSD, true
}

func numericValue(value any) (float64, bool) {
	var number float64
	switch v := value.(type) {
	case int:
		number = float64(v)
	case int64:
		number = float64(v)
	case float64:
		number = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		parsed, err := strconv.ParseFloat(jsonString(value), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	}

	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func float64Ptr(value float64) *float64 {
	return &value
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func siteRequiresUserIDHeader(ctx context.Context, site config.Site, credential authCredential) (bool, string) {
	for _, auth := range buildAuthHeaderVariants(credential) {
		result, err := doRequest(ctx, site, http.MethodGet, buildSiteURL(site.BaseURL, "/api/user/self"), nil, auth)
		msg := ""
		if result != nil {
			msg = extractResponseMessage(result.Payload)
		}
		if err != nil {
			msg = firstNonEmptyString(msg, err.Error())
		}
		if isUserIDHeaderError(err, resultPayload(result)) || isUserIDHeaderError(fmt.Errorf("%s", msg), nil) {
			return true, firstNonEmptyString(msg, "New-Api-User required")
		}
		// If self succeeded without header and returned id, discoverUserID should have handled it.
		if err == nil && result != nil && jsonBool(result.Payload["success"]) {
			return false, ""
		}
	}
	return false, ""
}

func discoverUserID(ctx context.Context, site config.Site, credential authCredential) (int, error) {
	authVariants := buildAuthHeaderVariants(credential)
	var lastErr error

	for _, auth := range authVariants {
		// Only try without user header. If the site needs New-Api-User, discovery is impossible
		// without already knowing the correct id.
		if id, err := trySelfUserID(ctx, site, auth, 0); err == nil && id > 0 {
			return id, nil
		} else if err != nil {
			lastErr = err
		}
	}

	if lastErr != nil {
		return 0, lastErr
	}
	return 0, fmt.Errorf("user id not found")
}

func trySelfUserID(ctx context.Context, site config.Site, auth map[string]string, userID int) (int, error) {
	headers := mergeHeaders(auth, managedUserIDHeaders(userID))
	result, err := doRequest(ctx, site, http.MethodGet, buildSiteURL(site.BaseURL, "/api/user/self"), nil, headers)
	if err != nil {
		return 0, err
	}
	if !jsonBool(result.Payload["success"]) {
		msg := firstNonEmptyString(extractResponseMessage(result.Payload), "fetch self failed")
		return 0, fmt.Errorf("%s", msg)
	}
	id := extractUserID(result.Payload)
	if id > 0 {
		return id, nil
	}
	if userID > 0 {
		return userID, nil
	}
	return 0, fmt.Errorf("user id missing in /api/user/self response")
}

func extractUserID(payload map[string]any) int {
	candidates := []any{
		nestedValue(payload, "data", "id"),
		nestedValue(payload, "data", "user_id"),
		nestedValue(payload, "data", "userId"),
		nestedValue(payload, "data", "user", "id"),
		payload["id"],
		payload["user_id"],
		payload["userId"],
	}
	for _, c := range candidates {
		if id := toPositiveInt(c); id > 0 {
			return id
		}
	}
	return 0
}

func resultPayload(result *httpResult) map[string]any {
	if result == nil {
		return nil
	}
	return result.Payload
}

func isAlreadyCheckedInMessage(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	patterns := []string{
		"already checked in",
		"already checkin",
		"already check-in",
		"checked in today",
		"checkin today",
		"今日已签到",
		"已经签到",
		"已签到",
		"重复签到",
		"签到过了",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) || strings.Contains(message, p) {
			return true
		}
	}
	return false
}

func toPositiveInt(value any) int {
	switch v := value.(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil && n > 0 {
			return n
		}
	}
	if n, err := strconv.Atoi(jsonString(value)); err == nil && n > 0 {
		return n
	}
	return 0
}
