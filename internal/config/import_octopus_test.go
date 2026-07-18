package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleBackup = `{
  "version": "2.0",
  "type": "accounts",
  "accounts": {
    "accounts": [
      {
        "id": "account-1",
        "site_name": "Enabled NewAPI",
        "site_url": "https://enabled.example/",
        "site_type": "new-api",
        "disabled": false,
        "authType": "access_token",
        "account_info": {
          "id": "123",
          "access_token": "token-enabled",
          "username": "u1"
        },
        "checkIn": { "autoCheckInEnabled": true }
      },
      {
        "id": "account-2",
        "site_name": "Disabled Site",
        "site_url": "https://disabled.example",
        "site_type": "new-api",
        "disabled": true,
        "authType": "access_token",
        "account_info": {
          "id": 456,
          "access_token": "token-disabled"
        },
        "checkIn": { "autoCheckInEnabled": true }
      },
      {
        "id": "account-3",
        "site_name": "Anyrouter Cookie",
        "site_url": "https://any.example",
        "site_type": "anyrouter",
        "disabled": false,
        "authType": "cookie",
        "account_info": { "id": "180049", "access_token": "" },
        "cookieAuth": { "sessionCookie": "session=abc123" },
        "checkIn": { "autoCheckInEnabled": true }
      },
      {
        "id": "account-4",
        "site_name": "Sub2API Skip",
        "site_url": "https://sub.example",
        "site_type": "sub2api",
        "disabled": false,
        "authType": "access_token",
        "account_info": { "id": "1", "access_token": "x" },
        "checkIn": { "autoCheckInEnabled": true }
      },
      {
        "id": "account-5",
        "site_name": "Unknown Platform",
        "site_url": "https://unknown.example",
        "site_type": "unknown",
        "disabled": false,
        "authType": "access_token",
        "account_info": { "id": "9", "access_token": "tok-unknown" },
        "checkIn": { "autoCheckInEnabled": false }
      },
      {
        "id": "account-6",
        "site_name": "No Creds",
        "site_url": "https://nocreds.example",
        "site_type": "new-api",
        "disabled": false,
        "authType": "access_token",
        "account_info": { "id": "10", "access_token": "" },
        "checkIn": { "autoCheckInEnabled": true }
      }
    ]
  }
}`

func TestImportOctopus_Default(t *testing.T) {
	result, err := ImportOctopus([]byte(sampleBackup), OctopusImportOptions{})
	if err != nil {
		t.Fatalf("ImportOctopus: %v", err)
	}
	// enabled new-api, anyrouter cookie, unknown platform
	if result.Imported != 3 {
		t.Fatalf("imported=%d want 3; skipped=%v", result.Imported, result.Skipped)
	}

	byName := map[string]Site{}
	for _, s := range result.Config.Sites {
		byName[s.Name] = s
	}

	s := byName["Enabled NewAPI"]
	if s.BaseURL != "https://enabled.example" {
		t.Errorf("base_url trailing slash not trimmed: %q", s.BaseURL)
	}
	if s.Platform != "new-api" || s.CredentialType != CredentialAccessToken || s.AccessToken != "token-enabled" || s.UserID != 123 {
		t.Errorf("Enabled NewAPI unexpected: %+v", s)
	}

	s = byName["Anyrouter Cookie"]
	if s.Platform != "any-router" || s.CredentialType != CredentialSessionCookie || s.SessionCookie != "session=abc123" || s.AccessToken != "" || s.UserID != 180049 {
		t.Errorf("Anyrouter Cookie unexpected: %+v", s)
	}

	s = byName["Unknown Platform"]
	if s.Platform != "new-api-like" {
		t.Errorf("unknown platform map: %q", s.Platform)
	}
}

func TestImportOctopus_IncludeDisabled(t *testing.T) {
	result, err := ImportOctopus([]byte(sampleBackup), OctopusImportOptions{IncludeDisabled: true})
	if err != nil {
		t.Fatalf("ImportOctopus: %v", err)
	}
	if result.Imported != 4 {
		t.Fatalf("imported=%d want 4", result.Imported)
	}
	found := false
	for _, s := range result.Config.Sites {
		if s.Name == "Disabled Site" {
			found = true
			if s.UserID != 456 {
				t.Errorf("disabled user_id=%d", s.UserID)
			}
		}
	}
	if !found {
		t.Fatal("expected disabled site imported")
	}
}

func TestImportOctopus_RequireAutoCheckIn(t *testing.T) {
	result, err := ImportOctopus([]byte(sampleBackup), OctopusImportOptions{RequireAutoCheckIn: true})
	if err != nil {
		t.Fatalf("ImportOctopus: %v", err)
	}
	// Unknown Platform has autoCheckInEnabled=false → skipped
	for _, s := range result.Config.Sites {
		if s.Name == "Unknown Platform" {
			t.Fatal("should skip auto-checkin disabled site")
		}
	}
	if result.Imported != 2 {
		t.Fatalf("imported=%d want 2; skipped=%v", result.Imported, result.Skipped)
	}
}

func TestImportOctopusFileAndSave(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "accounts.json")
	out := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(in, []byte(sampleBackup), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ImportOctopusFile(in, OctopusImportOptions{TimeoutSeconds: 45})
	if err != nil {
		t.Fatalf("ImportOctopusFile: %v", err)
	}
	if err := Save(out, result.Config); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(out)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.TimeoutSeconds != 45 {
		t.Errorf("timeout=%d", loaded.TimeoutSeconds)
	}
	if len(loaded.Sites) != result.Imported {
		t.Errorf("sites=%d want %d", len(loaded.Sites), result.Imported)
	}

	raw, _ := os.ReadFile(out)
	if !strings.Contains(string(raw), "timeout_seconds: 45") {
		t.Errorf("yaml missing timeout: %s", raw)
	}
	if !strings.Contains(string(raw), "credential_type: session_cookie") || !strings.Contains(string(raw), "session_cookie: session=abc123") {
		t.Errorf("yaml missing explicit session cookie credential: %s", raw)
	}
}

func TestMapOctopusPlatform(t *testing.T) {
	cases := map[string]string{
		"new-api":      "new-api",
		"anyrouter":    "any-router",
		"any-router":   "any-router",
		"one-api":      "one-api",
		"veloera":      "veloera",
		"done-hub":     "done-hub",
		"unknown":      "new-api-like",
		"new-api-like": "new-api-like",
	}
	for in, want := range cases {
		got, ok := mapOctopusPlatform(in)
		if !ok || got != want {
			t.Errorf("map(%q)=(%q,%v) want %q", in, got, ok, want)
		}
	}
	if _, ok := mapOctopusPlatform("sub2api"); ok {
		t.Error("sub2api should be unsupported")
	}
}
