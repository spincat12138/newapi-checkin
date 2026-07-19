package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newapi-checkin/internal/checkin"
)

func TestOpenCheckinOutputAppendsToLogFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "checkin.log")

	for _, line := range []string{"first run\n", "second run\n"} {
		var console bytes.Buffer
		output, closeLog, err := openCheckinOutput(logPath, &console)
		if err != nil {
			t.Fatalf("open log output: %v", err)
		}
		fmt.Fprint(output, line)
		if err := closeLog(); err != nil {
			t.Fatalf("close log output: %v", err)
		}
		if console.String() != line {
			t.Fatalf("expected console output %q, got %q", line, console.String())
		}
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if got, want := string(content), "first run\nsecond run\n"; got != want {
		t.Fatalf("unexpected appended log\nwant: %q\n got: %q", want, got)
	}
}

func TestRunCheckinRejectsImportSubcommand(t *testing.T) {
	originalStderr := os.Stderr
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = writeEnd
	t.Cleanup(func() {
		os.Stderr = originalStderr
		readEnd.Close()
		writeEnd.Close()
	})

	code := runCheckin([]string{"import", "-from", "accounts-backup.json"})
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	os.Stderr = originalStderr

	output, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if code != 1 {
		t.Fatalf("exit code=%d want 1", code)
	}
	if !strings.Contains(string(output), `unexpected argument "import"`) {
		t.Fatalf("stderr missing positional argument error: %q", output)
	}
}

func TestOpenCheckinOutputRejectsEmptyPath(t *testing.T) {
	if _, _, err := openCheckinOutput("  ", &bytes.Buffer{}); err == nil {
		t.Fatal("expected empty log path to fail")
	}
}

func TestPrintCheckinLog(t *testing.T) {
	reward := 0.005
	total := 2.5
	result := checkin.Result{
		Site:            "test-site",
		CheckedAt:       time.Date(2026, time.July, 18, 22, 30, 1, 0, time.Local),
		Success:         true,
		RewardUSD:       &reward,
		TotalBalanceUSD: &total,
	}

	var output bytes.Buffer
	printCheckinLog(&output, result)

	want := "  [2026-07-18 22:30:01] 站点=\"test-site\" 签到成功=是 本次获得=$0.005 总余额=$2.50\n"
	if output.String() != want {
		t.Fatalf("unexpected log output\nwant: %q\n got: %q", want, output.String())
	}
}

func TestPrintCheckinLogMarksUnknownValues(t *testing.T) {
	result := checkin.Result{
		Site:      "failed-site",
		CheckedAt: time.Date(2026, time.July, 18, 22, 31, 2, 0, time.Local),
	}

	var output bytes.Buffer
	printCheckinLog(&output, result)

	want := "  [2026-07-18 22:31:02] 站点=\"failed-site\" 签到成功=否 本次获得=不可用 总余额=不可用\n"
	if output.String() != want {
		t.Fatalf("unexpected log output\nwant: %q\n got: %q", want, output.String())
	}
}
