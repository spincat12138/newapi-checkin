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
	if got := cfg.Sites[0].AdditionalVerification; got != AdditionalVerificationNone {
		t.Fatalf("expected default additional verification %q, got %q", AdditionalVerificationNone, got)
	}
}

func TestNormalizeCanonicalizesAdditionalVerification(t *testing.T) {
	cfg := &Config{Sites: []Site{mapTestSite("captcha"), mapTestSite("turnstile")}}
	cfg.Sites[0].AdditionalVerification = "captcha"
	cfg.Sites[1].AdditionalVerification = "TURNSTILE"

	if err := normalize(cfg); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := cfg.Sites[0].AdditionalVerification; got != AdditionalVerificationCaptcha {
		t.Fatalf("captcha canonical form=%q", got)
	}
	if got := cfg.Sites[1].AdditionalVerification; got != AdditionalVerificationTurnstile {
		t.Fatalf("turnstile canonical form=%q", got)
	}
}

func TestNormalizeRejectsInvalidAdditionalVerification(t *testing.T) {
	site := mapTestSite("invalid")
	site.AdditionalVerification = "auto"
	err := normalize(&Config{Sites: []Site{site}})
	if err == nil || !strings.Contains(err.Error(), "additional_verification") {
		t.Fatalf("expected additional verification error, got %v", err)
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

func mapTestSite(name string) Site {
	return Site{
		Name:           name,
		BaseURL:        "https://example.com",
		CredentialType: CredentialAccessToken,
		AccessToken:    "token",
	}
}
