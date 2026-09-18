package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLifecycleLogLine(t *testing.T) {
	raw1 := "[2026-09-18 13:27:04.522] [main] Stopping fn-docker-to-desktop..."
	entry1 := parseLifecycleLogLine(raw1)
	if entry1.Timestamp != "2026-09-18 13:27:04.522" {
		t.Errorf("Expected timestamp 2026-09-18 13:27:04.522, got %s", entry1.Timestamp)
	}
	if entry1.Level != "INFO" {
		t.Errorf("Expected level INFO, got %s", entry1.Level)
	}
	if !strings.Contains(entry1.Message, "Stopping fn-docker-to-desktop...") {
		t.Errorf("Expected message to contain Stopping..., got %s", entry1.Message)
	}

	raw2 := "[2026-09-18 13:27:05.100] [uninstall_init] ERROR: failed to remove app"
	entry2 := parseLifecycleLogLine(raw2)
	if entry2.Level != "ERROR" {
		t.Errorf("Expected level ERROR, got %s", entry2.Level)
	}

	raw3 := "[2026-09-18 13:27:06.200] [upgrade_init] WARN 警告：注意跳过清理"
	entry3 := parseLifecycleLogLine(raw3)
	if entry3.Level != "WARN" {
		t.Errorf("Expected level WARN, got %s", entry3.Level)
	}
}

func TestReadLifecycleLogs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	l, err := Init(tempDir, 8)
	if err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer l.Close()

	// Write mock lifecycle log
	lifecyclePath := filepath.Join(tempDir, "logs", "lifecycle.log")
	content := `[2026-09-18 13:27:04.522] [main] Stopping fn-docker-to-desktop...
[2026-09-18 13:27:05.775] [upgrade_init] START
[2026-09-18 13:27:11.903] [uninstall_init] 检测到当前处于应用覆盖升级/重装阶段，跳过注销！
[2026-09-18 13:27:18.432] [main] Process is healthy and running
`
	if err := os.WriteFile(lifecyclePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write mock lifecycle log: %v", err)
	}

	resp, err := l.ReadLifecycleLogs("ALL", "", 100)
	if err != nil {
		t.Fatalf("ReadLifecycleLogs failed: %v", err)
	}

	if resp.TotalLines < 4 {
		t.Errorf("Expected at least 4 lines, got %d", resp.TotalLines)
	}

	// Test search filter
	respSearch, err := l.ReadLifecycleLogs("ALL", "覆盖升级", 100)
	if err != nil {
		t.Fatalf("ReadLifecycleLogs with search failed: %v", err)
	}
	if len(respSearch.Lines) != 1 {
		t.Errorf("Expected 1 line with search, got %d", len(respSearch.Lines))
	}
}
