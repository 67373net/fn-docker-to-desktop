package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fn-docker-to-desktop/internal/auth"
	"fn-docker-to-desktop/internal/desktop"
)

func TestHandleExportDesktopItems(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-api-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	authMgr := auth.NewManager("")

	// Add a sample desktop item
	testItem := desktop.DesktopItem{
		ID:        "item-test-1",
		AppName:   "fndocker.testapp",
		Name:      "Test App",
		Port:      8080,
		Protocol:  "http",
		TargetURL: "http://127.0.0.1:8080",
		Mode:      desktop.ModeLocalPort,
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := storage.SaveItem(testItem); err != nil {
		t.Fatalf("Failed to save test item: %v", err)
	}

	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    authMgr,
		DataDir:    tempDir,
		AppVersion: "1.1.10",
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/desktop/export", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	disposition := rec.Header().Get("Content-Disposition")
	if disposition == "" || !strings.Contains(disposition, "fn-desktop-icons-") {
		t.Errorf("Expected Content-Disposition to contain 'fn-desktop-icons-', got %q", disposition)
	}

	var exportResult struct {
		Version    string                `json:"version"`
		ExportedAt string                `json:"exported_at"`
		Total      int                   `json:"total"`
		Items      []desktop.DesktopItem `json:"items"`
	}

	if err := json.NewDecoder(rec.Body).Decode(&exportResult); err != nil {
		t.Fatalf("Failed to decode export response: %v", err)
	}

	if exportResult.Version != "1.1.10" {
		t.Errorf("Expected version 1.1.10, got %s", exportResult.Version)
	}
	if exportResult.Total != 1 {
		t.Errorf("Expected total 1, got %d", exportResult.Total)
	}
	if len(exportResult.Items) != 1 || exportResult.Items[0].ID != "item-test-1" {
		t.Errorf("Expected 1 item with ID item-test-1, got %+v", exportResult.Items)
	}
}

func TestWANSecurityBlocking(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-wan-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// No password configured
	authMgr := auth.NewManager("")

	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    authMgr,
		DataDir:    tempDir,
		AppVersion: "1.1.10",
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Simulate request from public internet IP (e.g. 203.0.113.199)
	req := httptest.NewRequest("GET", "/api/desktop/items", nil)
	req.RemoteAddr = "203.0.113.199:54321"
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Expected WAN unauthenticated access to be blocked with 401, got %d. Body: %s", rec.Code, rec.Body.String())
	}
}

func TestWANWithValidSessionToken(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-session-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	authMgr := auth.NewManager("")

	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    authMgr,
		DataDir:    tempDir,
		AppVersion: "1.1.10",
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create valid frontend session token
	token := globalAppSessionMgr.CreateSession()

	// Simulate WAN access (e.g. fnOS Connect) with valid session token in header
	req := httptest.NewRequest("GET", "/api/desktop/items", nil)
	req.RemoteAddr = "203.0.113.199:54321"
	req.Header.Set("X-App-Session", token)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected WAN access with valid session token to succeed with 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}
}
