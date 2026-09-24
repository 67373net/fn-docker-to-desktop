package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyInstance represents an active reverse proxy listener.
type ProxyInstance struct {
	ID            string
	Port          int
	TargetURL     string
	SkipTLSVerify bool
	server        *http.Server
	listener      net.Listener
}

// DialContextFunc is a function signature for dialing network connections.
type DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type defaultBufferPool struct {
	pool sync.Pool
}

func newDefaultBufferPool() *defaultBufferPool {
	return &defaultBufferPool{
		pool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, 32*1024)
				return &b
			},
		},
	}
}

func (bp *defaultBufferPool) Get() []byte {
	return *bp.pool.Get().(*[]byte)
}

func (bp *defaultBufferPool) Put(b []byte) {
	bp.pool.Put(&b)
}

// Manager manages dynamic reverse proxy listeners.
type Manager struct {
	mu              sync.RWMutex
	instances       map[string]*ProxyInstance // key: service ID
	portMap         map[int]string            // key: port -> service ID
	dialContextFunc DialContextFunc
	bufferPool      httputil.BufferPool
}

// NewManager creates a new ProxyManager.
func NewManager() *Manager {
	return &Manager{
		instances:  make(map[string]*ProxyInstance),
		portMap:    make(map[int]string),
		bufferPool: newDefaultBufferPool(),
	}
}

// SetDialContext sets custom dialer function (e.g. SmartDialer with SSH fallback).
func (m *Manager) SetDialContext(fn DialContextFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dialContextFunc = fn
}

// StartProxy starts a reverse proxy on specified local port pointing to targetURL.
func (m *Manager) StartProxy(id string, port int, targetURL string, skipTLSVerify bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If already running with same config, return
	if existing, ok := m.instances[id]; ok {
		if existing.Port == port && existing.TargetURL == targetURL && existing.SkipTLSVerify == skipTLSVerify {
			return nil
		}
		// Stop old instance before re-starting
		m.stopInstanceLocked(id)
	}

	// Check if port is already used by another instance
	if ownerID, ok := m.portMap[port]; ok && ownerID != id {
		return fmt.Errorf("端口 %d 已被服务 [%s] 占用", port, ownerID)
	}

	// Parse target URL
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("无效的目标地址: %w", err)
	}
	if target.Scheme == "" {
		target.Scheme = "http"
	}

	// Create listener
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("无法监听本地端口 %d: %w", port, err)
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(target)
	if m.bufferPool != nil {
		proxy.BufferPool = m.bufferPool
	}

	dialer := m.dialContextFunc
	if dialer == nil {
		dialer = (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext
	}

	// Custom transport for TLS verification bypass and connection pooling
	customTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipTLSVerify,
		},
	}
	proxy.Transport = customTransport

	targetScheme := target.Scheme
	if targetScheme == "" {
		targetScheme = "http"
	}
	targetOrigin := fmt.Sprintf("%s://%s", targetScheme, target.Host)

	// Custom Director to ensure Host and headers are properly set
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		incomingHost := req.Host
		if incomingHost == "" {
			incomingHost = req.Header.Get("Host")
		}
		incomingOrigin := req.Header.Get("Origin")

		originalDirector(req)
		req.Host = target.Host

		// 1. Rewrite Origin to target's origin if present
		// This bypasses target server's CSRF / Host mismatch / CORS blocking
		if incomingOrigin != "" {
			req.Header.Set("Origin", targetOrigin)
			req.Header.Set("X-Original-Origin", incomingOrigin)
		}

		// 2. Rewrite Referer: if present, replace the scheme://host with targetOrigin
		if ref := req.Header.Get("Referer"); ref != "" {
			if refURL, err := url.Parse(ref); err == nil {
				refURL.Scheme = targetScheme
				refURL.Host = target.Host
				req.Header.Set("Referer", refURL.String())
				req.Header.Set("X-Original-Referer", ref)
			}
		}

		// 3. Set standard forwarding headers
		if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			if prior, ok := req.Header["X-Forwarded-For"]; ok {
				clientIP = strings.Join(prior, ", ") + ", " + clientIP
			}
			req.Header.Set("X-Forwarded-For", clientIP)
		}
		if req.TLS != nil {
			req.Header.Set("X-Forwarded-Proto", "https")
		} else {
			req.Header.Set("X-Forwarded-Proto", "http")
		}
		if incomingHost != "" {
			req.Header.Set("X-Forwarded-Host", incomingHost)
		}
	}

	// ModifyResponse to handle redirects, CORS, iframe embedding, and cookies for WAN access
	proxy.ModifyResponse = func(resp *http.Response) error {
		// 1. Rewrite Location header in redirects (301, 302, 303, 307, 308)
		if loc := resp.Header.Get("Location"); loc != "" {
			if locURL, err := url.Parse(loc); err == nil {
				if locURL.Host == target.Host || (locURL.Host != "" && strings.EqualFold(locURL.Host, target.Host)) {
					relPath := locURL.RequestURI()
					if locURL.Fragment != "" {
						relPath += "#" + locURL.Fragment
					}
					if relPath == "" {
						relPath = "/"
					}
					resp.Header.Set("Location", relPath)
				}
			}
		}

		// 2. Strip X-Frame-Options to allow iframe embedding in fnOS desktop
		resp.Header.Del("X-Frame-Options")

		// 3. Relax Content-Security-Policy frame-ancestors
		if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
			parts := strings.Split(csp, ";")
			var newParts []string
			for _, p := range parts {
				trimmed := strings.TrimSpace(p)
				if !strings.HasPrefix(strings.ToLower(trimmed), "frame-ancestors") {
					newParts = append(newParts, p)
				}
			}
			if len(newParts) > 0 {
				resp.Header.Set("Content-Security-Policy", strings.Join(newParts, "; "))
			} else {
				resp.Header.Del("Content-Security-Policy")
			}
		}

		// 4. Ensure permissive CORS headers for client-side AJAX/WebSockets
		reqOrigin := ""
		if resp.Request != nil {
			reqOrigin = resp.Request.Header.Get("X-Original-Origin")
		}
		if reqOrigin != "" {
			resp.Header.Set("Access-Control-Allow-Origin", reqOrigin)
			resp.Header.Set("Access-Control-Allow-Credentials", "true")
			resp.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
			resp.Header.Set("Access-Control-Allow-Headers", "*, Authorization, Content-Type, X-Requested-With, Cookie")
		}

		// 5. Rewrite Set-Cookie: strip backend LAN Domain= so cookie binds to proxy domain
		if cookies := resp.Header["Set-Cookie"]; len(cookies) > 0 {
			var newCookies []string
			for _, c := range cookies {
				parts := strings.Split(c, ";")
				var cookieParts []string
				for _, p := range parts {
					trimmed := strings.TrimSpace(p)
					if strings.HasPrefix(strings.ToLower(trimmed), "domain=") {
						continue
					}
					cookieParts = append(cookieParts, p)
				}
				newCookies = append(newCookies, strings.Join(cookieParts, "; "))
			}
			resp.Header["Set-Cookie"] = newCookies
		}

		return nil
	}

	// Custom ErrorHandler to return clean JSON error instead of blank 502
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("Reverse proxy forwarding error", "id", id, "target", targetURL, "error", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "<html><head><title>代理连接失败</title></head><body style='font-family:sans-serif;padding:2rem;'>"+
			"<h2>无法连接到目标服务</h2>"+
			"<p>目标地址: <code>%s</code></p>"+
			"<p>错误详情: <code>%s</code></p>"+
			"</body></html>", targetURL, err.Error())
	}

	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "*, Authorization, Content-Type, X-Requested-With, Cookie")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	server := &http.Server{
		Handler: proxyHandler,
	}

	instance := &ProxyInstance{
		ID:            id,
		Port:          port,
		TargetURL:     targetURL,
		SkipTLSVerify: skipTLSVerify,
		server:        server,
		listener:      ln,
	}

	m.instances[id] = instance
	m.portMap[port] = id

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("Proxy server error", "id", id, "port", port, "error", err)
		}
	}()

	slog.Info("Reverse proxy started", "id", id, "port", port, "target", targetURL)
	return nil
}

