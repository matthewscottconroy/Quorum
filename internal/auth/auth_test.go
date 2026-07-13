package auth_test

import (
	"strings"
	"testing"
	"time"

	"quorum/internal/auth"
)

// ---- HashPassword / CheckPassword ----

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if !auth.CheckPassword(hash, "correct-horse-battery") {
		t.Error("CheckPassword: expected true for matching password")
	}
	if auth.CheckPassword(hash, "wrong-password") {
		t.Error("CheckPassword: expected false for wrong password")
	}
}

func TestHashPassword_DifferentHashesForSameInput(t *testing.T) {
	h1, _ := auth.HashPassword("same-password")
	h2, _ := auth.HashPassword("same-password")
	if h1 == h2 {
		t.Error("expected different bcrypt hashes (random salt) for same password")
	}
}

func TestCheckPassword_EmptyInputs(t *testing.T) {
	if auth.CheckPassword("", "") {
		t.Error("empty hash/password should return false")
	}
	if auth.CheckPassword("badsalt", "password") {
		t.Error("invalid hash should return false")
	}
}

// ---- IssueAccessToken / ParseToken ----

func TestIssueAndParseToken(t *testing.T) {
	secret := "test-secret-value"
	tok, err := auth.IssueAccessToken("user-123", "admin", secret, time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	claims, err := auth.ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID: got %q, want %q", claims.UserID, "user-123")
	}
	if claims.Role != "admin" {
		t.Errorf("Role: got %q, want %q", claims.Role, "admin")
	}
}

func TestParseToken_WrongSecret(t *testing.T) {
	tok, _ := auth.IssueAccessToken("u1", "member", "secret-a", time.Minute)
	_, err := auth.ParseToken(tok, "secret-b")
	if err == nil {
		t.Error("expected error when verifying with wrong secret")
	}
}

func TestParseToken_Expired(t *testing.T) {
	tok, _ := auth.IssueAccessToken("u1", "member", "secret", -time.Second)
	_, err := auth.ParseToken(tok, "secret")
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestParseToken_Malformed(t *testing.T) {
	_, err := auth.ParseToken("not.a.jwt", "secret")
	if err == nil {
		t.Error("expected error for malformed token")
	}
}

func TestParseToken_Empty(t *testing.T) {
	_, err := auth.ParseToken("", "secret")
	if err == nil {
		t.Error("expected error for empty token string")
	}
}

// ---- GenerateRefreshToken ----

func TestGenerateRefreshToken(t *testing.T) {
	plain, hashed, err := auth.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if len(plain) == 0 {
		t.Error("plain token should not be empty")
	}
	if len(hashed) == 0 {
		t.Error("hashed token should not be empty")
	}
	if plain == hashed {
		t.Error("plain and hashed should differ")
	}
	// The hash must be reproducible via HashRefreshToken.
	if auth.HashRefreshToken(plain) != hashed {
		t.Error("HashRefreshToken(plain) != hashed returned by GenerateRefreshToken")
	}
}

func TestGenerateRefreshToken_Uniqueness(t *testing.T) {
	p1, _, _ := auth.GenerateRefreshToken()
	p2, _, _ := auth.GenerateRefreshToken()
	if p1 == p2 {
		t.Error("successive refresh tokens should be unique")
	}
}

// ---- HashRefreshToken ----

func TestHashRefreshToken_Deterministic(t *testing.T) {
	if auth.HashRefreshToken("abc") != auth.HashRefreshToken("abc") {
		t.Error("HashRefreshToken must be deterministic")
	}
	if auth.HashRefreshToken("abc") == auth.HashRefreshToken("xyz") {
		t.Error("different inputs should produce different hashes")
	}
}

func TestHashRefreshToken_Length(t *testing.T) {
	h := auth.HashRefreshToken("anything")
	if len(h) != 64 {
		t.Errorf("expected 64-char SHA-256 hex string, got %d chars", len(h))
	}
}

// ---- TokenFromHeader ----

func TestTokenFromHeader(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc.def.ghi", "abc.def.ghi"},
		{"Bearer ", ""},
		{"abc.def.ghi", "abc.def.ghi"}, // no prefix — returned unchanged
		{"", ""},
	}
	for _, tc := range cases {
		got := auth.TokenFromHeader(tc.header)
		if got != tc.want {
			t.Errorf("TokenFromHeader(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestTokenFromHeader_BearerPrefix(t *testing.T) {
	full := "Bearer eyJhbGciOiJIUzI1NiJ9.test.sig"
	got := auth.TokenFromHeader(full)
	if strings.HasPrefix(got, "Bearer ") {
		t.Error("result should not start with 'Bearer '")
	}
}
