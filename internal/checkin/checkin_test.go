package checkin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newapi-checkin/internal/config"
)

func TestRunReportsRewardAndTotalBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":2500}}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"id":42,"quota":1250000,"used_quota":500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	startedAt := time.Now()
	result := Run(context.Background(), config.Site{
		Name:           "test-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         42,
	})

	if !result.Success {
		t.Fatalf("expected successful check-in, got error %q", result.Error)
	}
	if result.Reward != "2500" {
		t.Fatalf("expected raw reward 2500, got %q", result.Reward)
	}
	assertFloatPointer(t, "reward USD", result.RewardUSD, 0.005)
	assertFloatPointer(t, "total balance USD", result.TotalBalanceUSD, 2.5)
	if result.CheckedAt.Before(startedAt) || result.CheckedAt.After(time.Now()) {
		t.Fatalf("unexpected check-in timestamp %s", result.CheckedAt)
	}
}

func TestRunAlreadyCheckedInReportsZeroReward(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			fmt.Fprint(w, `{"success":false,"message":"今日已签到"}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "checked-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         7,
	})

	if !result.Success {
		t.Fatalf("expected already-checked-in response to be successful, got %q", result.Error)
	}
	assertFloatPointer(t, "reward USD", result.RewardUSD, 0)
	assertFloatPointer(t, "total balance USD", result.TotalBalanceUSD, 1)
}

func TestRunKeepsBalanceFailureVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/checkin":
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":5000}}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"id":9}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "missing-balance-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         9,
	})

	if !result.Success {
		t.Fatalf("expected check-in success, got %q", result.Error)
	}
	if result.TotalBalanceUSD != nil {
		t.Fatalf("expected unknown balance, got %v", *result.TotalBalanceUSD)
	}
	if result.BalanceError == "" {
		t.Fatal("expected balance error to remain visible")
	}
}

func TestRunUsesExplicitSessionCookieCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/status is unauthenticated public status; other user APIs must use Cookie only.
		if r.URL.Path != "/api/status" {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("session cookie credential must not send Authorization")
			}
			if got := r.Header.Get("Cookie"); got != "session=test-session" {
				t.Errorf("expected explicit session cookie, got %q", got)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"checkin_enabled":true}}`)
		case "/api/user/checkin":
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":500000}}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":1000000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "cookie-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialSessionCookie,
		SessionCookie:  "session=test-session",
		UserID:         7,
	})

	if !result.Success {
		t.Fatalf("expected session-cookie check-in success, got %q", result.Error)
	}
	assertFloatPointer(t, "reward USD", result.RewardUSD, 1)
	assertFloatPointer(t, "total balance USD", result.TotalBalanceUSD, 2)
}

func TestExtractLoginCredentialPreservesSessionType(t *testing.T) {
	credential := extractLoginCredential(map[string]any{
		"data": map[string]any{"session": "session=login-session"},
	})
	if credential.Type != config.CredentialSessionCookie || credential.Value != "session=login-session" {
		t.Fatalf("unexpected login credential: %#v", credential)
	}
}

func TestExtractBalanceUSDForOneAPIUsesRemainingQuota(t *testing.T) {
	balance, err := extractBalanceUSD(map[string]any{
		"data": map[string]any{
			"quota":      float64(3000000),
			"used_quota": float64(1000000),
		},
	}, PlatformOneAPI)
	if err != nil {
		t.Fatalf("extract balance: %v", err)
	}
	if balance != 4 {
		t.Fatalf("expected remaining balance 4 USD, got %v", balance)
	}
}

