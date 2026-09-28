package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProxyOptions holds optional settings like interstitial notice and title for a proxy instance.
type ProxyOptions struct {
	NoticeEnabled bool
	NoticeContent string
	Title         string
	IconDataUrl   string
}

// ProxyInstance represents an active reverse proxy listener.
type ProxyInstance struct {
	ID            string
	Port          int
	TargetURL     string
	SkipTLSVerify bool
	NoticeEnabled bool
	NoticeContent string
	Title         string
	IconDataUrl   string
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
	return m.StartProxyWithOptions(id, port, targetURL, skipTLSVerify, ProxyOptions{})
}

// StartProxyWithOptions starts a reverse proxy on specified local port pointing to targetURL with options.
func (m *Manager) StartProxyWithOptions(id string, port int, targetURL string, skipTLSVerify bool, opts ProxyOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If already running with same config, return
	if existing, ok := m.instances[id]; ok {
		if existing.Port == port && existing.TargetURL == targetURL && existing.SkipTLSVerify == skipTLSVerify &&
			existing.NoticeEnabled == opts.NoticeEnabled && existing.NoticeContent == opts.NoticeContent &&
			existing.Title == opts.Title && existing.IconDataUrl == opts.IconDataUrl {
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
	urlStr := strings.TrimSpace(targetURL)
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "http://" + urlStr
	}
	target, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("无效的目标地址: %w", err)
	}

	// Create listener with retry in case previous instance just closed
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		time.Sleep(100 * time.Millisecond)
		ln, err = net.Listen("tcp", addr)
	}
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

		// Prevent duplicate path prefix if incoming request path already includes target's path prefix
		targetBasePath := strings.TrimRight(target.Path, "/")
		if targetBasePath != "" && targetBasePath != "/" {
			if strings.HasPrefix(req.URL.Path, targetBasePath+targetBasePath) {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, targetBasePath)
			}
		}

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

		// 4. Strip _notice_ack from query string before forwarding to target
		if req.URL.Query().Has("_notice_ack") {
			q := req.URL.Query()
			q.Del("_notice_ack")
			req.URL.RawQuery = q.Encode()
		}

		// 5. Strip internal notice ack cookies from forwarded request
		if rawCookie := req.Header.Get("Cookie"); rawCookie != "" {
			cookies := strings.Split(rawCookie, ";")
			var filtered []string
			for _, c := range cookies {
				trimmed := strings.TrimSpace(c)
				if !strings.HasPrefix(trimmed, "fn_notice_ack_") && !strings.HasPrefix(trimmed, "fn_notice_today_") {
					filtered = append(filtered, trimmed)
				}
			}
			if len(filtered) > 0 {
				req.Header.Set("Cookie", strings.Join(filtered, "; "))
			} else {
				req.Header.Del("Cookie")
			}
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

		// 6. Proactively clear any legacy fn_notice_ack cookie so it doesn't linger in client browsers
		clearAckCookie := fmt.Sprintf("fn_notice_ack_%s=; Path=/; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; SameSite=Lax", id)
		resp.Header.Add("Set-Cookie", clearAckCookie)

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
		if opts.NoticeEnabled && strings.TrimSpace(opts.NoticeContent) != "" {
			if shouldShowNotice(r, id, target.Path) {
				renderProxyNotice(w, r, id, opts.Title, opts.IconDataUrl, opts.NoticeContent)
				return
			}
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
		NoticeEnabled: opts.NoticeEnabled,
		NoticeContent: opts.NoticeContent,
		Title:         opts.Title,
		IconDataUrl:   opts.IconDataUrl,
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

func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if strings.HasSuffix(h, ":80") {
		return strings.TrimSuffix(h, ":80")
	}
	if strings.HasSuffix(h, ":443") {
		return strings.TrimSuffix(h, ":443")
	}
	return h
}

func shouldShowNotice(r *http.Request, id, targetPath string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	// 1. If "今日不再提示" is active, skip notice
	if c, err := r.Cookie("fn_notice_today_" + id); err == nil && c.Value != "" {
		return false
	}
	// 2. Query param bypass (set only when user clicks "进入应用")
	if r.URL.Query().Get("_notice_ack") == "1" {
		return false
	}
	// 3. Static assets
	reqPath := strings.ToLower(r.URL.Path)
	staticExts := []string{".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".woff", ".woff2", ".ttf", ".eot", ".map", ".json", ".wasm"}
	for _, ext := range staticExts {
		if strings.HasSuffix(reqPath, ext) {
			return false
		}
	}
	// 4. Target path check (only root or entry path)
	targetBase := strings.ToLower(strings.TrimRight(targetPath, "/"))
	isRootOrTarget := reqPath == "/" || reqPath == "" || (targetBase != "" && (reqPath == targetBase || reqPath == targetBase+"/"))
	if !isRootOrTarget {
		return false
	}
	// 5. If browser supports Fetch Metadata, only top-level navigation (document / iframe) triggers notice
	if secDest := r.Header.Get("Sec-Fetch-Dest"); secDest != "" {
		if secDest != "document" && secDest != "iframe" {
			return false
		}
	}
	// 6. Internal navigation inside app (Referer host matches current Host)
	if ref := r.Header.Get("Referer"); ref != "" {
		if refURL, err := url.Parse(ref); err == nil && normalizeHost(refURL.Host) == normalizeHost(r.Host) {
			return false
		}
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/html") || accept == "" || accept == "*/*" {
		return true
	}
	return false
}

func renderProxyNotice(w http.ResponseWriter, r *http.Request, id, title, iconDataUrl, noticeContent string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	escapedTitle := html.EscapeString(title)
	if escapedTitle == "" {
		escapedTitle = "应用提示"
	}
	escapedNotice := html.EscapeString(noticeContent)

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s - 开屏提示</title>
  <style>
    :root {
      --bg-page: #f1f5f9;
      --bg-card: #ffffff;
      --text-main: #0f172a;
      --text-muted: #64748b;
      --border: #e2e8f0;
      --primary: #2563eb;
      --primary-hover: #1d4ed8;
      --notice-bg: #eff6ff;
      --notice-border: #bfdbfe;
      --notice-text: #1e3a8a;
    }
    @media (prefers-color-scheme: dark) {
      :root {
        --bg-page: #0b0f17;
        --bg-card: #151b28;
        --text-main: #f8fafc;
        --text-muted: #94a3b8;
        --border: #242f42;
        --primary: #3b82f6;
        --primary-hover: #2563eb;
        --notice-bg: #172554;
        --notice-border: #1e40af;
        --notice-text: #dbeafe;
      }
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif;
      background-color: var(--bg-page);
      color: var(--text-main);
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      padding: 1.5rem;
    }
    .notice-card {
      background: var(--bg-card);
      border: 1px solid var(--border);
      border-radius: 14px;
      padding: 24px;
      max-width: 460px;
      width: 100%%;
      box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.05);
      animation: fadeIn 0.2s ease-out;
    }
    @keyframes fadeIn {
      from { opacity: 0; transform: translateY(6px); }
      to { opacity: 1; transform: translateY(0); }
    }
    .notice-header {
      display: flex;
      align-items: center;
      gap: 14px;
      margin-bottom: 16px;
    }
    .notice-icon {
      width: 48px;
      height: 48px;
      border-radius: 10px;
      object-fit: cover;
      background: var(--bg-page);
      border: 1px solid var(--border);
      flex-shrink: 0;
    }
    .notice-title-group {
      flex: 1;
      overflow: hidden;
    }
    .notice-app-name {
      font-size: 17px;
      font-weight: 700;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      color: var(--text-main);
    }
    .notice-body {
      background: var(--notice-bg);
      border: 1px solid var(--notice-border);
      color: var(--notice-text);
      border-radius: 10px;
      padding: 14px 16px;
      font-size: 14px;
      line-height: 1.6;
      white-space: pre-wrap;
      word-break: break-word;
      max-height: 260px;
      overflow-y: auto;
      margin-bottom: 20px;
    }
    .notice-footer {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      flex-wrap: wrap;
    }
    .skip-label {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      font-size: 13px;
      color: var(--text-muted);
      cursor: pointer;
      user-select: none;
    }
    .btn-proceed {
      background-color: var(--primary);
      color: #ffffff;
      border: none;
      padding: 9px 18px;
      border-radius: 8px;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
      transition: background-color 0.15s, transform 0.1s;
      outline: none;
    }
    .btn-proceed:hover {
      background-color: var(--primary-hover);
    }
    .btn-proceed:active {
      transform: scale(0.98);
    }
  </style>
</head>
<body>
  <div class="notice-card">
    <div class="notice-header">
      <img src="%s" alt="icon" class="notice-icon" onerror="this.onerror=null; this.style.display='none';">
      <div class="notice-title-group">
        <div class="notice-app-name">%s</div>
      </div>
    </div>
    <div class="notice-body">%s</div>
    <div class="notice-footer">
      <label class="skip-label">
        <input type="checkbox" id="skip-today">
        <span>今日不再提示</span>
      </label>
      <button type="button" class="btn-proceed" id="btn-proceed">进入应用</button>
    </div>
  </div>
  <script>
    (function() {
      const ITEM_ID = %q;
      try {
        // Clear any legacy ack cookie or sessionStorage from previous versions
        document.cookie = 'fn_notice_ack_' + ITEM_ID + '=; path=/; max-age=0; expires=Thu, 01 Jan 1970 00:00:00 GMT; SameSite=Lax';
        sessionStorage.removeItem('fn_notice_session_' + ITEM_ID);
      } catch(e) {}

      const btn = document.getElementById('btn-proceed');
      const chk = document.getElementById('skip-today');
      function proceed() {
        if (chk && chk.checked) {
          const d = new Date();
          d.setTime(d.getTime() + 24*60*60*1000);
          document.cookie = 'fn_notice_today_' + ITEM_ID + '=1; path=/; expires=' + d.toUTCString() + '; SameSite=Lax';
        }
        const u = new URL(window.location.href);
        u.searchParams.set('_notice_ack', '1');
        window.location.replace(u.toString());
      }
      if (btn) btn.addEventListener('click', proceed);
      document.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') proceed();
      });
    })();
  </script>
</body>
</html>`, escapedTitle, iconDataUrl, escapedTitle, escapedNotice, id)
}
