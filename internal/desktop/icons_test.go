package desktop

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func createTestPNG(t *testing.T, dir, filename string) string {
	t.Helper()
	imgPath := filepath.Join(dir, filename)
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode failed: %v", err)
	}
	if err := os.WriteFile(imgPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	return imgPath
}

func TestIsLocalOrLoopbackIconURL(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
		baseName string
	}{
		{"/icons/images.png", true, "images.png"},
		{"icons/danmu.png", true, "danmu.png"},
		{"http://127.0.0.1:5900/icons/images.png", true, "images.png"},
		{"http://localhost:5900/icons/danmu.png", true, "danmu.png"},
		{"http://127.0.0.1/icons/siyuan-128.png", true, "siyuan-128.png"},
		{"http://localhost/icons/images2.png?v=1", true, "images2.png"},
		{"https://raw.githubusercontent.com/tf4fun/watchcow/main/icon.png", false, ""},
		{"https://example.com/icon.png", false, ""},
		{"data:image/png;base64,abc", false, ""},
	}

	for _, tt := range tests {
		isLocal, base := IsLocalOrLoopbackIconURL(tt.input)
		if isLocal != tt.expected {
			t.Errorf("IsLocalOrLoopbackIconURL(%q) isLocal = %v, expected %v", tt.input, isLocal, tt.expected)
		}
		if isLocal && base != tt.baseName {
			t.Errorf("IsLocalOrLoopbackIconURL(%q) base = %q, expected %q", tt.input, base, tt.baseName)
		}
	}
}

func TestLoadIconImageLocalLoopback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "icons-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	createTestPNG(t, tempDir, "images.png")
	createTestPNG(t, tempDir, "danmu.png")

	// 1. Test loopback URL pointing to images.png on port 5900 (should load from tempDir without network call)
	img, err := loadIconImage("http://127.0.0.1:5900/icons/images.png", tempDir)
	if err != nil {
		t.Fatalf("loadIconImage with loopback URL failed: %v", err)
	}
	if img == nil {
		t.Fatalf("expected non-nil image")
	}

	// 2. Test relative /icons/danmu.png path
	img2, err := loadIconImage("/icons/danmu.png", tempDir)
	if err != nil {
		t.Fatalf("loadIconImage with relative path failed: %v", err)
	}
	if img2 == nil {
		t.Fatalf("expected non-nil image")
	}
}
