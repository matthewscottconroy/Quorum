package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"quorum/internal/auth"
)

// TestParseToken_RejectsAlgNone ensures an unsigned ("alg":"none") token is
// rejected — accepting one would let anyone forge admin tokens.
func TestParseToken_RejectsAlgNone(t *testing.T) {
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"user_id": "x", "role": "superadmin"})
	s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	if _, err := auth.ParseToken(s, "test-secret"); err == nil {
		t.Error("ParseToken must reject an alg:none token")
	}
}

// TestParseToken_RejectsRSAToken ensures an RS256-signed token is rejected by
// the HMAC-pinned parser (algorithm-confusion defense).
func TestParseToken_RejectsRSAToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"user_id": "x", "role": "superadmin", "exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign rs256: %v", err)
	}
	if _, err := auth.ParseToken(s, "test-secret"); err == nil {
		t.Error("ParseToken must reject an RS256 token")
	}
}

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

// ---- DummyCheckPassword ----

func TestDummyCheckPassword_AlwaysFalse(t *testing.T) {
	// The dummy comparison exists only to equalize timing on the
	// unknown-email login path; it must never report a match.
	for _, pw := range []string{"", "password", "correct-horse-battery"} {
		if auth.DummyCheckPassword(pw) {
			t.Errorf("DummyCheckPassword(%q) = true, want false", pw)
		}
	}
}

// ---- IssueAccessToken / ParseToken ----

func TestIssueAndParseToken(t *testing.T) {
	secret := "test-secret-value"
	tok, err := auth.IssueAccessToken("user-123", "admin", "member-77", secret, time.Minute)
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
	if claims.MemberID != "member-77" {
		t.Errorf("MemberID: got %q, want %q", claims.MemberID, "member-77")
	}
}

// TestIssueAndParseToken_EmptyMemberID verifies an unlinked account round-trips
// with an empty MemberID (the omitempty JSON tag must not corrupt parsing).
func TestIssueAndParseToken_EmptyMemberID(t *testing.T) {
	secret := "test-secret-value"
	tok, err := auth.IssueAccessToken("user-9", "restricted", "", secret, time.Minute)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	claims, err := auth.ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.MemberID != "" {
		t.Errorf("MemberID: got %q, want empty", claims.MemberID)
	}
}

func TestParseToken_WrongSecret(t *testing.T) {
	tok, _ := auth.IssueAccessToken("u1", "member", "", "secret-a", time.Minute)
	_, err := auth.ParseToken(tok, "secret-b")
	if err == nil {
		t.Error("expected error when verifying with wrong secret")
	}
}

func TestParseToken_Expired(t *testing.T) {
	tok, _ := auth.IssueAccessToken("u1", "member", "", "secret", -time.Second)
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
	first, second := auth.HashRefreshToken("abc"), auth.HashRefreshToken("abc")
	if first != second {
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
