package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsCliSpinnerLine(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"| Verifying files.", true},
		{"/ Verifying files.", true},
		{"- Verifying files.", true},
		{"\\ Verifying files.", true},
		{"/ installing.", true},
		{"- installing.", true},
		{"\\ installing.", true},
		{"| installing.", true},
		{"/ installing..", true},
		{"- installing..", true},
		{"\\ installing..", true},
		{"Installation complete", true},
		{"starting service", true},
		{"stopping service", true},
		{"uninstalling", true},
		{"-", true},
		{"|", true},
		{"/", true},
		{"\\", true},
		{"normal log message", false},
		{"error: package not found", false},
		{"success: installed app 123", false},
	}

	for _, tt := range tests {
		got := isCliSpinnerLine(tt.input)
		if got != tt.expected {
			t.Errorf("isCliSpinnerLine(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestCleanCliOutput(t *testing.T) {
	raw := []byte("\r| Verifying files.\r/ Verifying files.\r- Verifying files.\r\\ Verifying files.\r/ installing.\r- installing.\r- installing..\r\nreal output line\r\nanother output line\r\n")
	cleaned := cleanCliOutput(raw)
	if strings.Contains(cleaned, "installing") {
		t.Errorf("cleanCliOutput contains installing animation: %q", cleaned)
	}
	if strings.Contains(cleaned, "Verifying") {
		t.Errorf("cleanCliOutput contains Verifying animation: %q", cleaned)
	}
	if !strings.Contains(cleaned, "real output line") {
		t.Errorf("cleanCliOutput missed real output line: %q", cleaned)
	}
}

func TestBuildPackageRedirectMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-installer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	installer := NewInstaller(tmpDir, "icon.png")
	pkgDir, err := installer.BuildPackage(AppcenterPackageConfig{
		AppName:  "fndocker.baidu",
		Title:    "百度",
		Desc:     "百度快捷方式",
		Port:     0,
		Protocol: "",
		Path:     "https://www.baidu.com",
		UIType:   "url",
		AllUsers: false,
	})
	if err != nil {
		t.Fatalf("BuildPackage failed: %v", err)
	}
	defer os.RemoveAll(pkgDir)

	// Verify ui/config
	cfgBytes, err := os.ReadFile(filepath.Join(pkgDir, "ui", "config"))
	if err != nil {
		t.Fatalf("read ui/config failed: %v", err)
	}

	var root map[string]interface{}
	if err := json.Unmarshal(cfgBytes, &root); err != nil {
		t.Fatalf("unmarshal ui/config failed: %v", err)
	}

	urlMap, ok := root[".url"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing .url in ui/config")
	}

	entry, ok := urlMap["fndocker.baidu"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing fndocker.baidu entry in ui/config")
	}

	// In redirect mode:
	// 1. url must be /cgi/ThirdParty/fndocker.baidu/index.cgi/redirect/fndocker.baidu/_
	expectedURL := "/cgi/ThirdParty/fndocker.baidu/index.cgi/redirect/fndocker.baidu/_"
	if entry["url"] != expectedURL {
		t.Errorf("entry url = %v, want %v", entry["url"], expectedURL)
	}

	// 2. port must NOT be present
	if _, hasPort := entry["port"]; hasPort {
		t.Errorf("entry port should be omitted in redirect mode, got %v", entry["port"])
	}

	// 3. protocol must be http
	if entry["protocol"] != "http" {
		t.Errorf("entry protocol = %v, want http", entry["protocol"])
	}

	// 4. index.cgi must exist and be executable
	cgiPath := filepath.Join(pkgDir, "ui", "index.cgi")
	fi, err := os.Stat(cgiPath)
	if err != nil {
		t.Fatalf("index.cgi not found at %s", cgiPath)
	}
	if fi.Mode()&0111 == 0 {
		t.Errorf("index.cgi is not executable: mode=%v", fi.Mode())
	}

	cgiContent, err := os.ReadFile(cgiPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cgiContent), "https://www.baidu.com") {
		t.Errorf("index.cgi does not contain target URL fallback: %s", string(cgiContent))
	}
}

func TestReconcileInstalledItems(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-reconcile-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	installer := NewInstaller(tmpDir, "icon.png")
	items := []DesktopItem{
		{
			ID:      "item-1",
			AppName: "fndocker.app-one",
			Name:    "App 1",
			Enabled: false,
		},
		{
			ID:      "item-2",
			AppName: "fndocker.app-two",
			Name:    "App 2",
			Enabled: true,
		},
	}

	// Should run cleanly without panic or error
	installer.ReconcileInstalledItems(items)
}

func TestGetAppStatus(t *testing.T) {
	installer := &Installer{}
	if status := installer.getAppStatus("any-app"); status != "" {
		t.Errorf("expected empty status when cliPath is empty, got %q", status)
	}
}

func TestUninstallItemProtectedApps(t *testing.T) {
	installer := &Installer{}
	item := DesktopItem{
		ID:            "docklabel-my-container",
		AppName:       "fndocker.dock-my-container",
		ContainerName: "my-container",
		Port:          8080,
	}

	// Should run cleanly and protect specified active packages
	err := installer.UninstallItem(item, "fndocker.dock-my-container", "fndocker.my-container-123456")
	if err != nil {
		t.Fatalf("UninstallItem returned unexpected error: %v", err)
	}
}

func TestBuildPackageNoticeMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-installer-notice-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	installer := NewInstaller(tmpDir, "icon.png")

	// Case 1: Port > 0 (Proxy / local port mode) with notice enabled.
	// MUST declare "port" and url="/" in ui/config so FN Connect assigns dedicated subdomain routing,
	// and notice modal is served directly by the reverse proxy on that port.
	pkgDir, err := installer.BuildPackage(AppcenterPackageConfig{
		AppName:       "fndocker.app-notice",
		Title:         "应用提示测试",
		Desc:          "测试开屏提示",
		Port:          5288,
		Protocol:      "http",
		Path:          "/",
		UIType:        "url",
		AllUsers:      true,
		NoticeEnabled: true,
		NoticeContent: "注意：这是开屏提示内容",
	})
	if err != nil {
		t.Fatalf("BuildPackage failed: %v", err)
	}
	defer os.RemoveAll(pkgDir)

	cfgBytes, err := os.ReadFile(filepath.Join(pkgDir, "ui", "config"))
	if err != nil {
		t.Fatalf("read ui/config failed: %v", err)
	}

	var root map[string]interface{}
	if err := json.Unmarshal(cfgBytes, &root); err != nil {
		t.Fatalf("unmarshal ui/config failed: %v", err)
	}

	urlMap := root[".url"].(map[string]interface{})
	entry := urlMap["fndocker.app-notice"].(map[string]interface{})

	// 1. Port MUST be preserved so FN Connect grants independent subdomain routing
	if portVal, ok := entry["port"].(string); !ok || portVal != "5288" {
		t.Errorf("expected entry port to be 5288, got %v", entry["port"])
	}
	if entry["url"] != "/" {
		t.Errorf("expected entry url to be /, got %v", entry["url"])
	}
	if entry["protocol"] != "http" {
		t.Errorf("expected entry protocol to be http, got %v", entry["protocol"])
	}

	// Case 2: Port == 0 (Shortcut mode) with notice enabled.
	// Uses CGI redirect on fnOS gateway.
	pkgDir2, err := installer.BuildPackage(AppcenterPackageConfig{
		AppName:       "fndocker.shortcut-notice",
		Title:         "外链提示测试",
		Desc:          "测试快捷方式开屏提示",
		Port:          0,
		Protocol:      "",
		Path:          "https://example.com",
		UIType:        "url",
		AllUsers:      true,
		NoticeEnabled: true,
		NoticeContent: "注意：这是外链提示",
	})
	if err != nil {
		t.Fatalf("BuildPackage shortcut failed: %v", err)
	}
	defer os.RemoveAll(pkgDir2)

	cfgBytes2, err := os.ReadFile(filepath.Join(pkgDir2, "ui", "config"))
	if err != nil {
		t.Fatalf("read shortcut ui/config failed: %v", err)
	}
	var root2 map[string]interface{}
	if err := json.Unmarshal(cfgBytes2, &root2); err != nil {
		t.Fatalf("unmarshal shortcut ui/config failed: %v", err)
	}
	entry2 := root2[".url"].(map[string]interface{})["fndocker.shortcut-notice"].(map[string]interface{})
	expectedCGI := "/cgi/ThirdParty/fndocker.shortcut-notice/index.cgi/redirect/fndocker.shortcut-notice/_"
	if entry2["url"] != expectedCGI {
		t.Errorf("shortcut entry url = %v, want %v", entry2["url"], expectedCGI)
	}
	if _, hasPort := entry2["port"]; hasPort {
		t.Errorf("shortcut entry port should be omitted, got %v", entry2["port"])
	}

	cgiPath := filepath.Join(pkgDir2, "ui", "index.cgi")
	fi, err := os.Stat(cgiPath)
	if err != nil {
		t.Fatalf("index.cgi not found at %s", cgiPath)
	}
	if fi.Mode()&0111 == 0 {
		t.Errorf("index.cgi is not executable: mode=%v", fi.Mode())
	}
	cgiContent, err := os.ReadFile(cgiPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cgiContent), "注意：这是外链提示") {
		t.Errorf("index.cgi does not contain notice content")
	}

	// Case 3: Port > 0 with AllUsers: false in input should still produce allUsers: true in ui/config
	// to prevent FN Connect 403 WAN rejection.
	pkgDir3, err := installer.BuildPackage(AppcenterPackageConfig{
		AppName:       "fndocker.app-allusers",
		Title:         "权限隔离测试",
		Port:          5289,
		AllUsers:      false,
	})
	if err != nil {
		t.Fatalf("BuildPackage failed: %v", err)
	}
	defer os.RemoveAll(pkgDir3)
	cfgBytes3, _ := os.ReadFile(filepath.Join(pkgDir3, "ui", "config"))
	var root3 map[string]interface{}
	_ = json.Unmarshal(cfgBytes3, &root3)
	entry3 := root3[".url"].(map[string]interface{})["fndocker.app-allusers"].(map[string]interface{})
	if entry3["allUsers"] != true {
		t.Errorf("expected allUsers to be true when port > 0, got %v", entry3["allUsers"])
	}
}

