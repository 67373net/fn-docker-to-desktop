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

//go:embed assets/PRODUCT_ICON.PNG
var productIcon64Bytes []byte

//go:embed assets/PRODUCT_ICON_256.PNG
var productIcon256Bytes []byte

var cdnMirrors = []string{
	"https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png",
	"https://gcore.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png",
	"https://testingcf.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png",
	"https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png",
}

// WriteProductIcons writes the official product icons (64x64 and 256x256) into the package.
func WriteProductIcons(pkgDir string) error {
	imagesDir := filepath.Join(pkgDir, "app", "ui", "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return err
	}
	return writeIconBytes(pkgDir, imagesDir, productIcon64Bytes, productIcon256Bytes)
}

// WritePackageIcons writes all required icons into the fnOS app directory.
// customIconPathOrURL: user-provided icon path/URL/dataURI/base64
// iconsDir: server icons cache directory
// candidates: list of candidate names (image, container name, service name, title) to auto-resolve
func WritePackageIcons(pkgDir string, customIconPathOrURL string, iconsDir string, candidates ...string) error {
	// If this is our own product package or user requested icon.png, always write product icon
	cleanCustom := strings.TrimPrefix(strings.TrimSpace(customIconPathOrURL), "/")
	if cleanCustom == "icon.png" || filepath.Base(pkgDir) == "fn-docker-to-desktop" {
		return WriteProductIcons(pkgDir)
	}

	imagesDir := filepath.Join(pkgDir, "app", "ui", "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return err
	}

	var iconImg image.Image

	// 1. Try loading user-provided custom icon
	if strings.TrimSpace(customIconPathOrURL) != "" {
		if img, err := loadIconImage(customIconPathOrURL, iconsDir); err == nil && img != nil {
			iconImg = img
			slog.Info("成功加载并解析自定义桌面图标", "source", SummarizeIconSource(customIconPathOrURL))
		} else {
			slog.Warn("加载自定义图标失败，将尝试自动匹配官方图标", "source", SummarizeIconSource(customIconPathOrURL), "error", err)
		}
	}

	// 2. If no custom icon or failed, try auto-resolving from CDN mirrors if candidate names available
	if iconImg == nil && len(candidates) > 0 {
		var allNames []string
		for _, raw := range candidates {
			if raw != "" {
				allNames = append(allNames, getIconCandidates(raw)...)
			}
		}

		seen := make(map[string]bool)
		for _, name := range allNames {
			if seen[name] || name == "" {
				continue
			}
			seen[name] = true
			if img, err := fetchIconFromMirrors(name); err == nil && img != nil {
				iconImg = img
				slog.Info("自动匹配并成功下载官方服务图标", "name", name)
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

	// 4. Fallback to embedded default icons (dedicated container shortcut icon)
	slog.Info("使用系统内置专属容器快捷方式默认图标", "pkgDir", pkgDir)
	return writeIconBytes(pkgDir, imagesDir, defaultIcon64Bytes, defaultIcon256Bytes)
}

// SummarizeIconSource returns a short readable summary of an icon source for logging.
func SummarizeIconSource(source string) string {
	s := strings.TrimSpace(source)
	if strings.HasPrefix(s, "data:") {
		idx := strings.Index(s, ",")
		if idx != -1 {
			return fmt.Sprintf("%s (base64 %d bytes)", s[:idx], len(s)-idx)
		}
		return "data:image/... (base64)"
	}
	if len(s) > 80 {
		return s[:40] + "..." + s[len(s)-20:]
	}
	return s
}

func getIconCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var candidates []string
	addCandidate := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.ToLower(s)
		s = strings.TrimPrefix(s, "/")
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				return r
			}
			return -1
		}, s)
		clean = strings.Trim(clean, "-")
		if clean != "" {
			for _, c := range candidates {
				if c == clean {
					return
				}
			}
			candidates = append(candidates, clean)
		}
	}

	// 1. If it's a docker image (e.g. linuxserver/qbittorrent:latest, alistteam/alist, or portainer/portainer-ce)
	imagePart := raw
	if strings.Contains(imagePart, "/") {
		parts := strings.Split(imagePart, "/")
		imagePart = parts[len(parts)-1]
	}
	if strings.Contains(imagePart, ":") {
		imagePart = strings.Split(imagePart, ":")[0]
	}
	if strings.Contains(imagePart, "@") {
		imagePart = strings.Split(imagePart, "@")[0]
	}
	addCandidate(imagePart)

	// 2. Also try sub-prefix before hyphen or underscore (e.g. "portainer-ce" -> "portainer")
	if idx := strings.Index(imagePart, "-"); idx > 0 {
		addCandidate(imagePart[:idx])
	}
	if idx := strings.Index(imagePart, "_"); idx > 0 {
		addCandidate(imagePart[:idx])
	}

	// 3. Raw cleaned string
	addCandidate(raw)
	rawClean := strings.TrimRight(strings.ToLower(raw), "0123456789-_")
	if rawClean != "" && rawClean != raw {
		addCandidate(rawClean)
	}

	return candidates
}

