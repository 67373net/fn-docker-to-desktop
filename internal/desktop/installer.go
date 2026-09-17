package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MinAppNameLength = 3
	MaxAppNameLength = 32
)

var validAppNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateAppName validates the identifier according to fnOS package naming rules.
func ValidateAppName(appName string) error {
	if len(appName) < MinAppNameLength {
		return fmt.Errorf("应用标识 %q 过短: 必须至少包含 %d 个字符", appName, MinAppNameLength)
	}
	if len(appName) > MaxAppNameLength {
		return fmt.Errorf("应用标识 %q 过长: 不能超过 %d 个字符 (当前: %d)", appName, MaxAppNameLength, len(appName))
	}
	if !validAppNamePattern.MatchString(appName) {
		return fmt.Errorf("应用标识 %q 包含非法字符: 需为字母数字开头且仅含字母、数字、点号、下划线与短横线", appName)
	}
	return nil
}

// Installer handles fnOS application packaging and appcenter-cli interaction.
type Installer struct {
	mu              sync.Mutex
	cliPath         string
	appsDir         string
	rootIconPath    string
	iconsDir        string
	hasAppcenterCLI bool
	reconcileStatus map[string]string
}

// SetIconsDir sets the server icon directory for uploaded icons.
func (i *Installer) SetIconsDir(iconsDir string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.iconsDir = iconsDir
}

// NewInstaller creates a new fnOS package installer.
func NewInstaller(dataDir string, rootIconPath string) *Installer {
	appsDir := filepath.Join(dataDir, "apps")
	_ = os.MkdirAll(appsDir, 0755)
	iconsDir := filepath.Join(dataDir, "icons")
	_ = os.MkdirAll(iconsDir, 0755)

	cliPath, found := findAppcenterCLI()

	inst := &Installer{
		cliPath:         cliPath,
		appsDir:         appsDir,
		iconsDir:        iconsDir,
		rootIconPath:    rootIconPath,
		hasAppcenterCLI: found,
		reconcileStatus: make(map[string]string),
	}

	if found {
		slog.Info("检测到飞牛官方包管理工具 appcenter-cli", "path", cliPath)
	} else {
		slog.Info("未检测到 appcenter-cli (运行于独立环境或模拟模式)")
	}

	return inst
}

// GetReconcileStatus returns whether the given item is currently being restored.
func (i *Installer) GetReconcileStatus(itemID, appName string) (bool, string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.reconcileStatus == nil {
		return false, ""
	}
	if status, ok := i.reconcileStatus[itemID]; ok && status != "" {
		return true, status
	}
	if status, ok := i.reconcileStatus[appName]; ok && status != "" {
		return true, status
	}
	return false, ""
}

// HasCLI returns whether appcenter-cli is detected on the host.
func (i *Installer) HasCLI() bool {
	return i.hasAppcenterCLI
}

func findAppcenterCLI() (string, bool) {
	paths := []string{
		"/var/apps/appcenter/target/bin/appcenter-cli",
		"/usr/bin/appcenter-cli",
		"/usr/local/bin/appcenter-cli",
		"/host/root/usr/bin/appcenter-cli",
		"/host/root/var/apps/appcenter/target/bin/appcenter-cli",
	}

	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}

	if p, err := exec.LookPath("appcenter-cli"); err == nil {
		return p, true
	}

	return "", false
}

// DeriveAppName generates a valid, guaranteed-unique fnOS package identifier for an item.
func (i *Installer) DeriveAppName(item DesktopItem) string {
	base := ""
	if item.ContainerName != "" {
		base = item.ContainerName
	} else if item.Name != "" && isASCIIAlphanumeric(item.Name) {
		base = item.Name
	} else if item.Port > 0 {
		base = fmt.Sprintf("port-%d", item.Port)
	} else {
		base = "app"
	}

	sanitizedBase := SanitizeAppNamePart(base)

	// Derive a short unique discriminator from item.ID (e.g. "item-528153" -> "528153")
	shortID := strings.TrimPrefix(item.ID, "item-")
	shortID = SanitizeAppNamePart(shortID)
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	if shortID == "" {
		digest := sha256.Sum256([]byte(item.ID + item.Name + strconv.Itoa(item.Port)))
		shortID = hex.EncodeToString(digest[:])[:6]
	}

	// Prefix "fndocker." is 9 chars. Max total is 32 chars.
	// We reserve len(shortID) + 1 for "-<shortID>".
	maxBaseLen := MaxAppNameLength - 9 - len(shortID) - 1
	if maxBaseLen < 3 {
		maxBaseLen = 3
	}
	if len(sanitizedBase) > maxBaseLen {
		sanitizedBase = strings.TrimRight(sanitizedBase[:maxBaseLen], "-")
	}
	if sanitizedBase == "" {
		sanitizedBase = "app"
	}

	appName := fmt.Sprintf("fndocker.%s-%s", sanitizedBase, shortID)
	if len(appName) > MaxAppNameLength {
		appName = appName[:MaxAppNameLength]
	}
	return appName
}

