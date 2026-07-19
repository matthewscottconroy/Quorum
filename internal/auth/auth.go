// Package auth provides password hashing, JWT issuance/validation, and
// refresh-token generation for the Quorum authentication system.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	jwt.RegisteredClaims
}

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
// memberID (may be empty), expiring after ttl.
func IssueAccessToken(userID, role, memberID, secret string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:   userID,
		Role:     role,
		MemberID: memberID,
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
