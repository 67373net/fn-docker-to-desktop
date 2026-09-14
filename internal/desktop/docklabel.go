package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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

func resolveWatchcowIconPath(iconVal string, workingDir string, mounts []dockerContainerMount) string {
	if !strings.HasPrefix(iconVal, "file://") {
		return ""
	}
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

	// 3. Mount sources and common variations
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
	}

	// 4. Common host storage locations
	commonBases := []string{
		"/home/net67373",
		"/vol1/1000/docker",
		"/var/apps",
	}
	for _, b := range commonBases {
		candidates := []string{
			filepath.Join(b, "watchcow-proxy", "icons", baseName),
			filepath.Join(b, "watchcow", "icons", baseName),
			filepath.Join(b, "watchcow", "README.assets", baseName),
			filepath.Join(b, "wild-live-bgm", "icons", baseName),
		}
		for _, cand := range candidates {
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return cand
			}
		}
	}

	return ""
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
	// prefix is 13 chars. Remaining space is 19 chars.
	if len(base) > 19 {
		h := sha256.Sum256([]byte(containerName + "/" + entryName + "/" + customName))
		suffix := hex.EncodeToString(h[:])[:4]
		base = strings.TrimRight(base[:14], "-") + "-" + suffix
	}
	return prefix + base
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
		sock := dockLabelDockerSock
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
	if _, err := os.Stat(dockLabelDockerSock); err != nil {
		return nil, nil
	}

	client := getDockLabelDockerClient()
	resp, err := client.Get("http://localhost/containers/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned status %d", resp.StatusCode)
	}

	var rawContainers []dockerContainerJSON
	if err := json.NewDecoder(resp.Body).Decode(&rawContainers); err != nil {
		return nil, err
	}

	var results []DockLabelItem

	for _, c := range rawContainers {
		if c.State != "" && c.State != "running" {
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

	return results, nil
}

// ScanWatchcowItems is an alias for ScanDockLabelItems for backward compatibility.
func ScanWatchcowItems(stateResolver func(id string, defaultEnabled bool) bool) ([]DockLabelItem, error) {
	return ScanDockLabelItems(stateResolver)
}