func isASCIIAlphanumeric(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return len(s) > 0
}

// SanitizeAppNamePart normalizes a string for fnOS package identifiers.
func SanitizeAppNamePart(name string) string {
	name = strings.ToLower(strings.TrimPrefix(name, "/"))
	name = strings.ReplaceAll(name, "_", "-")
	var sb strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			sb.WriteRune(c)
		}
	}
	res := strings.Trim(sb.String(), "-")
	if res == "" {
		return "app"
	}
	return res
}

func (i *Installer) resolveInstallVolume() string {
	if i.cliPath != "" {
		cmd := exec.Command(i.cliPath, "default-volume")
		output, err := cmd.CombinedOutput()
		if err == nil {
			outStr := strings.TrimSpace(string(output))
			slog.Info("查询飞牛默认存储卷输出", "output", outStr)
			if v, err := strconv.Atoi(outStr); err == nil && v > 0 {
				return strconv.Itoa(v)
			}
			re := regexp.MustCompile(`\d+`)
			if m := re.FindString(outStr); m != "" {
				if v, err := strconv.Atoi(m); err == nil && v > 0 {
					return strconv.Itoa(v)
				}
			}
		} else {
			slog.Debug("获取默认存储卷未返回有效值，将回退到默认卷1", "error", err)
		}
	}
	return "1"
}

func (i *Installer) isAppInstalled(appName string) bool {
	if i.cliPath == "" {
		return false
	}

	// Parse appcenter-cli list table output (strictly aligned with WatchCow approach)
	cmd := exec.Command(i.cliPath, "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Debug("appcenter-cli list 检查失败", "error", err)
		return false
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		// Table rows start with Unicode box character '│'
		if strings.HasPrefix(line, "│") {
			parts := strings.Split(line, "│")
			if len(parts) >= 2 {
				installedApp := strings.TrimSpace(parts[1])
				if installedApp == appName {
					slog.Debug("检测到应用已安装", "appName", appName)
					return true
				}
			}
		}
	}
	return false
}

func (i *Installer) getAppStatus(appName string) string {
	if i.cliPath == "" || appName == "" {
		return ""
	}
	cmd := exec.Command(i.cliPath, "status", appName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Debug("appcenter-cli status 检查失败", "appName", appName, "error", err)
	}
	return strings.ToLower(strings.TrimSpace(string(output)))
}

// AppcenterPackageConfig holds fields to generate an fnOS package.
type AppcenterPackageConfig struct {
	AppName       string
	Title         string
	Desc          string
	Port          int
	Protocol      string
	Path          string
	UIType        string // "url" or "iframe"
	AllUsers      bool
	IconPath      string
	ContainerName string
	Image         string
	NoticeEnabled bool
	NoticeContent string
	FileTypes     []string
	NoDisplay     bool
}

