package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	Sites          []Site `yaml:"sites"`
}

const (
	CredentialAccessToken      = "access_token"
	CredentialSessionCookie    = "session_cookie"
	CredentialUsernamePassword = "username_password"
)

type Site struct {
	Name           string            `yaml:"name"`
	BaseURL        string            `yaml:"base_url"`
	Platform       string            `yaml:"platform"`
	CredentialType string            `yaml:"credential_type"`
	AccessToken    string            `yaml:"access_token"`
	SessionCookie  string            `yaml:"session_cookie"`
	Username       string            `yaml:"username"`
	Password       string            `yaml:"password"`
	UserID         int               `yaml:"user_id"`
	Headers        map[string]string `yaml:"headers"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := normalize(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes config as YAML. Parent directories are created if needed.
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := normalize(cfg); err != nil {
		return err
	}

	node := buildExportNode(cfg)
	var buf strings.Builder
	buf.WriteString("# NewAPI / AnyRouter 站点签到配置\n")
	buf.WriteString("# 可由 `import` 子命令从 Octopus accounts 备份 JSON 生成\n\n")

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
	}

	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func normalize(cfg *Config) error {
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	if len(cfg.Sites) == 0 {
		return fmt.Errorf("config has no sites")
	}

	for i := range cfg.Sites {
		site := &cfg.Sites[i]
		site.Name = strings.TrimSpace(site.Name)
		site.BaseURL = strings.TrimRight(strings.TrimSpace(site.BaseURL), "/")
		site.Platform = strings.ToLower(strings.TrimSpace(site.Platform))
		site.CredentialType = strings.ToLower(strings.TrimSpace(site.CredentialType))
		site.AccessToken = strings.TrimSpace(site.AccessToken)
		site.SessionCookie = strings.TrimSpace(site.SessionCookie)
		site.Username = strings.TrimSpace(site.Username)
		site.Password = strings.TrimSpace(site.Password)

		if site.Name == "" {
			site.Name = fmt.Sprintf("site-%d", i+1)
		}
		if site.BaseURL == "" {
			return fmt.Errorf("site %q: base_url is required", site.Name)
		}
		if site.Platform == "" {
			site.Platform = "new-api"
		}
		if site.CredentialType == "" {
			site.CredentialType = inferCredentialType(site)
		}

		if err := validateSiteCredential(site); err != nil {
			return fmt.Errorf("site %q: %w", site.Name, err)
		}
	}
	return nil
}

// buildExportNode emits a clean YAML map, omitting empty optional fields.
func buildExportNode(cfg *Config) map[string]any {
	sites := make([]map[string]any, 0, len(cfg.Sites))
	for _, s := range cfg.Sites {
		item := map[string]any{
			"name":            s.Name,
			"base_url":        s.BaseURL,
			"platform":        s.Platform,
			"credential_type": s.CredentialType,
		}
		switch s.CredentialType {
		case CredentialAccessToken:
			item["access_token"] = s.AccessToken
		case CredentialSessionCookie:
			item["session_cookie"] = s.SessionCookie
		case CredentialUsernamePassword:
			item["username"] = s.Username
			item["password"] = s.Password
		}
		if s.UserID > 0 {
			item["user_id"] = s.UserID
		}
		if len(s.Headers) > 0 {
			item["headers"] = s.Headers
		}
		sites = append(sites, item)
	}
	return map[string]any{
		"timeout_seconds": cfg.TimeoutSeconds,
		"sites":           sites,
	}
}

func inferCredentialType(site *Site) string {
	switch {
	case site == nil:
		return ""
	case site.SessionCookie != "":
		return CredentialSessionCookie
	case site.AccessToken != "":
		return CredentialAccessToken
	default:
		return CredentialUsernamePassword
	}
}

func validateSiteCredential(site *Site) error {
	if site == nil {
		return fmt.Errorf("credential is required")
	}

	switch site.CredentialType {
	case CredentialAccessToken:
		if site.AccessToken == "" {
			return fmt.Errorf("access_token is required")
		}
		if site.SessionCookie != "" || site.Username != "" || site.Password != "" {
			return fmt.Errorf("access_token credentials cannot include session_cookie, username, or password")
		}
	case CredentialSessionCookie:
		if site.SessionCookie == "" {
			return fmt.Errorf("session_cookie is required")
		}
		if err := validateSessionCookie(site.SessionCookie); err != nil {
			return err
		}
		if site.AccessToken != "" || site.Username != "" || site.Password != "" {
			return fmt.Errorf("session_cookie credentials cannot include access_token, username, or password")
		}
	case CredentialUsernamePassword:
		if site.Username == "" || site.Password == "" {
			return fmt.Errorf("username and password are required")
		}
		if site.AccessToken != "" || site.SessionCookie != "" {
			return fmt.Errorf("username_password credentials cannot include access_token or session_cookie")
		}
	default:
		return fmt.Errorf("unsupported credential_type %q", site.CredentialType)
	}
	return nil
}

func validateSessionCookie(value string) error {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "cookie:") {
		return fmt.Errorf("session_cookie must contain only the Cookie header value, without the Cookie: prefix")
	}

	firstPair := value
	if separator := strings.IndexByte(firstPair, ';'); separator >= 0 {
		firstPair = firstPair[:separator]
	}
	equals := strings.IndexByte(firstPair, '=')
	if equals <= 0 || equals == len(firstPair)-1 {
		return fmt.Errorf("session_cookie must use name=value format")
	}
	return nil
}
