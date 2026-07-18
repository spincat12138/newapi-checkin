package checkin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		if r.Header.Get("Authorization") != "" {
			t.Errorf("session cookie credential must not send Authorization")
		}
		if got := r.Header.Get("Cookie"); got != "session=test-session" {
			t.Errorf("expected explicit session cookie, got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
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

func assertFloatPointer(t *testing.T, name string, actual *float64, expected float64) {
	t.Helper()
	if actual == nil {
		t.Fatalf("expected %s to be known", name)
	}
	if *actual != expected {
		t.Fatalf("expected %s %v, got %v", name, expected, *actual)
	}
}