// BuildPackage creates the fnOS app structure on disk in a temporary directory.
func (i *Installer) BuildPackage(cfg AppcenterPackageConfig) (string, error) {
	pkgDir, err := os.MkdirTemp("", "fndocker-"+cfg.AppName+"-")
	if err != nil {
		return "", fmt.Errorf("创建临时打包目录失败: %w", err)
	}
	_ = os.Chmod(pkgDir, 0755)

	dirs := []string{
		pkgDir,
		filepath.Join(pkgDir, "app"),
		filepath.Join(pkgDir, "app", "ui"),
		filepath.Join(pkgDir, "app", "ui", "images"),
		filepath.Join(pkgDir, "ui"),
		filepath.Join(pkgDir, "ui", "images"),
		filepath.Join(pkgDir, "cmd"),
		filepath.Join(pkgDir, "config"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			_ = os.RemoveAll(pkgDir)
			return "", fmt.Errorf("创建目录失败 %s: %w", d, err)
		}
		_ = os.Chmod(d, 0755)
	}

	// 1. Manifest
	title := cfg.Title
	if title == "" {
		title = "桌面应用"
	}
	desc := cfg.Desc
	if desc == "" {
		desc = fmt.Sprintf("由 把 Docker 放到桌面 生成的快捷方式 - %s", title)
	}

	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}

	manifestContent := fmt.Sprintf(`appname=%s
version=1.0.0
display_name=%s
desc=%s
arch=%s
platform=all
source=thirdparty
maintainer=67373net
distributor=67373net
os_min_version=0.9.0
install_type=root
desktop_uidir=ui
`, cfg.AppName, title, desc, arch)

	if !cfg.NoDisplay {
		manifestContent += fmt.Sprintf("desktop_applaunchname=%s\n", cfg.AppName)
		if cfg.Port > 0 {
			manifestContent += fmt.Sprintf("service_port=%d\ncheckport=false\n", cfg.Port)
		}
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "manifest"), []byte(manifestContent), 0644); err != nil {
		_ = os.RemoveAll(pkgDir)
		return "", err
	}

	// 2. UI Config
	portStr := ""
	if cfg.Port > 0 {
		portStr = strconv.Itoa(cfg.Port)
	}
	proto := cfg.Protocol
	urlPath := cfg.Path
	if urlPath == "" {
		urlPath = "/"
	}
	uiType := cfg.UIType
	if uiType == "" {
		uiType = "url"
	}

	isExternalURL := strings.HasPrefix(urlPath, "http://") || strings.HasPrefix(urlPath, "https://")

	entryMap := map[string]interface{}{
		"title":     title,
		"icon":      "images/icon-{0}.png",
		"type":      uiType,
		"allUsers":  cfg.AllUsers,
		"noDisplay": cfg.NoDisplay,
	}
	if len(cfg.FileTypes) > 0 {
		entryMap["fileTypes"] = cfg.FileTypes
	}

	isNotice := strings.TrimSpace(cfg.NoticeContent) != ""

	if (cfg.Port == 0 && isExternalURL) || isNotice {
		// CGI redirect mode strictly aligned with WatchCow and for Notice mode
		entryMap["type"] = uiType
		entryMap["protocol"] = "http"
		entryMap["url"] = fmt.Sprintf("/cgi/ThirdParty/%s/index.cgi/redirect/%s/_", cfg.AppName, cfg.AppName)
	} else {
		if proto == "" {
			proto = "http"
		}
		entryMap["protocol"] = proto
		if portStr != "" {
			entryMap["port"] = portStr
		}
		entryMap["url"] = urlPath
	}

	uiConfigMap := map[string]interface{}{
		".url": map[string]interface{}{
			cfg.AppName: entryMap,
		},
	}
	uiJson, err := json.MarshalIndent(uiConfigMap, "", "    ")
	if err != nil {
		_ = os.RemoveAll(pkgDir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "app", "ui", "config"), uiJson, 0644); err != nil {
		_ = os.RemoveAll(pkgDir)
		return "", err
	}
	// Also write to ui/config for desktop_uidir=ui compatibility
	_ = os.WriteFile(filepath.Join(pkgDir, "ui", "config"), uiJson, 0644)

	// Write index.cgi for CGI redirect mode or notice mode
	if (cfg.Port == 0 && isExternalURL) || isNotice {
		var cgiScript string
		if isNotice {
			targetJsExpr := ""
			if cfg.Port > 0 {
				protoVal := proto
				if protoVal == "" {
					protoVal = "http"
				}
				targetJsExpr = fmt.Sprintf(`"%s://" + window.location.hostname + ":%d%s"`, protoVal, cfg.Port, urlPath)
			} else {
				targetJsExpr = fmt.Sprintf(`"%s"`, urlPath)
			}

			escapedNotice := html.EscapeString(cfg.NoticeContent)
			escapedTitle := html.EscapeString(title)

			cgiScript = fmt.Sprintf(`#!/bin/bash
# CGI redirect with Notice for fn-docker-to-desktop
MAIN_BIN=""
for candidate in \
    "${TRIM_APPDEST}/../fn-docker-to-desktop/app/fn-docker-to-desktop" \
    "/var/apps/fn-docker-to-desktop/target/app/fn-docker-to-desktop" \
    "/usr/local/apps/@appcenter/fn-docker-to-desktop/app/fn-docker-to-desktop" \
    "/var/apps/fn-docker-to-desktop/target/fn-docker-to-desktop" \
    "/usr/local/apps/@appcenter/fn-docker-to-desktop/fn-docker-to-desktop"; do
    if [ -x "${candidate}" ]; then
        MAIN_BIN="${candidate}"
        break
    fi
done

SOCKET=""
for s in \
    "${TRIM_PKGVAR}/../fn-docker-to-desktop/app.sock" \
    "/tmp/fn-docker-to-desktop.sock" \
    "/var/apps/fn-docker-to-desktop/target/app.sock" \
    "/usr/local/apps/@appcenter/fn-docker-to-desktop/app.sock"; do
    if [ -S "${s}" ]; then
        SOCKET="${s}"
        break
    fi
done

if [ -n "${MAIN_BIN}" ] && [ -n "${SOCKET}" ]; then
    exec "${MAIN_BIN}" --mode cgi --socket "${SOCKET}"
fi

echo "Content-Type: text/html; charset=utf-8"
echo ""
cat << 'EOFCGIHTML'
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s - 开屏提示</title>
<style>
  :root { --bg-page: #f1f5f9; --bg-card: #ffffff; --text-main: #0f172a; --text-muted: #64748b; --border: #e2e8f0; --primary: #2563eb; --primary-hover: #1d4ed8; --notice-bg: #eff6ff; --notice-border: #bfdbfe; --notice-text: #1e3a8a; }
  @media (prefers-color-scheme: dark) { :root { --bg-page: #0b0f17; --bg-card: #151b28; --text-main: #f8fafc; --text-muted: #94a3b8; --border: #242f42; --primary: #3b82f6; --primary-hover: #2563eb; --notice-bg: #172554; --notice-border: #1e40af; --notice-text: #dbeafe; } }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif; background-color: var(--bg-page); color: var(--text-main); display: flex; align-items: center; justify-content: center; min-height: 100vh; padding: 1.5rem; }
  .notice-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 14px; padding: 24px; max-width: 460px; width: 100%%; box-shadow: 0 10px 25px -5px rgba(0,0,0,0.1); }
  .notice-header { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
  .notice-app-name { font-size: 17px; font-weight: 700; color: var(--text-main); }
  .notice-body { background: var(--notice-bg); border: 1px solid var(--notice-border); color: var(--notice-text); border-radius: 10px; padding: 14px 16px; font-size: 14px; line-height: 1.6; white-space: pre-wrap; word-break: break-word; max-height: 260px; overflow-y: auto; margin-bottom: 20px; }
  .notice-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
  .skip-label { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-muted); cursor: pointer; user-select: none; }
  .btn-proceed { background-color: var(--primary); color: #fff; border: none; padding: 9px 18px; border-radius: 8px; font-size: 14px; font-weight: 600; cursor: pointer; }
  .btn-proceed:hover { background-color: var(--primary-hover); }
</style>
</head>
<body>
<div class="notice-card">
  <div class="notice-header">
    <div>
      <div class="notice-app-name">%s</div>
    </div>
  </div>
  <div class="notice-body">%s</div>
  <div class="notice-footer">
    <label class="skip-label"><input type="checkbox" id="skip-today"> <span>今日不再提示</span></label>
    <button type="button" class="btn-proceed" id="btn-proceed">进入应用</button>
  </div>
</div>
<script>
(function() {
  const TARGET_URL = %s;
  const skipKey = 'fn_notice_skip_%s';
  const today = new Date().toISOString().slice(0, 10);
  try { if (localStorage.getItem(skipKey) === today) { window.location.replace(TARGET_URL); return; } } catch(e){}
  const btn = document.getElementById('btn-proceed');
  const chk = document.getElementById('skip-today');
  function proceed() {
    if (chk && chk.checked) { try { localStorage.setItem(skipKey, today); } catch(e){} }
    window.location.replace(TARGET_URL);
  }
  if (btn) { btn.addEventListener('click', proceed); btn.focus(); }
  window.addEventListener('keydown', function(e) { if (e.key === 'Enter') proceed(); });
})();
</script>
</body>
</html>
EOFCGIHTML
exit 0
`, escapedTitle, escapedTitle, escapedNotice, targetJsExpr, cfg.AppName)
		} else {
			cgiScript = fmt.Sprintf(`#!/bin/bash
# CGI redirect for fn-docker-to-desktop
MAIN_BIN=""
for candidate in \
    "${TRIM_APPDEST}/../fn-docker-to-desktop/app/fn-docker-to-desktop" \
    "/var/apps/fn-docker-to-desktop/target/app/fn-docker-to-desktop" \
    "/usr/local/apps/@appcenter/fn-docker-to-desktop/app/fn-docker-to-desktop" \
    "/var/apps/fn-docker-to-desktop/target/fn-docker-to-desktop" \
    "/usr/local/apps/@appcenter/fn-docker-to-desktop/fn-docker-to-desktop"; do
    if [ -x "${candidate}" ]; then
        MAIN_BIN="${candidate}"
        break
    fi
done

SOCKET=""
for s in \
    "${TRIM_PKGVAR}/../fn-docker-to-desktop/app.sock" \
    "/tmp/fn-docker-to-desktop.sock" \
    "/var/apps/fn-docker-to-desktop/target/app.sock" \
    "/usr/local/apps/@appcenter/fn-docker-to-desktop/app.sock"; do
    if [ -S "${s}" ]; then
        SOCKET="${s}"
        break
    fi
done

if [ -n "${MAIN_BIN}" ] && [ -n "${SOCKET}" ]; then
    exec "${MAIN_BIN}" --mode cgi --socket "${SOCKET}"
fi

echo "Content-Type: text/html; charset=utf-8"
echo ""
cat << 'EOFCGIHTML'
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0; url=%s">
<title>正在跳转...</title>
<script>
window.location.replace("%s");
</script>
</head>
<body>
<p>正在跳转至 <a href="%s">%s</a>...</p>
</body>
</html>
EOFCGIHTML
exit 0
`, urlPath, urlPath, urlPath, urlPath)
		}

		_ = os.WriteFile(filepath.Join(pkgDir, "app", "ui", "index.cgi"), []byte(cgiScript), 0755)
		_ = os.WriteFile(filepath.Join(pkgDir, "ui", "index.cgi"), []byte(cgiScript), 0755)
	}

	// 3. Icons (write root ICON.PNG, ICON_256.PNG and all app/ui/images variants)
	if err := WritePackageIcons(pkgDir, cfg.IconPath, i.iconsDir, cfg.Image, cfg.ContainerName, cfg.Title); err != nil {
		slog.Warn("写入图标警告", "error", err)
	}

	// 4. Lifecycle scripts (all 9 scripts with 0755 permissions)
	mainScript := fmt.Sprintf(`#!/bin/bash
# Generated by 把 Docker 放到桌面 (fn-docker-to-desktop)
# Desktop shortcut app for %s

case $1 in
start|stop|status)
    exit 0
    ;;
*)
    exit 0
    ;;
esac
`, cfg.AppName)

	if err := os.WriteFile(filepath.Join(pkgDir, "cmd", "main"), []byte(mainScript), 0755); err != nil {
		_ = os.RemoveAll(pkgDir)
		return "", err
	}

	emptyScript := "#!/bin/bash\nexit 0\n"
	cmdScripts := []string{
		"install_init", "install_callback",
		"uninstall_init", "uninstall_callback",
		"upgrade_init", "upgrade_callback",
		"config_init", "config_callback",
	}
	for _, s := range cmdScripts {
		content := emptyScript
		if s == "install_callback" && cfg.Port == 0 && isExternalURL {
			content = "#!/bin/bash\nif [ -f \"${TRIM_APPDEST}/ui/index.cgi\" ]; then\n  chmod +x \"${TRIM_APPDEST}/ui/index.cgi\"\nfi\nexit 0\n"
		}
		if err := os.WriteFile(filepath.Join(pkgDir, "cmd", s), []byte(content), 0755); err != nil {
			_ = os.RemoveAll(pkgDir)
			return "", err
		}
	}

	// 5. Config files
	privilegeJson := `{"defaults":{"run-as":"root"}}`
	_ = os.WriteFile(filepath.Join(pkgDir, "config", "privilege"), []byte(privilegeJson), 0644)
	_ = os.WriteFile(filepath.Join(pkgDir, "config", "resource"), []byte("{}"), 0644)

	// 6. License
	licenseText := fmt.Sprintf("MIT License\n\nGenerated by 把 Docker 放到桌面 for %s\n", title)
	_ = os.WriteFile(filepath.Join(pkgDir, "LICENSE"), []byte(licenseText), 0644)

	return pkgDir, nil
}

