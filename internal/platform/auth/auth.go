// Package auth issues and verifies the credentials used by admin users and by
// TV devices. Two token families exist because they have different lifetimes
// and different threat models: a short admin JWT and a long lived device token.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/alfateve/stayin-local-backend/internal/domain"
)

// Errors returned by the manager.
var (
	ErrInvalidToken = errors.New("auth: invalid token")
	ErrExpiredToken = errors.New("auth: token expired")
)

// Claims is the JWT payload for admin sessions.
type Claims struct {
	UserID   string      `json:"uid"`
	TenantID string      `json:"tid,omitempty"`
	Role     domain.Role `json:"role"`
	Email    string      `json:"email"`
	jwt.RegisteredClaims
}

// Manager signs and verifies tokens.
type Manager struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewManager builds a token manager.
func NewManager(secret []byte, accessTTL, refreshTTL time.Duration) *Manager {
	return &Manager{secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL}
}

// AccessTTL is the lifetime of an access token.
func (m *Manager) AccessTTL() time.Duration { return m.accessTTL }

// Issue creates an access and a refresh token for a user.
func (m *Manager) Issue(u *domain.User) (access string, refresh string, expiresIn int64, err error) {
	now := time.Now()

	accessClaims := Claims{
		UserID: u.ID,
		Role:   u.Role,
		Email:  u.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
			Issuer:    "stayin-local",
		},
	}
	if u.TenantID != nil {
		accessClaims.TenantID = *u.TenantID
	}

	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(m.secret)
	if err != nil {
		return "", "", 0, fmt.Errorf("auth: sign access token: %w", err)
	}

	refreshClaims := Claims{
		UserID: u.ID,
		Role:   u.Role,
		Email:  u.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.refreshTTL)),
			Issuer:    "stayin-local",
			Audience:  jwt.ClaimStrings{"refresh"},
		},
	}
	if u.TenantID != nil {
		refreshClaims.TenantID = *u.TenantID
	}

	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(m.secret)
	if err != nil {
		return "", "", 0, fmt.Errorf("auth: sign refresh token: %w", err)
	}

	return accessToken, refreshToken, int64(m.accessTTL.Seconds()), nil
}

// Verify parses and validates a token, returning the claims.
func (m *Manager) Verify(token string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %v", t.Header["alg"])
		}
		return m.secret, nil
	}, jwt.WithIssuer("stayin-local"), jwt.WithLeeway(30*time.Second))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	if claims.UserID == "" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// VerifyRefresh validates a refresh token specifically.
func (m *Manager) VerifyRefresh(token string) (*Claims, error) {
	claims, err := m.Verify(token)
	if err != nil {
		return nil, err
	}
	if len(claims.Audience) == 0 || claims.Audience[0] != "refresh" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// HashPassword produces a bcrypt digest.
func HashPassword(password string) (string, error) {
	if len(password) < 6 {
		return "", domain.NewValidation("password", "minimal 6 karakter")
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(digest), nil
}

// CheckPassword verifies a plaintext password against a digest.
func CheckPassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrInvalidToken
	}
	return nil
}

// NewDeviceToken mints a random device credential. The plaintext is returned
// once and never stored; only the SHA-256 digest is persisted.
func NewDeviceToken() (token string, digest string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: generate device token: %w", err)
	}
	token = "dvt_" + base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken digests a device token for storage lookup.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// NewPairingCode produces a six digit code shown on the TV.
func NewPairingCode() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate pairing code: %w", err)
	}
	n := int(buf[0])<<24 | int(buf[1])<<16 | int(buf[2])<<8 | int(buf[3])
	if n < 0 {
		n = -n
	}
	code := n % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// RandomSecret builds a random hex string, used for generated bootstrap secrets.
func RandomSecret(bytesLen int) (string, error) {
	if bytesLen < 16 {
		bytesLen = 16
	}
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
