package checkin

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newapi-checkin/internal/config"
)

// NewAPI stores quota in internal units rather than dollars.
const quotaPerUSD = 500000.0

// captchaSolveTimeout bounds how long we wait for human/OCR captcha input.
// HTTP request timeouts come from the caller context separately.
const captchaSolveTimeout = 5 * time.Minute

// Run performs check-in for a single site configuration with default options.
func Run(ctx context.Context, site config.Site) Result {
	return RunWithOptions(ctx, site, Options{})
}

// RunWithOptions performs the complete single-site transaction:
//
//  1. resolve the configured credential, logging in when necessary;
//  2. discover/validate the user ID required by many forks;
//  3. execute the compatible plain, captcha, or Turnstile check-in path;
//  4. reject weak success signals; and
//  5. query the current balance without changing an established check-in result.
//
// The named return value lets the deferred timestamp cover every early-return
// failure path as well as successful attempts.
func RunWithOptions(ctx context.Context, site config.Site, opts Options) (result Result) {
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
	// Note: /api/user/self often works without the header (so balance still shows),
	// while /api/user/checkin may hard-require New-Api-User — discovery is critical.
	if userID <= 0 {
		if discovered, err := discoverUserID(ctx, site, credential); err == nil && discovered > 0 {
			userID = discovered
		}
	}

	if userID <= 0 {
		// Confirm whether this site requires the header before failing hard.
		if requires, sample := siteRequiresUserIDHeader(ctx, site, credential); requires {
			result.Error = missingUserIDError(sample)
			return result
		}
	}

	configuredUserID := site.UserID
	checkinResult, usedUserID, err := checkinSite(ctx, site, credential, userID, opts)
	if err != nil && isUserIDHeaderError(err, nil) && userID <= 0 {
		// Self may have succeeded without exposing id on first pass; try again then retry checkin.
		if discovered, dErr := discoverUserID(ctx, site, credential); dErr == nil && discovered > 0 {
			userID = discovered
			checkinResult, usedUserID, err = checkinSite(ctx, site, credential, userID, opts)
		}
	}
	if err != nil {
		populateTotalBalance(ctx, site, credential, userID, &result)
		if isUserIDHeaderError(err, nil) {
			result.Error = formatUserIDHeaderFailure(err, configuredUserID, userID)
			return result
		}
		result.Error = err.Error()
		return result
	}

	// Final false-success guard: never report OK with empty English "checkin success"
	// and no reward (classic symptom of treating status query as action).
	if !isCredibleCheckinSuccess(checkinResult) {
		populateTotalBalance(ctx, site, credential, firstPositiveInt(usedUserID, userID), &result)
		result.Error = fmt.Sprintf(
			"签到未确认成功（无奖励且无已签到/成功文案）。原始响应=%q。请根据站点类型排查：未开启签到 / 需要图片验证码 / 需要 Turnstile",
			firstNonEmptyString(checkinResult.Message, "(empty)"),
		)
		return result
	}

	result.Success = true
	result.Message = checkinResult.Message
	result.Reward = checkinResult.Reward
	if rewardUSD, ok := quotaValueToUSD(checkinResult.Reward); ok {
		result.RewardUSD = float64Ptr(rewardUSD)
	} else if isAlreadyCheckedInMessage(checkinResult.Message) {
		result.RewardUSD = float64Ptr(0)
	}
	if strings.TrimSpace(result.Message) == "" {
		if result.Reward != "" {
			result.Message = "签到成功"
		} else if isAlreadyCheckedInMessage(checkinResult.Message) {
			result.Message = "今日已签到"
		}
	}
	if usedUserID > 0 && result.Message != "" {
		result.Message = fmt.Sprintf("%s (user_id=%d)", result.Message, usedUserID)
	}
	populateTotalBalance(ctx, site, credential, firstPositiveInt(usedUserID, userID), &result)
	return result
}