// InstallItem registers a DesktopItem into fnOS.
func (i *Installer) InstallItem(item DesktopItem) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	appName := item.AppName
	if appName == "" {
		appName = i.DeriveAppName(item)
	}
	if err := ValidateAppName(appName); err != nil {
		slog.Error("应用包名标识校验不合法", "appName", appName, "id", item.ID, "error", err)
		return err
	}

	port := item.Port
	path := item.Path
	protocol := item.Protocol
	uiType := item.UIType
	if item.Mode == ModeShortcut {
		port = 0
		target := strings.TrimSpace(item.TargetURL)
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			target = "https://" + target
		}
		path = target
		protocol = ""
		uiType = "url"
	}

	slog.Info("开始构建飞牛应用安装包", "appName", appName, "title", item.Name, "port", port, "id", item.ID, "mode", item.Mode, "path", path)

	pkgDir, err := i.BuildPackage(AppcenterPackageConfig{
		AppName:       appName,
		Title:         item.Name,
		Desc:          item.Desc,
		Port:          port,
		Protocol:      protocol,
		Path:          path,
		UIType:        uiType,
		AllUsers:      item.AllUsers,
		IconPath:      item.Icon,
		ContainerName: item.ContainerName,
		Image:         item.Image,
		NoticeEnabled: item.NoticeEnabled,
		NoticeContent: item.NoticeContent,
		FileTypes:     item.FileTypes,
		NoDisplay:     item.NoDisplay,
	})
	if err != nil {
		return fmt.Errorf("构建应用包失败: %w", err)
	}
	defer os.RemoveAll(pkgDir)

	if !i.hasAppcenterCLI {
		slog.Info("应用包已构建 (模拟/开发模式)", "appName", appName, "pkgDir", pkgDir)
		return nil
	}

	volume := i.resolveInstallVolume()
	slog.Info("正在通过 appcenter-cli 执行 install-local...", "appName", appName, "title", item.Name, "volume", volume)

	startInstall := time.Now()
	cmd := exec.Command(i.cliPath, "install-local", "--volume", volume)
	cmd.Dir = pkgDir
	output, err := cmd.CombinedOutput()
	duration := time.Since(startInstall)
	if err != nil {
		outStr := cleanCliOutput(output)
		slog.Error("appcenter-cli install-local 失败", "appName", appName, "volume", volume, "duration", duration, "error", err, "output", outStr)
		return fmt.Errorf("appcenter-cli install-local 失败: %w (详情: %s)", err, outStr)
	}
	slog.Info("appcenter-cli install-local 执行完成", "appName", appName, "volume", volume, "duration", duration)

	// Wait briefly for fnOS appcenter daemon to register state
	time.Sleep(500 * time.Millisecond)

	// Verify installation
	if i.isAppInstalled(appName) {
		slog.Info("成功注册桌面应用并上线", "appName", appName, "volume", volume)
	} else {
		slog.Warn("应用已执行安装，但在 appcenter-cli list 中未立即发现，尝试检查并触发启动...", "appName", appName)
		startOut, startErr := exec.Command(i.cliPath, "start", appName).CombinedOutput()
		if startErr != nil {
			slog.Debug("appcenter-cli start 输出", "appName", appName, "output", cleanCliOutput(startOut))
		}
		if i.isAppInstalled(appName) {
			slog.Info("桌面应用现已就绪并上线", "appName", appName)
		}
	}

	return nil
}

