package desktop

import (
	_ "embed"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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

	// 4. Common service mappings (including Chinese service titles)
	aliasMap := map[string][]string{
		"百度":   {"baidu", "baidupan"},
		"百度盘":  {"baidu", "baidupan"},
		"百度网盘": {"baidu", "baidupan"},
		"腾讯":   {"tencent", "qq"},
		"腾讯文档": {"tencent", "qq"},
		"邮件":   {"mail", "email"},
		"邮箱":   {"mail", "email"},
		"弹幕":   {"bilibili", "danmu"},
		"阿里":   {"aliyun", "alipan"},
		"阿里云盘": {"alipan", "aliyun"},
		"迅雷":   {"thunder", "xunlei"},
		"夸克":   {"quark"},
		"网易":   {"netease"},
		"微信":   {"wechat"},
	}
	for k, list := range aliasMap {
		if strings.Contains(raw, k) {
			for _, item := range list {
				addCandidate(item)
			}
		}
	}

	return candidates
}

// LoadIconImage loads and decodes an icon from source (proxy URL, local URL, base64, HTTP, or local file).
func LoadIconImage(source string, iconsDir string) (image.Image, error) {
	return loadIconImage(source, iconsDir)
}

// PersistItemIcon ensures that an item's icon is physically saved to iconsDir
// as "copy_<itemID>.png" and updates item.Icon to that filename.
// Returns true if the item was modified.
func PersistItemIcon(item *DesktopItem, iconsDir string) bool {
	if item == nil || strings.TrimSpace(item.Icon) == "" || iconsDir == "" {
		return false
	}
	targetName := fmt.Sprintf("copy_%s.png", item.ID)
	targetPath := filepath.Join(iconsDir, targetName)

	// If already pointing to copy_<id>.png and file exists on disk with content, nothing to do
	if item.Icon == targetName {
		if fi, err := os.Stat(targetPath); err == nil && fi.Size() > 0 {
			return false
		}
	}

	// If icon is already a clean local file in iconsDir and NOT a proxy URL or remote URL, keep it!
	cleanName := strings.TrimPrefix(strings.TrimPrefix(item.Icon, "/icons/"), "icons/")
	if !strings.Contains(cleanName, "/") && !strings.Contains(cleanName, "?") && !strings.Contains(cleanName, ":") {
		localPath := filepath.Join(iconsDir, cleanName)
		if fi, err := os.Stat(localPath); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return false
		}
	}

	// 1. If it's a docklabel icon URL (/api/desktop/docklabel/icon?id=...)
	if strings.Contains(item.Icon, "/api/desktop/docklabel/icon") {
		if u, err := url.Parse(item.Icon); err == nil {
			id := u.Query().Get("id")
			if id != "" {
				idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
				for _, prefix := range []string{"dock_cache_", "wc_cache_"} {
					cp := filepath.Join(iconsDir, prefix+idHash+".png")
					if data, err := os.ReadFile(cp); err == nil && len(data) > 0 {
						if err := os.WriteFile(targetPath, data, 0644); err == nil {
							item.Icon = targetName
							slog.Info("从 DockLabel 缓存物理持久化图标成功", "id", item.ID, "target", targetName)
							return true
						}
					}
				}
				// Also try to resolve via ScanDockLabelItems
				if dItems, err := ScanDockLabelItems(nil); err == nil {
					for _, dit := range dItems {
						if dit.ID == id {
							if dit.LocalIconPath != "" {
								if data, err := os.ReadFile(dit.LocalIconPath); err == nil && len(data) > 0 {
									if err := os.WriteFile(targetPath, data, 0644); err == nil {
										item.Icon = targetName
										slog.Info("从容器挂载路径物理持久化图标成功", "id", item.ID, "target", targetName)
										return true
									}
								}
							}
							if resolved := ResolveWatchcowIconPath(dit.Icon, "", nil); resolved != "" {
								if data, err := os.ReadFile(resolved); err == nil && len(data) > 0 {
									if err := os.WriteFile(targetPath, data, 0644); err == nil {
										item.Icon = targetName
										slog.Info("从宿主机路径物理持久化图标成功", "id", item.ID, "target", targetName)
										return true
									}
								}
							}
							break
						}
					}
				}
			}
		}
	}

	// 2. Try loading via loadIconImage
	if img, err := loadIconImage(item.Icon, iconsDir); err == nil && img != nil {
		buf := new(bytes.Buffer)
		if err := png.Encode(buf, img); err == nil && buf.Len() > 0 {
			if err := os.WriteFile(targetPath, buf.Bytes(), 0644); err == nil {
				item.Icon = targetName
				slog.Info("成功加载并物理持久化桌面图标文件", "id", item.ID, "target", targetName)
				return true
			}
		}
	}

	// 3. Fallback: try fetching from candidate names (bounded to 2s)
	candidates := []string{item.Icon, item.ContainerName, item.Name}
	if data, _, err := FetchIconBytesFromMirrors(candidates...); err == nil && len(data) > 0 {
		if err := os.WriteFile(targetPath, data, 0644); err == nil {
			item.Icon = targetName
			slog.Info("从镜像匹配并物理持久化桌面图标文件", "id", item.ID, "target", targetName)
			return true
		}
	}

	return false
}

