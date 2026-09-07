package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	CookieName = "wild_auth_session"
	TokenTTL   = 7 * 24 * time.Hour
)

type Manager struct {
	password     string
	authRequired bool
	secretKey    []byte
}

func NewManager(password string) *Manager {
	secret := make([]byte, 32)
	rand.Read(secret)

	return &Manager{
		password:     strings.TrimSpace(password),
		authRequired: strings.TrimSpace(password) != "",
		secretKey:    secret,
	}
}

func (m *Manager) IsAuthRequired() bool {
	return m.authRequired
}

func (m *Manager) VerifyPassword(pwd string) bool {
	if !m.authRequired {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(pwd), []byte(m.password)) == 1
}

// GenerateToken creates an HMAC signed timestamp token.
func (m *Manager) GenerateToken() string {
	now := time.Now().Unix()
	payload := strconv.FormatInt(now, 10)

	mac := hmac.New(sha256.New, m.secretKey)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s.%s", payload, sig)
}

// ValidateToken checks whether token is valid and not expired.
func (m *Manager) ValidateToken(token string) bool {
	if !m.authRequired {
		return true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}

	payload := parts[0]
	sig := parts[1]

	created, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}

	// Check expiration
	if time.Now().Unix()-created > int64(TokenTTL.Seconds()) {
		return false
	}

	mac := hmac.New(sha256.New, m.secretKey)
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) == 1
}

// ExtractToken retrieves token from Cookie, Authorization Header, or query param.
func (m *Manager) ExtractToken(r *http.Request) string {
	// 1. Check Cookie
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value
	}

	// 2. Check Authorization Header
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// 3. Check Query Parameter (for EventSource SSE)
	if qToken := r.URL.Query().Get("token"); qToken != "" {
		return qToken
	}

	return ""
}

// IsRequestAuthenticated checks if request is permitted.
func (m *Manager) IsRequestAuthenticated(r *http.Request) bool {
	if !m.authRequired {
		return true
	}
	token := m.ExtractToken(r)
	return m.ValidateToken(token)
}

// SetPassword dynamically updates the required password.
func (m *Manager) SetPassword(pwd string) {
	m.password = strings.TrimSpace(pwd)
	m.authRequired = m.password != ""
}
