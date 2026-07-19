package checkin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CaptchaChallenge is presented to a solver when a site requires captcha check-in.
type CaptchaChallenge struct {
	SiteName  string
	CaptchaID string
	Image     []byte
	MimeType  string
	ImagePath string // set when the image has been written to disk
}

// CaptchaSolver returns the captcha answer for a challenge.
type CaptchaSolver func(ctx context.Context, challenge CaptchaChallenge) (string, error)

// Options customizes a single-site check-in run.
type Options struct {
	// SolveCaptcha is required when the target site has captcha_enabled.
	// If nil and captcha is needed, check-in fails with a clear error.
	SolveCaptcha CaptchaSolver

	// CaptchaImageDir is where captcha images are saved for interactive/external solvers.
	// Empty means the system temp directory.
	CaptchaImageDir string

	// OpenCaptchaImage tries to open the captcha image with the OS default viewer
	// when using InteractiveCaptchaSolver.
	OpenCaptchaImage bool

	// TurnstileToken is a one-shot Cloudflare Turnstile response token
	// (query param ?turnstile= on POST /api/user/checkin).
	TurnstileToken string

	// SolveTurnstile obtains a Turnstile token when the site requires human verification.
	// Used when TurnstileToken is empty.
	SolveTurnstile TurnstileSolver

	// OpenTurnstilePage opens the site URL in a browser during interactive turnstile prompts.
	OpenTurnstilePage bool
}

type captchaPayload struct {
	ID       string
	Image    []byte
	MimeType string
}

// parseCaptchaPayload accepts the field-name variants used by NewAPI forks and
// produces a binary challenge independent of the original JSON layout.
func parseCaptchaPayload(payload map[string]any) (*captchaPayload, error) {
	if payload == nil {
		return nil, fmt.Errorf("captcha response is empty")
	}

	data := payload
	if nested, ok := payload["data"].(map[string]any); ok {
		data = nested
	}

	id := firstNonEmptyString(
		jsonString(data["captcha_id"]),
		jsonString(data["captchaId"]),
		jsonString(data["id"]),
	)
	if id == "" {
		return nil, fmt.Errorf("captcha response missing captcha_id")
	}

	rawImage := firstNonEmptyString(
		jsonString(data["image"]),
		jsonString(data["captcha_image"]),
		jsonString(data["captchaImage"]),
		jsonString(data["img"]),
		jsonString(data["base64"]),
	)
	if rawImage == "" {
		return nil, fmt.Errorf("captcha response missing image")
	}

	imageBytes, mimeType, err := decodeCaptchaImage(rawImage)
	if err != nil {
		return nil, err
	}
	return &captchaPayload{ID: id, Image: imageBytes, MimeType: mimeType}, nil
}

// decodeCaptchaImage supports both raw base64 and data URLs. Whitespace is
// removed because some servers wrap long encoded values; URL-safe base64 is a
// compatibility fallback for non-standard implementations.
func decodeCaptchaImage(raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", fmt.Errorf("empty captcha image")
	}

	mimeType := "image/png"
	encoded := raw
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma < 0 {
			return nil, "", fmt.Errorf("invalid captcha data URL")
		}
		meta := raw[:comma]
		encoded = raw[comma+1:]
		if semi := strings.IndexByte(meta, ';'); semi > 5 {
			mimeType = meta[5:semi]
		} else if strings.HasPrefix(meta, "data:") {
			mimeType = strings.TrimPrefix(meta, "data:")
		}
	}

	encoded = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, encoded)

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			return nil, "", fmt.Errorf("decode captcha image: %w", err)
		}
	}
	if len(decoded) == 0 {
		return nil, "", fmt.Errorf("decoded captcha image is empty")
	}
	return decoded, mimeType, nil
}

// saveCaptchaImage writes challenges with restrictive permissions and a
// filesystem-safe, bounded filename suitable for interactive viewing or an
// external OCR process.
func saveCaptchaImage(dir, siteName, captchaID string, image []byte, mimeType string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	ext := extensionForMime(mimeType)
	safeName := sanitizeFilePart(siteName)
	safeID := sanitizeFilePart(captchaID)
	if len(safeID) > 16 {
		safeID = safeID[:16]
	}
	filename := fmt.Sprintf("captcha_%s_%s_%d%s", safeName, safeID, time.Now().Unix(), ext)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, image, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func extensionForMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func sanitizeFilePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "site"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "site"
	}
	if len(out) > 32 {
		return out[:32]
	}
	return out
}

