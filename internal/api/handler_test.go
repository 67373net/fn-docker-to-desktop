package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	iconsDir := filepath.Join(tempDir, "icons")
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		t.Fatalf("Failed to create icons dir: %v", err)
	}
	// Create a dummy custom icon file
	customIconName := "test-custom-icon.png"
	if err := os.WriteFile(filepath.Join(iconsDir, customIconName), []byte("fake-icon-data"), 0644); err != nil {
		t.Fatalf("Failed to write custom icon: %v", err)
	}

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	authMgr := auth.NewManager("")

	// Add a sample desktop item with custom icon
	testItem := desktop.DesktopItem{
		ID:        "item-test-1",
		AppName:   "fndocker.testapp",
		Name:      "Test App",
		Port:      8080,
		Protocol:  "http",
		TargetURL: "http://127.0.0.1:8080",
		Mode:      desktop.ModeLocalPort,
		Icon:      "/icons/" + customIconName,
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
		AppVersion: "1.1.21",
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 1. Test ZIP Export (Default)
	req := httptest.NewRequest("GET", "/api/desktop/export", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	disposition := rec.Header().Get("Content-Disposition")
	if disposition == "" || !strings.Contains(disposition, "fn-desktop-icons-") || !strings.Contains(disposition, ".zip") {
		t.Errorf("Expected Content-Disposition to contain 'fn-desktop-icons-' and '.zip', got %q", disposition)
	}
	if cType := rec.Header().Get("Content-Type"); cType != "application/zip" {
		t.Errorf("Expected Content-Type application/zip, got %q", cType)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("Failed to parse returned ZIP archive: %v", err)
	}

	var hasJSON, hasIcon bool
	for _, f := range zipReader.File {
		if f.Name == "desktop-items.json" {
			hasJSON = true
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("Failed to open desktop-items.json inside ZIP: %v", err)
			}
			var exportResult struct {
				Version string                `json:"version"`
				Total   int                   `json:"total"`
				Items   []desktop.DesktopItem `json:"items"`
			}
			if err := json.NewDecoder(rc).Decode(&exportResult); err != nil {
				_ = rc.Close()
				t.Fatalf("Failed to decode desktop-items.json inside ZIP: %v", err)
			}
			_ = rc.Close()
			if exportResult.Version != "1.1.21" {
				t.Errorf("Expected version 1.1.21 inside ZIP, got %s", exportResult.Version)
			}
			if exportResult.Total != 1 || len(exportResult.Items) != 1 {
				t.Errorf("Expected 1 item inside ZIP, got %d items", exportResult.Total)
			}
		}
		if f.Name == "icons/"+customIconName {
			hasIcon = true
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("Failed to open custom icon in ZIP: %v", err)
			}
			data, _ := io.ReadAll(rc)
			_ = rc.Close()
			if string(data) != "fake-icon-data" {
				t.Errorf("Expected fake-icon-data in ZIP, got %q", string(data))
			}
		}
	}
	if !hasJSON {
		t.Error("Expected ZIP to contain desktop-items.json")
	}
	if !hasIcon {
		t.Error("Expected ZIP to contain icons/" + customIconName)
	}

	// 2. Test JSON Export (?format=json)
	reqJSON := httptest.NewRequest("GET", "/api/desktop/export?format=json", nil)
	reqJSON.RemoteAddr = "127.0.0.1:1234"
	recJSON := httptest.NewRecorder()
	mux.ServeHTTP(recJSON, reqJSON)

	if recJSON.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for JSON export, got %d", recJSON.Code)
	}
	var jsonExport struct {
		Version string                `json:"version"`
		Total   int                   `json:"total"`
		Items   []desktop.DesktopItem `json:"items"`
	}
	if err := json.NewDecoder(recJSON.Body).Decode(&jsonExport); err != nil {
		t.Fatalf("Failed to decode JSON export: %v", err)
	}
	if jsonExport.Version != "1.1.21" || jsonExport.Total != 1 {
		t.Errorf("Unexpected JSON export result: %+v", jsonExport)
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
		AppVersion: "1.1.21",
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
		AppVersion: "1.1.21",
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

func TestNoticePageRedirect(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-notice-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// 1. Item with Notice Enabled
	itemWithNotice := desktop.DesktopItem{
		ID:            "item-notice-1",
		Name:          "服务带公告",
		AppName:       "fndocker.withnotice-123456",
		Mode:          "local",
		Port:          8081,
		Enabled:       true,
		NoticeEnabled: true,
		NoticeContent: "注意：系统将在凌晨维护更新！",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := storage.SaveItem(itemWithNotice); err != nil {
		t.Fatalf("Failed to save itemWithNotice: %v", err)
	}

	// 2. Item without Notice
	itemWithoutNotice := desktop.DesktopItem{
		ID:            "item-no-notice-2",
		Name:          "普通服务",
		AppName:       "fndocker.nonotice-123456",
		Mode:          "local",
		Port:          8082,
		Enabled:       true,
		NoticeEnabled: false,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := storage.SaveItem(itemWithoutNotice); err != nil {
		t.Fatalf("Failed to save itemWithoutNotice: %v", err)
	}

	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    auth.NewManager(""),
		DataDir:    tempDir,
		AppVersion: "1.1.21",
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Test 1: Access item with notice -> expect 200 OK interstitial notice page
	req1 := httptest.NewRequest("GET", "/redirect/fndocker.withnotice-123456/_", nil)
	req1.Host = "192.168.1.100:5900"
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Errorf("Expected notice page 200 OK, got %d", rec1.Code)
	}
	body1 := rec1.Body.String()
	if !strings.Contains(body1, "注意：系统将在凌晨维护更新！") {
		t.Errorf("Expected notice content in body, got: %s", body1)
	}
	if !strings.Contains(body1, "进入应用") {
		t.Errorf("Expected proceed button '进入应用' in body, got: %s", body1)
	}

	// Test 2: Access item without notice -> expect 302 Found direct redirect
	req2 := httptest.NewRequest("GET", "/redirect/fndocker.nonotice-123456/_", nil)
	req2.Host = "192.168.1.100:5900"
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusFound {
		t.Errorf("Expected 302 Found direct redirect, got %d", rec2.Code)
	}
	location2 := rec2.Header().Get("Location")
	if !strings.Contains(location2, ":8082") {
		t.Errorf("Expected redirect target to contain port 8082, got: %s", location2)
	}
}

func TestHandleUploadIconSizeLimitAndResize(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-upload-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storage, _ := desktop.NewStorage(tempDir)
	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    auth.NewManager(""),
		DataDir:    tempDir,
		AppVersion: "1.1.21",
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 1. Test upload exceeding 10MB limit -> expect 400 Bad Request
	{
		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("icon", "huge.png")
		if err != nil {
			t.Fatalf("CreateFormFile failed: %v", err)
		}
		// Write 10MB + 1KB
		hugeData := make([]byte, (10<<20)+1024)
		_, _ = part.Write(hugeData)
		_ = writer.Close()

		req := httptest.NewRequest("POST", "/api/icons/upload", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("Expected status 400 for upload exceeding 10MB, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "10MB") {
			t.Errorf("Expected response to mention 10MB limit, got: %s", rec.Body.String())
		}
	}

	// 2. Test uploading a 400x200 PNG image -> auto-resized and compressed to 256x256 PNG
	{
		srcImg := image.NewRGBA(image.Rect(0, 0, 400, 200))
		for y := 0; y < 200; y++ {
			for x := 0; x < 400; x++ {
				srcImg.Set(x, y, color.RGBA{R: 20, G: 150, B: 220, A: 255})
			}
		}
		var imgBuf bytes.Buffer
		if err := png.Encode(&imgBuf, srcImg); err != nil {
			t.Fatalf("Failed to encode test png: %v", err)
		}

		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("icon", "test_banner.png")
		if err != nil {
			t.Fatalf("CreateFormFile failed: %v", err)
		}
		_, _ = part.Write(imgBuf.Bytes())
		_ = writer.Close()

		req := httptest.NewRequest("POST", "/api/icons/upload", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Expected status 200 for valid icon upload, got %d. Body: %s", rec.Code, rec.Body.String())
		}

		var resp struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("Failed to decode upload response: %v", err)
		}
		if !strings.HasPrefix(resp.URL, "/icons/") {
			t.Fatalf("Expected returned url to start with /icons/, got %s", resp.URL)
		}

		// Verify the saved file on disk is resized to 256x256
		savedFilePath := filepath.Join(tempDir, resp.URL)
		f, err := os.Open(savedFilePath)
		if err != nil {
			t.Fatalf("Failed to open saved icon file %s: %v", savedFilePath, err)
		}
		defer f.Close()

		decodedImg, _, err := image.Decode(f)
		if err != nil {
			t.Fatalf("Failed to decode saved icon file: %v", err)
		}
		bounds := decodedImg.Bounds()
		if bounds.Dx() != 256 || bounds.Dy() != 256 {
			t.Errorf("Expected resized image dimensions 256x256, got %dx%d", bounds.Dx(), bounds.Dy())
		}
	}
}

func TestWatchcowEndpoints(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-watchcow-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mockSock := filepath.Join(tempDir, "mock-docker.sock")
	listener, err := net.Listen("unix", mockSock)
	if err != nil {
		t.Fatalf("Failed to listen on mock unix socket: %v", err)
	}
	defer listener.Close()

	mockServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"Id":    "c1234567890abcdef",
					"Names": []string{"/watchcow-test-container"},
					"Image": "test/image:latest",
					"State": "running",
					"Labels": map[string]string{
						"watchcow.enable":       "false",
						"watchcow.display_name": "Test App",
						"watchcow.service_port": "9999",
					},
				},
			})
		}),
	}
	go func() {
		_ = mockServer.Serve(listener)
	}()
	defer mockServer.Close()

	desktop.SetWatchcowDockerSocketPath(mockSock)
	defer desktop.SetWatchcowDockerSocketPath("/var/run/docker.sock")

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    auth.NewManager(""),
		DataDir:    tempDir,
		AppVersion: "1.1.35",
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// 1. GET /api/desktop/docklabel
	req := httptest.NewRequest("GET", "/api/desktop/docklabel", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for GET /api/desktop/docklabel, got %d", rec.Code)
	}

	var items []desktop.DockLabelItem
	if err := json.NewDecoder(rec.Body).Decode(&items); err != nil {
		t.Fatalf("Failed to decode docklabel items response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Expected 1 docklabel item, got %d", len(items))
	}
	if items[0].ID != "docklabel-watchcow-test-container" {
		t.Errorf("Expected item ID 'docklabel-watchcow-test-container', got %s", items[0].ID)
	}
	if !strings.HasPrefix(items[0].AppName, "fndocker.dock-") {
		t.Errorf("Expected item AppName to start with 'fndocker.dock-', got %s", items[0].AppName)
	}

	// 2. Toggle a docklabel item state
	testID := items[0].ID
	storage.SetDockLabelState(testID, false)
	if storage.GetDockLabelState(testID, true) != false {
		t.Errorf("Expected initial state for %s to be false", testID)
	}

	reqToggle := httptest.NewRequest("POST", "/api/desktop/docklabel/"+testID+"/toggle", nil)
	reqToggle.RemoteAddr = "127.0.0.1:1234"
	recToggle := httptest.NewRecorder()
	mux.ServeHTTP(recToggle, reqToggle)

	if recToggle.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for toggle, got %d. Body: %s", recToggle.Code, recToggle.Body.String())
	}

	var toggleResp struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(recToggle.Body).Decode(&toggleResp); err != nil {
		t.Fatalf("Failed to decode toggle response: %v", err)
	}

	if !toggleResp.Enabled {
		t.Errorf("Expected toggle to switch from false to true, got false")
	}
	if storage.GetDockLabelState(testID, false) != true {
		t.Errorf("Expected storage state to be persisted as true")
	}

	// 3. GET /api/desktop/docklabel/icon (cached fast-path)
	idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(testID)))
	iconsDir := filepath.Join(tempDir, "icons")
	_ = os.MkdirAll(iconsDir, 0755)
	testIconData := []byte("fast-cache-icon-bytes")
	_ = os.WriteFile(filepath.Join(iconsDir, "dock_cache_"+idHash+".png"), testIconData, 0644)

	reqIcon := httptest.NewRequest("GET", "/api/desktop/docklabel/icon?id="+testID, nil)
	recIcon := httptest.NewRecorder()
	mux.ServeHTTP(recIcon, reqIcon)
	if recIcon.Code != http.StatusOK {
		t.Fatalf("Expected status 200 for GET /api/desktop/docklabel/icon, got %d", recIcon.Code)
	}
	if !bytes.Equal(recIcon.Body.Bytes(), testIconData) {
		t.Errorf("Expected icon body to match cached icon data")
	}
}

