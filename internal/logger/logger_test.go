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
	if entry1.Level != "info" {
		t.Errorf("Expected level info, got %s", entry1.Level)
	}
	if !strings.Contains(entry1.Message, "Stopping fn-docker-to-desktop...") {
		t.Errorf("Expected message to contain Stopping..., got %s", entry1.Message)
	}

	raw2 := "[2026-09-18 13:27:05.100] [uninstall_init] ERROR: failed to remove app"
	entry2 := parseLifecycleLogLine(raw2)
	if entry2.Level != "error" {
		t.Errorf("Expected level error, got %s", entry2.Level)
	}

	raw3 := "[2026-09-18 13:27:06.200] [upgrade_init] WARN 警告：注意跳过清理"
	entry3 := parseLifecycleLogLine(raw3)
	if entry3.Level != "warn" {
		t.Errorf("Expected level warn, got %s", entry3.Level)
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

func TestStreamingReadLogsDescending(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger-stream-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	l, err := Init(tempDir, 8)
	if err != nil {
		t.Fatalf("Failed to init logger: %v", err)
	}
	defer l.Close()

	// Write sequential app logs
	_, _ = l.Write([]byte("2026-09-22 10:00:00 [INFO] First message\n"))
	_, _ = l.Write([]byte("2026-09-22 10:05:00 [WARN] Second message warning\n"))
	_, _ = l.Write([]byte("2026-09-22 10:10:00 [ERROR] Third message failed\n"))

	// Also mock lifecycle log
	lifecyclePath := filepath.Join(tempDir, "logs", "lifecycle.log")
	_ = os.WriteFile(lifecyclePath, []byte("[2026-09-22 10:08:00.123] [main] Lifecycle event\n"), 0644)

	// Read combined unified logs
	resp, err := l.ReadLogs("ALL", "ALL", "", 100)
	if err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	if resp.TotalLines != 4 {
		t.Fatalf("Expected 4 total lines, got %d", resp.TotalLines)
	}

	// Verify descending order: newest at top (10:10:00, then 10:08:00, then 10:05:00, then 10:00:00)
	if resp.Lines[0].Timestamp != "2026-09-22 10:10:00" {
		t.Errorf("Expected index 0 to be newest timestamp 2026-09-22 10:10:00, got %s", resp.Lines[0].Timestamp)
	}
	if resp.Lines[0].Level != "error" {
		t.Errorf("Expected level error, got %s", resp.Lines[0].Level)
	}
	if resp.Lines[0].Source != "app" {
		t.Errorf("Expected source app, got %s", resp.Lines[0].Source)
	}

	if resp.Lines[1].Timestamp != "2026-09-22 10:08:00.123" {
		t.Errorf("Expected index 1 timestamp 2026-09-22 10:08:00.123, got %s", resp.Lines[1].Timestamp)
	}
	if resp.Lines[1].Source != "lifecycle" {
		t.Errorf("Expected source lifecycle, got %s", resp.Lines[1].Source)
	}

	if resp.Lines[3].Timestamp != "2026-09-22 10:00:00" {
		t.Errorf("Expected index 3 to be oldest timestamp 2026-09-22 10:00:00, got %s", resp.Lines[3].Timestamp)
	}
}
