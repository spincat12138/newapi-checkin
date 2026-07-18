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

func main() {
	if len(os.Args) >= 2 {
		switch strings.ToLower(os.Args[1]) {
		case "import":
			os.Exit(runImport(os.Args[2:]))
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
  newapi-checkin import [flags]          从 Octopus accounts 备份 JSON 导入配置
  newapi-checkin help                    显示帮助

签到参数:
  -config string    配置文件路径 (默认 "config.yaml")
  -log string       签到日志文件路径 (默认 "checkin.log"，追加写入)
  -only string      只签到名称包含关键字的站点（逗号分隔）
  -timeout int      覆盖超时秒数（0=使用配置）

导入参数:
  -from string              Octopus accounts 备份 JSON 路径（必填）
  -out string               输出 config.yaml 路径（默认 "config.yaml"）
  -include-disabled         同时导入已禁用账号
  -require-auto-checkin     仅导入开启了自动签到的账号
  -timeout int              生成配置中的 timeout_seconds（默认 30）

示例:
  newapi-checkin import -from accounts-backup.json -out config.yaml
  newapi-checkin -config config.yaml
  newapi-checkin -config config.yaml -only "ZMoon,烁"
`)
}

func runCheckin(args []string) int {
	fs := flag.NewFlagSet("checkin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "config.yaml", "path to config file")
	logPath := fs.String("log", defaultLogPath, "check-in log file path (append mode)")
	only := fs.String("only", "", "only checkin sites whose name contains this keyword (comma separated)")
	timeout := fs.Int("timeout", 0, "override timeout seconds (0 = use config)")
	if err := fs.Parse(args); err != nil {
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

	fmt.Fprintf(output, "NewAPI Checkin - %d site(s)\n", len(sites))
	fmt.Fprintln(output, strings.Repeat("-", 48))

	successCount := 0
	failCount := 0

	for i, site := range sites {
		fmt.Fprintf(output, "[%d/%d] %s (%s)\n", i+1, len(sites), site.Name, site.BaseURL)

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		result := checkin.Run(ctx, site)
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

func runImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	from := fs.String("from", "", "path to Octopus accounts backup JSON")
	out := fs.String("out", "config.yaml", "output config.yaml path")
	includeDisabled := fs.Bool("include-disabled", false, "import disabled accounts")
	requireAutoCheckIn := fs.Bool("require-auto-checkin", false, "only import accounts with auto check-in enabled")
	timeout := fs.Int("timeout", 30, "timeout_seconds written into generated config")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if strings.TrimSpace(*from) == "" {
		fmt.Fprintln(os.Stderr, "import: -from is required")
		fmt.Fprintln(os.Stderr, `usage: newapi-checkin import -from accounts-backup.json [-out config.yaml]`)
		return 1
	}

	result, err := config.ImportOctopusFile(*from, config.OctopusImportOptions{
		IncludeDisabled:    *includeDisabled,
		RequireAutoCheckIn: *requireAutoCheckIn,
		TimeoutSeconds:     *timeout,
	})
	if err != nil {
		// Still print partial skip info when available.
		if result != nil {
			printImportSkips(result)
		}
		fmt.Fprintf(os.Stderr, "import failed: %v\n", err)
		return 1
	}

	if err := config.Save(*out, result.Config); err != nil {
		fmt.Fprintf(os.Stderr, "write config failed: %v\n", err)
		return 1
	}

	fmt.Printf("imported %d site(s) -> %s\n", result.Imported, *out)
	printImportSkips(result)
	for i, site := range result.Config.Sites {
		fmt.Printf("  %2d. %s  %s  uid=%d  platform=%s\n", i+1, site.Name, site.BaseURL, site.UserID, site.Platform)
	}
	return 0
}

func printImportSkips(result *config.ImportResult) {
	if result == nil || len(result.Skipped) == 0 {
		return
	}
	fmt.Printf("skipped %d account(s):\n", len(result.Skipped))
	for _, s := range result.Skipped {
		fmt.Printf("  - %s\n", s)
	}
}

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