func TestDeleteIcon(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "fn-test-icon-del-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	iconsDir := filepath.Join(tempDir, "icons")
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		t.Fatalf("Failed to create icons dir: %v", err)
	}

	storage, err := desktop.NewStorage(tempDir)
	if err != nil {
		t.Fatalf("Failed to init storage: %v", err)
	}

	// 1. Create test icons: one unused, one in-use, one protected
	unusedIcon := "unused.png"
	inUseIcon := "inuse.png"
	os.WriteFile(filepath.Join(iconsDir, unusedIcon), []byte("fake-png-data"), 0644)
	os.WriteFile(filepath.Join(iconsDir, inUseIcon), []byte("fake-png-data"), 0644)
	os.WriteFile(filepath.Join(iconsDir, "icon.png"), []byte("fake-png-data"), 0644)

	// Add an item using inUseIcon
	storage.SaveItem(desktop.DesktopItem{
		ID:   "item-1",
		Name: "Item 1",
		Icon: "/icons/" + inUseIcon,
		Port: 8080,
	})

	handler := NewHandler(Config{
		Storage:    storage,
		AuthMgr:    auth.NewManager(""),
		DataDir:    tempDir,
		AppVersion: "1.1.35",
	})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Case 1: Try deleting protected icon -> 403 Forbidden
	reqProtected := httptest.NewRequest("DELETE", "/api/icons/icon.png", nil)
	recProtected := httptest.NewRecorder()
	mux.ServeHTTP(recProtected, reqProtected)
	if recProtected.Code != http.StatusForbidden {
		t.Errorf("Expected 403 for protected icon, got %d", recProtected.Code)
	}

	// Case 2: Try deleting in-use icon -> 400 Bad Request
	reqInUse := httptest.NewRequest("DELETE", "/api/icons/"+inUseIcon, nil)
	recInUse := httptest.NewRecorder()
	mux.ServeHTTP(recInUse, reqInUse)
	if recInUse.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for in-use icon, got %d", recInUse.Code)
	}

	// Case 3: Try deleting unused icon -> 200 OK and file removed
	reqUnused := httptest.NewRequest("DELETE", "/api/icons/"+unusedIcon, nil)
	recUnused := httptest.NewRecorder()
	mux.ServeHTTP(recUnused, reqUnused)
	if recUnused.Code != http.StatusOK {
		t.Errorf("Expected 200 for unused icon, got %d. Body: %s", recUnused.Code, recUnused.Body.String())
	}
	if _, err := os.Stat(filepath.Join(iconsDir, unusedIcon)); !os.IsNotExist(err) {
		t.Errorf("Expected file %s to be removed", unusedIcon)
	}
}

