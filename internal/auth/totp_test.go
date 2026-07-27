package auth

import (
	"strings"
	"testing"
	"time"
)

func TestTOTP_RoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Now()
	code, err := totpAt(secret, uint64(now.Unix()/totpPeriod))
	if err != nil {
		t.Fatalf("totpAt: %v", err)
	}
	if len(code) != totpDigits {
		t.Fatalf("code length: got %d want %d", len(code), totpDigits)
	}
	if !ValidateTOTP(secret, code, now) {
		t.Errorf("ValidateTOTP rejected a freshly generated code")
	}
}

func TestTOTP_AllowsOneStepSkew(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Now()
	prev, _ := totpAt(secret, uint64(now.Add(-30*time.Second).Unix()/totpPeriod))
	next, _ := totpAt(secret, uint64(now.Add(30*time.Second).Unix()/totpPeriod))
	if !ValidateTOTP(secret, prev, now) {
		t.Error("expected the previous 30s window to be accepted")
	}
	if !ValidateTOTP(secret, next, now) {
		t.Error("expected the next 30s window to be accepted")
	}
}

func TestTOTP_RejectsWrongAndTwoStepSkew(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Now()
	if ValidateTOTP(secret, "000000", now) && ValidateTOTP(secret, "123456", now) {
		t.Error("expected at least one arbitrary code to be rejected")
	}
	// A code two windows away must fail (only ±1 is tolerated).
	twoBack, _ := totpAt(secret, uint64(now.Add(-90*time.Second).Unix()/totpPeriod))
	if ValidateTOTP(secret, twoBack, now) {
		t.Error("expected a two-window-old code to be rejected")
	}
	if ValidateTOTP(secret, "", now) {
		t.Error("expected empty code to be rejected")
	}
}

func TestGenerateRecoveryCodes(t *testing.T) {
	plain, hashed, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(plain) != 10 || len(hashed) != 10 {
		t.Fatalf("counts: got %d/%d want 10/10", len(plain), len(hashed))
	}
	seen := map[string]bool{}
	for i, code := range plain {
		if seen[code] {
			t.Errorf("duplicate recovery code %q", code)
		}
		seen[code] = true
		if HashRefreshToken(code) != hashed[i] {
			t.Errorf("hash mismatch at %d", i)
		}
	}
}

func TestTOTPProvisioningURI(t *testing.T) {
	uri := TOTPProvisioningURI("ABC234", "user@example.com", "Quorum")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("bad scheme: %s", uri)
	}
	for _, want := range []string{"secret=ABC234", "issuer=Quorum", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("uri missing %q: %s", want, uri)
		}
	}
}
