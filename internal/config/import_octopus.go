package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// OctopusImportOptions controls how accounts backup JSON is converted.
type OctopusImportOptions struct {
	// IncludeDisabled imports accounts with disabled=true.
	IncludeDisabled bool
	// RequireAutoCheckIn only imports accounts with checkIn.autoCheckInEnabled=true.
	RequireAutoCheckIn bool
	// TimeoutSeconds written into the generated config (default 30).
	TimeoutSeconds int
}

// ImportResult summarizes an Octopus accounts import.
type ImportResult struct {
	Config   *Config
	Imported int
	Skipped  []string
}

// octopusBackup is the top-level structure of Octopus / AionUi accounts backup JSON.
type octopusBackup struct {
	Version  any              `json:"version"`
	Type     string           `json:"type"`
	Accounts *octopusAccounts `json:"accounts"`
}

type octopusAccounts struct {
	Accounts []octopusAccount `json:"accounts"`
}

type octopusAccount struct {
	ID          string             `json:"id"`
	SiteName    string             `json:"site_name"`
	SiteURL     string             `json:"site_url"`
	SiteType    string             `json:"site_type"`
	Disabled    bool               `json:"disabled"`
	AuthType    string             `json:"authType"`
	AccountInfo octopusAccountInfo `json:"account_info"`
	CookieAuth  *octopusCookieAuth `json:"cookieAuth"`
	CheckIn     *octopusCheckIn    `json:"checkIn"`
}

type octopusAccountInfo struct {
	ID          any    `json:"id"`
	AccessToken string `json:"access_token"`
	Username    string `json:"username"`
}

type octopusCookieAuth struct {
	SessionCookie string `json:"sessionCookie"`
}

type octopusCheckIn struct {
	AutoCheckInEnabled bool `json:"autoCheckInEnabled"`
	EnableDetection    bool `json:"enableDetection"`
}

// ImportOctopusFile reads an Octopus accounts backup JSON and converts it to Config.
func ImportOctopusFile(path string, opts OctopusImportOptions) (*ImportResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read octopus json: %w", err)
	}
	return ImportOctopus(data, opts)
}

// ImportOctopus converts Octopus accounts backup JSON bytes into Config.
func ImportOctopus(data []byte, opts OctopusImportOptions) (*ImportResult, error) {
	if opts.TimeoutSeconds <= 0 {
		opts.TimeoutSeconds = 30
	}

	var backup octopusBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		return nil, fmt.Errorf("parse octopus json: %w", err)
	}

	accounts, err := extractOctopusAccounts(&backup, data)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no accounts found in octopus json")
	}

	result := &ImportResult{
		Config: &Config{
			TimeoutSeconds: opts.TimeoutSeconds,
			Sites:          make([]Site, 0, len(accounts)),
		},
		Skipped: make([]string, 0),
	}

	// Import is intentionally best-effort per account. Structural JSON failures
	// abort the operation, while unsupported or incomplete accounts are recorded
	// in Skipped so valid peers can still be exported.
	for _, acc := range accounts {
		name := strings.TrimSpace(acc.SiteName)
		if name == "" {
			name = strings.TrimSpace(acc.ID)
		}
		if name == "" {
			name = "unnamed"
		}

		if acc.Disabled && !opts.IncludeDisabled {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: disabled", name))
			continue
		}

		if opts.RequireAutoCheckIn {
			if acc.CheckIn == nil || !acc.CheckIn.AutoCheckInEnabled {
				result.Skipped = append(result.Skipped, fmt.Sprintf("%s: auto check-in disabled", name))
				continue
			}
		}

		platform, ok := mapOctopusPlatform(acc.SiteType)
		if !ok {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: unsupported site_type %q", name, acc.SiteType))
			continue
		}

		baseURL := strings.TrimRight(strings.TrimSpace(acc.SiteURL), "/")
		if baseURL == "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: empty site_url", name))
			continue
		}

		credentialType, credentialValue := resolveOctopusCredential(acc)
		if credentialValue == "" {
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: no access_token/session cookie", name))
			continue
		}
		if credentialType == CredentialSessionCookie {
			if err := validateSessionCookie(credentialValue); err != nil {
				result.Skipped = append(result.Skipped, fmt.Sprintf("%s: invalid session cookie: %v", name, err))
				continue
			}
		}

		site := Site{
			Name:           name,
			BaseURL:        baseURL,
			Platform:       platform,
			CredentialType: credentialType,
			UserID:         parseOctopusUserID(acc.AccountInfo.ID),
		}
		if credentialType == CredentialSessionCookie {
			site.SessionCookie = credentialValue
		} else {
			site.AccessToken = credentialValue
		}
		result.Config.Sites = append(result.Config.Sites, site)
		result.Imported++
	}

	if result.Imported == 0 {
		return result, fmt.Errorf("no importable sites found (%d skipped)", len(result.Skipped))
	}
	return result, nil
}

// extractOctopusAccounts supports both wrapped backup format and a bare accounts array.
func extractOctopusAccounts(backup *octopusBackup, raw []byte) ([]octopusAccount, error) {
	if backup.Accounts != nil && len(backup.Accounts.Accounts) > 0 {
		return backup.Accounts.Accounts, nil
	}

	// Fallback: {"accounts":[...]} without nested accounts.accounts
	var flat struct {
		Accounts []octopusAccount `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &flat); err == nil && len(flat.Accounts) > 0 {
		return flat.Accounts, nil
	}

	// Fallback: bare array
	var arr []octopusAccount
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}

	if backup.Accounts != nil {
		return backup.Accounts.Accounts, nil
	}
	return nil, fmt.Errorf("unrecognized octopus accounts json format")
}

func mapOctopusPlatform(siteType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(siteType)) {
	case "new-api":
		return "new-api", true
	case "anyrouter", "any-router":
		return "any-router", true
	case "one-api":
		return "one-api", true
	case "veloera":
		return "veloera", true
	case "done-hub", "donehub":
		return "done-hub", true
	case "new-api-like", "newapilike":
		return "new-api-like", true
	case "unknown", "":
		// Octopus sometimes labels NewAPI-compatible sites as unknown.
		return "new-api-like", true
	default:
		return "", false
	}
}

// resolveOctopusCredential follows the backup's declared auth type when the
// corresponding value exists, then falls back to the other usable credential.
// This tolerates older backups whose authType and populated fields disagree.
func resolveOctopusCredential(acc octopusAccount) (string, string) {
	authType := strings.ToLower(strings.TrimSpace(acc.AuthType))
	token := strings.TrimSpace(acc.AccountInfo.AccessToken)
	cookie := ""
	if acc.CookieAuth != nil {
		cookie = strings.TrimSpace(acc.CookieAuth.SessionCookie)
	}

	switch authType {
	case "cookie":
		if cookie != "" {
			return CredentialSessionCookie, cookie
		}
		return CredentialAccessToken, token
	default:
		if token != "" {
			return CredentialAccessToken, token
		}
		return CredentialSessionCookie, cookie
	}
}

// parseOctopusUserID accepts the loose scalar types produced by encoding/json
// and older backup exporters. Invalid or non-positive IDs become zero so the
// runtime can attempt discovery instead of emitting a misleading identifier.
func parseOctopusUserID(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case float32:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return n
	default:
		s := strings.TrimSpace(fmt.Sprint(t))
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return n
	}
}
