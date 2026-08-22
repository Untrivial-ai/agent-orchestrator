// Package config loads and validates hosted control-plane configuration.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/auth"
)

const (
	defaultAddress         = "127.0.0.1:8080"
	defaultAccessTokenTTL  = 15 * time.Minute
	defaultRefreshTokenTTL = 30 * 24 * time.Hour
)

// Config is the validated configuration required by the control-plane API.
type Config struct {
	Address             string
	DatabaseURL         string
	GoogleIssuer        string
	GoogleJWKSURL       string
	GoogleClientIDs     []string
	AllowedEmails       []string
	AccessTokenKey      []byte
	AccessTokenIssuer   string
	AccessTokenAudience string
	AccessTokenTTL      time.Duration
	RefreshTokenTTL     time.Duration
	DaytonaAPIKey       string
	DaytonaAPIURL       string
	DaytonaTarget       string
	SandboxAOBinaryPath string
	GitHubToken         []byte
	PublicURL           string
}

// Load reads control-plane configuration from the process environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	accessTTL, err := durationValue(getenv("AO_CLOUD_ACCESS_TOKEN_TTL"), defaultAccessTokenTTL)
	if err != nil {
		return Config{}, fmt.Errorf("AO_CLOUD_ACCESS_TOKEN_TTL: %w", err)
	}
	refreshTTL, err := durationValue(getenv("AO_CLOUD_REFRESH_TOKEN_TTL"), defaultRefreshTokenTTL)
	if err != nil {
		return Config{}, fmt.Errorf("AO_CLOUD_REFRESH_TOKEN_TTL: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(getenv("AO_CLOUD_ACCESS_TOKEN_KEY_BASE64")))
	if err != nil {
		return Config{}, fmt.Errorf("AO_CLOUD_ACCESS_TOKEN_KEY_BASE64: %w", err)
	}
	githubToken, err := optionalBase64(getenv("AO_CLOUD_GITHUB_TOKEN_BASE64"))
	if err != nil {
		return Config{}, fmt.Errorf("AO_CLOUD_GITHUB_TOKEN_BASE64: %w", err)
	}
	cfg := Config{
		Address:             valueOrDefault(getenv("AO_CLOUD_ADDR"), defaultAddress),
		DatabaseURL:         strings.TrimSpace(getenv("AO_CLOUD_DATABASE_URL")),
		GoogleIssuer:        valueOrDefault(getenv("AO_CLOUD_GOOGLE_ISSUER"), auth.GoogleIssuer),
		GoogleJWKSURL:       valueOrDefault(getenv("AO_CLOUD_GOOGLE_JWKS_URL"), auth.GoogleJWKSURL),
		GoogleClientIDs:     splitValues(getenv("AO_CLOUD_GOOGLE_CLIENT_IDS")),
		AllowedEmails:       normalizedEmails(getenv("AO_CLOUD_ALLOWED_EMAILS")),
		AccessTokenKey:      key,
		AccessTokenIssuer:   valueOrDefault(getenv("AO_CLOUD_ACCESS_TOKEN_ISSUER"), "ao-cloud"),
		AccessTokenAudience: valueOrDefault(getenv("AO_CLOUD_ACCESS_TOKEN_AUDIENCE"), "ao-desktop"),
		AccessTokenTTL:      accessTTL,
		RefreshTokenTTL:     refreshTTL,
		DaytonaAPIKey:       strings.TrimSpace(getenv("DAYTONA_API_KEY")),
		DaytonaAPIURL:       valueOrDefault(getenv("DAYTONA_API_URL"), "https://app.daytona.io/api"),
		DaytonaTarget:       valueOrDefault(getenv("DAYTONA_TARGET"), "us"),
		SandboxAOBinaryPath: valueOrDefault(getenv("AO_CLOUD_SANDBOX_AO_BINARY"), "/ao"),
		GitHubToken:         githubToken,
		PublicURL:           strings.TrimRight(strings.TrimSpace(getenv("AO_CLOUD_PUBLIC_URL")), "/"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("AO_CLOUD_DATABASE_URL is required")
	}
	if len(cfg.GoogleClientIDs) == 0 {
		return Config{}, errors.New("AO_CLOUD_GOOGLE_CLIENT_IDS is required")
	}
	if len(cfg.AllowedEmails) == 0 {
		return Config{}, errors.New("AO_CLOUD_ALLOWED_EMAILS is required")
	}
	if len(cfg.AccessTokenKey) < 32 {
		return Config{}, errors.New("AO_CLOUD_ACCESS_TOKEN_KEY_BASE64 must decode to at least 32 bytes")
	}
	if cfg.DaytonaAPIKey != "" && cfg.PublicURL == "" {
		return Config{}, errors.New("AO_CLOUD_PUBLIC_URL is required when Daytona is configured")
	}
	return cfg, nil
}

func normalizedEmails(raw string) []string {
	values := splitValues(raw)
	for index := range values {
		values[index] = strings.ToLower(values[index])
	}
	return values
}

func optionalBase64(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("must be valid base64")
	}
	return value, nil
}

func durationValue(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	return value, nil
}

func valueOrDefault(raw, fallback string) string {
	if value := strings.TrimSpace(raw); value != "" {
		return value
	}
	return fallback
}

func splitValues(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}
