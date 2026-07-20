package checkin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"newapi-checkin/internal/config"
)

// TurnstileChallenge is presented when a site requires Cloudflare Turnstile
// on POST /api/user/checkin (NewAPI middleware.TurnstileCheck).
type TurnstileChallenge struct {
	SiteName string
	BaseURL  string
	PageURL  string // typically base URL + /
	SiteKey  string // public turnstile_site_key from /api/status
}

// TurnstileSolver returns a short-lived Turnstile response token.
type TurnstileSolver func(ctx context.Context, challenge TurnstileChallenge) (string, error)

type publicSiteStatus struct {
	TurnstileEnabled bool
	TurnstileSiteKey string
	// CheckinEnabled is tri-state: nil unknown, true/false from /api/status.
	CheckinEnabled *bool
}

// fetchPublicSiteStatus reads unauthenticated feature flags used to classify a
// failed check-in and to supply a domain-bound site key to solver integrations.
func fetchPublicSiteStatus(ctx context.Context, site config.Site) (*publicSiteStatus, error) {
	result, err := doRequest(ctx, site, http.MethodGet, buildSiteURL(site.BaseURL, "/api/status"), nil, nil)
	if err != nil {
		return nil, err
	}
	data, _ := result.Payload["data"].(map[string]any)
	if data == nil {
		data = result.Payload
	}
	status := &publicSiteStatus{
		TurnstileEnabled: jsonBool(data["turnstile_check"]) || jsonBool(data["TurnstileCheckEnabled"]),
		TurnstileSiteKey: firstNonEmptyString(
			jsonString(data["turnstile_site_key"]),
			jsonString(data["TurnstileSiteKey"]),
		),
	}
	if v, ok := data["checkin_enabled"]; ok {
		b := jsonBool(v)
		status.CheckinEnabled = &b
	} else if v, ok := data["CheckinEnabled"]; ok {
		b := jsonBool(v)
		status.CheckinEnabled = &b
	}
	// If site key present but flag missing, still treat as turnstile-capable.
	if !status.TurnstileEnabled && status.TurnstileSiteKey != "" {
		status.TurnstileEnabled = true
	}
	return status, nil
}

// checkinWithTurnstile obtains a short-lived token from the configured 2Captcha
// solver, sends it in the query parameter expected by NewAPI middleware, and
// applies the same action evidence rules as the other check-in paths.
func checkinWithTurnstile(ctx context.Context, site config.Site, headers map[string]string, opts Options) (*checkinOK, error) {
	siteKey := ""
	pageURL := strings.TrimRight(site.BaseURL, "/") + "/"

	if pub, err := fetchPublicSiteStatus(ctx, site); err == nil && pub != nil {
		siteKey = pub.TurnstileSiteKey
	}

	if opts.SolveTurnstile == nil {
		return nil, fmt.Errorf("Turnstile 验证需要 2Captcha，请设置 TWOCAPTCHA_API_KEY")
	}

	solveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), verificationSolveTimeout)
	defer cancel()
	token, err := opts.SolveTurnstile(solveCtx, TurnstileChallenge{
		SiteName: site.Name,
		BaseURL:  site.BaseURL,
		PageURL:  pageURL,
		SiteKey:  siteKey,
	})
	if err != nil {
		return nil, fmt.Errorf("solve turnstile with 2captcha: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("2captcha returned an empty turnstile token")
	}

	requestURL := buildSiteURL(site.BaseURL, "/api/user/checkin") + "?turnstile=" + url.QueryEscape(token)
	submit, err := doRequest(ctx, site, http.MethodPost, requestURL, nil, headers)
	if err != nil {
		return nil, fmt.Errorf("turnstile checkin: %w", err)
	}

	if isCheckinStatusPayload(submit.Payload) {
		parsed := parseCheckinStatus(submit.Payload)
		if parsed.CheckedInToday || isAlreadyCheckedInMessage(parsed.Message) {
			return &checkinOK{Message: firstNonEmptyString(parsed.Message, "今日已签到")}, nil
		}
		return nil, fmt.Errorf("%s", firstNonEmptyString(parsed.Message, "turnstile checkin status only"))
	}

	if ok, message, reward := interpretCheckinActionPayload(submit.Payload); ok {
		return verifyCheckedInAfterAction(ctx, site, headers, &checkinOK{Message: message, Reward: reward})
	}

	message := firstNonEmptyString(extractResponseMessage(submit.Payload), "turnstile checkin failed")
	if isAlreadyCheckedInMessage(message) {
		return &checkinOK{Message: message}, nil
	}
	if looksLikeTurnstileRequired(message) {
		return nil, fmt.Errorf("%s（token 可能已过期，请重新获取）", message)
	}
	return nil, fmt.Errorf("%s", message)
}

// looksLikeTurnstileRequired uses narrow API-rejection patterns so explanatory
// client messages mentioning Turnstile do not recursively trigger the solver.
func looksLikeTurnstileRequired(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	// Match NewAPI middleware / site errors only. Avoid matching our own help text
	// (e.g. long Chinese tips that mention "Turnstile") which would recurse wrongly.
	patterns := []string{
		"turnstile token",
		"turnstile 校验",
		"turnstile check failed",
		"cf-turnstile",
		"turnstile token 为空",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	// Bare "turnstile" only when clearly an API rejection, not our tips.
	if strings.Contains(msg, "turnstile") && (strings.Contains(msg, "为空") ||
		strings.Contains(msg, "失败") || strings.Contains(msg, "empty") ||
		strings.Contains(msg, "invalid") || strings.Contains(msg, "required") ||
		strings.Contains(msg, "校验")) {
		return true
	}
	return false
}
