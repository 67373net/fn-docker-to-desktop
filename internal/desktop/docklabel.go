package desktop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DockLabelItem represents a desktop application parsed from Docker container labels.
type DockLabelItem struct {
	ID            string   `json:"id"`
	ContainerID   string   `json:"container_id"`
	ContainerName string   `json:"container_name"`
	Image         string   `json:"image"`
	EntryName     string   `json:"entry_name"`
	AppName       string   `json:"app_name"`
	Name          string   `json:"name"`
	Mode          string   `json:"mode"`
	Port          int      `json:"port"`
	Protocol      string   `json:"protocol"`
	Path          string   `json:"path"`
	TargetURL     string   `json:"target_url,omitempty"`
	Redirect      string   `json:"redirect,omitempty"`
	UIType        string   `json:"ui_type"`
	AllUsers      bool     `json:"all_users"`
	Icon          string   `json:"icon"`
	DisplayIcon   string   `json:"display_icon"`
	LocalIconPath string   `json:"local_icon_path,omitempty"`
	FileTypes     []string `json:"file_types,omitempty"`
	NoDisplay     bool     `json:"no_display,omitempty"`
	Enabled       bool     `json:"enabled"`
	IsDockLabel   bool     `json:"is_docklabel"`
	IsWatchcow    bool     `json:"is_watchcow"`
	Reconciling   bool     `json:"reconciling,omitempty"`
	StatusText    string   `json:"status_text,omitempty"`
}

// WatchcowItem is an alias for backward compatibility.
type WatchcowItem = DockLabelItem

type dockerContainerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type dockerContainerJSON struct {
	ID     string                 `json:"Id"`
	Names  []string               `json:"Names"`
	Image  string                 `json:"Image"`
	State  string                 `json:"State"`
	Status string                 `json:"Status"`
	Labels map[string]string      `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	Mounts []dockerContainerMount `json:"Mounts"`
}

// ResolveWatchcowIconPath resolves a file:// or remote URL icon into a local filesystem path if available.
func ResolveWatchcowIconPath(iconVal string, workingDir string, mounts []dockerContainerMount) string {
	if strings.HasPrefix(iconVal, "file://") {
		clean := strings.TrimPrefix(iconVal, "file://")
		clean = strings.TrimPrefix(clean, "./")
		clean = strings.TrimPrefix(clean, "/")

		// 1. Host absolute path
		if strings.HasPrefix(iconVal, "file:///") {
			absPath := strings.TrimPrefix(iconVal, "file://")
			if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
				return absPath
			}
		}

		// 2. Compose workingDir
		if workingDir != "" {
			cand := filepath.Join(workingDir, clean)
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return cand
			}
		}

		baseName := filepath.Base(clean)

		// 3. Mount sources, variations, and sibling directories
		for _, m := range mounts {
			if m.Source == "" {
				continue
			}
			sources := []string{
				m.Source,
				m.Source + "-proxy",
				m.Source + "-portal",
			}
			for _, s := range sources {
				candidates := []string{
					filepath.Join(s, clean),
					filepath.Join(s, "icons", baseName),
					filepath.Join(s, baseName),
				}
				for _, cand := range candidates {
					if info, err := os.Stat(cand); err == nil && !info.IsDir() {
						return cand
					}
				}
			}

			// Sibling directories of mount source
			parent := filepath.Dir(m.Source)
			if parent != "" && parent != "/" && parent != "." {
				globPatterns := []string{
					filepath.Join(parent, "*", "icons", baseName),
					filepath.Join(parent, "*", clean),
					filepath.Join(parent, "*", baseName),
					filepath.Join(parent, "*", "README.assets", baseName),
				}
				for _, pat := range globPatterns {
					matches, _ := filepath.Glob(pat)
					for _, match := range matches {
						if info, err := os.Stat(match); err == nil && !info.IsDir() {
							return match
						}
					}
				}
			}
		}

		// 4. Known Watchcow directories
		legacyDirs := []string{
			"/usr/local/apps/@appdata/watchcow/icons",
			"/usr/local/apps/@appdata/watchcow/data/icons",
			"/usr/local/apps/@appdata/watchcow/target/icons",
			"/usr/local/apps/@appcenter/watchcow/icons",
			"/usr/local/apps/@appcenter/watchcow/ui/images",
			"/var/apps/watchcow/icons",
			"/var/apps/watchcow/data/icons",
			"/var/apps/watchcow/target/icons",
			"/var/apps/watchcow/target/ui/images",
			"/vol1/@appdata/watchcow/icons",
			"/vol1/@appdata/watchcow/data/icons",
			"/vol2/@appdata/watchcow/icons",
			"/vol2/@appdata/watchcow/data/icons",
			"/vol3/@appdata/watchcow/icons",
			"/vol4/@appdata/watchcow/icons",
			"/vol1/1000/docker",
			"/vol1/docker",
		}
		for _, d := range legacyDirs {
			cand := filepath.Join(d, baseName)
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return cand
			}
		}

		// 5. Common host storage locations
		commonBases := []string{
			"/usr/local/apps",
			"/var/apps",
			"/vol1",
			"/vol1/@appdata",
			"/vol2",
			"/vol3",
			"/vol4",
			"/home",
			"/home/net67373",
			"/var/lib/docker/volumes",
		}
		for _, b := range commonBases {
			globPatterns := []string{
				filepath.Join(b, "*", "icons", baseName),
				filepath.Join(b, "*", clean),
				filepath.Join(b, "*", "README.assets", baseName),
				filepath.Join(b, "*", "*", "icons", baseName),
			}
			for _, pat := range globPatterns {
				matches, _ := filepath.Glob(pat)
				for _, match := range matches {
					if info, err := os.Stat(match); err == nil && !info.IsDir() {
						return match
					}
				}
			}
		}
	} else if strings.HasPrefix(iconVal, "http://") || strings.HasPrefix(iconVal, "https://") {
		// Even for HTTP/HTTPS URLs (like http://127.0.0.1:5900/icons/... or raw.githubusercontent.com/...),
		// check if the file is cloned or stored locally on the host
		u, err := url.Parse(iconVal)
		if err == nil {
			baseName := filepath.Base(u.Path)
			if baseName != "" && baseName != "/" && baseName != "." {
				// 1. Check Compose workingDir
				if workingDir != "" {
					for _, cand := range []string{
						filepath.Join(workingDir, "icons", baseName),
						filepath.Join(workingDir, baseName),
					} {
						if info, err := os.Stat(cand); err == nil && !info.IsDir() {
							return cand
						}
					}
				}

				// 2. Check mounts
				for _, m := range mounts {
					if m.Source == "" {
						continue
					}
					for _, cand := range []string{
						filepath.Join(m.Source, "icons", baseName),
						filepath.Join(m.Source, baseName),
					} {
						if info, err := os.Stat(cand); err == nil && !info.IsDir() {
							return cand
						}
					}
					parent := filepath.Dir(m.Source)
					if parent != "" && parent != "/" && parent != "." {
						matches, _ := filepath.Glob(filepath.Join(parent, "*", "icons", baseName))
						for _, match := range matches {
							if info, err := os.Stat(match); err == nil && !info.IsDir() {
								return match
							}
						}
					}
				}

				// 3. Known Watchcow directories
				legacyDirs := []string{
					"/usr/local/apps/@appdata/watchcow/icons",
					"/usr/local/apps/@appdata/watchcow/data/icons",
					"/usr/local/apps/@appdata/watchcow/target/icons",
					"/usr/local/apps/@appcenter/watchcow/icons",
					"/usr/local/apps/@appcenter/watchcow/ui/images",
					"/var/apps/watchcow/icons",
					"/var/apps/watchcow/data/icons",
					"/var/apps/watchcow/target/icons",
					"/var/apps/watchcow/target/ui/images",
					"/vol1/@appdata/watchcow/icons",
					"/vol1/@appdata/watchcow/data/icons",
					"/vol2/@appdata/watchcow/icons",
					"/vol2/@appdata/watchcow/data/icons",
					"/vol3/@appdata/watchcow/icons",
					"/vol4/@appdata/watchcow/icons",
					"/vol1/1000/docker",
					"/vol1/docker",
				}
				for _, d := range legacyDirs {
					cand := filepath.Join(d, baseName)
					if info, err := os.Stat(cand); err == nil && !info.IsDir() {
						return cand
					}
				}

				// 4. Common host storage locations
				commonBases := []string{
					"/usr/local/apps",
					"/var/apps",
					"/vol1",
					"/vol1/@appdata",
					"/vol2",
					"/vol3",
					"/vol4",
					"/home",
					"/home/net67373",
					"/var/lib/docker/volumes",
				}
				for _, b := range commonBases {
					globPatterns := []string{
						filepath.Join(b, "*", "README.assets", baseName),
						filepath.Join(b, "*", "icons", baseName),
						filepath.Join(b, "*", "*", "icons", baseName),
						filepath.Join(b, "*", "*", "*", "icons", baseName),
						filepath.Join(b, "*", "*", "docker", "*", "icons", baseName),
						filepath.Join(b, "*", baseName),
						filepath.Join(b, "*", "_data", "icons", baseName),
					}
					for _, pat := range globPatterns {
						matches, _ := filepath.Glob(pat)
						for _, match := range matches {
							if info, err := os.Stat(match); err == nil && !info.IsDir() {
								return match
							}
						}
					}
				}
			}
		}
	}

	return ""
}

func resolveWatchcowIconPath(iconVal string, workingDir string, mounts []dockerContainerMount) string {
	return ResolveWatchcowIconPath(iconVal, workingDir, mounts)
}

// MigrateLegacyWatchcowIcons scans known Watchcow directories on fnOS and copies any image files to iconsDir
func MigrateLegacyWatchcowIcons(iconsDir string) {
	if iconsDir == "" {
		return
	}
	_ = os.MkdirAll(iconsDir, 0755)

	legacyDirs := []string{
		"/usr/local/apps/@appdata/watchcow/icons",
		"/usr/local/apps/@appdata/watchcow/data/icons",
		"/usr/local/apps/@appdata/watchcow/target/icons",
		"/usr/local/apps/@appcenter/watchcow/icons",
		"/usr/local/apps/@appcenter/watchcow/ui/images",
		"/var/apps/watchcow/icons",
		"/var/apps/watchcow/data/icons",
		"/var/apps/watchcow/target/icons",
		"/var/apps/watchcow/target/ui/images",
		"/vol1/@appdata/watchcow/icons",
		"/vol1/@appdata/watchcow/data/icons",
		"/vol2/@appdata/watchcow/icons",
		"/vol2/@appdata/watchcow/data/icons",
		"/vol3/@appdata/watchcow/icons",
		"/vol4/@appdata/watchcow/icons",
		"/vol1/1000/docker",
		"/vol1/docker",
	}

	for _, d := range legacyDirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".svg" && ext != ".ico" {
				continue
			}
			srcPath := filepath.Join(d, e.Name())
			dstPath := filepath.Join(iconsDir, e.Name())
			if _, err := os.Stat(dstPath); os.IsNotExist(err) {
				data, err := os.ReadFile(srcPath)
				if err == nil && len(data) > 0 {
					if err := os.WriteFile(dstPath, data, 0644); err == nil {
						slog.Info("[DOCKLABEL] 自动导入旧版 Watchcow 图标", "name", e.Name(), "from", srcPath)
					}
				}
			}
		}
	}
}

// DeriveDockLabelAppName generates an isolated fnOS package name within our app's namespace (fndocker.dock-*)
func DeriveDockLabelAppName(containerName, entryName, customName string) string {
	base := ""
	if customName != "" {
		base = SanitizeAppNamePart(customName)
	}
	if base == "" {
		base = SanitizeAppNamePart(containerName)
		if entryName != "" && entryName != "default" {
			base = base + "-" + SanitizeAppNamePart(entryName)
		}
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "app"
	}

	prefix := "fndocker.dock-"
	// Max allowed length for fnOS app name is 32 chars.
	// prefix is 14 chars ("fndocker.dock-"). Max remaining space is 18 chars.
	// We append "-" + suffix (4 chars) = 5 chars.
	// Thus base prefix can be at most 18 - 5 = 13 chars.
	if len(base) > 13 {
		h := sha256.Sum256([]byte(containerName + "/" + entryName + "/" + customName))
		suffix := hex.EncodeToString(h[:])[:4]
		base = strings.TrimRight(base[:13], "-") + "-" + suffix
	}
	res := prefix + base
	if len(res) > 32 {
		res = res[:32]
		res = strings.TrimRight(res, "-.")
	}
	return res
}

// DeriveWatchcowAppName is an alias for backward compatibility.
func DeriveWatchcowAppName(containerName, entryName, customName string) string {
	return DeriveDockLabelAppName(containerName, entryName, customName)
}

var (
	dockLabelMu         sync.RWMutex
	dockLabelDockerSock = "/var/run/docker.sock"
	dockLabelClient     *http.Client
)

func getDockerSocketPathLocked() string {
	if _, err := os.Stat(dockLabelDockerSock); err == nil {
		return dockLabelDockerSock
	}
	for _, cand := range []string{"/var/run/docker.sock", "/run/docker.sock"} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return dockLabelDockerSock
}

func getDockerSocketPath() string {
	dockLabelMu.RLock()
	defer dockLabelMu.RUnlock()
	return getDockerSocketPathLocked()
}

// SetDockLabelDockerSocketPath allows overriding the docker socket path for testing.
func SetDockLabelDockerSocketPath(path string) {
	dockLabelMu.Lock()
	defer dockLabelMu.Unlock()
	dockLabelDockerSock = path
	dockLabelClient = nil
}

// SetWatchcowDockerSocketPath is an alias for backward compatibility.
func SetWatchcowDockerSocketPath(path string) {
	SetDockLabelDockerSocketPath(path)
}

func getDockLabelDockerClient() *http.Client {
	dockLabelMu.Lock()
	defer dockLabelMu.Unlock()
	if dockLabelClient == nil {
		sock := getDockerSocketPathLocked()
		dockLabelClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
				DisableKeepAlives: false,
				MaxIdleConns:      2,
				IdleConnTimeout:   30 * time.Second,
			},
			Timeout: 3 * time.Second,
		}
	}
	return dockLabelClient
}

// ScanDockLabelItems queries docker containers and parses any configured container labels (e.g. watchcow.* labels).
func ScanDockLabelItems(stateResolver func(id string, defaultEnabled bool) bool) ([]DockLabelItem, error) {
	start := time.Now()
	defer func() {
		dur := time.Since(start)
		if dur > 1000*time.Millisecond {
			slog.Warn("[PERF] ScanDockLabelItems 扫描耗时过长", "duration", dur)
		}
	}()

	sock := getDockerSocketPath()
	if _, err := os.Stat(sock); err != nil {
		slog.Warn("[DOCKLABEL] Docker unix socket 不存在，跳过容器标签扫描", "sock", sock)
		return nil, nil
	}

	client := getDockLabelDockerClient()
	resp, err := client.Get("http://localhost/containers/json")
	if err != nil {
		slog.Warn("[DOCKLABEL] 调用 Docker API GET /containers/json 失败", "sock", sock, "error", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("[DOCKLABEL] Docker API 返回非 200 状态码", "status", resp.StatusCode)
		return nil, fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	var rawContainers []dockerContainerJSON
	if err := json.NewDecoder(resp.Body).Decode(&rawContainers); err != nil {
		slog.Warn("[DOCKLABEL] 解码 Docker 容器列表 JSON 失败", "error", err)
		return nil, err
	}

	var results []DockLabelItem

	for _, c := range rawContainers {
		st := strings.ToLower(strings.TrimSpace(c.State))
		status := strings.ToLower(strings.TrimSpace(c.Status))
		if st != "" && st != "running" && !strings.HasPrefix(status, "up") {
			continue
		}
		labels := c.Labels
		if len(labels) == 0 {
			continue
		}

		hasWatchcow := false
		for k := range labels {
			if strings.HasPrefix(k, "watchcow.") {
				hasWatchcow = true
				break
			}
		}
		if !hasWatchcow {
			continue
		}

		containerName := c.ID
		if len(c.Names) > 0 {
			containerName = strings.TrimPrefix(c.Names[0], "/")
		}
		shortCID := c.ID
		if len(shortCID) > 12 {
			shortCID = shortCID[:12]
		}
		workingDir := labels["com.docker.compose.project.working_dir"]
		if workingDir == "" {
			if cfgFile := labels["com.docker.compose.project.config_files"]; cfgFile != "" {
				workingDir = filepath.Dir(strings.Split(cfgFile, ",")[0])
			}
		}

		defaultPort := 0
		for _, p := range c.Ports {
			if p.PublicPort > 0 {
				defaultPort = p.PublicPort
				break
			}
		}
		if defaultPort == 0 && len(c.Ports) > 0 {
			defaultPort = c.Ports[0].PrivatePort
		}

		namedEntries := make(map[string]map[string]string)
		defaultEntry := make(map[string]string)

		for k, v := range labels {
			if !strings.HasPrefix(k, "watchcow.") {
				continue
			}
			suffix := strings.TrimPrefix(k, "watchcow.")
			parts := strings.SplitN(suffix, ".", 2)
			if len(parts) == 2 {
				entryName := parts[0]
				field := parts[1]
				if _, ok := namedEntries[entryName]; !ok {
					namedEntries[entryName] = make(map[string]string)
				}
				namedEntries[entryName][field] = v
			} else {
				defaultEntry[parts[0]] = v
			}
		}

		// 1. Process Default Entry
		if len(defaultEntry) > 0 {
			rawPortStr := defaultEntry["service_port"]
			if rawPortStr == "" {
				rawPortStr = defaultEntry["port"]
			}
			pVal := defaultPort
			if rawPortStr != "" {
				if n, err := strconv.Atoi(rawPortStr); err == nil {
					pVal = n
				}
			}

			displayName := defaultEntry["display_name"]
			if displayName == "" {
				displayName = defaultEntry["title"]
			}
			if displayName == "" {
				displayName = containerName
			}

			appName := DeriveDockLabelAppName(containerName, "default", defaultEntry["appname"])
			itemID := fmt.Sprintf("docklabel-%s", containerName)
			legacyID := fmt.Sprintf("watchcow-%s", containerName)

			redirectVal := defaultEntry["redirect"]
			forceExternal := defaultEntry["redirect_force_external"] == "true"
			mode := "local"
			targetURL := ""
			if redirectVal != "" {
				if pVal == 0 || forceExternal {
					mode = "shortcut"
					targetURL = redirectVal
				} else {
					mode = "local"
					targetURL = redirectVal
				}
			} else {
				if pVal > 0 {
					mode = "local"
				} else {
					mode = "shortcut"
				}
			}

			iconVal := defaultEntry["icon"]
			if iconVal == "" {
				iconVal = DefaultIconForImage(c.Image)
			}
			localIcon := resolveWatchcowIconPath(iconVal, workingDir, c.Mounts)
			displayIcon := fmt.Sprintf("/api/desktop/docklabel/icon?id=%s", itemID)

			var fileTypes []string
			if ft := defaultEntry["file_types"]; ft != "" {
				for _, t := range strings.Split(ft, ",") {
					if trimmed := strings.TrimSpace(t); trimmed != "" {
						fileTypes = append(fileTypes, strings.TrimPrefix(trimmed, "."))
					}
				}
			}

			uiType := defaultEntry["ui_type"]
			if uiType == "" {
				uiType = "url"
			}
			protocol := defaultEntry["protocol"]
			if protocol == "" {
				protocol = defaultEntry["scheme"]
			}
			if protocol == "" {
				protocol = "http"
			}
			pathVal := defaultEntry["path"]
			if pathVal == "" {
				pathVal = "/"
			}

			defaultEnabled := defaultEntry["enable"] != "false"
			enabled := defaultEnabled
			if stateResolver != nil {
				enabled = stateResolver(itemID, defaultEnabled)
				if !enabled && stateResolver(legacyID, defaultEnabled) {
					enabled = true
				}
			}

			results = append(results, DockLabelItem{
				ID:            itemID,
				ContainerID:   shortCID,
				ContainerName: containerName,
				Image:         c.Image,
				EntryName:     "default",
				AppName:       appName,
				Name:          displayName,
				Mode:          mode,
				Port:          pVal,
				Protocol:      protocol,
				Path:          pathVal,
				TargetURL:     targetURL,
				Redirect:      redirectVal,
				UIType:        uiType,
				AllUsers:      defaultEntry["all_users"] == "true",
				Icon:          iconVal,
				DisplayIcon:   displayIcon,
				LocalIconPath: localIcon,
				FileTypes:     fileTypes,
				NoDisplay:     defaultEntry["no_display"] == "true",
				Enabled:       enabled,
				IsDockLabel:   true,
				IsWatchcow:    true,
			})
		}

		// 2. Process Named Sub-entries
		var entryNames []string
		for name := range namedEntries {
			entryNames = append(entryNames, name)
		}
		sort.Strings(entryNames)

		for _, en := range entryNames {
			eData := namedEntries[en]
			rawPortStr := eData["service_port"]
			if rawPortStr == "" {
				rawPortStr = eData["port"]
			}
			pVal := defaultPort
			if rawPortStr != "" {
				if n, err := strconv.Atoi(rawPortStr); err == nil {
					pVal = n
				}
			}

			title := eData["title"]
			if title == "" {
				title = eData["display_name"]
			}
			if title == "" {
				title = fmt.Sprintf("%s (%s)", containerName, en)
			}

			appName := DeriveDockLabelAppName(containerName, en, eData["appname"])
			itemID := fmt.Sprintf("docklabel-%s-%s", containerName, en)
			legacyID := fmt.Sprintf("watchcow-%s-%s", containerName, en)

			redirectVal := eData["redirect"]
			forceExternal := eData["redirect_force_external"] == "true"
			mode := "local"
			targetURL := ""
			if redirectVal != "" {
				if pVal == 0 || forceExternal {
					mode = "shortcut"
					targetURL = redirectVal
				} else {
					mode = "local"
					targetURL = redirectVal
				}
			} else {
				if pVal > 0 {
					mode = "local"
				} else {
					mode = "shortcut"
				}
			}

			iconVal := eData["icon"]
			if iconVal == "" {
				iconVal = defaultEntry["icon"]
			}
			if iconVal == "" {
				iconVal = DefaultIconForImage(c.Image)
			}
			localIcon := resolveWatchcowIconPath(iconVal, workingDir, c.Mounts)
			displayIcon := fmt.Sprintf("/api/desktop/docklabel/icon?id=%s", itemID)

			var fileTypes []string
			if ft := eData["file_types"]; ft != "" {
				for _, t := range strings.Split(ft, ",") {
					if trimmed := strings.TrimSpace(t); trimmed != "" {
						fileTypes = append(fileTypes, strings.TrimPrefix(trimmed, "."))
					}
				}
			}

			uiType := eData["ui_type"]
			if uiType == "" {
				uiType = "url"
			}
			protocol := eData["protocol"]
			if protocol == "" {
				protocol = eData["scheme"]
			}
			if protocol == "" {
				protocol = "http"
			}
			pathVal := eData["path"]
			if pathVal == "" {
				pathVal = "/"
			}

			defaultEnabled := eData["enable"] != "false"
			enabled := defaultEnabled
			if stateResolver != nil {
				enabled = stateResolver(itemID, defaultEnabled)
				if !enabled && stateResolver(legacyID, defaultEnabled) {
					enabled = true
				}
			}

			results = append(results, DockLabelItem{
				ID:            itemID,
				ContainerID:   shortCID,
				ContainerName: containerName,
				Image:         c.Image,
				EntryName:     en,
				AppName:       appName,
				Name:          title,
				Mode:          mode,
				Port:          pVal,
				Protocol:      protocol,
				Path:          pathVal,
				TargetURL:     targetURL,
				Redirect:      redirectVal,
				UIType:        uiType,
				AllUsers:      eData["all_users"] == "true" || (eData["all_users"] == "" && defaultEntry["all_users"] == "true"),
				Icon:          iconVal,
				DisplayIcon:   displayIcon,
				LocalIconPath: localIcon,
				FileTypes:     fileTypes,
				NoDisplay:     eData["no_display"] == "true",
				Enabled:       enabled,
				IsDockLabel:   true,
				IsWatchcow:    true,
			})
		}
	}
	slog.Info("[DOCKLABEL] 容器标签扫描完成", "containersCount", len(rawContainers), "matchedCount", len(results))
	return results, nil
}

// ResolveDockLabelIconBytes resolves the raw binary icon and content-type for a DockLabelItem.
// It checks in order:
// 1. Existing disk cache in iconsDir (dock_cache_<hash>.png or wc_cache_<hash>.png)
// 2. LocalIconPath (if mounted in container)
// 3. Local/loopback icon URL
// 4. ResolveWatchcowIconPath on host
// 5. Local /icons/ directory reference
// 6. Remote HTTP/HTTPS URL download (with proxy mirrors if GitHub)
// 7. Homarr dashboard icon mirrors by candidate names
// If resolved, it writes to disk cache and returns the bytes and MIME type.
func ResolveDockLabelIconBytes(found *DockLabelItem, iconsDir string) ([]byte, string, error) {
	if found == nil {
		return nil, "", fmt.Errorf("item is nil")
	}

	idHash := fmt.Sprintf("%x", sha256.Sum256([]byte(found.ID)))
	var cacheHashes []string
	cacheHashes = append(cacheHashes, idHash)
	if strings.HasPrefix(found.ID, "docklabel-") {
		legacyID := "watchcow-" + strings.TrimPrefix(found.ID, "docklabel-")
		cacheHashes = append(cacheHashes, fmt.Sprintf("%x", sha256.Sum256([]byte(legacyID))))
	} else if strings.HasPrefix(found.ID, "watchcow-") {
		modernID := "docklabel-" + strings.TrimPrefix(found.ID, "watchcow-")
		cacheHashes = append(cacheHashes, fmt.Sprintf("%x", sha256.Sum256([]byte(modernID))))
	}

	// 0. Fast-path disk cache check
	if iconsDir != "" {
		for _, h := range cacheHashes {
			for _, prefix := range []string{"dock_cache_", "wc_cache_"} {
				cp := filepath.Join(iconsDir, prefix+h+".png")
				if info, err := os.Stat(cp); err == nil && info.Size() > 0 {
					if data, err := os.ReadFile(cp); err == nil && len(data) > 0 {
						return data, "image/png", nil
					}
				}
			}
		}
	}

	saveCache := func(data []byte) {
		if iconsDir != "" && len(data) > 0 {
			for _, h := range cacheHashes {
				_ = os.WriteFile(filepath.Join(iconsDir, "dock_cache_"+h+".png"), data, 0644)
			}
		}
	}

	// 1. If LocalIconPath exists
	if found.LocalIconPath != "" {
		if info, err := os.Stat(found.LocalIconPath); err == nil && !info.IsDir() {
			if data, err := os.ReadFile(found.LocalIconPath); err == nil && len(data) > 0 {
				saveCache(data)
				return data, "image/png", nil
			}
		}
	}

	// 2. Check if Icon is a local/loopback icon URL
	if isLocal, baseName := IsLocalOrLoopbackIconURL(found.Icon); isLocal && baseName != "" {
		if iconsDir != "" {
			target := filepath.Join(iconsDir, baseName)
			if info, err := os.Stat(target); err == nil && !info.IsDir() {
				if data, err := os.ReadFile(target); err == nil && len(data) > 0 {
					saveCache(data)
					return data, "image/png", nil
				}
			}
		}
		if resolved := ResolveWatchcowIconPath(baseName, "", nil); resolved != "" {
			if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
				if data, err := os.ReadFile(resolved); err == nil && len(data) > 0 {
					if iconsDir != "" {
						_ = os.WriteFile(filepath.Join(iconsDir, baseName), data, 0644)
					}
					saveCache(data)
					return data, "image/png", nil
				}
			}
		}
	}

	// 3. Try dynamic resolution on host if LocalIconPath was empty or moved
	if found.Icon != "" {
		if resolved := ResolveWatchcowIconPath(found.Icon, "", nil); resolved != "" {
			if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
				if data, err := os.ReadFile(resolved); err == nil && len(data) > 0 {
					saveCache(data)
					return data, "image/png", nil
				}
			}
		}
	}

	// 4. If Icon is a local icons directory reference
	if found.Icon != "" && iconsDir != "" {
		if strings.HasPrefix(found.Icon, "/icons/") || strings.HasPrefix(found.Icon, "icons/") {
			rel := strings.TrimPrefix(strings.TrimPrefix(found.Icon, "/icons/"), "icons/")
			target := filepath.Join(iconsDir, filepath.Clean(rel))
			if info, err := os.Stat(target); err == nil && !info.IsDir() {
				if data, err := os.ReadFile(target); err == nil && len(data) > 0 {
					saveCache(data)
					return data, "image/png", nil
				}
			}
		}
	}

	// 5. If Icon is an HTTP/HTTPS URL, proxy and cache (skip if loopback/local URL)
	if strings.HasPrefix(found.Icon, "http://") || strings.HasPrefix(found.Icon, "https://") {
		if isLocal, _ := IsLocalOrLoopbackIconURL(found.Icon); !isLocal {
			urlCandidates := []string{}
			if strings.HasPrefix(found.Icon, "https://raw.githubusercontent.com/") {
				cleanRaw := strings.TrimPrefix(found.Icon, "https://raw.githubusercontent.com/")
				parts := strings.SplitN(cleanRaw, "/", 4)
				if len(parts) == 4 {
					jsDelivrURL := fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s@%s/%s", parts[0], parts[1], parts[2], parts[3])
					urlCandidates = append(urlCandidates, jsDelivrURL)
				}
				urlCandidates = append(urlCandidates, "https://ghproxy.net/"+found.Icon)
			}
			urlCandidates = append(urlCandidates, found.Icon)

			client := &http.Client{Timeout: 2 * time.Second}
			for _, targetURL := range urlCandidates {
				req, err := http.NewRequest(http.MethodGet, targetURL, nil)
				if err != nil {
					continue
				}
				req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; fn-docker-to-desktop)")
				resp, err := client.Do(req)
				if err == nil && resp.StatusCode == http.StatusOK {
					data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
					resp.Body.Close()
					if err == nil && len(data) > 0 {
						saveCache(data)
						ct := resp.Header.Get("Content-Type")
						if ct == "" {
							ct = http.DetectContentType(data)
						}
						slog.Info("[DOCKLABEL-ICON] 远端图标下载并缓存成功", "id", found.ID, "from", targetURL, "size", len(data))
						return data, ct, nil
					}
				} else if resp != nil {
					resp.Body.Close()
				}
			}
		}
	}

	// 6. Try resolving from Homarr dashboard icon mirrors using candidate names (bounded to 2s)
	candidates := []string{found.Icon, found.ContainerName, found.Image, found.Name}
	if data, ct, err := FetchIconBytesFromMirrors(candidates...); err == nil && len(data) > 0 {
		saveCache(data)
		slog.Info("[DOCKLABEL-ICON] 从官方图标库镜像自动匹配并缓存图标成功", "id", found.ID, "candidates", candidates, "size", len(data))
		return data, ct, nil
	}

	return nil, "", fmt.Errorf("icon not found for %s", found.ID)
}

// DefaultIconForImage returns the homarr CDN icon URL for a given Docker image name.
func DefaultIconForImage(image string) string {
	parts := strings.Split(image, "/")
	imageName := parts[len(parts)-1]
	imageName = strings.Split(imageName, ":")[0]
	imageName = strings.Split(imageName, "@")[0]
	imageName = strings.ToLower(imageName)
	return fmt.Sprintf("https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png", imageName)
}

// StartDockerEventListener listens to docker container events and invokes onChange when container state changes.
func StartDockerEventListener(ctx context.Context, onChange func()) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			sock := getDockerSocketPath()
			if _, err := os.Stat(sock); err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
					continue
				}
			}

			client := &http.Client{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						var d net.Dialer
						d.KeepAlive = 30 * time.Second
						return d.DialContext(ctx, "unix", sock)
					},
					DisableKeepAlives:     false,
					IdleConnTimeout:       0,
					ResponseHeaderTimeout: 0,
				},
				Timeout: 0, // NO timeout for persistent streaming event reader!
			}
			// Filters for container events
			reqURL := "http://localhost/events?filters=%7B%22type%22%3A%5B%22container%22%5D%7D"
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			resp, err := client.Do(req)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("[DOCKLABEL] 连接 Docker 事件流失败，5秒后重试...", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			if resp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				resp.Body.Close()
				slog.Warn("[DOCKLABEL] Docker 事件流返回非 200 状态码，5秒后重试...", "status", resp.StatusCode, "body", string(bodyBytes))
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			slog.Info("[DOCKLABEL] 正在监听 Docker 实时事件流...")
			reader := bufio.NewReader(resp.Body)
			var debounceTimer *time.Timer
			var debounceMu sync.Mutex

			triggerChange := func() {
				debounceMu.Lock()
				defer debounceMu.Unlock()
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(1*time.Second, func() {
					slog.Info("[DOCKLABEL] 触发容器状态变更自动同步")
					if onChange != nil {
						onChange()
					}
				})
			}

			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					resp.Body.Close()
					if ctx.Err() != nil {
						// Normal context cancellation / server shutdown, exit cleanly without warning
						return
					}
					slog.Warn("[DOCKLABEL] Docker 事件流断开，5秒后尝试重连...", "error", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Second):
					}
					break
				}
				trimmed := bytes.TrimSpace(line)
				if len(trimmed) > 0 {
					var ev struct {
						Type   string `json:"Type"`
						Action string `json:"Action"`
					}
					if err := json.Unmarshal(trimmed, &ev); err == nil {
						// Ignore frequent background exec events (e.g. container health checks)
						if strings.HasPrefix(ev.Action, "exec_") {
							continue
						}
					}
					triggerChange()
				}
			}
		}
	}()
}

// ScanWatchcowItems is an alias for ScanDockLabelItems for backward compatibility.
func ScanWatchcowItems(stateResolver func(id string, defaultEnabled bool) bool) ([]DockLabelItem, error) {
	return ScanDockLabelItems(stateResolver)
}
