package config

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestLoadRequiresHostedIdentityAndDatabaseSettings(t *testing.T) {
	values := map[string]string{
		"AO_CLOUD_DATABASE_URL":            "postgres://runtime@example.test/ao",
		"AO_CLOUD_GOOGLE_CLIENT_IDS":       "desktop, web ",
		"AO_CLOUD_ALLOWED_EMAILS":          " Person@Example.com,other@example.com ",
		"AO_CLOUD_ACCESS_TOKEN_KEY_BASE64": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		"AO_CLOUD_ACCESS_TOKEN_TTL":        "10m",
		"AO_CLOUD_REFRESH_TOKEN_TTL":       "720h",
	}
	cfg, err := load(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != defaultAddress || len(cfg.GoogleClientIDs) != 2 ||
		cfg.AccessTokenTTL != 10*time.Minute || cfg.RefreshTokenTTL != 30*24*time.Hour ||
		len(cfg.AllowedEmails) != 2 || cfg.AllowedEmails[0] != "person@example.com" {
		t.Fatalf("config = %#v", cfg)
	}

	delete(values, "AO_CLOUD_GOOGLE_CLIENT_IDS")
	if _, err := load(func(key string) string { return values[key] }); err == nil {
		t.Fatal("missing Google client IDs were accepted")
	}
	values["AO_CLOUD_GOOGLE_CLIENT_IDS"] = "desktop"
	delete(values, "AO_CLOUD_ALLOWED_EMAILS")
	if _, err := load(func(key string) string { return values[key] }); err == nil {
		t.Fatal("missing allowed emails were accepted")
	}
}

func TestLoadRejectsWeakSigningKey(t *testing.T) {
	values := map[string]string{
		"AO_CLOUD_DATABASE_URL":            "postgres://runtime@example.test/ao",
		"AO_CLOUD_GOOGLE_CLIENT_IDS":       "desktop",
		"AO_CLOUD_ALLOWED_EMAILS":          "person@example.com",
		"AO_CLOUD_ACCESS_TOKEN_KEY_BASE64": base64.StdEncoding.EncodeToString([]byte("too-short")),
	}
	if _, err := load(func(key string) string { return values[key] }); err == nil {
		t.Fatal("weak access-token key was accepted")
	}
}