// isCredibleCheckinSuccess rejects the classic false positive:
// Success with empty/generic message and no quota reward.
func isCredibleCheckinSuccess(ok *checkinOK) bool {
	if ok == nil {
		return false
	}
	if strings.TrimSpace(ok.Reward) != "" {
		return true
	}
	if isAlreadyCheckedInMessage(ok.Message) {
		return true
	}
	// Explicit site message without reward is rare but allowed after status verify upstream.
	if hasExplicitCheckinSuccessMessage(ok.Message) {
		return true
	}
	return false
}

type checkinOK struct {
	Message string
	Reward  string
}

// checkinStatus is an internal normalized view of several incompatible status
// payload shapes. CheckinEnabled is tri-state because an absent flag must not be
// interpreted as disabled.
type checkinStatus struct {
	CheckedInToday bool
	CaptchaEnabled bool
	// CheckinEnabled is tri-state: nil = unknown, &true/&false from API.
	CheckinEnabled *bool
	Message        string
	HasStatusShape bool
	Raw            map[string]any
}

// login exchanges username/password for the credential format returned by the
// target fork and extracts a user ID when the login response exposes one.
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

// extractLoginCredential checks known response locations in priority order and
// preserves whether the value is an access token or a session credential.
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

// checkinSite is the compatibility state machine. For each accepted auth header
// and optional user-ID variant it prefers a read-only status probe, then chooses
// the required action path. Validation challenges are attempted only after
// explicit configuration or an API response proves they are required.
func checkinSite(ctx context.Context, site config.Site, credential authCredential, userID int, opts Options) (*checkinOK, int, error) {
	authVariants := buildAuthHeaderVariants(credential)
	if len(authVariants) == 0 {
		return nil, 0, fmt.Errorf("credential is required")
	}

	// Public status helps classify failures (disabled vs turnstile) without forcing CF token.
	pub, _ := fetchPublicSiteStatus(ctx, site)
	if pub != nil && pub.CheckinEnabled != nil && !*pub.CheckinEnabled {
		return nil, 0, fmt.Errorf("该站点未开启签到功能（/api/status checkin_enabled=false）")
	}

	// Only force Turnstile *before* plain POST when the user already supplied a token.
	// Having SolveTurnstile configured must NOT prompt every site for CF token.
	hasExplicitTurnstileToken := strings.TrimSpace(opts.TurnstileToken) != ""

	userIDs := userIDAttempts(userID)
	var lastErr error

	for _, auth := range authVariants {
		for _, uid := range userIDs {
			headers := mergeHeaders(auth, managedUserIDHeaders(uid))

			// 1) Prefer status query — never treat "查询成功" as check-in success.
			status, statusErr := fetchCheckinStatus(ctx, site, headers)
			if statusErr == nil && status != nil {
				if status.CheckinEnabled != nil && !*status.CheckinEnabled {
					return nil, 0, fmt.Errorf("该站点未开启签到功能（checkin status enabled=false）")
				}
				if looksLikeCheckinDisabled(status.Message) {
					return nil, 0, fmt.Errorf("该站点未开启签到功能：%s", status.Message)
				}
				if status.CheckedInToday || isAlreadyCheckedInMessage(status.Message) {
					return &checkinOK{Message: firstNonEmptyString(status.Message, "今日已签到"), Reward: ""}, uid, nil
				}
				if status.CaptchaEnabled {
					ok, err := checkinWithCaptcha(ctx, site, headers, opts)
					if err != nil {
						lastErr = err
						continue
					}
					return ok, uid, nil
				}
			} else if statusErr != nil {
				if looksLikeCheckinDisabled(statusErr.Error()) {
					return nil, 0, fmt.Errorf("该站点未开启签到功能：%s", statusErr.Error())
				}
				lastErr = statusErr
			}

			// 2) Explicit -turnstile-token only: try Turnstile POST first.
			if hasExplicitTurnstileToken {
				ok, err := checkinWithTurnstile(ctx, site, headers, opts)
				if err == nil {
					return ok, uid, nil
				}
				lastErr = err
			}

			// 3) Plain check-in (normal sites; session may already pass Turnstile).
			ok, err := attemptPlainCheckin(ctx, site, headers)
			if err != nil {
				lastErr = err
				if looksLikeCheckinDisabled(err.Error()) {
					return nil, 0, fmt.Errorf("该站点未开启签到功能：%s", err.Error())
				}
				// Turnstile only after the site actually asks for it (or public flag + token empty).
				if looksLikeTurnstileRequired(err.Error()) {
					turnstileOK, turnstileErr := checkinWithTurnstile(ctx, site, headers, opts)
					if turnstileErr != nil {
						lastErr = turnstileErr
						continue
					}
					return turnstileOK, uid, nil
				}
				if looksLikeCaptchaRequired(err.Error()) {
					captchaOK, captchaErr := checkinWithCaptcha(ctx, site, headers, opts)
					if captchaErr != nil {
						lastErr = captchaErr
						continue
					}
					return captchaOK, uid, nil
				}
				continue
			}
			return ok, uid, nil
		}
	}

	if lastErr != nil {
		// Rephrase generic status-only errors when public status says checkin is off.
		if looksLikeCheckinDisabled(lastErr.Error()) {
			return nil, 0, fmt.Errorf("该站点未开启签到功能：%s", lastErr.Error())
		}
		if pub != nil && pub.CheckinEnabled != nil && !*pub.CheckinEnabled {
			return nil, 0, fmt.Errorf("该站点未开启签到功能（/api/status checkin_enabled=false）")
		}
		return nil, 0, lastErr
	}
	return nil, 0, fmt.Errorf("checkin failed")
}

