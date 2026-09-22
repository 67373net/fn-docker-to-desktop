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

	// Verify "app" sourceFilter only reads app logs
	appResp, err := l.ReadLogs("app", "ALL", "", 100)
	if err != nil {
		t.Fatalf("ReadLogs for app failed: %v", err)
	}
	if appResp.TotalLines != 3 {
		t.Errorf("Expected exactly 3 app logs, got %d", appResp.TotalLines)
	}
	for _, line := range appResp.Lines {
		if line.Source != "app" {
			t.Errorf("Expected only app logs, got source %s", line.Source)
		}
	}
}

func TestCliSpinnerAndGarbageFiltering(t *testing.T) {
	garbageLines := []string{
		"| uninstalling.[Info]Uninstall",
		"checking",
		"- uninstalling..",
		"\\ uninstalling..",
		"| uninstalling.",
		"/ uninstalling....",
		"Verifying files",
		"|",
		"/",
		"-",
		"\\",
	}

	for _, g := range garbageLines {
		if !isCliSpinnerLine(g) {
			// At least one filter must catch it
			entry := parseLifecycleLogLine(g)
			if entry.Raw != "" {
				t.Errorf("Expected garbage line to be rejected, but got parsed: %q", g)
			}
		}
		entry := parseLifecycleLogLine(g)
		if entry.Raw != "" {
			t.Errorf("parseLifecycleLogLine should return empty LogEntry for garbage: %q", g)
		}
	}
}

func TestDualRetentionPruning(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "logger-prune-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "test.log")
	// Write 5 lines: 2 older than 28 days, 3 recent
	content := "2020-01-01 10:00:00 [INFO] Old line 1\n" +
		"2020-01-02 10:00:00 [INFO] Old line 2\n" +
		"2026-09-20 10:00:00 [INFO] Recent line 1\n" +
		"2026-09-21 10:00:00 [INFO] Recent line 2\n" +
		"2026-09-22 10:00:00 [INFO] Recent line 3\n"

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Prune with 28 days retention: the 2020 lines must be removed
	if err := pruneLogFile(filePath, 28, 1024*1024, 1024*1024); err != nil {
		t.Fatalf("pruneLogFile failed: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read pruned file: %v", err)
	}
	result := string(data)
	if strings.Contains(result, "2020-01-01") || strings.Contains(result, "2020-01-02") {
		t.Errorf("Old lines older than 28 days were not pruned: %s", result)
	}
	if !strings.Contains(result, "Recent line 1") || !strings.Contains(result, "Recent line 3") {
		t.Errorf("Recent lines were accidentally pruned: %s", result)
	}

	// Test size-based pruning: set targetBytes to very small (e.g. 50 bytes)
	if err := pruneLogFile(filePath, 28, 50, 45); err != nil {
		t.Fatalf("pruneLogFile size limit failed: %v", err)
	}

	dataSize, _ := os.ReadFile(filePath)
	if len(dataSize) > 50 {
		t.Errorf("Expected pruned file to be <= 50 bytes, got %d bytes", len(dataSize))
	}
	// The latest line should be preserved
	if !strings.Contains(string(dataSize), "Recent line 3") {
		t.Errorf("Expected newest line 3 to be kept, got: %s", string(dataSize))
	}
}