// FetchIconBytesFromMirrors attempts to download an icon by candidate names (e.g. image name, service name) from Homarr CDN mirrors.
// It is hard-bounded to 2 seconds total timeout to prevent browser connection starvation.
func FetchIconBytesFromMirrors(candidates ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 1200 * time.Millisecond}
	var allNames []string
	for _, raw := range candidates {
		if raw != "" {
			allNames = append(allNames, getIconCandidates(raw)...)
		}
	}
	if len(allNames) > 3 {
		allNames = allNames[:3]
	}

	fastMirrors := []string{
		"https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png",
		"https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png",
	}

	seen := make(map[string]bool)
	var lastErr error
	for _, name := range allNames {
		if seen[name] || name == "" {
			continue
		}
		seen[name] = true
		for _, tmpl := range fastMirrors {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			default:
			}
			url := fmt.Sprintf(tmpl, name)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				continue
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
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

// IsLocalOrLoopbackIconURL checks if a URL is pointing to a local/loopback icon endpoint
// such as http://127.0.0.1:5900/icons/images.png or http://localhost/icons/danmu.png or /icons/xxx.png.
// If so, it returns true and the extracted icon base filename.
func IsLocalOrLoopbackIconURL(urlStr string) (bool, string) {
	s := strings.TrimSpace(urlStr)
	if s == "" {
		return false, ""
	}
	if strings.HasPrefix(s, "/icons/") || strings.HasPrefix(s, "icons/") {
		clean := strings.TrimPrefix(strings.TrimPrefix(s, "/icons/"), "icons/")
		base := filepath.Base(clean)
		if idx := strings.Index(base, "?"); idx != -1 {
			base = base[:idx]
		}
		if base != "" && base != "." && base != "/" {
			return true, base
		}
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err == nil {
			host := strings.ToLower(u.Hostname())
			isLoopback := host == "127.0.0.1" || host == "localhost" || host == "0.0.0.0" || host == "::1" || host == "ip6-localhost" || host == "ip6-loopback"
			if strings.HasPrefix(u.Path, "/icons/") {
				base := filepath.Base(u.Path)
				if idx := strings.Index(base, "?"); idx != -1 {
					base = base[:idx]
				}
				if base != "" && base != "." && base != "/" {
					if isLoopback || u.Port() == "5900" || u.Port() == "" {
						return true, base
					}
					return true, base
				}
			}
		}
	}
	return false, ""
}

func loadIconImage(source string, iconsDir string) (image.Image, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("empty icon source")
	}

	// 0. Check if it's an internal Docklabel icon proxy URL (e.g. /api/desktop/docklabel/icon?id=docklabel-xxx)
	if strings.Contains(source, "/api/desktop/docklabel/icon") {
		if u, err := url.Parse(source); err == nil {
			id := u.Query().Get("id")
			if id != "" {
				idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
				for _, prefix := range []string{"dock_cache_", "wc_cache_"} {
					cachedPath := filepath.Join(iconsDir, prefix+idHash+".png")
					if data, err := os.ReadFile(cachedPath); err == nil && len(data) > 0 {
						if img, err := decodeAnyImage(data); err == nil && img != nil {
							return img, nil
						}
					}
				}
				cleanID := strings.TrimPrefix(id, "docklabel-")
				cleanID = strings.TrimPrefix(cleanID, "watchcow-")
				if resolved := ResolveWatchcowIconPath(cleanID, "", nil); resolved != "" {
					if data, err := os.ReadFile(resolved); err == nil && len(data) > 0 {
						if img, err := decodeAnyImage(data); err == nil && img != nil {
							return img, nil
						}
					}
				}
			}
		}
	}

	// 1. Check if it's a local/loopback icon URL (e.g. http://127.0.0.1:5900/icons/xxx, /icons/xxx)
	if isLocal, baseName := IsLocalOrLoopbackIconURL(source); isLocal && baseName != "" {
		// A. Check in iconsDir
		if iconsDir != "" {
			target := filepath.Join(iconsDir, baseName)
			if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
				if data, err := os.ReadFile(target); err == nil {
					if img, err := decodeAnyImage(data); err == nil && img != nil {
						return img, nil
					}
				}
			}
		}
		// B. Check via ResolveWatchcowIconPath on host
		if resolved := ResolveWatchcowIconPath(baseName, "", nil); resolved != "" {
			if data, err := os.ReadFile(resolved); err == nil {
				if img, err := decodeAnyImage(data); err == nil && img != nil {
					if iconsDir != "" {
						_ = os.WriteFile(filepath.Join(iconsDir, baseName), data, 0644)
					}
					return img, nil
				}
			}
		}
	}

	// 2. Data URI: data:image/png;base64,...
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

	// 3. Raw Base64 string without data: prefix
	if len(source) > 64 && !strings.Contains(source, " ") && !strings.Contains(source, "/") && !strings.Contains(source, ":") {
		if raw, err := base64.StdEncoding.DecodeString(source); err == nil {
			if img, err := decodeAnyImage(raw); err == nil {
				return img, nil
			}
		}
	}

	// 4. HTTP/HTTPS URL
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		if isLocal, _ := IsLocalOrLoopbackIconURL(source); !isLocal {
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

			client := &http.Client{Timeout: 2 * time.Second}
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
			if resolved := ResolveWatchcowIconPath(baseName, "", nil); resolved != "" {
				if data, err := os.ReadFile(resolved); err == nil {
					if img, err := decodeAnyImage(data); err == nil && img != nil {
						if iconsDir != "" {
							_ = os.WriteFile(filepath.Join(iconsDir, baseName), data, 0644)
						}
						return img, nil
					}
				}
			}
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
