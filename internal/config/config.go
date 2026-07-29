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

	// SMTPRequireTLS, when true, aborts sending rather than falling back to a
	// plaintext session if the relay does not offer STARTTLS.
	SMTPRequireTLS bool

	// Stripe webhook signing secret. Checked only when non-empty.
	StripeWebhookSecret string

	// PayPal webhook ID for verification. Optional.
	PayPalWebhookID string

	// AllowUnsignedWebhooks permits webhook processing without signature
	// verification when the provider's secret is unset. Local development
	// only (e.g. Stripe CLI); production deployments must leave this false,
	// where unconfigured providers return 503 instead of accepting
	// unverified events.
	AllowUnsignedWebhooks bool

	// Public base URL used in email links (default: http://localhost:8080).
	BaseURL string

	// SecureCookies is true when BaseURL starts with "https://".
	// Set the Secure flag on HttpOnly cookies only when the service is reachable
	// over HTTPS — r.TLS is always nil behind a reverse proxy.
	SecureCookies bool

	// TrustProxyHeaders makes the rate limiter key on the X-Real-IP /
	// X-Forwarded-For header set by a trusted reverse proxy instead of the
	// socket address. Enable ONLY behind a proxy that strips client-supplied
	// forwarding headers (e.g. the k8s ingress); otherwise leave false so
	// clients cannot spoof their rate-limit key.
	TrustProxyHeaders bool

	// LogLevel sets the minimum structured-log level: debug, info, warn, or
	// error (default: info).
	LogLevel string

	// MetricsToken gates the Prometheus /metrics endpoint. When empty (default)
	// the endpoint is disabled entirely; when set, scrapers must present it as a
	// bearer token (Authorization: Bearer <token>) or ?token=<token>. This keeps
	// internal metrics from leaking when the service is exposed directly.
	MetricsToken string
}

// Load reads configuration from environment variables and returns a validated Config.
// Returns an error if any required field is absent or any value is unparseable.
func Load() (*Config, error) {
	cfg := &Config{
		Port:        getEnv("QUORUM_PORT", "8080"),
		DatabaseURL: getEnv("QUORUM_DATABASE_URL", ""),
		JWTSecret:   getEnv("QUORUM_JWT_SECRET", ""),
		BaseURL:     getEnv("QUORUM_BASE_URL", "http://localhost:8080"),

		SMTPHost:       getEnv("QUORUM_SMTP_HOST", ""),
		SMTPUser:       getEnv("QUORUM_SMTP_USER", ""),
		SMTPPass:       getEnv("QUORUM_SMTP_PASS", ""),
		EmailFrom:      getEnv("QUORUM_EMAIL_FROM", "quorum@localhost"),
		SMTPRequireTLS: getEnv("QUORUM_SMTP_REQUIRE_TLS", "") == "true",

		StripeWebhookSecret: getEnv("QUORUM_STRIPE_WEBHOOK_SECRET", ""),
		PayPalWebhookID:     getEnv("QUORUM_PAYPAL_WEBHOOK_ID", ""),

		AllowUnsignedWebhooks: getEnv("QUORUM_ALLOW_UNSIGNED_WEBHOOKS", "") == "true",
		TrustProxyHeaders:     getEnv("QUORUM_TRUST_PROXY_HEADERS", "") == "true",

		LogLevel:     getEnv("QUORUM_LOG_LEVEL", "info"),
		MetricsToken: getEnv("QUORUM_METRICS_TOKEN", ""),
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
	// Refuse well-known placeholder secrets from deployment templates: they
	// pass the length check but are public knowledge, letting anyone forge
	// admin tokens.
	if strings.Contains(strings.ToUpper(cfg.JWTSecret), "CHANGEME") {
		return nil, fmt.Errorf("QUORUM_JWT_SECRET is a placeholder value; generate a real secret (e.g. `make secret`)")
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
