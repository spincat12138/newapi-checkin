package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"newapi-checkin/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run keeps CLI concerns outside internal/config: it validates flags, delegates
// conversion and persistence, then prints both imported and skipped accounts.
// The injected writers make all user-visible branches deterministic in tests.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(stderr) }

	from := fs.String("from", "", "path to Octopus accounts backup JSON")
	out := fs.String("out", "config.yaml", "output config.yaml path")
	includeDisabled := fs.Bool("include-disabled", false, "import disabled accounts")
	requireAutoCheckIn := fs.Bool("require-auto-checkin", false, "only import accounts with auto check-in enabled")
	timeout := fs.Int("timeout", 30, "timeout_seconds written into generated config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "import-config: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 1
	}
	if strings.TrimSpace(*from) == "" {
		fmt.Fprintln(stderr, "import-config: -from is required")
		fs.Usage()
		return 1
	}

	result, err := config.ImportOctopusFile(*from, config.OctopusImportOptions{
		IncludeDisabled:    *includeDisabled,
		RequireAutoCheckIn: *requireAutoCheckIn,
		TimeoutSeconds:     *timeout,
	})
	if err != nil {
		printImportSkips(stdout, result)
		fmt.Fprintf(stderr, "import failed: %v\n", err)
		return 1
	}

	if err := config.Save(*out, result.Config); err != nil {
		fmt.Fprintf(stderr, "write config failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "imported %d site(s) -> %s\n", result.Imported, *out)
	printImportSkips(stdout, result)
	for i, site := range result.Config.Sites {
		fmt.Fprintf(stdout, "  %2d. %s  %s  uid=%d  platform=%s\n", i+1, site.Name, site.BaseURL, site.UserID, site.Platform)
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `NewAPI Config Import - Octopus / AionUi 配置转换工具

用法:
  newapi-import-config -from accounts-backup.json [flags]

参数:
  -from string              Octopus accounts 备份 JSON 路径（必填）
  -out string               输出 config.yaml 路径（默认 "config.yaml"）
  -include-disabled         同时导入已禁用账号
  -require-auto-checkin     仅导入开启了自动签到的账号
  -timeout int              生成配置中的 timeout_seconds（默认 30）

示例:
  newapi-import-config -from accounts-backup.json -out config.yaml
`)
}

// printImportSkips is shared by successful partial imports and failed imports
// that still produced a diagnostic ImportResult.
func printImportSkips(w io.Writer, result *config.ImportResult) {
	if result == nil || len(result.Skipped) == 0 {
		return
	}
	fmt.Fprintf(w, "skipped %d account(s):\n", len(result.Skipped))
	for _, skipped := range result.Skipped {
		fmt.Fprintf(w, "  - %s\n", skipped)
	}
}