// FetchIconBytesFromMirrors attempts to download an icon by candidate names (e.g. image name, service name) from Homarr CDN mirrors.
func FetchIconBytesFromMirrors(candidates ...string) ([]byte, string, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	var allNames []string
	for _, raw := range candidates {
		if raw != "" {
			allNames = append(allNames, getIconCandidates(raw)...)
		}
	}
	seen := make(map[string]bool)
	var lastErr error
	for _, name := range allNames {
		if seen[name] || name == "" {
			continue
		}
		seen[name] = true
		for _, tmpl := range cdnMirrors {
			url := fmt.Sprintf(tmpl, name)
			resp, err := client.Get(url)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				continue
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				lastErr = err
				continue
			}
			if len(data) > 0 {
				ct := resp.Header.Get("Content-Type")
				if ct == "" {
					ct = http.DetectContentType(data)
				}
				return data, ct, nil
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no matching candidates")
	}
	return nil, "", lastErr
}

func fetchIconFromMirrors(name string) (image.Image, error) {
	data, _, err := FetchIconBytesFromMirrors(name)
	if err != nil {
		return nil, err
	}
	return decodeAnyImage(data)
}

func decodeAnyImage(data []byte) (image.Image, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("空图像数据")
	}

	// Check for ICO format
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 {
		if img, err := decodeICO(data); err == nil {
			return img, nil
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func loadIconImage(source string, iconsDir string) (image.Image, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("empty icon source")
	}

	// 1. Data URI: data:image/png;base64,...
	if strings.HasPrefix(source, "data:") {
		idx := strings.Index(source, ",")
		if idx == -1 {
			return nil, fmt.Errorf("invalid data URI")
		}
		base64Data := strings.TrimSpace(source[idx+1:])
		raw, err := base64.StdEncoding.DecodeString(base64Data)
		if err != nil {
			// Try URL-safe encoding
			raw, err = base64.URLEncoding.DecodeString(base64Data)
		}
		if err != nil {
			return nil, fmt.Errorf("base64 decode failed: %w", err)
		}
		return decodeAnyImage(raw)
	}

	// 2. Raw Base64 string without data: prefix
	if len(source) > 64 && !strings.Contains(source, " ") && !strings.Contains(source, "/") && !strings.Contains(source, ":") {
		if raw, err := base64.StdEncoding.DecodeString(source); err == nil {
			if img, err := decodeAnyImage(raw); err == nil {
				return img, nil
			}
		}
	}

	// 3. HTTP/HTTPS URL
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		// Build candidates for raw.githubusercontent.com
		urlCandidates := []string{}
		if strings.HasPrefix(source, "https://raw.githubusercontent.com/") {
			cleanRaw := strings.TrimPrefix(source, "https://raw.githubusercontent.com/")
			parts := strings.SplitN(cleanRaw, "/", 4)
			if len(parts) == 4 {
				jsDelivrURL := fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s@%s/%s", parts[0], parts[1], parts[2], parts[3])
				fastlyURL := fmt.Sprintf("https://fastly.jsdelivr.net/gh/%s/%s@%s/%s", parts[0], parts[1], parts[2], parts[3])
				urlCandidates = append(urlCandidates, fastlyURL, jsDelivrURL)
			}
			urlCandidates = append(urlCandidates, "https://ghproxy.net/"+source)
		}
		urlCandidates = append(urlCandidates, source)

		client := &http.Client{Timeout: 6 * time.Second}
		for _, targetURL := range urlCandidates {
			resp, err := client.Get(targetURL)
			if err == nil && resp.StatusCode == http.StatusOK {
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err == nil && len(data) > 0 {
					if img, err := decodeAnyImage(data); err == nil && img != nil {
						return img, nil
					}
				}
			} else if resp != nil {
				resp.Body.Close()
			}
		}

		// If URL fetch failed (e.g. 404 or connection error), extract candidate name from URL and try CDN mirrors
		baseName := filepath.Base(source)
		if idx := strings.Index(baseName, "?"); idx != -1 {
			baseName = baseName[:idx]
		}
		cleanName := strings.TrimSuffix(baseName, filepath.Ext(baseName))
		cleanName = strings.TrimRight(cleanName, "0123456789-_")
		if cleanName != "" {
			if img, err := fetchIconFromMirrors(cleanName); err == nil && img != nil {
				return img, nil
			}
		}

		// Also check local host fallback for baseName
		if baseName != "" {
			for _, b := range []string{"/home", "/home/net67373", "/vol1", "/var/apps"} {
				pats := []string{
					filepath.Join(b, "*", "icons", baseName),
					filepath.Join(b, "*", "README.assets", baseName),
					filepath.Join(b, "*", baseName),
				}
				for _, pat := range pats {
					matches, _ := filepath.Glob(pat)
					for _, match := range matches {
						if fi, err := os.Stat(match); err == nil && !fi.IsDir() {
							if data, err := os.ReadFile(match); err == nil {
								if img, err := decodeAnyImage(data); err == nil && img != nil {
									return img, nil
								}
							}
						}
					}
				}
			}
		}
	}

	// 4. Local file paths (check multiple possible locations)
	cleanSource := strings.TrimPrefix(source, "file://")
	cleanSource = strings.TrimPrefix(cleanSource, "/icons/")
	cleanSource = strings.TrimPrefix(cleanSource, "icons/")
	cleanSource = strings.TrimPrefix(cleanSource, "./")

	var possiblePaths []string
	if iconsDir != "" {
		possiblePaths = append(possiblePaths,
			filepath.Join(iconsDir, filepath.Base(cleanSource)),
			filepath.Join(iconsDir, cleanSource),
		)
	}
	possiblePaths = append(possiblePaths,
		cleanSource,
		strings.TrimPrefix(source, "file://"),
	)
	if dataShare := os.Getenv("TRIM_DATA_SHARE_PATHS"); dataShare != "" {
		possiblePaths = append(possiblePaths, filepath.Join(dataShare, filepath.Base(cleanSource)))
	}

	baseName := filepath.Base(cleanSource)
	for _, b := range []string{"/home", "/home/net67373", "/vol1", "/var/apps"} {
		pats := []string{
			filepath.Join(b, "*", "icons", baseName),
			filepath.Join(b, "*", "README.assets", baseName),
			filepath.Join(b, "*", baseName),
		}
		for _, pat := range pats {
			matches, _ := filepath.Glob(pat)
			possiblePaths = append(possiblePaths, matches...)
		}
	}

	for _, p := range possiblePaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			fileBytes, err := os.ReadFile(p)
			if err == nil {
				if img, err := decodeAnyImage(fileBytes); err == nil {
					return img, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("未能通过任何已知路径找到或解析图标文件: %s", source)
}

func writeIconBytes(pkgDir, imagesDir string, b64, b256 []byte) error {
	// Root icons for App Center
	_ = os.WriteFile(filepath.Join(pkgDir, "ICON.PNG"), b64, 0644)
	_ = os.WriteFile(filepath.Join(pkgDir, "ICON_256.PNG"), b256, 0644)

	// Write icons to both app/ui/images and ui/images for maximum compatibility
	iconDirs := []string{imagesDir}
	uiImagesDir := filepath.Join(pkgDir, "ui", "images")
	if uiImagesDir != imagesDir {
		_ = os.MkdirAll(uiImagesDir, 0755)
		iconDirs = append(iconDirs, uiImagesDir)
	}

	for _, dir := range iconDirs {
		_ = os.WriteFile(filepath.Join(dir, "icon-64.png"), b64, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon-256.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon-{0}.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon_64.png"), b64, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon_256.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon_{0}.png"), b256, 0644)
		_ = os.WriteFile(filepath.Join(dir, "icon.png"), b256, 0644)
	}
	return nil
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
