package checkin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	twoCaptchaAPIBaseURL   = "https://api.2captcha.com"
	twoCaptchaPollInterval = 5 * time.Second
	twoCaptchaHTTPTimeout  = 30 * time.Second
)

type twoCaptchaClient struct {
	apiKey       string
	baseURL      string
	httpClient   *http.Client
	pollInterval time.Duration
}

type twoCaptchaResponse struct {
	ErrorID          int                `json:"errorId"`
	ErrorCode        string             `json:"errorCode"`
	ErrorDescription string             `json:"errorDescription"`
	TaskID           int64              `json:"taskId"`
	Status           string             `json:"status"`
	Solution         twoCaptchaSolution `json:"solution"`
}

type twoCaptchaSolution struct {
	Text  string `json:"text"`
	Token string `json:"token"`
}

// TwoCaptchaOptions wires both supported verification flows to the official
// 2Captcha JSON API. The key may be empty for configurations containing only
// normal sites; verification attempts then fail with an explicit setup error.
func TwoCaptchaOptions(apiKey string) Options {
	client := newTwoCaptchaClient(apiKey)
	return Options{
		SolveCaptcha:   client.solveCaptcha,
		SolveTurnstile: client.solveTurnstile,
	}
}

func newTwoCaptchaClient(apiKey string) *twoCaptchaClient {
	return &twoCaptchaClient{
		apiKey:       strings.TrimSpace(apiKey),
		baseURL:      twoCaptchaAPIBaseURL,
		httpClient:   &http.Client{Timeout: twoCaptchaHTTPTimeout},
		pollInterval: twoCaptchaPollInterval,
	}
}

func (c *twoCaptchaClient) solveCaptcha(ctx context.Context, challenge CaptchaChallenge) (string, error) {
	if len(challenge.Image) == 0 {
		return "", fmt.Errorf("captcha image is empty")
	}

	taskID, err := c.createTask(ctx, map[string]any{
		"type": "ImageToTextTask",
		"body": base64.StdEncoding.EncodeToString(challenge.Image),
	})
	if err != nil {
		return "", err
	}
	return c.waitForResult(ctx, taskID, func(solution twoCaptchaSolution) string {
		return solution.Text
	})
}

func (c *twoCaptchaClient) solveTurnstile(ctx context.Context, challenge TurnstileChallenge) (string, error) {
	if strings.TrimSpace(challenge.SiteKey) == "" {
		return "", fmt.Errorf("turnstile site key is unavailable from /api/status")
	}
	if strings.TrimSpace(challenge.PageURL) == "" {
		return "", fmt.Errorf("turnstile page URL is empty")
	}

	taskID, err := c.createTask(ctx, map[string]any{
		"type":       "TurnstileTaskProxyless",
		"websiteURL": challenge.PageURL,
		"websiteKey": challenge.SiteKey,
	})
	if err != nil {
		return "", err
	}
	return c.waitForResult(ctx, taskID, func(solution twoCaptchaSolution) string {
		return solution.Token
	})
}

func (c *twoCaptchaClient) createTask(ctx context.Context, task map[string]any) (int64, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return 0, fmt.Errorf("TWOCAPTCHA_API_KEY is required")
	}

	var response twoCaptchaResponse
	if err := c.postJSON(ctx, "/createTask", map[string]any{
		"clientKey": c.apiKey,
		"task":      task,
	}, &response); err != nil {
		return 0, fmt.Errorf("2captcha create task: %w", err)
	}
	if err := response.apiError(); err != nil {
		return 0, fmt.Errorf("2captcha create task: %w", err)
	}
	if response.TaskID <= 0 {
		return 0, fmt.Errorf("2captcha create task returned no taskId")
	}
	return response.TaskID, nil
}

func (c *twoCaptchaClient) waitForResult(ctx context.Context, taskID int64, extract func(twoCaptchaSolution) string) (string, error) {
	interval := c.pollInterval
	if interval <= 0 {
		interval = twoCaptchaPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			var response twoCaptchaResponse
			if err := c.postJSON(ctx, "/getTaskResult", map[string]any{
				"clientKey": c.apiKey,
				"taskId":    taskID,
			}, &response); err != nil {
				return "", fmt.Errorf("2captcha get task result: %w", err)
			}
			if err := response.apiError(); err != nil {
				return "", fmt.Errorf("2captcha get task result: %w", err)
			}

			switch strings.ToLower(strings.TrimSpace(response.Status)) {
			case "processing":
				continue
			case "ready":
				value := strings.TrimSpace(extract(response.Solution))
				if value == "" {
					return "", fmt.Errorf("2captcha returned an empty solution")
				}
				return value, nil
			default:
				return "", fmt.Errorf("2captcha returned unexpected status %q", response.Status)
			}
		}
	}
}

func (c *twoCaptchaClient) postJSON(ctx context.Context, path string, requestBody any, responseBody any) error {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}

	requestURL := strings.TrimRight(c.baseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: twoCaptchaHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 240))
	}
	if err := json.Unmarshal(body, responseBody); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (r twoCaptchaResponse) apiError() error {
	if r.ErrorID == 0 {
		return nil
	}
	detail := firstNonEmptyString(r.ErrorDescription, r.ErrorCode, "unknown error")
	if r.ErrorCode != "" && !strings.Contains(detail, r.ErrorCode) {
		detail = r.ErrorCode + ": " + detail
	}
	return fmt.Errorf("%s", detail)
}