func TestRunNewAPIStatusWithNestedStatsDoesNotFakeSuccess(t *testing.T) {
	// Reproduces cngov / 大喵喵: GET returns NewAPI GetCheckinStatus shape,
	// POST without turnstile fails, must NOT log "checkin success" with 不可用 reward.
	var postCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"turnstile_check":true,"turnstile_site_key":"0xKey","checkin_enabled":true}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"success":true,"message":"","data":{"enabled":true,"min_quota":100,"max_quota":200,"stats":{"checked_in_today":false,"total_checkins":3,"records":[]}}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost:
			postCount++
			if r.URL.Query().Get("turnstile") == "" {
				fmt.Fprint(w, `{"success":false,"message":"Turnstile token 为空"}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":1500,"checkin_date":"2026-07-19"}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":46505087}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "大喵喵API",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         48,
	})
	if result.Success {
		t.Fatalf("expected failure without turnstile, got success message=%q reward=%q", result.Message, result.Reward)
	}
	if !strings.Contains(strings.ToLower(result.Error), "turnstile") {
		t.Fatalf("expected turnstile-related error, got %q", result.Error)
	}
	if postCount == 0 {
		t.Fatal("expected at least one POST checkin attempt")
	}
}

func TestInterpretRejectsEmptySuccessWithoutReward(t *testing.T) {
	ok, msg, reward := interpretCheckinActionPayload(map[string]any{
		"success": true,
		"message": "",
		"data":    map[string]any{},
	})
	if ok {
		t.Fatalf("empty success must not count as check-in, msg=%q reward=%q", msg, reward)
	}
}

func TestIsCredibleCheckinSuccessGuard(t *testing.T) {
	if isCredibleCheckinSuccess(&checkinOK{Message: "", Reward: ""}) {
		t.Fatal("empty result must not be credible")
	}
	if isCredibleCheckinSuccess(&checkinOK{Message: "checkin success", Reward: ""}) {
		t.Fatal("generic english checkin success without reward must not be credible")
	}
	if !isCredibleCheckinSuccess(&checkinOK{Message: "签到成功", Reward: "1000"}) {
		t.Fatal("reward should make success credible")
	}
	if !isCredibleCheckinSuccess(&checkinOK{Message: "今日已签到", Reward: ""}) {
		t.Fatal("already checked in should be credible")
	}
}

func TestParseCheckinStatusReadsNestedStats(t *testing.T) {
	status := parseCheckinStatus(map[string]any{
		"success": true,
		"data": map[string]any{
			"enabled":   true,
			"min_quota": 1,
			"max_quota": 2,
			"stats": map[string]any{
				"checked_in_today": false,
				"total_checkins":   1,
				"records":          []any{},
			},
		},
	})
	if !status.HasStatusShape {
		t.Fatal("expected status shape for NewAPI GetCheckinStatus")
	}
	if status.CheckedInToday {
		t.Fatal("expected checked_in_today=false from nested stats")
	}
}

func TestRunDoesNotTreatStatusQueryAsCheckinSuccess(t *testing.T) {
	var postCheckin int
	checkedIn := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			// Status query shaped like jianzhile: success + 查询成功 is NOT check-in.
			fmt.Fprintf(w, `{"success":true,"message":"查询成功","data":{"captcha_enabled":false,"checked_in_today":%t}}`, checkedIn)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost:
			postCheckin++
			checkedIn = true
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":1000}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "status-query-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         1,
	})
	if !result.Success {
		t.Fatalf("expected real POST check-in success, got %q", result.Error)
	}
	if postCheckin == 0 {
		t.Fatal("expected POST /api/user/checkin after status query")
	}
	assertFloatPointer(t, "reward USD", result.RewardUSD, 0.002)
}

func TestRunCaptchaFlowWithSolver(t *testing.T) {
	var gotBody string
	var captchaPosts, checkinPosts int
	checkedIn := false
	// 1x1 PNG
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			fmt.Fprintf(w, `{"success":true,"message":"查询成功","data":{"captcha_enabled":true,"checked_in_today":%t}}`, checkedIn)
		case r.URL.Path == "/api/user/checkin/captcha" && r.Method == http.MethodPost:
			captchaPosts++
			fmt.Fprintf(w, `{"success":true,"data":{"captcha_id":"cap-1","image":"data:image/png;base64,%s"}}`, pngBase64)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost:
			checkinPosts++
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			gotBody = string(buf)
			checkedIn = true
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":2500}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":1000000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := RunWithOptions(context.Background(), config.Site{
		Name:           "captcha-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         9,
	}, Options{
		SolveCaptcha: func(ctx context.Context, challenge CaptchaChallenge) (string, error) {
			if challenge.CaptchaID != "cap-1" {
				t.Fatalf("unexpected captcha id %q", challenge.CaptchaID)
			}
			if len(challenge.Image) == 0 {
				t.Fatal("expected captcha image bytes")
			}
			return "AB12", nil
		},
	})

	if !result.Success {
		t.Fatalf("expected captcha check-in success, got %q", result.Error)
	}
	if captchaPosts != 1 || checkinPosts < 1 {
		t.Fatalf("unexpected request counts captcha=%d checkin=%d", captchaPosts, checkinPosts)
	}
	if !strings.Contains(gotBody, `"captcha_id":"cap-1"`) || !strings.Contains(gotBody, `"captcha_answer":"AB12"`) {
		t.Fatalf("unexpected captcha submit body: %s", gotBody)
	}
	assertFloatPointer(t, "reward USD", result.RewardUSD, 0.005)
}

func TestRunCaptchaRequiredWithoutSolverFailsClearly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"success":true,"message":"查询成功","data":{"captcha_enabled":true,"checked_in_today":false}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "captcha-needed",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         3,
	})
	if result.Success {
		t.Fatal("expected failure without captcha solver")
	}
	if !strings.Contains(result.Error, "验证码") {
		t.Fatalf("expected captcha error hint, got %q", result.Error)
	}
}

func TestInterpretCheckinPayloadRejectsQuerySuccessStatus(t *testing.T) {
	ok, message, _ := interpretCheckinPayload(map[string]any{
		"success": true,
		"message": "查询成功",
		"data": map[string]any{
			"captcha_enabled":  true,
			"checked_in_today": false,
		},
	})
	if ok {
		t.Fatalf("status query must not be success, message=%q", message)
	}
}

func TestRunTurnstileCheckinWithToken(t *testing.T) {
	var gotURL string
	checkedIn := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"turnstile_check":true,"turnstile_site_key":"0xSiteKey","checkin_enabled":true}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			fmt.Fprintf(w, `{"success":true,"data":{"enabled":true,"min_quota":1,"max_quota":2,"stats":{"checked_in_today":%t}}}`, checkedIn)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost:
			gotURL = r.URL.String()
			if r.URL.Query().Get("turnstile") != "tok-abc" {
				fmt.Fprint(w, `{"success":false,"message":"Turnstile token 为空"}`)
				return
			}
			checkedIn = true
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":5000}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":1500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Without token: plain POST should fail with turnstile empty, then solver provides token.
	result := RunWithOptions(context.Background(), config.Site{
		Name:           "turnstile-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         11,
	}, Options{
		TurnstileToken: "tok-abc",
	})
	if !result.Success {
		t.Fatalf("expected turnstile check-in success, got %q", result.Error)
	}
	if !strings.Contains(gotURL, "turnstile=tok-abc") {
		t.Fatalf("expected turnstile query on checkin URL, got %q", gotURL)
	}
	assertFloatPointer(t, "reward USD", result.RewardUSD, 0.01)
}

func TestRunTurnstileRequiredWithoutTokenFailsClearly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"turnstile_check":true,"turnstile_site_key":"0xKey"}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"success":true,"data":{"enabled":true,"stats":{}}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"success":false,"message":"Turnstile token 为空"}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "need-turnstile",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         5,
	})
	if result.Success {
		t.Fatal("expected failure without turnstile token")
	}
	if !strings.Contains(strings.ToLower(result.Error), "turnstile") {
		t.Fatalf("expected turnstile error, got %q", result.Error)
	}
}

func TestLooksLikeTurnstileRequired(t *testing.T) {
	if !looksLikeTurnstileRequired("Turnstile token 为空") {
		t.Fatal("expected turnstile empty message to match")
	}
	if looksLikeCaptchaRequired("Turnstile token 为空") {
		t.Fatal("turnstile must not be treated as image captcha")
	}
}

func TestRunCheckinDisabledDoesNotAskTurnstile(t *testing.T) {
	var turnstileSolverCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"checkin_enabled":false,"turnstile_check":true,"turnstile_site_key":"0xKey"}}`)
		case r.URL.Path == "/api/user/checkin":
			fmt.Fprint(w, `{"success":true,"data":{"enabled":false}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":500000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := RunWithOptions(context.Background(), config.Site{
		Name:           "no-checkin-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         1,
	}, Options{
		SolveTurnstile: func(ctx context.Context, challenge TurnstileChallenge) (string, error) {
			turnstileSolverCalls++
			return "should-not-be-called", nil
		},
	})
	if result.Success {
		t.Fatal("expected failure when checkin disabled")
	}
	if !strings.Contains(result.Error, "未开启签到") {
		t.Fatalf("expected disabled-checkin error, got %q", result.Error)
	}
	if turnstileSolverCalls != 0 {
		t.Fatalf("turnstile solver must not run for disabled checkin, calls=%d", turnstileSolverCalls)
	}
	if strings.Contains(strings.ToLower(result.Error), "turnstile token") {
		t.Fatalf("disabled checkin must not demand turnstile token, got %q", result.Error)
	}
}

func TestRunDoesNotPromptTurnstileBeforePlainCheckin(t *testing.T) {
	// SolveTurnstile configured must not force CF prompt on normal sites.
	var turnstileSolverCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"checkin_enabled":true}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"success":true,"message":"查询成功","data":{"captcha_enabled":false,"checked_in_today":false}}`)
		case r.URL.Path == "/api/user/checkin" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":2000}}`)
		case r.URL.Path == "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"quota":1000000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := RunWithOptions(context.Background(), config.Site{
		Name:           "normal-site",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         2,
	}, Options{
		SolveTurnstile: func(ctx context.Context, challenge TurnstileChallenge) (string, error) {
			turnstileSolverCalls++
			return "unused", nil
		},
	})
	if !result.Success {
		t.Fatalf("expected plain check-in success, got %q", result.Error)
	}
	if turnstileSolverCalls != 0 {
		t.Fatalf("turnstile solver should not run for normal sites, calls=%d", turnstileSolverCalls)
	}
}

