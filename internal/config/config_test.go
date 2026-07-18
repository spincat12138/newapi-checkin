package config

import (
	"strings"
	"testing"
)

func TestNormalizeInfersSessionCookieCredential(t *testing.T) {
	cfg := &Config{Sites: []Site{{
		Name:          "cookie-site",
		BaseURL:       "https://example.com",
		SessionCookie: "session=test-session",
	}}}

	if err := normalize(cfg); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := cfg.Sites[0].CredentialType; got != CredentialSessionCookie {
		t.Fatalf("expected inferred session_cookie type, got %q", got)
	}
}

func TestNormalizeRejectsMixedCredentialFields(t *testing.T) {
	cfg := &Config{Sites: []Site{{
		Name:           "mixed-site",
		BaseURL:        "https://example.com",
		CredentialType: CredentialAccessToken,
		AccessToken:    "token",
		SessionCookie:  "session=test-session",
	}}}

	err := normalize(cfg)
	if err == nil || !strings.Contains(err.Error(), "cannot include session_cookie") {
		t.Fatalf("expected mixed credential error, got %v", err)
	}
}

func TestNormalizeRejectsMalformedSessionCookie(t *testing.T) {
	cfg := &Config{Sites: []Site{{
		Name:           "cookie-site",
		BaseURL:        "https://example.com",
		CredentialType: CredentialSessionCookie,
		SessionCookie:  "raw-cookie-value",
	}}}

	err := normalize(cfg)
	if err == nil || !strings.Contains(err.Error(), "name=value") {
		t.Fatalf("expected session cookie format error, got %v", err)
	}
}