// attemptPlainCheckin tries POST first and legacy GET second. Any status-shaped
// response is handled as observation, never as action success; strong action
// responses are verified when their evidence is otherwise incomplete.
func attemptPlainCheckin(ctx context.Context, site config.Site, headers map[string]string) (*checkinOK, error) {
	var lastErr error
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		result, err := doRequest(ctx, site, method, buildSiteURL(site.BaseURL, "/api/user/checkin"), nil, headers)
		if err != nil {
			lastErr = err
			continue
		}

		if isCheckinStatusPayload(result.Payload) {
			parsed := parseCheckinStatus(result.Payload)
			if parsed.CheckinEnabled != nil && !*parsed.CheckinEnabled {
				return nil, fmt.Errorf("该站点未开启签到功能（enabled=false）")
			}
			if looksLikeCheckinDisabled(parsed.Message) {
				return nil, fmt.Errorf("该站点未开启签到功能：%s", parsed.Message)
			}
			if parsed.CheckedInToday || isAlreadyCheckedInMessage(parsed.Message) {
				return &checkinOK{Message: firstNonEmptyString(parsed.Message, "今日已签到")}, nil
			}
			if parsed.CaptchaEnabled {
				return nil, fmt.Errorf("验证码")
			}
			// GET status must not be mistaken for check-in success.
			lastErr = fmt.Errorf("%s", firstNonEmptyString(parsed.Message, "checkin status only; no action performed"))
			continue
		}

		if ok, message, reward := interpretCheckinActionPayload(result.Payload); ok {
			return verifyCheckedInAfterAction(ctx, site, headers, &checkinOK{Message: message, Reward: reward})
		}

		message := firstNonEmptyString(extractResponseMessage(result.Payload), "checkin failed")
		lastErr = fmt.Errorf("%s", message)
		// Do not fall through to GET when the site clearly needs Turnstile/captcha.
		if looksLikeTurnstileRequired(message) || looksLikeCaptchaRequired(message) {
			return nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("checkin failed")
}

// checkinWithCaptcha performs the three-step image challenge protocol: fetch
// challenge, solve it through the injected callback, then submit ID and answer.
// Human/OCR waiting receives its own timeout so the caller's short HTTP deadline
// does not expire while an operator is reading the image.
func checkinWithCaptcha(ctx context.Context, site config.Site, headers map[string]string, opts Options) (*checkinOK, error) {
	if opts.SolveCaptcha == nil {
		return nil, fmt.Errorf("该站点开启了签到验证码，请使用交互模式或 -captcha-cmd 提供识别命令（见 README 验证码签到）")
	}

	// Captcha fetch uses the already-selected auth headers.
	result, err := doRequest(ctx, site, http.MethodPost, buildSiteURL(site.BaseURL, "/api/user/checkin/captcha"), map[string]any{}, headers)
	if err != nil {
		return nil, fmt.Errorf("fetch captcha: %w", err)
	}
	if success, exists := result.Payload["success"]; exists && !jsonBool(success) {
		return nil, fmt.Errorf("fetch captcha: %s", firstNonEmptyString(extractResponseMessage(result.Payload), "failed"))
	}
	captcha, err := parseCaptchaPayload(result.Payload)
	if err != nil {
		return nil, err
	}

	imagePath, err := saveCaptchaImage(opts.CaptchaImageDir, site.Name, captcha.ID, captcha.Image, captcha.MimeType)
	if err != nil {
		return nil, fmt.Errorf("save captcha image: %w", err)
	}

	solveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captchaSolveTimeout)
	defer cancel()

	answer, err := opts.SolveCaptcha(solveCtx, CaptchaChallenge{
		SiteName:  site.Name,
		CaptchaID: captcha.ID,
		Image:     captcha.Image,
		MimeType:  captcha.MimeType,
		ImagePath: imagePath,
	})
	if err != nil {
		return nil, fmt.Errorf("solve captcha: %w", err)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil, fmt.Errorf("captcha answer is empty")
	}

	submit, err := doRequest(ctx, site, http.MethodPost, buildSiteURL(site.BaseURL, "/api/user/checkin"), map[string]any{
		"captcha_id":     captcha.ID,
		"captcha_answer": answer,
	}, headers)
	if err != nil {
		return nil, fmt.Errorf("submit captcha checkin: %w", err)
	}

	if isCheckinStatusPayload(submit.Payload) {
		// Unexpected: action endpoint returned status shape.
		parsed := parseCheckinStatus(submit.Payload)
		if parsed.CheckedInToday {
			return &checkinOK{Message: firstNonEmptyString(parsed.Message, "今日已签到")}, nil
		}
		return nil, fmt.Errorf("%s", firstNonEmptyString(parsed.Message, "captcha checkin did not complete"))
	}

	if ok, message, reward := interpretCheckinActionPayload(submit.Payload); ok {
		return verifyCheckedInAfterAction(ctx, site, headers, &checkinOK{Message: message, Reward: reward})
	}

	message := firstNonEmptyString(extractResponseMessage(submit.Payload), "captcha checkin failed")
	if isAlreadyCheckedInMessage(message) {
		return &checkinOK{Message: message}, nil
	}
	return nil, fmt.Errorf("%s", message)
}

