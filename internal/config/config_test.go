package config_test

import (
	"os"
	"testing"
	"time"

	"quorum/internal/config"
)

func setEnv(t *testing.T, pairs ...string) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		old, exists := os.LookupEnv(pairs[i])
		key := pairs[i]
		if exists {
			t.Cleanup(func() { os.Setenv(key, old) })
		} else {
			t.Cleanup(func() { os.Unsetenv(key) })
		}
		os.Setenv(pairs[i], pairs[i+1])
	}
}

func baseEnv(t *testing.T) {
	t.Helper()
	setEnv(t,
		"QUORUM_DATABASE_URL", "postgres://u:p@localhost/db",
		"QUORUM_JWT_SECRET", "a-test-secret-that-is-at-least-32-chars!",
	)
	// Clear optional env vars that might bleed in from a real .env.
	for _, k := range []string{
		"QUORUM_PORT", "QUORUM_JWT_ACCESS_TTL", "QUORUM_JWT_REFRESH_TTL",
		"QUORUM_SMTP_PORT", "QUORUM_BASE_URL",
	} {
		old, exists := os.LookupEnv(k)
		if exists {
			k := k
			t.Cleanup(func() { os.Setenv(k, old) })
		}
		os.Unsetenv(k)
	}
}

func TestLoad_Defaults(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "8080")
	}
	if cfg.JWTAccessTTL != 15*time.Minute {
		t.Errorf("JWTAccessTTL: got %v, want 15m", cfg.JWTAccessTTL)
	}
	if cfg.JWTRefreshTTL != 168*time.Hour {
		t.Errorf("JWTRefreshTTL: got %v, want 168h", cfg.JWTRefreshTTL)
	}
	if cfg.SMTPPort != 587 {
		t.Errorf("SMTPPort: got %d, want 587", cfg.SMTPPort)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL: got %q", cfg.BaseURL)
	}
}

func TestLoad_RequiredFields(t *testing.T) {
	// Missing DATABASE_URL.
	setEnv(t, "QUORUM_DATABASE_URL", "", "QUORUM_JWT_SECRET", "s")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error when DATABASE_URL is missing")
	}
}

func TestLoad_RequiredJWTSecret(t *testing.T) {
	setEnv(t, "QUORUM_DATABASE_URL", "postgres://x", "QUORUM_JWT_SECRET", "")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error when JWT_SECRET is missing")
	}
}

func TestLoad_JWTSecretTooShort(t *testing.T) {
	setEnv(t, "QUORUM_DATABASE_URL", "postgres://x", "QUORUM_JWT_SECRET", "tooshort")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error when JWT_SECRET is shorter than 32 characters")
	}
}

func TestLoad_CustomPort(t *testing.T) {
	baseEnv(t)
	setEnv(t, "QUORUM_PORT", "9090")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "9090")
	}
}

func TestLoad_CustomDurations(t *testing.T) {
	baseEnv(t)
	setEnv(t, "QUORUM_JWT_ACCESS_TTL", "5m", "QUORUM_JWT_REFRESH_TTL", "24h")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.JWTAccessTTL != 5*time.Minute {
		t.Errorf("JWTAccessTTL: got %v", cfg.JWTAccessTTL)
	}
	if cfg.JWTRefreshTTL != 24*time.Hour {
		t.Errorf("JWTRefreshTTL: got %v", cfg.JWTRefreshTTL)
	}
}

func TestLoad_InvalidAccessTTL(t *testing.T) {
	baseEnv(t)
	setEnv(t, "QUORUM_JWT_ACCESS_TTL", "not-a-duration")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid JWT_ACCESS_TTL")
	}
}

func TestLoad_InvalidRefreshTTL(t *testing.T) {
	baseEnv(t)
	setEnv(t, "QUORUM_JWT_REFRESH_TTL", "bad")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid JWT_REFRESH_TTL")
	}
}

func TestLoad_InvalidSMTPPort(t *testing.T) {
	baseEnv(t)
	setEnv(t, "QUORUM_SMTP_HOST", "smtp.example.com", "QUORUM_SMTP_PORT", "notaport")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid SMTP_PORT")
	}
}

func TestLoad_OptionalSMTP(t *testing.T) {
	baseEnv(t)
	setEnv(t,
		"QUORUM_SMTP_HOST", "smtp.example.com",
		"QUORUM_SMTP_USER", "user@example.com",
		"QUORUM_SMTP_PASS", "pass",
		"QUORUM_EMAIL_FROM", "noreply@example.com",
	)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SMTPHost != "smtp.example.com" {
		t.Errorf("SMTPHost: got %q", cfg.SMTPHost)
	}
	if cfg.EmailFrom != "noreply@example.com" {
		t.Errorf("EmailFrom: got %q", cfg.EmailFrom)
	}
}

func TestLoad_PaymentSecrets(t *testing.T) {
	baseEnv(t)
	setEnv(t,
		"QUORUM_STRIPE_WEBHOOK_SECRET", "whsec_abc",
		"QUORUM_PAYPAL_WEBHOOK_ID", "pp-id-123",
	)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StripeWebhookSecret != "whsec_abc" {
		t.Errorf("StripeWebhookSecret: got %q", cfg.StripeWebhookSecret)
	}
	if cfg.PayPalWebhookID != "pp-id-123" {
		t.Errorf("PayPalWebhookID: got %q", cfg.PayPalWebhookID)
	}
}
