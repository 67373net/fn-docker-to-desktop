package desktop

import (
	_ "embed"
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/ICON.PNG
var defaultIcon64Bytes []byte

//go:embed assets/ICON_256.PNG
var defaultIcon256Bytes []byte

// WritePackageIcons writes all required icons into the fnOS app directory.
// customIconPathOrURL: user-provided icon path/URL/dataURI
// fallbackCandidate: service name, container name, or title used to auto-resolve from homarr-labs CDN
func WritePackageIcons(pkgDir string, customIconPathOrURL string, fallbackCandidate string) error {
	imagesDir := filepath.Join(pkgDir, "app", "ui", "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return err
	}

	var iconImg image.Image

	// 1. Try loading user-provided custom icon
	if customIconPathOrURL != "" {
		if img, err := loadIconImage(customIconPathOrURL); err == nil && img != nil {
			iconImg = img
		} else {
			slog.Debug("加载自定义图标未成功，尝试备选方案", "source", customIconPathOrURL, "error", err)
		}
	}

	// 2. If no custom icon or failed, try auto-resolving from Homarr CDN if candidate name available
	if iconImg == nil && fallbackCandidate != "" {
		candidates := getIconCandidates(fallbackCandidate)
		for _, name := range candidates {
			cdnURL := fmt.Sprintf("https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png", name)
			if img, err := loadIconImageWithTimeout(cdnURL, 2*time.Second); err == nil && img != nil {
				iconImg = img
				slog.Info("自动匹配并下载官方服务图标成功", "name", name, "url", cdnURL)
				break
			}
		}
	}

	// 3. If loaded an image, scale and save both 64 and 256 versions
	if iconImg != nil {
		sqImg := padToSquare(iconImg)
		img64 := resizeNearest(sqImg, 64, 64)
		img256 := resizeNearest(sqImg, 256, 256)

		buf64 := new(bytes.Buffer)
		buf256 := new(bytes.Buffer)
		if err := png.Encode(buf64, img64); err == nil && png.Encode(buf256, img256) == nil {
			return writeIconBytes(pkgDir, imagesDir, buf64.Bytes(), buf256.Bytes())
		}
	}

	// 4. Fallback to embedded default icons (generic container cube icon)
	return writeIconBytes(pkgDir, imagesDir, defaultIcon64Bytes, defaultIcon256Bytes)
}

func getIconCandidates(raw string) []string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.TrimPrefix(raw, "/")

	var candidates []string
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, raw)
	clean = strings.Trim(clean, "-")
	if clean != "" {
		candidates = append(candidates, clean)
	}

	trimmed := strings.TrimRight(clean, "0123456789-")
	if trimmed != "" && trimmed != clean {
		candidates = append(candidates, trimmed)
	}

	if idx := strings.Index(clean, "-"); idx > 0 {
		candidates = append(candidates, clean[:idx])
	}
	return candidates
}

func loadIconImageWithTimeout(source string, timeout time.Duration) (image.Image, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		client := &http.Client{Timeout: timeout}
		resp, err := client.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}
	return loadIconImage(source)
}

func writeIconBytes(pkgDir, imagesDir string, b64, b256 []byte) error {
	// Root icons for App Center
	_ = os.WriteFile(filepath.Join(pkgDir, "ICON.PNG"), b64, 0644)
	_ = os.WriteFile(filepath.Join(pkgDir, "ICON_256.PNG"), b256, 0644)

	// Write icons to both app/ui/images and ui/images for compatibility
	iconDirs := []string{imagesDir}
	uiImagesDir := filepath.Join(pkgDir, "ui", "images")
	if uiImagesDir != imagesDir {
		_ = os.MkdirAll(uiImagesDir, 0755)
		iconDirs = append(iconDirs, uiImagesDir)
	}

	for _, dir := range iconDirs {
		_ = os.WriteFile(filepath.Join(dir, "icon_64.png"), b64, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon_256.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon_{0}.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon-{0}.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon-64.png"), b64, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon-256.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon.png"), b256, 0644)
	}
	return nil
}

func loadIconImage(source string) (image.Image, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("empty icon source")
	}

	// Data URI
	if strings.HasPrefix(source, "data:") {
		idx := strings.Index(source, ",")
		if idx == -1 {
			return nil, fmt.Errorf("invalid data URI")
		}
		raw, err := base64.StdEncoding.DecodeString(source[idx+1:])
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		return img, err
	}

	// HTTP/HTTPS URL
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}

	// Local file
	filePath := strings.TrimPrefix(source, "file://")
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(fileBytes))
	return img, err
}

func padToSquare(src image.Image) image.Image {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w == h {
		return src
	}
	size := w
	if h > size {
		size = h
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	// Transparent fill
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dst.Set(x, y, color.Transparent)
		}
	}
	offsetX := (size - w) / 2
	offsetY := (size - h) / 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(offsetX+x, offsetY+y, src.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return dst
}

func resizeNearest(src image.Image, targetWidth, targetHeight int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW == 0 || srcH == 0 {
		return dst
	}

	for y := 0; y < targetHeight; y++ {
		srcY := bounds.Min.Y + (y * srcH) / targetHeight
		for x := 0; x < targetWidth; x++ {
			srcX := bounds.Min.X + (x * srcW) / targetWidth
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}
