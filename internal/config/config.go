// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for the Quorum service.
// Required fields: DatabaseURL, JWTSecret.
// All other fields have defaults or are optional.
type Config struct {
	// HTTP listen port (default: 8080).
	Port string

	// PostgreSQL connection string. Required.
	DatabaseURL string

	// HMAC-SHA256 secret for signing JWT tokens. Required.
	JWTSecret string

	// Lifetime of access tokens (default: 15m).
	JWTAccessTTL time.Duration

	// Lifetime of refresh tokens (default: 168h / 7 days).
	JWTRefreshTTL time.Duration

	// SMTP connection details for outbound email reminders. Optional.
	SMTPHost  string
	SMTPPort  int
	SMTPUser  string
	SMTPPass  string
	EmailFrom string

	// Stripe webhook signing secret. Checked only when non-empty.
	StripeWebhookSecret string

	// PayPal webhook ID for verification. Optional.
	PayPalWebhookID string

	// Public base URL used in email links (default: http://localhost:8080).
	BaseURL string

	// SecureCookies is true when BaseURL starts with "https://".
	// Set the Secure flag on HttpOnly cookies only when the service is reachable
	// over HTTPS — r.TLS is always nil behind a reverse proxy.
	SecureCookies bool
}

// Load reads configuration from environment variables and returns a validated Config.
// Returns an error if any required field is absent or any value is unparseable.
func Load() (*Config, error) {
	cfg := &Config{
		Port:        getEnv("QUORUM_PORT", "8080"),
		DatabaseURL: getEnv("QUORUM_DATABASE_URL", ""),
		JWTSecret:   getEnv("QUORUM_JWT_SECRET", ""),
		BaseURL:     getEnv("QUORUM_BASE_URL", "http://localhost:8080"),

		SMTPHost:  getEnv("QUORUM_SMTP_HOST", ""),
		SMTPUser:  getEnv("QUORUM_SMTP_USER", ""),
		SMTPPass:  getEnv("QUORUM_SMTP_PASS", ""),
		EmailFrom: getEnv("QUORUM_EMAIL_FROM", "quorum@localhost"),

		StripeWebhookSecret: getEnv("QUORUM_STRIPE_WEBHOOK_SECRET", ""),
		PayPalWebhookID:     getEnv("QUORUM_PAYPAL_WEBHOOK_ID", ""),
	}

	cfg.SecureCookies = strings.HasPrefix(cfg.BaseURL, "https://")

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("QUORUM_DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("QUORUM_JWT_SECRET is required")
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, fmt.Errorf("QUORUM_JWT_SECRET must be at least 32 characters")
	}

	var err error

	accessTTL := getEnv("QUORUM_JWT_ACCESS_TTL", "15m")
	cfg.JWTAccessTTL, err = time.ParseDuration(accessTTL)
	if err != nil {
		return nil, fmt.Errorf("invalid QUORUM_JWT_ACCESS_TTL: %w", err)
	}

	refreshTTL := getEnv("QUORUM_JWT_REFRESH_TTL", "168h")
	cfg.JWTRefreshTTL, err = time.ParseDuration(refreshTTL)
	if err != nil {
		return nil, fmt.Errorf("invalid QUORUM_JWT_REFRESH_TTL: %w", err)
	}

	cfg.SMTPPort = 587
	if cfg.SMTPHost != "" {
		smtpPort := getEnv("QUORUM_SMTP_PORT", "587")
		cfg.SMTPPort, err = strconv.Atoi(smtpPort)
		if err != nil {
			return nil, fmt.Errorf("invalid QUORUM_SMTP_PORT: %w", err)
		}
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
