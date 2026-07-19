package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newapi-checkin/internal/config"
)

const testBackup = `{
  "accounts": {
    "accounts": [
      {
        "id": "site-1",
        "site_name": "Example",
        "site_url": "https://example.com/",
        "site_type": "new-api",
        "disabled": false,
        "authType": "access_token",
        "account_info": {
          "id": "42",
          "access_token": "test-token"
        }
      }
    ]
  }
}`

func TestRunRequiresSourceFile(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code=%d want 1", code)
	}
	if !strings.Contains(stderr.String(), "-from is required") {
		t.Fatalf("stderr missing required flag error: %q", stderr.String())
	}
}

func TestRunHelpSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code=%d want 0", code)
	}
	if !strings.Contains(stderr.String(), "newapi-import-config") {
		t.Fatalf("stderr missing usage: %q", stderr.String())
	}
}

func TestRunImportsConfig(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "accounts-backup.json")
	out := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(from, []byte(testBackup), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"-from", from, "-out", out, "-timeout", "45"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code=%d want 0; stderr=%q", code, stderr.String())
	}

	cfg, err := config.Load(out)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	if cfg.TimeoutSeconds != 45 || len(cfg.Sites) != 1 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Sites[0].BaseURL != "https://example.com" || cfg.Sites[0].UserID != 42 {
		t.Fatalf("unexpected site: %+v", cfg.Sites[0])
	}
	if !strings.Contains(stdout.String(), "imported 1 site(s)") {
		t.Fatalf("stdout missing import summary: %q", stdout.String())
	}
}
