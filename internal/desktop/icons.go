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
func WritePackageIcons(pkgDir string, customIconPathOrURL string, rootIconPath string) error {
	imagesDir := filepath.Join(pkgDir, "app", "ui", "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return err
	}

	var iconImg image.Image

	// 1. Try loading custom icon
	if customIconPathOrURL != "" {
		iconImg, _ = loadIconImage(customIconPathOrURL)
	}

	// 2. Try loading root product icon if custom icon failed or wasn't provided
	if iconImg == nil && rootIconPath != "" {
		iconImg, _ = loadIconImage(rootIconPath)
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

	// 4. Fallback to embedded default icons
	return writeIconBytes(pkgDir, imagesDir, defaultIcon64Bytes, defaultIcon256Bytes)
}

func writeIconBytes(pkgDir, imagesDir string, b64, b256 []byte) error {
	// Root icons for App Center
	_ = os.WriteFile(filepath.Join(pkgDir, "ICON.PNG"), b64, 0644)
	_ = os.WriteFile(filepath.Join(pkgDir, "ICON_256.PNG"), b256, 0644)

	// UI Images for Desktop
	_ = os.WriteFile(filepath.Join(imagesDir, "icon_64.png"), b64, 0644)
	_ = os.WriteFile(filepath.Join(imagesDir, "icon_256.png"), b256, 0644)
	_ = os.WriteFile(filepath.Join(imagesDir, "icon_{0}.png"), b256, 0644)
	_ = os.WriteFile(filepath.Join(imagesDir, "icon-64.png"), b64, 0644)
	_ = os.WriteFile(filepath.Join(imagesDir, "icon-256.png"), b256, 0644)
	_ = os.WriteFile(filepath.Join(imagesDir, "icon.png"), b256, 0644)
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
