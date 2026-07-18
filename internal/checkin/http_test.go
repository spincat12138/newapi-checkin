package checkin

import (
	"testing"

	"newapi-checkin/internal/config"
)

func TestBuildAuthHeaderVariantsUsesOnlyAuthorizationForAccessToken(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyzABCDE="
	variants := buildAuthHeaderVariants(authCredential{
		Type:  config.CredentialAccessToken,
		Value: token,
	})

	assertHeaderVariant(t, variants, "Authorization", token)
	assertHeaderVariant(t, variants, "Authorization", "Bearer "+token)

	for _, variant := range variants {
		if variant["Cookie"] != "" {
			t.Fatalf("access token must not be sent as a cookie: %#v", variant)
		}
	}
}

func TestBuildAuthHeaderVariantsUsesOnlyCookieForSessionCookie(t *testing.T) {
	variants := buildAuthHeaderVariants(authCredential{
		Type:  config.CredentialSessionCookie,
		Value: "session=session-value",
	})
	if len(variants) != 1 {
		t.Fatalf("expected one session-cookie variant, got %d", len(variants))
	}
	if got := variants[0]["Cookie"]; got != "session=session-value" {
		t.Fatalf("expected session cookie to remain unchanged, got %q", got)
	}
	if got := variants[0]["Authorization"]; got != "" {
		t.Fatalf("session cookie must not send Authorization, got %q", got)
	}
}

func assertHeaderVariant(t *testing.T, variants []map[string]string, header, value string) {
	t.Helper()
	for _, variant := range variants {
		if variant[header] == value {
			return
		}
	}
	t.Fatalf("missing header variant %s=%q in %#v", header, value, variants)
}