// IsAppInstalled checks whether an application identifier is currently registered in fnOS.
func (i *Installer) IsAppInstalled(appName string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.isAppInstalled(appName)
}

// UninstallSingleApp immediately stops and uninstalls a single fnOS application package.
func (i *Installer) UninstallSingleApp(appName string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.uninstallSingleApp(appName)
}

// PruneOrphanApps uninstalls any shortcut applications (prefixed with fndocker. or put-port.)
// registered in fnOS that do not belong to activeApps.
func (i *Installer) PruneOrphanApps(activeApps map[string]bool) error {
	if !i.hasAppcenterCLI {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	cmd := exec.Command(i.cliPath, "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "│") {
			parts := strings.Split(line, "│")
			if len(parts) >= 2 {
				installedApp := strings.TrimSpace(parts[1])
				if strings.HasPrefix(installedApp, "fndocker.") || strings.HasPrefix(installedApp, "put-port.") {
					if !activeApps[installedApp] {
						slog.Info("发现历史孤立桌面图标，正在自动清理注销...", "appName", installedApp)
						_ = exec.Command(i.cliPath, "stop", installedApp).Run()
						out, uErr := exec.Command(i.cliPath, "uninstall", installedApp).CombinedOutput()
						if uErr != nil {
							slog.Warn("清理历史孤立桌面图标提示", "appName", installedApp, "output", cleanCliOutput(out))
						} else {
							slog.Info("成功注销清理历史孤立桌面图标", "appName", installedApp)
						}
					}
				}
			}
		}
	}
	return nil
}

