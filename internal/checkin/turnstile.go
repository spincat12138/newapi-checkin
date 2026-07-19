package checkin

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
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

// checkinWithTurnstile obtains or consumes a short-lived token, sends it in the
// query parameter expected by NewAPI middleware, and applies the same action
// evidence rules as the plain and image-captcha paths.
func checkinWithTurnstile(ctx context.Context, site config.Site, headers map[string]string, opts Options) (*checkinOK, error) {
	token := strings.TrimSpace(opts.TurnstileToken)
	siteKey := ""
	pageURL := strings.TrimRight(site.BaseURL, "/") + "/"

	if pub, err := fetchPublicSiteStatus(ctx, site); err == nil && pub != nil {
		siteKey = pub.TurnstileSiteKey
	}

	if token == "" && opts.SolveTurnstile != nil {
		solveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captchaSolveTimeout)
		defer cancel()
		solved, err := opts.SolveTurnstile(solveCtx, TurnstileChallenge{
			SiteName: site.Name,
			BaseURL:  site.BaseURL,
			PageURL:  pageURL,
			SiteKey:  siteKey,
		})
		if err != nil {
			return nil, fmt.Errorf("solve turnstile: %w", err)
		}
		token = strings.TrimSpace(solved)
	}

	if token == "" {
		return nil, fmt.Errorf(
			"该站点开启了 Cloudflare Turnstile 人机验证（POST /api/user/checkin?turnstile=TOKEN）。"+
				"请用 -turnstile-token 粘贴浏览器拿到的 token，或 -turnstile-cmd 外挂打码/解题；"+
				"交互模式会提示粘贴。site_key=%s page=%s",
			firstNonEmptyString(siteKey, "unknown"),
			pageURL,
		)
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

// InteractiveTurnstileSolver asks the operator to paste a Turnstile token from the browser.
// Turnstile sitekeys are domain-bound, so the token must be obtained on the real site page
// (or via a third-party solver that targets that page URL).
func InteractiveTurnstileSolver(stdin io.Reader, stdout io.Writer, openPage bool) TurnstileSolver {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stderr
	}
	return func(ctx context.Context, challenge TurnstileChallenge) (string, error) {
		fmt.Fprintf(stdout, "\n[turnstile] 站点=%q 需要 Cloudflare Turnstile 人机验证\n", challenge.SiteName)
		fmt.Fprintf(stdout, "[turnstile] 页面: %s\n", challenge.PageURL)
		if challenge.SiteKey != "" {
			fmt.Fprintf(stdout, "[turnstile] site_key: %s\n", challenge.SiteKey)
		}
		fmt.Fprintf(stdout, "[turnstile] 获取方式（任选）：\n")
		fmt.Fprintf(stdout, "  1) 浏览器打开站点 → 登录 → 点签到前在 F12 Network 找到 POST .../checkin?turnstile=... 复制参数\n")
		fmt.Fprintf(stdout, "  2) 在站点控制台执行（若页面已加载 turnstile）：查看 widget 回调 token\n")
		fmt.Fprintf(stdout, "  3) 使用 -turnstile-cmd 调用 CapSolver/2captcha 等（推荐无人值守）\n")
		if openPage && challenge.PageURL != "" {
			if err := openBrowserURL(challenge.PageURL); err != nil {
				fmt.Fprintf(stdout, "[turnstile] 无法自动打开浏览器: %v\n", err)
			}
		}
		fmt.Fprintf(stdout, "[turnstile] 请粘贴 turnstile token 后回车: ")

		type readResult struct {
			line string
			err  error
		}
		ch := make(chan readResult, 1)
		go func() {
			reader := bufio.NewReader(stdin)
			line, err := reader.ReadString('\n')
			ch <- readResult{line: line, err: err}
		}()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case res := <-ch:
			token := strings.TrimSpace(res.line)
			if res.err != nil && token == "" {
				return "", fmt.Errorf("read turnstile token: %w", res.err)
			}
			if token == "" {
				return "", fmt.Errorf("turnstile token is empty")
			}
			return token, nil
		}
	}
}

// CommandTurnstileSolver runs an external program to obtain a Turnstile token.
// Placeholders: {sitekey} {url} {site} {base_url}
// If no placeholder is present, args are appended: <sitekey> <page_url>
// First non-empty stdout line is the token.
func CommandTurnstileSolver(command string) TurnstileSolver {
	command = strings.TrimSpace(command)
	return func(ctx context.Context, challenge TurnstileChallenge) (string, error) {
		if command == "" {
			return "", fmt.Errorf("turnstile command is empty")
		}

		expanded := command
		replacements := map[string]string{
			"{sitekey}":  challenge.SiteKey,
			"{url}":      challenge.PageURL,
			"{site}":     challenge.SiteName,
			"{base_url}": challenge.BaseURL,
		}
		hasPlaceholder := false
		for k, v := range replacements {
			if strings.Contains(expanded, k) {
				hasPlaceholder = true
				expanded = strings.ReplaceAll(expanded, k, v)
			}
		}

		var cmd *exec.Cmd
		if hasPlaceholder {
			cmd = shellCommand(ctx, expanded)
		} else {
			parts := splitCommandLine(command)
			if len(parts) == 0 {
				return "", fmt.Errorf("turnstile command is empty")
			}
			args := append(append([]string{}, parts[1:]...), challenge.SiteKey, challenge.PageURL)
			cmd = exec.CommandContext(ctx, parts[0], args...)
		}

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = err.Error()
			}
			return "", fmt.Errorf("turnstile command failed: %s", truncate(detail, 240))
		}
		for _, line := range strings.Split(stdout.String(), "\n") {
			token := strings.TrimSpace(line)
			if token != "" {
				return token, nil
			}
		}
		return "", fmt.Errorf("turnstile command returned empty token")
	}
}

// openBrowserURL opens an http(s) URL with the OS default browser.
func openBrowserURL(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}
