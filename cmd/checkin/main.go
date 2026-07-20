package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"newapi-checkin/internal/checkin"
	"newapi-checkin/internal/config"
)

const defaultLogPath = "checkin.log"

// main handles the special help spelling before delegating to the testable
// runCheckin function. os.Exit remains here so deferred cleanup inside
// runCheckin can complete normally.
func main() {
	if len(os.Args) >= 2 {
		switch strings.ToLower(os.Args[1]) {
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}
	os.Exit(runCheckin(os.Args[1:]))
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `NewAPI Checkin - 站点签到工具

用法:
  newapi-checkin [flags]                 执行签到
  newapi-checkin help                    显示帮助

签到参数:
  -config string           配置文件路径 (默认 "config.yaml")
  -log string              签到日志文件路径 (默认 "checkin.log"，追加写入)
  -only string             只签到名称包含关键字的站点（逗号分隔）
  -timeout int             覆盖超时秒数（0=使用配置；不含 2Captcha 等待）

环境变量:
  TWOCAPTCHA_API_KEY       CAPTCHA / Turnstile 全自动求解所需的 2Captcha API Key

示例:
  newapi-checkin -config config.yaml
  newapi-checkin -config config.yaml -only "ZMoon,烁"
  $env:TWOCAPTCHA_API_KEY = "你的 2Captcha API Key"
  newapi-checkin -config config.yaml -only "简直了,cngov"
`)
}

// runCheckin is the CLI orchestration boundary: it parses flags, loads and
// filters sites, executes them serially, mirrors output to the append-only log,
// and converts the aggregate result into the documented process exit code.
func runCheckin(args []string) int {
	fs := flag.NewFlagSet("checkin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = printUsage
	configPath := fs.String("config", "config.yaml", "path to config file")
	logPath := fs.String("log", defaultLogPath, "check-in log file path (append mode)")
	only := fs.String("only", "", "only checkin sites whose name contains this keyword (comma separated)")
	timeout := fs.Int("timeout", 0, "override timeout seconds (0 = use config)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "checkin: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 1
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		return 1
	}

	timeoutSec := cfg.TimeoutSeconds
	if *timeout > 0 {
		timeoutSec = *timeout
	}

	filters := parseOnly(*only)
	sites := make([]config.Site, 0, len(cfg.Sites))
	for _, site := range cfg.Sites {
		if matchOnly(site.Name, filters) {
			sites = append(sites, site)
		}
	}
	if len(sites) == 0 {
		fmt.Fprintln(os.Stderr, "no sites matched")
		return 1
	}

	output, closeLog, err := openCheckinOutput(*logPath, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open check-in log failed: %v\n", err)
		return 1
	}

	opts := checkin.TwoCaptchaOptions(os.Getenv("TWOCAPTCHA_API_KEY"))

	fmt.Fprintf(output, "NewAPI Checkin - %d site(s)\n", len(sites))
	fmt.Fprintln(output, strings.Repeat("-", 48))

	successCount := 0
	failCount := 0

	for i, site := range sites {
		fmt.Fprintf(output, "[%d/%d] %s (%s)\n", i+1, len(sites), site.Name, site.BaseURL)

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		result := checkin.RunWithOptions(ctx, site, opts)
		cancel()

		if result.Success {
			successCount++
			fmt.Fprintf(output, "  OK  %s\n", result.Message)
		} else {
			failCount++
			fmt.Fprintf(output, "  FAIL %s\n", result.Error)
		}
		printCheckinLog(output, result)
		if result.BalanceError != "" {
			fmt.Fprintf(output, "  余额查询失败: %s\n", result.BalanceError)
		}
	}

	fmt.Fprintln(output, strings.Repeat("-", 48))
	fmt.Fprintf(output, "done: success=%d fail=%d\n", successCount, failCount)
	if err := closeLog(); err != nil {
		fmt.Fprintf(os.Stderr, "write check-in log failed: %v\n", err)
		return 1
	}
	if failCount > 0 {
		return 2
	}
	return 0
}

// errorTrackingWriter lets io.MultiWriter keep writing to the console after the
// log file fails. The first log error is retained and reported when closeLog is
// called, so a visible run cannot be mistaken for a durably recorded run.
type errorTrackingWriter struct {
	writer io.Writer
	err    error
}

func (w *errorTrackingWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return len(p), nil
	}

	written, err := w.writer.Write(p)
	if err != nil {
		w.err = err
		return len(p), nil
	}
	if written != len(p) {
		w.err = io.ErrShortWrite
		return len(p), nil
	}
	return written, nil
}

// openCheckinOutput returns a tee writer plus an explicit finalizer. Log files
// use restrictive permissions and append mode so each invocation preserves the
// previous audit trail.
func openCheckinOutput(logPath string, console io.Writer) (io.Writer, func() error, error) {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return nil, nil, fmt.Errorf("log path is required")
	}

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}

	trackedLog := &errorTrackingWriter{writer: file}
	output := io.MultiWriter(trackedLog, console)
	closeLog := func() error {
		return errors.Join(trackedLog.err, file.Sync(), file.Close())
	}
	return output, closeLog, nil
}

// printCheckinLog emits the stable, machine-greppable per-site summary line.
func printCheckinLog(w io.Writer, result checkin.Result) {
	success := "否"
	if result.Success {
		success = "是"
	}

	fmt.Fprintf(
		w,
		"  [%s] 站点=%q 签到成功=%s 本次获得=%s 总余额=%s\n",
		result.CheckedAt.Format("2006-01-02 15:04:05"),
		result.Site,
		success,
		formatUSD(result.RewardUSD),
		formatUSD(result.TotalBalanceUSD),
	)
}

// formatUSD keeps unavailable values distinct from a real zero balance while
// avoiding noisy trailing fractional zeros.
func formatUSD(value *float64) string {
	if value == nil {
		return "不可用"
	}

	formatted := strconv.FormatFloat(*value, 'f', 6, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if !strings.Contains(formatted, ".") {
		formatted += ".00"
	} else if len(formatted)-strings.LastIndex(formatted, ".") == 2 {
		formatted += "0"
	}
	return "$" + formatted
}

// parseOnly normalizes the comma-separated filter once; matchOnly can then use
// simple case-insensitive substring checks for every configured site.
func parseOnly(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

// matchOnly applies OR semantics: a site is selected when any filter occurs in
// its display name. An empty filter list selects every site.
func matchOnly(name string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	lower := strings.ToLower(name)
	for _, f := range filters {
		if strings.Contains(lower, f) {
			return true
		}
	}
	return false
}