// fetchCheckinStatus probes both the monthly status form used by newer NewAPI
// releases and the plain endpoint used by forks. It returns only payloads with
// status evidence, preventing a legacy GET action response from being relabeled.
func fetchCheckinStatus(ctx context.Context, site config.Site, headers map[string]string) (*checkinStatus, error) {
	month := time.Now().Format("2006-01")
	candidates := []string{
		buildSiteURL(site.BaseURL, "/api/user/checkin") + "?month=" + url.QueryEscape(month),
		buildSiteURL(site.BaseURL, "/api/user/checkin"),
	}

	var lastErr error
	for _, requestURL := range candidates {
		result, err := doRequest(ctx, site, http.MethodGet, requestURL, nil, headers)
		if err != nil {
			lastErr = err
			continue
		}
		// Even success:false may carry status-like messages (e.g. already checked in).
		status := parseCheckinStatus(result.Payload)
		if status.HasStatusShape || isAlreadyCheckedInMessage(status.Message) {
			return status, nil
		}
		// Some sites use GET as the action endpoint (legacy). Do not invent status.
		if jsonBool(result.Payload["success"]) && !isCheckinStatusPayload(result.Payload) {
			lastErr = fmt.Errorf("GET /api/user/checkin is not a status query on this site")
			continue
		}
		lastErr = fmt.Errorf("%s", firstNonEmptyString(status.Message, "status query failed"))
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("status query failed")
}

// parseCheckinStatus collapses top-level, data-level, and data.stats variants
// into one view while retaining whether the payload actually had status shape.
func parseCheckinStatus(payload map[string]any) *checkinStatus {
	status := &checkinStatus{
		Message: extractResponseMessage(payload),
		Raw:     payload,
	}
	if payload == nil {
		return status
	}

	data, _ := payload["data"].(map[string]any)
	if data == nil {
		// Some deployments put flags at top level.
		data = payload
	}

	// NewAPI GetCheckinStatus nests flags under data.stats.
	stats, _ := data["stats"].(map[string]any)

	readCheckedIn := func(src map[string]any) {
		if src == nil {
			return
		}
		if v, ok := src["checked_in_today"]; ok {
			status.HasStatusShape = true
			status.CheckedInToday = jsonBool(v)
		}
		if v, ok := src["checkedInToday"]; ok {
			status.HasStatusShape = true
			status.CheckedInToday = jsonBool(v)
		}
	}
	readCaptcha := func(src map[string]any) {
		if src == nil {
			return
		}
		if v, ok := src["captcha_enabled"]; ok {
			status.HasStatusShape = true
			status.CaptchaEnabled = jsonBool(v)
		}
		if v, ok := src["captchaEnabled"]; ok {
			status.HasStatusShape = true
			status.CaptchaEnabled = jsonBool(v)
		}
	}

	readCheckedIn(data)
	readCheckedIn(stats)
	readCaptcha(data)
	readCaptcha(stats)

	// NewAPI GetCheckinStatus: data.enabled
	if v, ok := data["enabled"]; ok {
		status.HasStatusShape = true
		b := jsonBool(v)
		status.CheckinEnabled = &b
	}
	if v, ok := data["checkin_enabled"]; ok {
		status.HasStatusShape = true
		b := jsonBool(v)
		status.CheckinEnabled = &b
	}

	// Calendar/history/stats fields also mark a status response (NewAPI GetCheckinStatus).
	for _, key := range []string{
		"check_in_history", "checkin_history", "records", "days", "calendar",
		"stats", "min_quota", "max_quota", "enabled", "total_checkins", "checkin_count",
	} {
		if _, ok := data[key]; ok {
			status.HasStatusShape = true
			break
		}
	}
	if stats != nil {
		status.HasStatusShape = true
		for _, key := range []string{"records", "total_checkins", "checkin_count", "total_quota"} {
			if _, ok := stats[key]; ok {
				status.HasStatusShape = true
				break
			}
		}
	}
	if strings.Contains(status.Message, "查询成功") {
		status.HasStatusShape = true
	}
	return status
}

// isCheckinStatusPayload is intentionally structural rather than success-flag
// based: both successful status queries and failed/already-checked-in replies can
// carry useful state.
func isCheckinStatusPayload(payload map[string]any) bool {
	return parseCheckinStatus(payload).HasStatusShape
}

// interpretCheckinActionPayload decides whether a POST check-in response is a real action success.
// Status-query shaped payloads must be filtered out before calling this.
//
// Important: many NewAPI status endpoints return {"success":true,"message":""} without
// performing check-in. Empty message + success must NOT be treated as check-in success
// unless there is reward or an explicit success/already-checked-in message.
func interpretCheckinActionPayload(payload map[string]any) (bool, string, string) {
	if payload == nil {
		return false, "", ""
	}
	message := strings.TrimSpace(extractResponseMessage(payload))
	if isAlreadyCheckedInMessage(message) {
		return true, message, ""
	}
	if !jsonBool(payload["success"]) {
		return false, firstNonEmptyString(message, "checkin failed"), ""
	}

	reward := extractCheckinReward(payload)
	if reward != "" {
		// Real NewAPI DoCheckin always returns quota_awarded (message may be empty).
		return true, firstNonEmptyString(message, "签到成功"), reward
	}
	if isStatusOnlyMessage(message) || message == "" {
		return false, firstNonEmptyString(message, "checkin response missing reward evidence"), ""
	}
	if hasExplicitCheckinSuccessMessage(message) {
		return true, message, ""
	}
	// success:true with a non-action message (e.g. generic "ok") is not enough.
	return false, message, ""
}

// interpretCheckinPayload is retained for tests/back-compat: treats already-checked-in
// and successful action payloads as OK, but rejects pure status queries.
func interpretCheckinPayload(payload map[string]any) (bool, string, string) {
	if payload == nil {
		return false, "", ""
	}
	if isCheckinStatusPayload(payload) {
		status := parseCheckinStatus(payload)
		if status.CheckedInToday {
			return true, firstNonEmptyString(status.Message, "今日已签到"), ""
		}
		return false, firstNonEmptyString(status.Message, "status query only"), ""
	}
	return interpretCheckinActionPayload(payload)
}

func extractCheckinReward(payload map[string]any) string {
	return firstNonEmptyString(
		jsonString(nestedValue(payload, "data", "quota_awarded")),
		jsonString(nestedValue(payload, "data", "quotaAwarded")),
		jsonString(nestedValue(payload, "data", "reward")),
		// Avoid treating status calendar quota as reward; only use data.quota when
		// other award fields are absent AND message looks like an action.
		jsonString(nestedValue(payload, "data", "amount")),
	)
}

// hasCheckinActionEvidence centralizes the three signals strong enough to prove
// that an action occurred: an awarded quota, an already-checked-in result, or an
// explicit action-success message.
func hasCheckinActionEvidence(payload map[string]any, message string) bool {
	if extractCheckinReward(payload) != "" {
		return true
	}
	if isAlreadyCheckedInMessage(message) {
		return true
	}
	return hasExplicitCheckinSuccessMessage(message)
}

// hasExplicitCheckinSuccessMessage intentionally accepts a narrow vocabulary.
// Generic English text formerly synthesized by this client is excluded because
// it cannot prove what the server did.
func hasExplicitCheckinSuccessMessage(message string) bool {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return false
	}
	// Never treat bare English "checkin success" as evidence — that was our old
	// default for empty API messages and caused false OK + 本次获得=不可用.
	lower := strings.ToLower(msg)
	if lower == "checkin success" || lower == "check-in success" {
		return false
	}
	actionHints := []string{
		"签到成功",
		"checked in successfully",
		"领取成功",
	}
	for _, h := range actionHints {
		if strings.Contains(lower, strings.ToLower(h)) || strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

// isStatusOnlyMessage recognizes read-operation wording that must not satisfy
// the action-success contract.
func isStatusOnlyMessage(message string) bool {
	msg := strings.TrimSpace(message)
	if msg == "" {
		// Empty message alone is ambiguous; treated as non-action by interpretCheckinActionPayload.
		return true
	}
	patterns := []string{
		"查询成功",
		"获取成功",
		"query success",
		"query succeeded",
		"fetched successfully",
	}
	lower := strings.ToLower(msg)
	for _, p := range patterns {
		if lower == strings.ToLower(p) || msg == p {
			return true
		}
	}
	return false
}

// verifyCheckedInAfterAction re-queries status when available. If the status API clearly
// says not checked in today and there is no strong action evidence (reward), reject.
func verifyCheckedInAfterAction(ctx context.Context, site config.Site, headers map[string]string, ok *checkinOK) (*checkinOK, error) {
	if ok == nil {
		return nil, fmt.Errorf("empty checkin result")
	}
	if isAlreadyCheckedInMessage(ok.Message) {
		return ok, nil
	}
	// quota_awarded (or equivalent) is strong evidence from DoCheckin; do not fail on
	// a lagging or nested status field.
	if strings.TrimSpace(ok.Reward) != "" {
		return ok, nil
	}

	status, err := fetchCheckinStatus(ctx, site, headers)
	if err != nil || status == nil || !status.HasStatusShape {
		// No reliable status API — keep action result only if message is explicit.
		if hasExplicitCheckinSuccessMessage(ok.Message) {
			return ok, nil
		}
		return nil, fmt.Errorf("签到响应缺少奖励字段且无法核对状态：%s", firstNonEmptyString(ok.Message, "unknown"))
	}
	if status.CheckedInToday || isAlreadyCheckedInMessage(status.Message) {
		ok.Message = firstNonEmptyString(ok.Message, status.Message, "今日已签到")
		return ok, nil
	}
	// Status API exists and says not checked in — reject weak/false success.
	return nil, fmt.Errorf(
		"签到未完成：状态仍为未签到（checked_in_today=false）；响应=%s",
		firstNonEmptyString(ok.Message, status.Message, "no message"),
	)
}

func looksLikeCaptchaRequired(message string) bool {
	// Turnstile is a different flow (query token), not image captcha.
	if looksLikeTurnstileRequired(message) {
		return false
	}
	if looksLikeCheckinDisabled(message) {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	patterns := []string{
		"captcha",
		"验证码",
		"校验码",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) || strings.Contains(message, p) {
			return true
		}
	}
	return false
}

// looksLikeCheckinDisabled detects sites/messages where check-in is simply not enabled.
// These must not be misclassified as Turnstile or image captcha requirements.
func looksLikeCheckinDisabled(message string) bool {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	patterns := []string{
		"未开启签到",
		"签到功能未开启",
		"签到未开启",
		"未启用签到",
		"签到功能已关闭",
		"签到已关闭",
		"不支持签到",
		"没有开启签到",
		"checkin is not enabled",
		"check-in is not enabled",
		"checkin disabled",
		"check-in disabled",
		"checkin not enabled",
		"check-in not enabled",
		"checkin_enabled=false",
		"enabled=false",
	}
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) || strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// userIDAttempts returns New-Api-User values to try.
// When config already has user_id, do NOT fall back to 0 (no header): that second
// attempt always fails with "New-Api-User header not provided" and hides the real
// error from the first attempt (wrong id, captcha, turnstile, etc.).
func userIDAttempts(userID int) []int {
	if userID > 0 {
		return []int{userID}
	}
	return []int{0}
}

// populateTotalBalance treats balance as supplementary data: failures populate
// BalanceError but never overwrite the already determined check-in outcome.
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

// fetchAccountBalance retries the same authentication/user-ID combinations used
// for check-in against /api/user/self and returns the first parseable balance.
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

// extractBalanceUSD converts platform quota units to dollars. OneAPI exposes a
// total quota and used_quota pair, whereas NewAPI-style responses expose the
// remaining quota directly.
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

// quotaValueToUSD converts a reward field from internal quota units.
func quotaValueToUSD(raw string) (float64, bool) {
	quota, ok := numericValue(raw)
	if !ok {
		return 0, false
	}
	return quota / quotaPerUSD, true
}

// numericValue accepts decoded JSON numbers and numeric strings while rejecting
// NaN and infinities that cannot represent currency/quota values.
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

// siteRequiresUserIDHeader distinguishes a genuinely mandatory user header from
// a site where /api/user/self works without one. This check supports a precise
// configuration error instead of misclassifying the response as a captcha.
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

// discoverUserID bootstraps New-Api-User from /api/user/self using only the
// configured credential. It deliberately avoids guessing IDs.
func discoverUserID(ctx context.Context, site config.Site, credential authCredential) (int, error) {
	authVariants := buildAuthHeaderVariants(credential)
	var lastErr error

	for _, auth := range authVariants {
		// Prefer without user header — that is how we bootstrap the id.
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

// trySelfUserID performs one self lookup and tolerates forks that omit the
// conventional success field while still returning a usable data object.
func trySelfUserID(ctx context.Context, site config.Site, auth map[string]string, userID int) (int, error) {
	headers := mergeHeaders(auth, managedUserIDHeaders(userID))
	result, err := doRequest(ctx, site, http.MethodGet, buildSiteURL(site.BaseURL, "/api/user/self"), nil, headers)
	if err != nil {
		return 0, err
	}
	// Some deployments omit success or only return data; still try to parse id.
	if success, exists := result.Payload["success"]; exists && !jsonBool(success) {
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

// extractUserID checks common nesting and spelling variants, then performs a
// conservative case-insensitive scan of the data object.
func extractUserID(payload map[string]any) int {
	if payload == nil {
		return 0
	}
	candidates := []any{
		nestedValue(payload, "data", "id"),
		nestedValue(payload, "data", "user_id"),
		nestedValue(payload, "data", "userId"),
		nestedValue(payload, "data", "UserId"),
		nestedValue(payload, "data", "uid"),
		nestedValue(payload, "data", "UID"),
		nestedValue(payload, "data", "user", "id"),
		nestedValue(payload, "data", "user", "user_id"),
		nestedValue(payload, "data", "user", "userId"),
		nestedValue(payload, "user", "id"),
		payload["id"],
		payload["user_id"],
		payload["userId"],
		payload["UserId"],
	}
	for _, c := range candidates {
		if id := toPositiveInt(c); id > 0 {
			return id
		}
	}
	// Fallback: scan data object for common id keys (case-insensitive).
	if data, ok := payload["data"].(map[string]any); ok {
		for k, v := range data {
			lk := strings.ToLower(strings.TrimSpace(k))
			if lk == "id" || lk == "user_id" || lk == "userid" || lk == "uid" {
				if id := toPositiveInt(v); id > 0 {
					return id
				}
			}
		}
	}
	return 0
}

// missingUserIDError turns a low-level header failure into an actionable CLI
// diagnostic, including why a successful balance lookup does not disprove it.
func missingUserIDError(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "New-Api-User header not provided"
	}
	return fmt.Sprintf(
		"签到接口要求 New-Api-User，但未能自动识别用户 ID（%s）。"+
			"请在 config.yaml 该站点填写 user_id（整数）。"+
			"获取方法：浏览器登录站点 → F12 → Network → 任意 /api/user/* 请求头中的 New-Api-User。"+
			"说明：余额能查到也不代表已带齐签到头；此错误与图片验证码无关，填好 user_id 后才会进入验证码流程",
		detail,
	)
}

func formatUserIDHeaderFailure(err error, configuredUserID, effectiveUserID int) string {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	if effectiveUserID <= 0 && configuredUserID <= 0 {
		return missingUserIDError(detail)
	}
	uid := firstPositiveInt(effectiveUserID, configuredUserID)
	// Site still claims header missing even though we send it — often means wrong token,
	// reverse proxy stripping headers, or outdated binary; not "forgot to fill user_id".
	if isUserIDHeaderMissing(err) && uid > 0 {
		return fmt.Sprintf(
			"%s；当前已配置/使用 user_id=%d 并会发送 New-Api-User。"+
				"若仍报 header not provided，请核对：1) access_token 是否对应该账号；"+
				"2) 浏览器 F12 里 New-Api-User 是否就是 %d（不是别的号）；"+
				"3) 是否用了最新编译的 newapi-checkin.exe。此错误与图片验证码无关",
			detail, uid, uid,
		)
	}
	return fmt.Sprintf(
		"%v；已使用 user_id=%d。请确认与浏览器 F12 请求头 New-Api-User 一致，且 access_token 属于该账号。此错误与图片验证码无关",
		err, uid,
	)
}

func isUserIDHeaderMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not provided") ||
		strings.Contains(msg, "header not provided") ||
		strings.Contains(msg, "缺少") && strings.Contains(msg, "user") ||
		strings.Contains(msg, "required") && strings.Contains(msg, "new-api-user")
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

// toPositiveInt converts loosely typed JSON scalars while rejecting zero,
// negative, malformed, or missing identifiers.
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
