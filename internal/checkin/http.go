package checkin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"newapi-checkin/internal/config"
)

var defaultHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	},
}

type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Payload    map[string]any
}

func buildSiteURL(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

func applyDefaultHeaders(req *http.Request, hasBody bool) {
	if hasBody && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/plain, */*")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	}
	if req.Header.Get("Cache-Control") == "" {
		req.Header.Set("Cache-Control", "no-store")
	}
}

func doRequest(ctx context.Context, site config.Site, method, requestURL string, body any, headers map[string]string) (*httpResult, error) {
	var bodyReader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return nil, err
	}
	applyDefaultHeaders(req, body != nil)

	// NewAPI sites often check Origin/Referer.
	origin := parseOrigin(site.BaseURL)
	if origin != "" {
		if req.Header.Get("Origin") == "" {
			req.Header.Set("Origin", origin)
		}
		if req.Header.Get("Referer") == "" {
			req.Header.Set("Referer", origin+"/")
		}
	}

	for key, value := range site.Headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result := &httpResult{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       bodyBytes,
	}

	if isCloudflareResponse(resp.StatusCode, resp.Header, bodyBytes) {
		return result, fmt.Errorf("cloudflare protection (http %d)", resp.StatusCode)
	}

	if len(bodyBytes) > 0 && (bytes.HasPrefix(bytes.TrimSpace(bodyBytes), []byte("{")) || bytes.HasPrefix(bytes.TrimSpace(bodyBytes), []byte("["))) {
		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err == nil {
			result.Payload = payload
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := ""
		if result.Payload != nil {
			msg = extractResponseMessage(result.Payload)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(bodyBytes))
		}
		if msg == "" {
			return result, fmt.Errorf("http %d", resp.StatusCode)
		}
		return result, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(msg, 240))
	}

	if result.Payload == nil && len(bodyBytes) > 0 {
		return result, fmt.Errorf("decode response failed: %s", truncate(string(bodyBytes), 120))
	}
	if result.Payload == nil {
		result.Payload = map[string]any{}
	}
	return result, nil
}

func requestJSON(ctx context.Context, site config.Site, method, requestURL string, body any, headers map[string]string) (map[string]any, error) {
	result, err := doRequest(ctx, site, method, requestURL, body, headers)
	if err != nil {
		return nil, err
	}
	return result.Payload, nil
}

func isCloudflareResponse(statusCode int, header http.Header, body []byte) bool {
	if statusCode != 403 && statusCode != 503 && statusCode != 429 {
		return false
	}
	server := strings.ToLower(header.Get("Server"))
	if strings.Contains(server, "cloudflare") {
		return true
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "cloudflare") &&
		(strings.Contains(text, "attention required") ||
			strings.Contains(text, "just a moment") ||
			strings.Contains(text, "cf-browser-verification") ||
			strings.Contains(text, "checking your browser"))
}

func extractResponseMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	return firstNonEmptyString(
		jsonString(payload["message"]),
		jsonString(payload["msg"]),
		jsonString(payload["error"]),
		jsonString(nestedValue(payload, "data", "message")),
		jsonString(nestedValue(payload, "data", "msg")),
	)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func ensureBearer(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

// buildAuthHeaderVariants returns several auth header styles used by NewAPI-like sites.
func buildAuthHeaderVariants(credential authCredential) []map[string]string {
	credential.Value = strings.TrimSpace(credential.Value)
	if credential.Value == "" {
		return nil
	}

	variants := make([]map[string]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(h map[string]string) {
		key := ""
		for k, v := range h {
			key += k + "=" + v + ";"
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		variants = append(variants, h)
	}

	switch credential.Type {
	case config.CredentialAccessToken:
		add(map[string]string{"Authorization": credential.Value})
		add(map[string]string{"Authorization": ensureBearer(credential.Value)})
	case config.CredentialSessionCookie:
		add(map[string]string{"Cookie": credential.Value})
	}
	return variants
}

func managedUserIDHeaders(userID int) map[string]string {
	if userID <= 0 {
		return nil
	}
	value := fmt.Sprintf("%d", userID)
	return map[string]string{
		"New-API-User": value,
		"New-Api-User": value,
		"Veloera-User": value,
		"voapi-user":   value,
		"Done-User":    value,
		"User-id":      value,
		"X-User-Id":    value,
		"Rix-Api-User": value,
		"neo-api-user": value,
	}
}

func mergeHeaders(parts ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, part := range parts {
		for k, v := range part {
			if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
				out[k] = v
			}
		}
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func jsonString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	case json.Number:
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func jsonBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "ok", "success":
			return true
		default:
			return false
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	case json.Number:
		n, err := v.Float64()
		return err == nil && n != 0
	default:
		return false
	}
}

func nestedValue(root map[string]any, keys ...string) any {
	var current any = root
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[key]
		if !ok {
			return nil
		}
	}
	return current
}

func parseOrigin(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	if u.Scheme == "" || u.Host == "" {
		return baseURL
	}
	return u.Scheme + "://" + u.Host
}

func isUserIDHeaderError(err error, payload map[string]any) bool {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if payload != nil {
		msg = firstNonEmptyString(msg, extractResponseMessage(payload))
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "new-api-user") ||
		strings.Contains(lower, "new-api user") ||
		strings.Contains(lower, "user id") ||
		strings.Contains(lower, "userid") ||
		strings.Contains(msg, "用户") && strings.Contains(msg, "不匹配") ||
		strings.Contains(msg, "缺少") && strings.Contains(strings.ToLower(msg), "user")
}
