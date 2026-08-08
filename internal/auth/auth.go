// Package auth provides password hashing, JWT issuance/validation, and
// refresh-token generation for the Quorum authentication system.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is required by the TOTP standard (RFC 6238)
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// Claims extends jwt.RegisteredClaims with Quorum-specific fields.
type Claims struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	// MemberID links the account to a member record, or "" if unlinked. Used to
	// scope what a `restricted` user may read (only their own record).
	MemberID string `json:"member_id,omitempty"`
	// MFA is true when the account had two-factor enabled at issuance. The
	// require-2FA middleware gate reads it (stateless enforcement); it
	// refreshes to true on the first token minted after enrollment.
	MFA bool `json:"mfa,omitempty"`
	// Purpose distinguishes token types. Access tokens leave it empty; an
	// interim two-factor token sets it to "mfa" and is accepted ONLY by the
	// second-factor login step, never by the API middleware.
	Purpose string `json:"purpose,omitempty"`
	jwt.RegisteredClaims
}

// PurposeMFA marks an interim token that authorizes only the second-factor
// (TOTP) login step.
const PurposeMFA = "mfa"

// HashPassword returns a bcrypt hash of password at cost 12.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(b), err
}

// CheckPassword returns true when password matches the bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is a valid cost-12 bcrypt hash of an unguessable random value,
// used to equalize login timing when the email does not exist.
const dummyHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"

// DummyCheckPassword burns the same bcrypt work as a real comparison and
// always returns false. Call it on the unknown-user login path so response
// timing does not reveal whether an email is registered.
func DummyCheckPassword(password string) bool {
	bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(password)) //nolint:errcheck
	return false
}

// IssueAccessToken signs an HS256 JWT carrying userID, role, and the linked
// memberID (may be empty), expiring after ttl. MFA claim defaults to false;
// login/refresh paths use IssueAccessTokenFor to carry enrollment state.
func IssueAccessToken(userID, role, memberID, secret string, ttl time.Duration) (string, error) {
	return IssueAccessTokenFor(userID, role, memberID, false, secret, ttl)
}

// IssueAccessTokenFor is IssueAccessToken with the MFA-enrolled claim.
func IssueAccessTokenFor(userID, role, memberID string, mfa bool, secret string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:   userID,
		Role:     role,
		MemberID: memberID,
		MFA:      mfa,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseToken validates an HS256 JWT and returns its claims.
// Returns an error if the token is expired, has an unexpected signing method,
// or the signature does not match secret.
func ParseToken(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// GenerateRefreshToken creates a cryptographically random 32-byte token.
// It returns the hex-encoded plain token (sent to the client) and its
// SHA-256 hash (stored in the database). Never store the plain value.
func GenerateRefreshToken() (plain, hashed string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	plain = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(plain))
	hashed = hex.EncodeToString(sum[:])
	return
}

// HashRefreshToken returns the SHA-256 hex digest of plain, suitable for
// database lookup without storing the raw token value.
func HashRefreshToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// TokenFromHeader strips the "Bearer " prefix from an Authorization header value.
// If the header does not start with "Bearer ", the input is returned unchanged.
func TokenFromHeader(authHeader string) string {
	return strings.TrimPrefix(authHeader, "Bearer ")
}

// GenerateResetToken creates a random password-reset token: the plain value
// (emailed to the user) and its SHA-256 hash (stored). It reuses the opaque
// token construction of refresh tokens.
func GenerateResetToken() (plain, hashed string, err error) {
	return GenerateRefreshToken()
}

// HashResetToken returns the SHA-256 hex digest of a reset token for DB lookup.
func HashResetToken(plain string) string { return HashRefreshToken(plain) }

// IssueMFAToken signs a short-lived token that authorizes only the second
// (TOTP) step of login for the given user. It carries Purpose == PurposeMFA and
// is rejected by the API auth middleware.
func IssueMFAToken(userID, secret string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:  userID,
		Purpose: PurposeMFA,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ---- TOTP two-factor authentication (RFC 6238) ----

const (
	totpDigits = 6
	totpPeriod = 30 // seconds per code
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new base32-encoded 160-bit TOTP shared secret.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32NoPad.EncodeToString(b), nil
}

// totpAt computes the 6-digit TOTP for a secret at a given 30-second counter.
func totpAt(secret string, counter uint64) (string, error) {
	key, err := base32NoPad.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[off]&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])) % 1_000_000
	return fmt.Sprintf("%06d", code), nil
}

// CurrentTOTP returns the TOTP code for secret at time t. It exists mainly so
// callers (and tests) can compute the expected code without reaching into the
// unexported window arithmetic.
func CurrentTOTP(secret string, t time.Time) (string, error) {
	c := t.Unix() / totpPeriod
	if c < 0 {
		c = 0
	}
	return totpAt(secret, uint64(c))
}

// ValidateTOTP reports whether code is a valid TOTP for secret at time t,
// allowing one 30-second step of clock skew on either side.
func ValidateTOTP(secret, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	base := t.Unix() / totpPeriod
	for _, step := range []int64{0, -1, 1} {
		c := base + step
		if c < 0 {
			continue
		}
		if got, err := totpAt(secret, uint64(c)); err == nil && hmac.Equal([]byte(got), []byte(code)) {
			return true
		}
	}
	return false
}

// TOTPProvisioningURI builds an otpauth:// URI suitable for an authenticator
// app QR code (or manual entry of the secret).
func TOTPProvisioningURI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// GenerateRecoveryCodes returns n one-time recovery codes (shown once to the
// user) and their SHA-256 hashes (stored). Each code is 8 base32 characters.
func GenerateRecoveryCodes(n int) (plain, hashed []string, err error) {
	for i := 0; i < n; i++ {
		b := make([]byte, 5) // 5 bytes -> 8 base32 chars
		if _, err = rand.Read(b); err != nil {
			return nil, nil, err
		}
		code := base32NoPad.EncodeToString(b)
		plain = append(plain, code)
		hashed = append(hashed, HashRefreshToken(code))
	}
	return plain, hashed, nil
}
