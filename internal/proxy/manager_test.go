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

	// 2. Request with ack cookie -> should NOT show notice
	req2 := httptest.NewRequest("GET", "http://localhost:18006/", nil)
	req2.Header.Set("Accept", "text/html,application/xhtml+xml")
	req2.AddCookie(&http.Cookie{Name: "fn_notice_ack_" + id, Value: "1"})
	if shouldShowNotice(req2, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false when ack cookie is present")
	}

	// 3. Static asset request (e.g. logo.png or app.js) -> should NOT show notice
	req3 := httptest.NewRequest("GET", "http://localhost:18006/static/app.js", nil)
	if shouldShowNotice(req3, id, "/") {
		t.Errorf("Expected shouldShowNotice to be false for static js asset")
	}

	// 4. POST request -> should NOT show notice
	req4 := httptest.NewRequest("POST", "http://localhost:18006/api/data", nil)
	if shouldShowNotice(req4, id, "/") {
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
		fmt.Fprint(w, "Hello from backend service!")
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

	// Request 1: Without cookie -> should return notice page
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

	// Request 2: With cookie -> should bypass notice and show backend service
	req2, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req2.AddCookie(&http.Cookie{Name: "fn_notice_ack_" + id, Value: "1"})
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("Failed to do request with cookie: %v", err)
	}
	defer resp2.Body.Close()
	bodyBytes2, _ := io.ReadAll(resp2.Body)
	body2 := string(bodyBytes2)

	if !strings.Contains(body2, "Hello from backend service!") {
		t.Errorf("Expected backend service content when ack cookie present, got: %s", body2)
	}

	// Request 3: With fn_notice_today_ cookie -> should bypass notice even with cross-site launch
	req3, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req3.Header.Set("Sec-Fetch-Site", "cross-site")
	req3.AddCookie(&http.Cookie{Name: "fn_notice_today_" + id, Value: "1"})
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("Failed to do request with today cookie: %v", err)
	}
	defer resp3.Body.Close()
	bodyBytes3, _ := io.ReadAll(resp3.Body)
	body3 := string(bodyBytes3)
	if !strings.Contains(body3, "Hello from backend service!") {
		t.Errorf("Expected backend service content when today cookie present, got: %s", body3)
	}

	// Request 4: With only fn_notice_ack_ but launched from desktop (Sec-Fetch-Site: cross-site) -> should show notice
	req4, err := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req4.Header.Set("Sec-Fetch-Site", "cross-site")
	req4.AddCookie(&http.Cookie{Name: "fn_notice_ack_" + id, Value: "1"})
	resp4, err := client.Do(req4)
	if err != nil {
		t.Fatalf("Failed to do request with cross-site ack cookie: %v", err)
	}
	defer resp4.Body.Close()
	bodyBytes4, _ := io.ReadAll(resp4.Body)
	body4 := string(bodyBytes4)
	if !strings.Contains(body4, "系统维护开屏提示") {
		t.Errorf("Expected notice page for fresh cross-site desktop launch without today cookie, got: %s", body4)
	}
}