func TestLooksLikeCheckinDisabled(t *testing.T) {
	if !looksLikeCheckinDisabled("签到功能未开启") {
		t.Fatal("expected Chinese disabled message")
	}
	if !looksLikeCheckinDisabled("checkin is not enabled") {
		t.Fatal("expected English disabled message")
	}
	if looksLikeCheckinDisabled("Turnstile token 为空") {
		t.Fatal("turnstile must not look like disabled")
	}
}

func TestRunConfiguredUserIDDoesNotFallbackToEmptyHeader(t *testing.T) {
	// Regression: with user_id set, old code still tried uid=0 and lastErr became
	// "header not provided", making it look like config was ignored.
	var attemptsWithoutHeader int
	var attemptsWith2801 int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"checkin_enabled":true}}`)
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"id":2801,"quota":1000000}}`)
		case "/api/user/checkin":
			uid := r.Header.Get("New-Api-User")
			if uid == "" {
				uid = r.Header.Get("New-API-User")
			}
			if uid == "" {
				attemptsWithoutHeader++
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"success":false,"message":"Unauthorized, New-Api-User header not provided"}`)
				return
			}
			if uid == "2801" {
				attemptsWith2801++
				// Simulate first real failure that is NOT "not provided"
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"success":false,"message":"token 无效"}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"success":false,"message":"unexpected uid"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "简直了",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		UserID:         2801,
	})
	if result.Success {
		t.Fatal("expected failure from token 无效")
	}
	if attemptsWithoutHeader != 0 {
		t.Fatalf("must not retry without New-Api-User when user_id is set, got %d bare attempts", attemptsWithoutHeader)
	}
	if attemptsWith2801 == 0 {
		t.Fatal("expected checkin attempts with user_id=2801")
	}
	if strings.Contains(result.Error, "header not provided") {
		t.Fatalf("last error must not be masked as header not provided: %q", result.Error)
	}
	if !strings.Contains(result.Error, "token 无效") {
		t.Fatalf("expected real error, got %q", result.Error)
	}
}

func TestRunDiscoversUserIDFromSelfForCheckin(t *testing.T) {
	// jianzhile-like: self works without New-Api-User (balance OK), checkin requires header.
	var sawUserHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"checkin_enabled":true}}`)
		case "/api/user/self":
			// No New-Api-User required for self; returns id for discovery.
			fmt.Fprint(w, `{"success":true,"data":{"id":77,"quota":124975689}}`)
		case "/api/user/checkin":
			if r.Header.Get("New-Api-User") == "" && r.Header.Get("New-API-User") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"success":false,"message":"Unauthorized, New-Api-User header not provided"}`)
				return
			}
			if r.Header.Get("New-Api-User") != "77" && r.Header.Get("New-API-User") != "77" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"success":false,"message":"New-Api-User does not match"}`)
				return
			}
			sawUserHeader = true
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"success":true,"message":"查询成功","data":{"captcha_enabled":false,"checked_in_today":false}}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"message":"签到成功","data":{"quota_awarded":2500}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "简直了",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
		// user_id intentionally omitted — must be discovered from /self
	})
	if !result.Success {
		t.Fatalf("expected discover+checkin success, got %q", result.Error)
	}
	if !sawUserHeader {
		t.Fatal("expected New-Api-User header on checkin")
	}
	if !strings.Contains(result.Message, "user_id=77") {
		t.Fatalf("expected user_id in message, got %q", result.Message)
	}
}

