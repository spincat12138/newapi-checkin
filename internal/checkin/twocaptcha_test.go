package checkin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTwoCaptchaSolvesImageTask(t *testing.T) {
	var createPayload map[string]any
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/createTask":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode create task: %v", err)
			}
			fmt.Fprint(w, `{"errorId":0,"taskId":101}`)
		case "/getTaskResult":
			polls++
			if polls == 1 {
				fmt.Fprint(w, `{"errorId":0,"status":"processing"}`)
				return
			}
			fmt.Fprint(w, `{"errorId":0,"status":"ready","solution":{"text":"AB12"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testTwoCaptchaClient(server)
	answer, err := client.solveCaptcha(context.Background(), CaptchaChallenge{Image: []byte("image-bytes")})
	if err != nil {
		t.Fatalf("solve captcha: %v", err)
	}
	if answer != "AB12" {
		t.Fatalf("answer=%q", answer)
	}
	if polls != 2 {
		t.Fatalf("polls=%d want 2", polls)
	}
	if createPayload["clientKey"] != "test-api-key" {
		t.Fatalf("unexpected client key payload")
	}
	task, _ := createPayload["task"].(map[string]any)
	if task["type"] != "ImageToTextTask" || strings.TrimSpace(jsonString(task["body"])) == "" {
		t.Fatalf("unexpected image task: %#v", task)
	}
}

func TestTwoCaptchaSolvesTurnstileTask(t *testing.T) {
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/createTask":
			if err := json.NewDecoder(r.Body).Decode(&createPayload); err != nil {
				t.Errorf("decode create task: %v", err)
			}
			fmt.Fprint(w, `{"errorId":0,"taskId":202}`)
		case "/getTaskResult":
			fmt.Fprint(w, `{"errorId":0,"status":"ready","solution":{"token":"0.turnstile-token"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := testTwoCaptchaClient(server)
	token, err := client.solveTurnstile(context.Background(), TurnstileChallenge{
		PageURL: "https://example.com/",
		SiteKey: "0x-site-key",
	})
	if err != nil {
		t.Fatalf("solve turnstile: %v", err)
	}
	if token != "0.turnstile-token" {
		t.Fatalf("token=%q", token)
	}
	task, _ := createPayload["task"].(map[string]any)
	if task["type"] != "TurnstileTaskProxyless" || task["websiteURL"] != "https://example.com/" || task["websiteKey"] != "0x-site-key" {
		t.Fatalf("unexpected turnstile task: %#v", task)
	}
}

func TestTwoCaptchaReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errorId":1,"errorCode":"ERROR_KEY_DOES_NOT_EXIST","errorDescription":"invalid key"}`)
	}))
	defer server.Close()

	client := testTwoCaptchaClient(server)
	_, err := client.solveCaptcha(context.Background(), CaptchaChallenge{Image: []byte("image")})
	if err == nil || !strings.Contains(err.Error(), "ERROR_KEY_DOES_NOT_EXIST") {
		t.Fatalf("expected API error, got %v", err)
	}
}

func TestTwoCaptchaRequiresAPIKey(t *testing.T) {
	client := newTwoCaptchaClient("")
	_, err := client.solveCaptcha(context.Background(), CaptchaChallenge{Image: []byte("image")})
	if err == nil || !strings.Contains(err.Error(), "TWOCAPTCHA_API_KEY") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func testTwoCaptchaClient(server *httptest.Server) *twoCaptchaClient {
	client := newTwoCaptchaClient("test-api-key")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	client.pollInterval = time.Millisecond
	return client
}