// isManagedApp checks if an app identifier strictly belongs to fn-docker-to-desktop namespaces.
func isManagedApp(appName string) bool {
	return strings.HasPrefix(appName, "fndocker.") || strings.HasPrefix(appName, "put-port.")
}

// ReconcileInstalledItems checks if any enabled desktop item is not yet installed or stopped in fnOS.
// It installs missing items and starts stopped/disabled items,
// ensuring desktop icons are properly restored on server startup / reinstall without disrupting already running items.
func (i *Installer) ReconcileInstalledItems(items []DesktopItem) {
	if !i.HasCLI() {
		return
	}

	// 1. Identify missing items and stopped items
	var missing []DesktopItem
	var stopped []DesktopItem

	i.mu.Lock()
	if i.reconcileStatus == nil {
		i.reconcileStatus = make(map[string]string)
	}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		appName := item.AppName
		if appName == "" {
			appName = i.DeriveAppName(item)
		}
		if !i.isAppInstalled(appName) {
			missing = append(missing, item)
			i.reconcileStatus[item.ID] = "排队中..."
			if appName != "" {
				i.reconcileStatus[appName] = "排队中..."
			}
		} else {
			// If app is installed in fnOS, check whether it is stopped/disabled
			status := i.getAppStatus(appName)
			if status == "stopped" || (status != "running" && status != "starting" && status != "") {
				stopped = append(stopped, item)
				i.reconcileStatus[item.ID] = "恢复中..."
				if appName != "" {
					i.reconcileStatus[appName] = "恢复中..."
				}
			}
		}
	}
	i.mu.Unlock()

	// 2. Restore stopped/disabled items by starting them
	if len(stopped) > 0 {
		slog.Info("检测到已在应用中心注册但处于未启用/停用状态的应用，正在启动恢复桌面图标...", "total_stopped", len(stopped))
		for idx, item := range stopped {
			appName := item.AppName
			if appName == "" {
				appName = i.DeriveAppName(item)
			}
			slog.Info("正在恢复启动已停用应用...", "appName", appName, "title", item.Name, "progress", fmt.Sprintf("%d/%d", idx+1, len(stopped)))
			startOut, startErr := exec.Command(i.cliPath, "start", appName).CombinedOutput()
			if startErr != nil {
				slog.Warn("appcenter-cli start 启动应用失败，尝试重新打包安装...", "appName", appName, "error", startErr, "output", cleanCliOutput(startOut))
				if err := i.InstallItem(item); err != nil {
					slog.Warn("重新打包安装应用失败", "appName", appName, "error", err)
				}
			} else {
				slog.Info("成功启动应用，桌面图标已恢复显示", "appName", appName, "title", item.Name)
			}

			i.mu.Lock()
			delete(i.reconcileStatus, item.ID)
			if appName != "" {
				delete(i.reconcileStatus, appName)
			}
			i.mu.Unlock()
		}
	}

	// 3. Install completely missing items
	if len(missing) > 0 {
		total := len(missing)
		slog.Info("开始启动未安装桌面应用的补齐安装...", "total_missing", total)
		for idx, item := range missing {
			appName := item.AppName
			if appName == "" {
				appName = i.DeriveAppName(item)
			}

			progressText := "恢复中..."
			i.mu.Lock()
			i.reconcileStatus[item.ID] = progressText
			if appName != "" {
				i.reconcileStatus[appName] = progressText
			}
			i.mu.Unlock()

			slog.Info("检测到未安装的已启用桌面应用，执行补齐安装...", "appName", appName, "title", item.Name, "progress", fmt.Sprintf("%d/%d", idx+1, total))
			if err := i.InstallItem(item); err != nil {
				slog.Warn("补齐安装应用失败", "appName", appName, "error", err)
			}

			i.mu.Lock()
			delete(i.reconcileStatus, item.ID)
			if appName != "" {
				delete(i.reconcileStatus, appName)
			}
			i.mu.Unlock()
		}
	}

	if len(stopped) > 0 || len(missing) > 0 {
		slog.Info("桌面应用状态对齐检查与恢复完成，未对已正常运行的应用产生任何扰动")
	}
}