func TestRunMissingUserIDClearErrorNotCaptcha(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			fmt.Fprint(w, `{"success":true,"data":{"checkin_enabled":true}}`)
		case "/api/user/self":
			// Balance-like payload without id — discovery fails.
			fmt.Fprint(w, `{"success":true,"data":{"quota":124975689}}`)
		case "/api/user/checkin":
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"success":false,"message":"Unauthorized, New-Api-User header not provided"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), config.Site{
		Name:           "简直了-no-id",
		BaseURL:        server.URL,
		Platform:       PlatformNewAPI,
		CredentialType: config.CredentialAccessToken,
		AccessToken:    "test-token",
	})
	if result.Success {
		t.Fatal("expected failure without user_id")
	}
	if !strings.Contains(result.Error, "user_id") {
		t.Fatalf("expected user_id guidance, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "验证码无关") {
		t.Fatalf("expected note that captcha is unrelated, got %q", result.Error)
	}
}

func TestExtractUserIDFromNestedAndCaseVariants(t *testing.T) {
	if id := extractUserID(map[string]any{"data": map[string]any{"UserId": float64(42)}}); id != 42 {
		t.Fatalf("UserId: got %d", id)
	}
	if id := extractUserID(map[string]any{"data": map[string]any{"uid": "99"}}); id != 99 {
		t.Fatalf("uid string: got %d", id)
	}
}

func TestParseCaptchaPayloadDataURL(t *testing.T) {
	pngBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	payload, err := parseCaptchaPayload(map[string]any{
		"success": true,
		"data": map[string]any{
			"captcha_id": "x1",
			"image":      "data:image/png;base64," + pngBase64,
		},
	})
	if err != nil {
		t.Fatalf("parse captcha: %v", err)
	}
	if payload.ID != "x1" || len(payload.Image) == 0 || payload.MimeType != "image/png" {
		t.Fatalf("unexpected captcha payload: %#v", payload)
	}
}

func assertFloatPointer(t *testing.T, name string, actual *float64, expected float64) {
	t.Helper()
	if actual == nil {
		t.Fatalf("expected %s to be known", name)
	}
	if *actual != expected {
		t.Fatalf("expected %s %v, got %v", name, expected, *actual)
	}
}
