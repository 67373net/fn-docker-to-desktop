package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SecurityManager handles request-level security checks, rate limiting, and WAN protection.
type SecurityManager struct {
	mu           sync.Mutex
	failedLogins map[string][]time.Time
}

var globalSecurityMgr = &SecurityManager{
	failedLogins: make(map[string][]time.Time),
}

// AppSessionManager manages ephemeral frontend session tokens for anti-bypass protection.
type AppSessionManager struct {
	mu       sync.RWMutex
	sessions map[string]time.Time
}

var globalAppSessionMgr = &AppSessionManager{
	sessions: make(map[string]time.Time),
}

// CreateSession generates and records a new cryptographically random session token.
func (sm *AppSessionManager) CreateSession() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	token := hex.EncodeToString(b)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	// Clean expired sessions
	for k, exp := range sm.sessions {
		if now.After(exp) {
			delete(sm.sessions, k)
		}
	}
	// Active session valid for 24 hours
	sm.sessions[token] = now.Add(24 * time.Hour)
	return token
}

// ValidateSession checks whether the session token is valid and unexpired.
func (sm *AppSessionManager) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	exp, exists := sm.sessions[token]
	if !exists {
		return false
	}
	if time.Now().After(exp) {
		delete(sm.sessions, token)
		return false
	}
	// Slide expiration window
	sm.sessions[token] = time.Now().Add(24 * time.Hour)
	return true
}

// IsPrivateOrLocalIP determines if an IP address is a private/local network address.
func IsPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Loopback (127.0.0.1, ::1)
	if ip.IsLoopback() {
		return true
	}
	// Private subnets (RFC 1918: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, RFC 4193: fc00::/7)
	if ip.IsPrivate() {
		return true
	}
	// Link-local (fe80::/10, 169.254.0.0/16)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// Carrier Grade NAT (100.64.0.0/10)
	cgnat := net.IPNet{
		IP:   net.ParseIP("100.64.0.0"),
		Mask: net.CIDRMask(10, 32),
	}
	if cgnat.Contains(ip) {
		return true
	}
	return false
}

// GetClientIP parses the client IP address from the request.
func GetClientIP(r *http.Request) net.IP {
	// 1. Check X-Forwarded-For if behind trusted proxy/gateway
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ipStr := strings.TrimSpace(parts[0])
			if ip := net.ParseIP(ipStr); ip != nil {
				return ip
			}
		}
	}
	// 2. Check X-Real-IP
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		if ip := net.ParseIP(strings.TrimSpace(xrip)); ip != nil {
			return ip
		}
	}
	// 3. Fallback to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip
		}
	}
	return net.ParseIP(r.RemoteAddr)
}

// IsBlockedByRateLimit checks if client IP is temporarily blocked due to excessive failed attempts.
func (sm *SecurityManager) IsBlockedByRateLimit(clientIP string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)
	var validAttempts []time.Time
	for _, t := range sm.failedLogins[clientIP] {
		if t.After(cutoff) {
			validAttempts = append(validAttempts, t)
		}
	}
	sm.failedLogins[clientIP] = validAttempts

	// Block if 5 or more failures in the last 5 minutes
	return len(validAttempts) >= 5
}

// RecordFailedLogin records a failed authentication attempt.
func (sm *SecurityManager) RecordFailedLogin(clientIP string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.failedLogins[clientIP] = append(sm.failedLogins[clientIP], time.Now())
}

// ResetFailedLogin resets failure counter upon successful login.
func (sm *SecurityManager) ResetFailedLogin(clientIP string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.failedLogins, clientIP)
}

// SecurityHeadersMiddleware injects standard HTTP security defense headers.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")

		// Block CSRF on mutating requests (POST, PUT, DELETE) if cross-origin
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			if origin := r.Header.Get("Origin"); origin != "" {
				if parsed, err := url.Parse(origin); err == nil {
					reqHost := r.Host
					if h, _, err := net.SplitHostPort(reqHost); err == nil {
						reqHost = h
					}
					origHost := parsed.Hostname()
					if !isAllowedOrigin(r, origHost, reqHost) {
						http.Error(w, `{"error":"forbidden_cross_origin","message":"拒绝跨域操作请求 (CSRF 防护)"}`, http.StatusForbidden)
						return
					}
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

func isAllowedOrigin(r *http.Request, origHost string, reqHost string) bool {
	// 1. Direct match with Host header
	if origHost == reqHost || origHost == "localhost" || origHost == "127.0.0.1" || origHost == "::1" {
		return true
	}

	// 2. Match with reverse proxy X-Forwarded-Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		if h, _, err := net.SplitHostPort(xfh); err == nil {
			xfh = h
		}
		if strings.EqualFold(origHost, strings.TrimSpace(xfh)) {
			return true
		}
	}

	// 3. Match with Referer hostname
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil {
			if strings.EqualFold(origHost, u.Hostname()) {
				return true
			}
		}
	}

	// 4. fnOS Connect domain (*.fnos.net)
	if strings.HasSuffix(strings.ToLower(origHost), ".fnos.net") {
		return true
	}

	// 5. Private / LAN IP (e.g. 192.168.x.x, 10.x.x.x, 172.16-31.x.x)
	if ip := net.ParseIP(origHost); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return true
		}
	}

	// 6. Custom headers that cannot be set by unauthorized cross-origin HTML forms:
	// A malicious cross-origin website cannot inject X-App-Session, X-Session-Token,
	// X-Requested-With, or X-Trim-User without explicit CORS approval from the server.
	if r.Header.Get("X-App-Session") != "" ||
		r.Header.Get("X-Session-Token") != "" ||
		r.Header.Get("X-Requested-With") != "" ||
		r.Header.Get("X-Trim-User") != "" ||
		r.Header.Get("X-Feiniu-User") != "" {
		return true
	}

	return false
}

// IsRestrictedMetadataTarget checks whether a target URL is cloud metadata service (SSRF prevention).
func IsRestrictedMetadataTarget(targetURL string) bool {
	u, err := url.Parse(targetURL)
	if err != nil {
		return true
	}
	host := u.Hostname()
	if host == "169.254.169.254" || host == "metadata.google.internal" || host == "100.100.100.200" {
		return true
	}
	if strings.HasPrefix(host, "169.254.") {
		return true
	}
	return false
}

// SanitizeSVGContent checks if an SVG contains script tags or inline JS event handlers.
func SanitizeSVGContent(content []byte) error {
	lower := strings.ToLower(string(content))
	dangerousPatterns := []string{
		"<script",
		"onload=",
		"onerror=",
		"onclick=",
		"onmouseover=",
		"javascript:",
		"<iframe",
		"<object",
		"<embed",
	}
	for _, p := range dangerousPatterns {
		if strings.Contains(lower, p) {
			return fmt.Errorf("SVG 包含潜在危险代码 (%s)", p)
		}
	}
	return nil
}