// UninstallItem unregisters a DesktopItem from fnOS.
func (i *Installer) UninstallItem(item DesktopItem) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	var candidates []string
	if item.AppName != "" {
		candidates = append(candidates, item.AppName)
	}
	derived := i.DeriveAppName(item)
	if derived != item.AppName {
		candidates = append(candidates, derived)
	}
	if item.Port > 0 {
		candidates = append(candidates, fmt.Sprintf("fndocker.port-%d", item.Port))
	}
	legacy := "put-port." + sanitizeAppName(item.ID)
	candidates = append(candidates, legacy)

	seen := make(map[string]bool)
	for _, appName := range candidates {
		if !seen[appName] {
			seen[appName] = true
			_ = i.uninstallSingleApp(appName)
		}
	}
	return nil
}

// UninstallItemByID unregisters an item using its ID.
func (i *Installer) UninstallItemByID(itemID string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	_ = i.uninstallSingleApp("fndocker." + SanitizeAppNamePart(itemID))
	_ = i.uninstallSingleApp("put-port." + sanitizeAppName(itemID))
	return nil
}

func (i *Installer) uninstallSingleApp(appName string) error {
	if !i.hasAppcenterCLI || appName == "" {
		return nil
	}
	if !isManagedApp(appName) {
		slog.Warn("拒绝卸载非本程序管理的外部第三方应用", "appName", appName)
		return fmt.Errorf("拒绝操作非本程序创建的应用: %s", appName)
	}

	slog.Info("正在通过 appcenter-cli 停止并卸载桌面应用...", "appName", appName)
	_ = exec.Command(i.cliPath, "stop", appName).Run()
	out, err := exec.Command(i.cliPath, "uninstall", appName).CombinedOutput()
	if err != nil {
		outStr := cleanCliOutput(out)
		slog.Warn("卸载应用产生异常", "appName", appName, "output", outStr, "error", err)
		return err
	}
	slog.Info("成功注销桌面应用", "appName", appName)
	return nil
}

func cleanCliOutput(output []byte) string {
	lines := strings.Split(string(output), "\n")
	var kept []string
	for _, line := range lines {
		sublines := strings.Split(line, "\r")
		for _, s := range sublines {
			trimmed := strings.TrimSpace(s)
			if trimmed == "" || isCliSpinnerLine(trimmed) {
				continue
			}
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "; ")
}

func isCliSpinnerLine(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "verifying files") ||
		strings.Contains(lower, "installing") ||
		strings.Contains(lower, "starting") ||
		strings.Contains(lower, "stopping") ||
		strings.Contains(lower, "uninstalling") ||
		strings.Contains(lower, "installation complete") {
		return true
	}
	if len(s) <= 2 && strings.ContainsAny(s, "|/\\-") {
		return true
	}
	if len(s) >= 2 && (s[0] == '/' || s[0] == '\\' || s[0] == '|' || s[0] == '-') && (s[1] == ' ' || s[1] == '\t') {
		return true
	}
	return false
}

// updateManifestDisplayName updates display_name in fnOS manifest file.
func updateManifestDisplayName(mfPath string, displayName string) error {
	data, err := os.ReadFile(mfPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	var newLines []string
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "display_name") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) >= 1 && strings.TrimSpace(parts[0]) == "display_name" {
				newLines = append(newLines, fmt.Sprintf("display_name    = %s", displayName))
				changed = true
				continue
			}
		}
		newLines = append(newLines, line)
	}
	if !changed {
		newLines = append(newLines, fmt.Sprintf("display_name    = %s", displayName))
	}
	return os.WriteFile(mfPath, []byte(strings.Join(newLines, "\n")), 0644)
}

