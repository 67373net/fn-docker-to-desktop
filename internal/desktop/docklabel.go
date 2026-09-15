package desktop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

		// 4. Common host storage locations
		commonBases := []string{
			"/home",
			"/home/net67373",
			"/vol1",
			"/vol1/1000/docker",
			"/var/apps",
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

				// 3. Common host storage locations
				commonBases := []string{
					"/home",
					"/home/net67373",
					"/vol1",
					"/vol2",
					"/vol3",
					"/vol4",
					"/var/apps",
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
				AllUsers:      eData["all_users"] == "true",
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
						return d.DialContext(ctx, "unix", sock)
					},
					DisableKeepAlives: false,
				},
				Timeout: 0, // NO timeout for persistent streaming event reader!
			}
			// Filters for container events
			reqURL := "http://localhost/events?filters=%7B%22type%22%3A%5B%22container%22%5D%7D"
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			resp, err := client.Do(req)
			if err != nil {
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
					slog.Warn("[DOCKLABEL] Docker 事件流断开，5秒后尝试重连...", "error", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Second):
					}
					break
				}
				if len(bytes.TrimSpace(line)) > 0 {
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