// InteractiveCaptchaSolver prompts the operator for the captcha answer on stdin.
// The image is written to disk; when openImage is true the OS default viewer is started.
func InteractiveCaptchaSolver(stdin io.Reader, stdout io.Writer, imageDir string, openImage bool) CaptchaSolver {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stderr
	}
	return func(ctx context.Context, challenge CaptchaChallenge) (string, error) {
		path := challenge.ImagePath
		if path == "" {
			var err error
			path, err = saveCaptchaImage(imageDir, challenge.SiteName, challenge.CaptchaID, challenge.Image, challenge.MimeType)
			if err != nil {
				return "", fmt.Errorf("save captcha image: %w", err)
			}
		}

		fmt.Fprintf(stdout, "\n[captcha] 站点=%q 需要验证码\n", challenge.SiteName)
		fmt.Fprintf(stdout, "[captcha] 图片已保存: %s\n", path)
		if openImage {
			if err := openFileWithDefaultApp(path); err != nil {
				fmt.Fprintf(stdout, "[captcha] 无法自动打开图片（请手动查看）: %v\n", err)
			}
		}
		fmt.Fprintf(stdout, "[captcha] 请输入验证码后回车: ")

		type readResult struct {
			line string
			err  error
		}
		ch := make(chan readResult, 1)
		go func() {
			var buf bytes.Buffer
			tmp := make([]byte, 1)
			for {
				n, err := stdin.Read(tmp)
				if n > 0 {
					if tmp[0] == '\n' {
						ch <- readResult{line: buf.String(), err: nil}
						return
					}
					if tmp[0] != '\r' {
						buf.WriteByte(tmp[0])
					}
				}
				if err != nil {
					ch <- readResult{line: buf.String(), err: err}
					return
				}
			}
		}()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case res := <-ch:
			answer := strings.TrimSpace(res.line)
			if res.err != nil && answer == "" {
				return "", fmt.Errorf("read captcha answer: %w", res.err)
			}
			if answer == "" {
				return "", fmt.Errorf("captcha answer is empty")
			}
			return answer, nil
		}
	}
}

// CommandCaptchaSolver runs an external program to recognize the captcha.
// The command string may contain "{image}" which is replaced with the image path;
// if the placeholder is absent, the image path is appended as the last argument.
// The first non-empty stdout line is used as the answer.
func CommandCaptchaSolver(command string) CaptchaSolver {
	command = strings.TrimSpace(command)
	return func(ctx context.Context, challenge CaptchaChallenge) (string, error) {
		if command == "" {
			return "", fmt.Errorf("captcha command is empty")
		}
		path := challenge.ImagePath
		if path == "" {
			return "", fmt.Errorf("captcha image path is required for external solver")
		}

		var cmd *exec.Cmd
		if strings.Contains(command, "{image}") {
			expanded := strings.ReplaceAll(command, "{image}", path)
			cmd = shellCommand(ctx, expanded)
		} else {
			// Append path as a separate argument when possible.
			parts := splitCommandLine(command)
			if len(parts) == 0 {
				return "", fmt.Errorf("captcha command is empty")
			}
			args := append(append([]string{}, parts[1:]...), path)
			cmd = exec.CommandContext(ctx, parts[0], args...)
		}

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = err.Error()
			}
			return "", fmt.Errorf("captcha command failed: %s", truncate(detail, 240))
		}

		for _, line := range strings.Split(stdout.String(), "\n") {
			answer := strings.TrimSpace(line)
			if answer != "" {
				return answer, nil
			}
		}
		return "", fmt.Errorf("captcha command returned empty answer")
	}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// splitCommandLine is a minimal splitter for simple "prog arg1 arg2" commands.
// Quotes are not fully supported; use {image} form for complex shells.
func splitCommandLine(command string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	flush := func() {
		if current.Len() > 0 {
			parts = append(parts, current.String())
			current.Reset()
		}
	}

	for i := 0; i < len(command); i++ {
		c := command[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
				continue
			}
			current.WriteByte(c)
			continue
		}
		switch c {
		case ' ', '\t':
			flush()
		case '"', '\'':
			inQuote = true
			quoteChar = c
		default:
			current.WriteByte(c)
		}
	}
	flush()
	return parts
}

func openFileWithDefaultApp(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}
