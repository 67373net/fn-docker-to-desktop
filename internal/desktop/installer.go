package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	hasAppcenterCLI bool
}

// NewInstaller creates a new fnOS package installer.
func NewInstaller(dataDir string, rootIconPath string) *Installer {
	appsDir := filepath.Join(dataDir, "apps")
	_ = os.MkdirAll(appsDir, 0755)

	cliPath, found := findAppcenterCLI()

	inst := &Installer{
		cliPath:         cliPath,
		appsDir:         appsDir,
		rootIconPath:    rootIconPath,
		hasAppcenterCLI: found,
	}

	if found {
		slog.Info("检测到飞牛官方包管理工具 appcenter-cli", "path", cliPath)
	} else {
		slog.Info("未检测到 appcenter-cli (运行于独立环境或模拟模式)")
	}

	return inst
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

// DeriveAppName generates a valid, unique fnOS package identifier.
func (i *Installer) DeriveAppName(item DesktopItem) string {
	if item.AppName != "" && ValidateAppName(item.AppName) == nil {
		return item.AppName
	}

	base := ""
	if item.ContainerName != "" {
		if item.Port > 0 {
			base = fmt.Sprintf("%s-%d", item.ContainerName, item.Port)
		} else {
			base = item.ContainerName
		}
	} else if item.Port > 0 {
		base = fmt.Sprintf("port-%d", item.Port)
	} else if item.Name != "" && isASCIIAlphanumeric(item.Name) {
		base = item.Name
	} else {
		base = item.ID
	}

	sanitized := SanitizeAppNamePart(base)
	appName := "fndocker." + sanitized
	if len(appName) > MaxAppNameLength {
		digest := sha256.Sum256([]byte(appName))
		hash := hex.EncodeToString(digest[:])[:6]
		prefixLength := MaxAppNameLength - len(hash) - 1
		appName = appName[:prefixLength] + "-" + hash
	}

	if len(appName) < MinAppNameLength {
		appName = "fndocker.app"
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
desktop_applaunchname=%s
`, cfg.AppName, title, desc, arch, cfg.AppName)

	if cfg.Port > 0 {
		manifestContent += fmt.Sprintf("service_port=%d\ncheckport=false\n", cfg.Port)
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
	if proto == "" {
		proto = "http"
	}
	urlPath := cfg.Path
	if urlPath == "" {
		urlPath = "/"
	}
	uiType := cfg.UIType
	if uiType == "" {
		uiType = "url"
	}

	entryMap := map[string]interface{}{
		"title":     title,
		"icon":      "images/icon-{0}.png",
		"type":      uiType,
		"protocol":  proto,
		"url":       urlPath,
		"allUsers":  cfg.AllUsers,
		"noDisplay": false,
	}
	if portStr != "" {
		entryMap["port"] = portStr
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

	// 3. Icons (write root ICON.PNG, ICON_256.PNG and all app/ui/images variants)
	if err := WritePackageIcons(pkgDir, cfg.IconPath, i.rootIconPath); err != nil {
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
		if err := os.WriteFile(filepath.Join(pkgDir, "cmd", s), []byte(emptyScript), 0755); err != nil {
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

	appName := i.DeriveAppName(item)
	if err := ValidateAppName(appName); err != nil {
		return err
	}

	port := item.Port
	path := item.Path
	if item.Mode == ModeShortcut {
		port = 0
		path = item.TargetURL
	}

	pkgDir, err := i.BuildPackage(AppcenterPackageConfig{
		AppName:       appName,
		Title:         item.Name,
		Desc:          item.Desc,
		Port:          port,
		Protocol:      item.Protocol,
		Path:          path,
		UIType:        item.UIType,
		AllUsers:      item.AllUsers,
		IconPath:      item.Icon,
		ContainerName: item.ContainerName,
	})
	if err != nil {
		return fmt.Errorf("构建应用包失败: %w", err)
	}
	defer os.RemoveAll(pkgDir)

	if !i.hasAppcenterCLI {
		slog.Info("应用包已构建 (模拟/开发模式)", "appName", appName, "pkgDir", pkgDir)
		return nil
	}

	// If already installed, stop & uninstall first for clean update
	if i.isAppInstalled(appName) {
		slog.Info("应用已在系统中安装，先停止并卸载旧版本以应用更新...", "appName", appName)
		_ = exec.Command(i.cliPath, "stop", appName).Run()
		out, err := exec.Command(i.cliPath, "uninstall", appName).CombinedOutput()
		if err != nil {
			slog.Warn("卸载旧应用产生输出", "appName", appName, "output", strings.TrimSpace(string(out)))
		}
	}

	volume := i.resolveInstallVolume()
	slog.Info("正在通过 appcenter-cli 安装飞牛桌面应用...", "appName", appName, "volume", volume)

	cmd := exec.Command(i.cliPath, "install-local", "--volume", volume)
	cmd.Dir = pkgDir
	output, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))
	if err != nil {
		slog.Error("appcenter-cli install-local 失败", "appName", appName, "volume", volume, "error", err, "output", outStr)
		return fmt.Errorf("appcenter-cli install-local 失败: %w (详情: %s)", err, outStr)
	}
	slog.Info("appcenter-cli install-local 执行完成", "appName", appName, "volume", volume, "output", outStr)

	// Wait briefly for fnOS appcenter daemon to register state
	time.Sleep(300 * time.Millisecond)

	// Verify installation
	if i.isAppInstalled(appName) {
		slog.Info("成功注册桌面应用并上线", "appName", appName, "volume", volume)
	} else {
		slog.Warn("应用已执行安装，但在 appcenter-cli list 中未立即发现，尝试检查并触发启动...", "appName", appName)
		startOut, startErr := exec.Command(i.cliPath, "start", appName).CombinedOutput()
		if startErr != nil {
			slog.Debug("appcenter-cli start 输出", "appName", appName, "output", strings.TrimSpace(string(startOut)))
		}
		if i.isAppInstalled(appName) {
			slog.Info("桌面应用现已就绪并上线", "appName", appName)
		}
	}

	return nil
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
	legacy := "put-port." + sanitizeAppName(item.ID)
	candidates = append(candidates, legacy)

	for _, appName := range candidates {
		i.uninstallSingleApp(appName)
	}
	return nil
}

// UninstallItemByID unregisters an item using its ID.
func (i *Installer) UninstallItemByID(itemID string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.uninstallSingleApp("fndocker." + SanitizeAppNamePart(itemID))
	i.uninstallSingleApp("put-port." + sanitizeAppName(itemID))
	return nil
}

func (i *Installer) uninstallSingleApp(appName string) {
	if !i.hasAppcenterCLI {
		slog.Info("应用已清理 (模拟模式)", "appName", appName)
		return
	}

	if i.isAppInstalled(appName) {
		slog.Info("正在从飞牛系统卸载桌面应用...", "appName", appName)
		_ = exec.Command(i.cliPath, "stop", appName).Run()
		out, err := exec.Command(i.cliPath, "uninstall", appName).CombinedOutput()
		if err != nil {
			slog.Warn("卸载应用产生警告", "appName", appName, "output", strings.TrimSpace(string(out)))
		} else {
			slog.Info("成功卸载桌面应用", "appName", appName)
		}
	}
}

// SyncSelfApp updates fn-docker-to-desktop itself on fnOS desktop with user settings.
func (i *Installer) SyncSelfApp(settings Settings) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	appName := "fn-docker-to-desktop"
	slog.Info("正在同步自身桌面图标配置...", "appName", appName, "uiType", settings.PortalUIType, "allUsers", settings.PortalAllUsers)

	// In-place update of native ui/config
	trimAppDest := os.Getenv("TRIM_APPDEST")
	var possibleConfigs []string
	if trimAppDest != "" {
		possibleConfigs = append(possibleConfigs,
			filepath.Join(trimAppDest, "ui", "config"),
			filepath.Join(trimAppDest, "app", "ui", "config"),
		)
	}
	possibleConfigs = append(possibleConfigs,
		fmt.Sprintf("/var/apps/%s/target/ui/config", appName),
		fmt.Sprintf("/var/apps/%s/ui/config", appName),
	)

	updatedNative := false
	for _, cfgPath := range possibleConfigs {
		if fi, err := os.Stat(cfgPath); err == nil && !fi.IsDir() {
			if err := updateUIConfigFile(cfgPath, settings); err == nil {
				slog.Info("已直接更新原生飞牛桌面配置文件", "path", cfgPath)
				updatedNative = true
			} else {
				slog.Warn("更新原生桌面配置异常", "path", cfgPath, "error", err)
			}
		}
	}

	// Clean service_port from manifest
	var possibleManifests []string
	if trimAppDest != "" {
		possibleManifests = append(possibleManifests,
			filepath.Join(trimAppDest, "manifest"),
			filepath.Join(trimAppDest, "app", "manifest"),
		)
	}
	possibleManifests = append(possibleManifests,
		fmt.Sprintf("/var/apps/%s/target/manifest", appName),
		fmt.Sprintf("/var/apps/%s/manifest", appName),
	)
	for _, mfPath := range possibleManifests {
		if fi, err := os.Stat(mfPath); err == nil && !fi.IsDir() {
			_ = removeManifestServicePort(mfPath)
		}
	}

	if updatedNative {
		slog.Info("产品自身桌面图标配置更新完成 (原生模式)")
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