// SyncSelfApp updates fn-docker-to-desktop itself on fnOS desktop with user settings.
func (i *Installer) SyncSelfApp(settings Settings) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	appName := "fn-docker-to-desktop"
	slog.Info("正在同步自身桌面图标配置...", "appName", appName, "title", settings.PortalName, "icon", settings.PortalIcon, "allUsers", settings.PortalAllUsers)

	trimAppDest := os.Getenv("TRIM_APPDEST")
	var possibleDirs []string
	if trimAppDest != "" {
		possibleDirs = append(possibleDirs, trimAppDest)
	}
	if execPath, err := os.Executable(); err == nil && execPath != "" {
		execDir := filepath.Dir(execPath)
		possibleDirs = append(possibleDirs, execDir, filepath.Dir(execDir))
	}
	possibleDirs = append(possibleDirs,
		filepath.Join("/var/apps", appName, "target"),
		filepath.Join("/var/apps", appName),
		filepath.Join("/usr/local/apps/@appcenter", appName),
		filepath.Join("/host/root/var/apps", appName, "target"),
		filepath.Join("/host/root/usr/local/apps/@appcenter", appName),
	)

	seenDirs := make(map[string]bool)
	var validDirs []string
	for _, d := range possibleDirs {
		d = filepath.Clean(d)
		if seenDirs[d] {
			continue
		}
		seenDirs[d] = true
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			validDirs = append(validDirs, d)
		}
	}

	updatedNative := false
	for _, dir := range validDirs {
		// Update manifest
		mfCandidates := []string{
			filepath.Join(dir, "manifest"),
			filepath.Join(dir, "app", "manifest"),
		}
		for _, mfPath := range mfCandidates {
			if fi, err := os.Stat(mfPath); err == nil && !fi.IsDir() {
				_ = removeManifestServicePort(mfPath)
				if settings.PortalName != "" {
					if err := updateManifestDisplayName(mfPath, settings.PortalName); err == nil {
						slog.Info("已更新 manifest 应用显示名称", "path", mfPath, "displayName", settings.PortalName)
					} else {
						slog.Warn("更新 manifest 异常", "path", mfPath, "error", err)
					}
				}
			}
		}

		// In-place update of native ui/config
		cfgCandidates := []string{
			filepath.Join(dir, "ui", "config"),
			filepath.Join(dir, "app", "ui", "config"),
		}
		for _, cfgPath := range cfgCandidates {
			if fi, err := os.Stat(cfgPath); err == nil && !fi.IsDir() {
				if err := updateUIConfigFile(cfgPath, settings); err == nil {
					slog.Info("已直接更新原生飞牛桌面配置文件", "path", cfgPath)
					updatedNative = true
				} else {
					slog.Warn("更新原生桌面配置异常", "path", cfgPath, "error", err)
				}
			}
		}

		// Update package icons
		if settings.PortalIcon != "" && settings.PortalIcon != "icon.png" {
			if err := WritePackageIcons(dir, settings.PortalIcon, i.iconsDir, appName, "docker", settings.PortalName); err == nil {
				slog.Info("已同步更新自身桌面图标资源", "dir", dir, "icon", settings.PortalIcon)
			} else {
				slog.Warn("同步自身桌面图标资源异常", "dir", dir, "error", err)
			}
		} else {
			if err := WriteProductIcons(dir); err == nil {
				slog.Info("已还原产品官方桌面图标", "dir", dir)
			} else {
				slog.Warn("还原产品官方桌面图标异常", "dir", dir, "error", err)
			}
		}
	}

	if updatedNative {
		slog.Info("产品自身桌面配置文件同步更新完成 (原生模式)")
		return nil
	}

	return nil
}

func updateUIConfigFile(cfgPath string, settings Settings) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}

	urlMap, ok := root[".url"].(map[string]interface{})
	if !ok {
		return fmt.Errorf(".url field not found in %s", cfgPath)
	}

	for key, val := range urlMap {
		if entry, ok := val.(map[string]interface{}); ok {
			if settings.PortalName != "" {
				entry["title"] = settings.PortalName
			}
			entry["type"] = "iframe"
			entry["allUsers"] = settings.PortalAllUsers
			entry["protocol"] = ""
			entry["gatewaySocket"] = "app.sock"
			entry["gatewayPrefix"] = "/app/fn-docker-to-desktop"
			entry["url"] = "/app/fn-docker-to-desktop/"
			delete(entry, "port")
			urlMap[key] = entry
		}
	}
	root[".url"] = urlMap

	newData, err := json.MarshalIndent(root, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, newData, 0644)
}

func sanitizeAppName(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	var sb strings.Builder
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('-')
		}
	}
	res := strings.Trim(sb.String(), "-_")
	if res == "" {
		res = "app"
	}
	return res
}

// SanitizeFileName cleans up a filename to be safe on the filesystem.
func SanitizeFileName(name string) string {
	name = filepath.Base(name)
	var sb strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_' {
			sb.WriteRune(c)
		} else {
			sb.WriteRune('_')
		}
	}
	res := strings.Trim(sb.String(), "._-")
	if res == "" {
		res = "icon.png"
	}
	return res
}

func removeManifestServicePort(mfPath string) error {
	data, err := os.ReadFile(mfPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	var newLines []string
	changed := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "service_port") {
			changed = true
			continue
		}
		newLines = append(newLines, line)
	}
	if !changed {
		return nil
	}
	return os.WriteFile(mfPath, []byte(strings.Join(newLines, "\n")), 0644)
}
