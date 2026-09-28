package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShouldShowNotice(t *testing.T) {
	id := "test-item-1"

	// 1. Initial page GET request -> should show notice
	req1 := httptest.NewRequest("GET", "http://localhost:18006/", nil)
	req1.Header.Set("Accept", "text/html,application/xhtml+xml")
	if !shouldShowNotice(req1, id, "/") {
		t.Errorf("Expected shouldShowNotice to be true for initial GET /")
	}

	// 2. Request with _notice_ack=1 query param -> should NOT show notice
	req2 := httptest.NewRequest("GET", "http://localhost:18006/?_notice_ack=1", nil)
	req2.Header.Set("Accept", "text/html,application/xhtml+xml")
	if shouldShowNotice(req2, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false when _notice_ack=1 is present")
	}

	// 3. Request with fn_notice_today_ cookie -> should NOT show notice
	req3 := httptest.NewRequest("GET", "http://localhost:18006/", nil)
	req3.Header.Set("Accept", "text/html,application/xhtml+xml")
	req3.AddCookie(&http.Cookie{Name: "fn_notice_today_" + id, Value: "1"})
	if shouldShowNotice(req3, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false when today cookie is present")
	}

	// 4. Subsequent desktop launch request (Referer from fnOS portal) -> should show notice
	req4 := httptest.NewRequest("GET", "http://localhost:18006/", nil)
	req4.Header.Set("Accept", "text/html,application/xhtml+xml")
	req4.Header.Set("Referer", "https://example.5ddd.com/")
	if !shouldShowNotice(req4, id, "/") {
		t.Errorf("Expected shouldShowNotice to be true for subsequent desktop launch")
	}

	// 5. Internal navigation inside app (Referer matches host) -> should NOT show notice
	req5 := httptest.NewRequest("GET", "http://localhost:18006/", nil)
	req5.Header.Set("Accept", "text/html,application/xhtml+xml")
	req5.Header.Set("Referer", "http://localhost:18006/settings")
	if shouldShowNotice(req5, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false for internal navigation")
	}

	// 6. Static asset request (e.g. logo.png or app.js) -> should NOT show notice
	req6 := httptest.NewRequest("GET", "http://localhost:18006/static/app.js", nil)
	if shouldShowNotice(req6, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false for static js asset")
	}

	// 7. POST request -> should NOT show notice
	req7 := httptest.NewRequest("POST", "http://localhost:18006/api/data", nil)
	if shouldShowNotice(req7, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false for POST request")
	}
}

func TestRenderProxyNotice(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://localhost:18006/", nil)

	renderProxyNotice(rec, req, "item-123", "测试应用", "", "欢迎使用测试应用系统")

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "欢迎使用测试应用系统") {
		t.Errorf("Expected notice content in body")
	}
	if !strings.Contains(body, "测试应用") {
		t.Errorf("Expected app title in body")
	}
	if !strings.Contains(body, "进入应用") {
		t.Errorf("Expected enter button in body")
	}
	if strings.Contains(body, "direct-url-bar") || strings.Contains(body, "直达网址") {
		t.Errorf("Expected NO direct-url-bar or '直达网址' in body")
	}
}

func TestProxyNoticeInterception(t *testing.T) {
	// Backend server (e.g. Nocobase)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Hello from backend service! query=%s", r.URL.RawQuery)
	}))
	defer backend.Close()

	mgr := NewManager()
	port := RecommendAvailablePort(18050, nil)
	id := "nocobase-test"

	opts := ProxyOptions{
		NoticeEnabled: true,
		NoticeContent: "系统维护开屏提示",
		Title:         "Nocobase",
	}

	if err := mgr.StartProxyWithOptions(id, port, backend.URL, false, opts); err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer mgr.StopProxy(id)

	client := &http.Client{}

	// Request 1: Without cookie or query param -> should return notice page
	resp1, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("Failed to request proxy: %v", err)
	}
	defer resp1.Body.Close()
	bodyBytes1, _ := io.ReadAll(resp1.Body)
	body1 := string(bodyBytes1)

	if !strings.Contains(body1, "系统维护开屏提示") {
		t.Errorf("Expected notice page content on initial access, got: %s", body1)
	}
	if strings.Contains(body1, "Hello from backend service!") {
		t.Errorf("Should not directly show backend before acknowledgement")
	}

	// Request 2: With _notice_ack=1 query param (clicking '进入应用') -> should bypass notice and show backend service
	resp2, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/?_notice_ack=1", port))
	if err != nil {
		t.Fatalf("Failed to do request with _notice_ack: %v", err)
	}
	defer resp2.Body.Close()
	bodyBytes2, _ := io.ReadAll(resp2.Body)
	body2 := string(bodyBytes2)

	if !strings.Contains(body2, "Hello from backend service!") {
		t.Errorf("Expected backend service content when _notice_ack present, got: %s", body2)
	}
	// Verify that _notice_ack was stripped before forwarding to backend
	if strings.Contains(body2, "_notice_ack=1") {
		t.Errorf("Expected _notice_ack to be stripped from target query string, got: %s", body2)
	}
	// Verify that legacy fn_notice_ack cookie is cleared on entry
	foundClearCookie := false
	for _, c := range resp2.Cookies() {
		if c.Name == "fn_notice_ack_"+id && c.MaxAge <= 0 {
			foundClearCookie = true
			break
		}
	}
	if !foundClearCookie {
		t.Errorf("Expected Set-Cookie clearing fn_notice_ack on entry")
	}

	// Request 3: Subsequent desktop launch without today cookie -> MUST SHOW NOTICE MODAL AGAIN!
	req3, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req3.Header.Set("Referer", "https://example.5ddd.com/")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("Failed to do request: %v", err)
	}
	defer resp3.Body.Close()
	bodyBytes3, _ := io.ReadAll(resp3.Body)
	body3 := string(bodyBytes3)
	if !strings.Contains(body3, "系统维护开屏提示") {
		t.Errorf("Expected notice page for subsequent desktop launch without today cookie, got: %s", body3)
	}

	// Request 4: With fn_notice_today_ cookie -> should bypass notice even when launched from desktop
	req4, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req4.Header.Set("Referer", "https://example.5ddd.com/")
	req4.AddCookie(&http.Cookie{Name: "fn_notice_today_" + id, Value: "1"})
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatalf("Failed to do request with today cookie: %v", err)
	}
	defer resp4.Body.Close()
	bodyBytes4, _ := io.ReadAll(resp4.Body)
	body4 := string(bodyBytes4)
	if !strings.Contains(body4, "Hello from backend service!") {
		t.Errorf("Expected backend service content when today cookie present, got: %s", body4)
	}

	// Request 5: Internal navigation inside the app (Referer matches host) -> should bypass notice
	req5, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req5.Header.Set("Referer", fmt.Sprintf("http://127.0.0.1:%d/dashboard", port))
	resp5, err := client.Do(req5)
	if err != nil {
		t.Fatalf("Failed to do request with internal referer: %v", err)
	}
	defer resp5.Body.Close()
	bodyBytes5, _ := io.ReadAll(resp5.Body)
	body5 := string(bodyBytes5)
	if !strings.Contains(body5, "Hello from backend service!") {
		t.Errorf("Expected backend service content for internal app navigation, got: %s", body5)
	}
}
