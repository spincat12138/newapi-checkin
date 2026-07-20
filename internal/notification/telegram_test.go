package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"newapi-checkin/internal/checkin"
)

func TestBuildTelegramMessagesFormatsMarkdownTable(t *testing.T) {
	reward := 0.005
	balance := 2.5
	results := []checkin.Result{
		{
			Site:            "成功|站点",
			Success:         true,
			RewardUSD:       &reward,
			TotalBalanceUSD: &balance,
		},
		{
			Site:    "失败站点",
			Success: false,
			Error:   "请求失败\nHTTP 500",
		},
	}

	messages := buildTelegramMessages(results)
	if len(messages) != 1 {
		t.Fatalf("message count=%d want 1", len(messages))
	}
	message := messages[0]
	if strings.Contains(message, "```") {
		t.Fatalf("rich message table must not be wrapped in a code block: %s", message)
	}
	for _, want := range []string{
		"# 签到结果",
		"| 站点 | 是否签到成功 | 本次签到余额 | 历史总余额 | 备注 |",
		"| 成功\\|站点 | 是 | \\$0.005 | \\$2.50 | - |",
		"| 失败站点 | 否 | 不可用 | 不可用 | 请求失败 HTTP 500 |",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

func TestBuildTelegramMessagesIncludesBalanceErrorRemark(t *testing.T) {
	results := []checkin.Result{{
		Site:         "站点",
		Success:      true,
		BalanceError: "接口不可用",
	}}

	message := buildTelegramMessages(results)[0]
	if !strings.Contains(message, "余额查询失败: 接口不可用") {
		t.Fatalf("message missing balance error: %s", message)
	}
}

func TestBuildTelegramMessagesSplitsAtTelegramLimit(t *testing.T) {
	results := make([]checkin.Result, 0, 150)
	for i := 0; i < 150; i++ {
		results = append(results, checkin.Result{
			Site:    fmt.Sprintf("site-%02d", i),
			Success: false,
			Error:   strings.Repeat("失败原因", 75),
		})
	}

	messages := buildTelegramMessages(results)
	if len(messages) < 2 {
		t.Fatalf("expected split messages, got %d", len(messages))
	}
	for i, message := range messages {
		if got := utf8.RuneCountInString(message); got > telegramRichMessageMaxRunes {
			t.Fatalf("message %d has %d runes", i, got)
		}
	}
}

func TestBuildTelegramMessagesSplitsAtRichMessageRowLimit(t *testing.T) {
	results := make([]checkin.Result, telegramRichMessageMaxRows+1)
	for i := range results {
		results[i] = checkin.Result{Site: fmt.Sprintf("site-%03d", i), Success: true}
	}

	messages := buildTelegramMessages(results)
	if got, want := len(messages), 2; got != want {
		t.Fatalf("message count=%d want %d", got, want)
	}
}

func TestFormatTelegramCellEscapesRichMarkdown(t *testing.T) {
	got := formatTelegramCell("site_* | $1 [link] <tag> `code` \\ path", 100)
	for _, want := range []string{`\_`, `\*`, `\|`, `\$`, `\[`, `\]`, `\<`, `\>`, "\\`", `\\`} {
		if !strings.Contains(got, want) {
			t.Fatalf("escaped cell missing %q: %s", want, got)
		}
	}
}

func TestSendTelegramMessagePostsRichMarkdownJSON(t *testing.T) {
	var request telegramMessageRequest
	var rawRequest []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s want POST", r.Method)
		}
		var err error
		rawRequest, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		} else if err := json.Unmarshal(rawRequest, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()

	err := sendTelegramMessage(context.Background(), server.Client(), server.URL, "-100123", "table")
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	if request.ChatID != "-100123" || request.RichMessage.Markdown != "table" {
		t.Fatalf("unexpected request: %+v", request)
	}
	if !request.RichMessage.SkipEntityDetection {
		t.Fatal("entity detection should be disabled")
	}
	if strings.Contains(string(rawRequest), "parse_mode") || strings.Contains(string(rawRequest), `"text"`) {
		t.Fatalf("legacy sendMessage fields found in request: %s", rawRequest)
	}
}

func TestSendTelegramMessageReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	}))
	defer server.Close()

	err := sendTelegramMessage(context.Background(), server.Client(), server.URL, "missing", "table")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected Telegram API error, got %v", err)
	}
}

func TestSendTelegramMessageRedactsBotTokenFromNetworkError(t *testing.T) {
	const token = "123456:secret-token"
	client := &http.Client{
		Timeout: time.Second,
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + token + "/sendRichMessage", Err: fmt.Errorf("proxy refused connection")}
		}),
	}

	err := sendTelegramMessage(context.Background(), client, "https://api.telegram.org/bot"+token+"/sendRichMessage", "1", "table")
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked bot token: %v", err)
	}
}

func TestNewTelegramHTTPClientUsesConfiguredProxy(t *testing.T) {
	client, err := newTelegramHTTPClient("http://127.0.0.1:7890")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	req := httptest.NewRequest(http.MethodGet, "https://api.telegram.org", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("resolve proxy: %v", err)
	}
	if got, want := proxyURL.String(), "http://127.0.0.1:7890"; got != want {
		t.Fatalf("proxy=%q want %q", got, want)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
