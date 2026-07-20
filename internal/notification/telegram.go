package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"newapi-checkin/internal/checkin"
	"newapi-checkin/internal/config"
	"newapi-checkin/internal/report"
)

const (
	telegramAPIBaseURL          = "https://api.telegram.org"
	telegramRichMessageMaxRunes = 32768
	telegramRichMessageMaxRows  = 480
	maxSiteCellRunes            = 80
	maxAmountCellRunes          = 32
	maxRemarkCellRunes          = 300
)

type inputRichMessage struct {
	Markdown            string `json:"markdown"`
	SkipEntityDetection bool   `json:"skip_entity_detection"`
}

type telegramMessageRequest struct {
	ChatID      string           `json:"chat_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

type telegramAPIResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

var richMarkdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	`|`, `\|`,
	"`", "\\`",
	`*`, `\*`,
	`_`, `\_`,
	`~`, `\~`,
	`$`, `\$`,
	`[`, `\[`,
	`]`, `\]`,
	`<`, `\<`,
	`>`, `\>`,
)

// SendTelegram sends all check-in results as one or more native Rich Markdown
// messages. Telegram Bot API 10.1+ renders the GFM-style table directly.
func SendTelegram(ctx context.Context, cfg config.TelegramConfig, results []checkin.Result) error {
	if !cfg.Enabled || len(results) == 0 {
		return nil
	}

	client, err := newTelegramHTTPClient(cfg.ProxyURL)
	if err != nil {
		return err
	}
	endpoint := telegramAPIBaseURL + "/bot" + cfg.BotToken + "/sendRichMessage"
	for i, message := range buildTelegramMessages(results) {
		if err := sendTelegramMessage(ctx, client, endpoint, cfg.ChatID, message); err != nil {
			return fmt.Errorf("send Telegram message %d: %w", i+1, err)
		}
	}
	return nil
}

func newTelegramHTTPClient(rawProxyURL string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if strings.TrimSpace(rawProxyURL) != "" {
		proxyURL, err := url.Parse(rawProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse Telegram proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}, nil
}

func sendTelegramMessage(ctx context.Context, client *http.Client, endpoint, chatID, message string) error {
	payload, err := json.Marshal(telegramMessageRequest{
		ChatID: chatID,
		RichMessage: inputRichMessage{
			Markdown:            message,
			SkipEntityDetection: true,
		},
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Telegram request: invalid API endpoint")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request Telegram API: %v", redactRequestURL(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read Telegram response: %w", err)
	}

	var apiResponse telegramAPIResponse
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		return fmt.Errorf("decode Telegram response (http %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !apiResponse.OK {
		description := strings.TrimSpace(apiResponse.Description)
		if description == "" {
			description = "unknown error"
		}
		return fmt.Errorf("Telegram API http %d error %d: %s", resp.StatusCode, apiResponse.ErrorCode, description)
	}
	return nil
}

func redactRequestURL(err error) error {
	for err != nil {
		var urlError *url.Error
		if !errors.As(err, &urlError) {
			return err
		}
		err = urlError.Err
	}
	return fmt.Errorf("unknown network error")
}

func buildTelegramMessages(results []checkin.Result) []string {
	const tableHeader = "| 站点 | 是否签到成功 | 本次签到余额 | 历史总余额 | 备注 |\n" +
		"| :--- | :---: | ---: | ---: | :--- |\n"
	const prefix = "# 签到结果\n\n"

	messages := make([]string, 0, 1)
	rows := make([]string, 0, len(results))
	for _, result := range results {
		row := formatTelegramRow(result)
		candidate := prefix + tableHeader + strings.Join(append(rows, row), "")
		if len(rows) > 0 && (len(rows) >= telegramRichMessageMaxRows || utf8.RuneCountInString(candidate) > telegramRichMessageMaxRunes) {
			messages = append(messages, prefix+tableHeader+strings.Join(rows, ""))
			rows = rows[:0]
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		messages = append(messages, prefix+tableHeader+strings.Join(rows, ""))
	}
	return messages
}

func formatTelegramRow(result checkin.Result) string {
	success := "否"
	if result.Success {
		success = "是"
	}

	return fmt.Sprintf(
		"| %s | %s | %s | %s | %s |\n",
		formatTelegramCell(result.Site, maxSiteCellRunes),
		success,
		formatTelegramCell(report.FormatUSD(result.RewardUSD), maxAmountCellRunes),
		formatTelegramCell(report.FormatUSD(result.TotalBalanceUSD), maxAmountCellRunes),
		formatTelegramCell(resultRemark(result), maxRemarkCellRunes),
	)
}

func resultRemark(result checkin.Result) string {
	if !result.Success {
		if strings.TrimSpace(result.Error) != "" {
			return result.Error
		}
		if strings.TrimSpace(result.Message) != "" {
			return result.Message
		}
		return "签到失败"
	}
	if strings.TrimSpace(result.BalanceError) != "" {
		return "余额查询失败: " + result.BalanceError
	}
	return "-"
}

func formatTelegramCell(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	value = truncateRunes(value, maxRunes)
	if value == "" {
		return "-"
	}
	return escapeRichMarkdown(value)
}

func escapeRichMarkdown(value string) string {
	return richMarkdownEscaper.Replace(value)
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 3 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes-3]) + "..."
}
