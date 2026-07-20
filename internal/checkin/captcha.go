package checkin

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

// CaptchaChallenge is presented to a solver when a site requires captcha check-in.
type CaptchaChallenge struct {
	SiteName  string
	CaptchaID string
	Image     []byte
	MimeType  string
}

// CaptchaSolver returns the captcha answer for a challenge.
type CaptchaSolver func(ctx context.Context, challenge CaptchaChallenge) (string, error)

// Options customizes a single-site check-in run.
type Options struct {
	// Solvers are injected so the protocol can be tested without calling the
	// external service. The CLI always wires both fields to 2Captcha.
	SolveCaptcha   CaptchaSolver
	SolveTurnstile TurnstileSolver
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