// StopProxy stops a proxy instance by ID.
func (m *Manager) StopProxy(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopInstanceLocked(id)
}

func (m *Manager) stopInstanceLocked(id string) {
	if instance, ok := m.instances[id]; ok {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = instance.server.Shutdown(ctx)
		_ = instance.listener.Close()
		delete(m.portMap, instance.Port)
		delete(m.instances, id)
		slog.Info("Reverse proxy stopped", "id", id, "port", instance.Port)
	}
}

// StopAll stops all proxy instances.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.instances {
		m.stopInstanceLocked(id)
	}
}

// IsProxyRunning checks if proxy for ID is active.
func (m *Manager) IsProxyRunning(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.instances[id]
	return ok
}

// GetRunningPorts returns all ports currently listened by proxies.
func (m *Manager) GetRunningPorts() map[int]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copyMap := make(map[int]string, len(m.portMap))
	for p, id := range m.portMap {
		copyMap[p] = id
	}
	return copyMap
}

// TestTargetResult represents connectivity test result.
type TestTargetResult struct {
	Success    bool   `json:"success"`
	StatusCode int    `json:"status_code"`
	LatencyMs  int64  `json:"latency_ms"`
	Message    string `json:"message"`
}

// TestTarget tests connectivity to a backend target URL.
func TestTarget(targetURL string, timeout time.Duration) TestTargetResult {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	urlStr := strings.TrimSpace(targetURL)
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "http://" + urlStr
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Allow self-signed during test
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	start := time.Now()
	resp, err := client.Get(urlStr)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestTargetResult{
			Success:   false,
			LatencyMs: latency,
			Message:   fmt.Sprintf("连接失败: %v", err),
		}
	}
	defer resp.Body.Close()

	return TestTargetResult{
		Success:    true,
		StatusCode: resp.StatusCode,
		LatencyMs:  latency,
		Message:    fmt.Sprintf("连接正常 (HTTP %d, %dms)", resp.StatusCode, latency),
	}
}

// CheckPortAvailable checks if a port is available to bind on localhost.
func CheckPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// RecommendAvailablePort recommends an available port starting from basePort.
func RecommendAvailablePort(basePort int, usedPorts map[int]bool) int {
	if basePort <= 1024 || basePort > 65530 {
		basePort = 18000
	}

	for p := basePort; p <= 65530; p++ {
		if usedPorts != nil && usedPorts[p] {
			continue
		}
		if CheckPortAvailable(p) {
			return p
		}
	}
	return 0
}
