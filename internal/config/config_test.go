package config

import (
	"os"
	"path/filepath"
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

func TestNormalizeTelegramRequiresCredentialsWhenEnabled(t *testing.T) {
	cfg := &Config{
		Telegram: TelegramConfig{Enabled: true},
		Sites:    []Site{mapTestSite("telegram")},
	}

	err := normalize(cfg)
	if err == nil || !strings.Contains(err.Error(), "telegram.bot_token") {
		t.Fatalf("expected missing bot token error, got %v", err)
	}
}

func TestNormalizeTelegramAcceptsProxyAndTrimsValues(t *testing.T) {
	cfg := &Config{
		Telegram: TelegramConfig{
			Enabled:  true,
			BotToken: " 123:token ",
			ChatID:   " -100123 ",
			ProxyURL: " socks5://127.0.0.1:1080 ",
		},
		Sites: []Site{mapTestSite("telegram")},
	}

	if err := normalize(cfg); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got, want := cfg.Telegram.BotToken, "123:token"; got != want {
		t.Fatalf("bot token=%q want %q", got, want)
	}
	if got, want := cfg.Telegram.ChatID, "-100123"; got != want {
		t.Fatalf("chat id=%q want %q", got, want)
	}
	if got, want := cfg.Telegram.ProxyURL, "socks5://127.0.0.1:1080"; got != want {
		t.Fatalf("proxy URL=%q want %q", got, want)
	}
}

func TestNormalizeTelegramRejectsUnsupportedProxyScheme(t *testing.T) {
	cfg := &Config{
		Telegram: TelegramConfig{ProxyURL: "ftp://127.0.0.1:21"},
		Sites:    []Site{mapTestSite("telegram")},
	}

	err := normalize(cfg)
	if err == nil || !strings.Contains(err.Error(), "http, https, socks5, or socks5h") {
		t.Fatalf("expected proxy scheme error, got %v", err)
	}
}

func TestSaveAndLoadPreserveTelegramConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &Config{
		TimeoutSeconds: 45,
		Telegram: TelegramConfig{
			Enabled:  true,
			BotToken: "123:token",
			ChatID:   "-100123",
			ProxyURL: "http://127.0.0.1:7890",
		},
		Sites: []Site{mapTestSite("telegram")},
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Telegram != cfg.Telegram {
		t.Fatalf("telegram config mismatch: got %+v want %+v", loaded.Telegram, cfg.Telegram)
	}
}

func TestLoadAcceptsNumericTelegramChatID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `timeout_seconds: 30
telegram:
  enabled: true
  bot_token: 123:token
  chat_id: -1001234567890
sites:
  - name: site
    base_url: https://example.com
    credential_type: access_token
    access_token: token
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := cfg.Telegram.ChatID, "-1001234567890"; got != want {
		t.Fatalf("chat id=%q want %q", got, want)
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
