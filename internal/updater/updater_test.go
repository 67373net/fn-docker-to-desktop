package updater

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatSpeed(t *testing.T) {
	cases := []struct {
		bytesSec float64
		expected string
	}{
		{500, "500 B/s"},
		{1024, "1 KB/s"},
		{1536, "2 KB/s"},
		{1024 * 1024 * 2.5, "2.5 MB/s"},
	}

	for _, c := range cases {
		got := formatSpeed(c.bytesSec)
		if got != c.expected {
			t.Errorf("formatSpeed(%v) = %q, expected %q", c.bytesSec, got, c.expected)
		}
	}
}

func TestUpdaterGetStatusDiskFallback(t *testing.T) {
	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")

	u := NewUpdater("")
	u.SetPaths(statusFile, "", "", "")

	// Idle state initially
	st := u.GetStatus()
	if st.State != "idle" {
		t.Fatalf("expected idle state, got %s", st.State)
	}

	// Write simulated disk status from previous run
	nowStr := time.Now().Format(time.RFC3339)
	diskSt := UpdateStatus{
		State:         "success",
		Progress:      100,
		Message:       "更新成功，服务已重启",
		TargetVersion: "1.1.86",
		UpdatedAt:     nowStr,
	}
	_ = u.writeDiskStatus(diskSt)

	// Since memory state is idle, GetStatus should pick up disk status
	readSt := u.GetStatus()
	if readSt.State != "success" || readSt.TargetVersion != "1.1.86" {
		t.Fatalf("expected disk status fallback, got %+v", readSt)
	}
}

func TestUpdaterDownloadAndProgress(t *testing.T) {
	// Setup test HTTP server with 200KB mock payload
	payloadSize := 200 * 1024
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "204800")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	statusFile := filepath.Join(tmpDir, "status.json")
	fpkFile := filepath.Join(tmpDir, "upgrade.fpk")
	scriptFile := filepath.Join(tmpDir, "upgrade.sh")
	logFile := filepath.Join(tmpDir, "upgrade.log")

	u := NewUpdater("/fake/bin/appcenter-cli")
	u.SetPaths(statusFile, fpkFile, scriptFile, logFile)

	// Intercept command runner
	cmdRun := false
	u.SetExecCommand(func(name string, arg ...string) *exec.Cmd {
		cmdRun = true
		// Return a harmless echo command
		return exec.Command("echo", "upgrade simulated")
	})

	err := u.StartUpdate(server.URL+"/test.fpk", "1.1.86")
	if err != nil {
		t.Fatalf("StartUpdate failed: %v", err)
	}

	// Wait up to 3 seconds for download and script launch to finish
	deadline := time.Now().Add(3 * time.Second)
	var finalSt UpdateStatus
	for time.Now().Before(deadline) {
		finalSt = u.GetStatus()
		if finalSt.State == "installing" || finalSt.State == "error" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if finalSt.State != "installing" {
		t.Fatalf("expected state installing, got: %+v", finalSt)
	}
	if !cmdRun {
		t.Fatalf("expected execCommand to be invoked")
	}

	// Verify file was written
	fi, err := os.Stat(fpkFile)
	if err != nil || fi.Size() != int64(payloadSize) {
		t.Fatalf("fpk file not saved properly: %v, size: %v", err, fi.Size())
	}

	// Verify script was written and contains target version
	scriptBytes, err := os.ReadFile(scriptFile)
	if err != nil {
		t.Fatalf("failed reading script file: %v", err)
	}
	if !strings.Contains(string(scriptBytes), "1.1.86") {
		t.Fatalf("expected script to contain target version 1.1.86")
	}
}

func TestUpdaterCancellation(t *testing.T) {
	// Slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10485760")
		w.WriteHeader(http.StatusOK)
		// Write 1KB then wait
		_, _ = w.Write(make([]byte, 1024))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	u := NewUpdater("")
	u.SetPaths(filepath.Join(tmpDir, "st.json"), filepath.Join(tmpDir, "pkg.fpk"), "", "")

	_ = u.StartUpdate(server.URL, "1.1.86")
	time.Sleep(50 * time.Millisecond)

	u.Cancel()

	st := u.GetStatus()
	if st.State != "cancelled" {
		t.Fatalf("expected cancelled state, got %s", st.State)
	}
}
