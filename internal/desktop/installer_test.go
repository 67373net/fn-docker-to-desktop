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

