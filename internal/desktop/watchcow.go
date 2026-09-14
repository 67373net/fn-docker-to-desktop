package desktop

import (
	"context"
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

// WatchcowItem represents a desktop application parsed from Docker container watchcow.* labels.
type WatchcowItem struct {
	ID            string   `json:"id"`
	ContainerID   string   `json:"container_id"`
	ContainerName string   `json:"container_name"`
	Image         string   `json:"image"`
	EntryName     string   `json:"entry_name"`
	AppName       string   `json:"app_name"`
	Name          string   `json:"name"`
	Port          int      `json:"port"`
	Protocol      string   `json:"protocol"`
	Path          string   `json:"path"`
	UIType        string   `json:"ui_type"`
	AllUsers      bool     `json:"all_users"`
	Icon          string   `json:"icon"`
	DisplayIcon   string   `json:"display_icon"`
	LocalIconPath string   `json:"local_icon_path,omitempty"`
	FileTypes     []string `json:"file_types,omitempty"`
	NoDisplay     bool     `json:"no_display,omitempty"`
	Enabled       bool     `json:"enabled"`
	IsWatchcow    bool     `json:"is_watchcow"`
}

type dockerContainerJSON struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

var (
	watchcowMu         sync.RWMutex
	watchcowDockerSock = "/var/run/docker.sock"
	watchcowClient     *http.Client
)

// SetWatchcowDockerSocketPath allows overriding the docker socket path for testing.
func SetWatchcowDockerSocketPath(path string) {
	watchcowMu.Lock()
	defer watchcowMu.Unlock()
	watchcowDockerSock = path
	watchcowClient = nil
}

func getWatchcowDockerClient() *http.Client {
	watchcowMu.Lock()
	defer watchcowMu.Unlock()
	if watchcowClient == nil {
		sock := watchcowDockerSock
		watchcowClient = &http.Client{
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
	return watchcowClient
}

// ScanWatchcowItems queries docker containers and parses any configured watchcow.* labels.
func ScanWatchcowItems(stateResolver func(id string, defaultEnabled bool) bool) ([]WatchcowItem, error) {
	if _, err := os.Stat(watchcowDockerSock); err != nil {
		return nil, nil
	}

	client := getWatchcowDockerClient()
	resp, err := client.Get("http://localhost/containers/json?all=1")
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

	var results []WatchcowItem

	for _, c := range rawContainers {
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

		containerName := ""
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

			appName := defaultEntry["appname"]
			if appName == "" {
				appName = "watchcow." + containerName
			}

			itemID := fmt.Sprintf("watchcow-%s", containerName)

			iconVal := defaultEntry["icon"]
			localIcon := ""
			displayIcon := iconVal
			if strings.HasPrefix(iconVal, "file://") && workingDir != "" {
				rel := strings.TrimPrefix(strings.TrimPrefix(iconVal, "file://./"), "file://")
				candidate := filepath.Join(workingDir, rel)
				if _, err := os.Stat(candidate); err == nil {
					localIcon = candidate
					displayIcon = fmt.Sprintf("/api/desktop/watchcow/icon?id=%s", itemID)
				}
			}

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
			}

			results = append(results, WatchcowItem{
				ID:            itemID,
				ContainerID:   shortCID,
				ContainerName: containerName,
				Image:         c.Image,
				EntryName:     "default",
				AppName:       appName,
				Name:          displayName,
				Port:          pVal,
				Protocol:      protocol,
				Path:          pathVal,
				UIType:        uiType,
				AllUsers:      defaultEntry["all_users"] == "true",
				Icon:          iconVal,
				DisplayIcon:   displayIcon,
				LocalIconPath: localIcon,
				FileTypes:     fileTypes,
				NoDisplay:     defaultEntry["no_display"] == "true",
				Enabled:       enabled,
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

			appName := eData["appname"]
			if appName == "" {
				appName = fmt.Sprintf("watchcow.%s.%s", containerName, en)
			}

			itemID := fmt.Sprintf("watchcow-%s-%s", containerName, en)

			iconVal := eData["icon"]
			localIcon := ""
			displayIcon := iconVal
			if strings.HasPrefix(iconVal, "file://") && workingDir != "" {
				rel := strings.TrimPrefix(strings.TrimPrefix(iconVal, "file://./"), "file://")
				candidate := filepath.Join(workingDir, rel)
				if _, err := os.Stat(candidate); err == nil {
					localIcon = candidate
					displayIcon = fmt.Sprintf("/api/desktop/watchcow/icon?id=%s", itemID)
				}
			}

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
			}

			results = append(results, WatchcowItem{
				ID:            itemID,
				ContainerID:   shortCID,
				ContainerName: containerName,
				Image:         c.Image,
				EntryName:     en,
				AppName:       appName,
				Name:          title,
				Port:          pVal,
				Protocol:      protocol,
				Path:          pathVal,
				UIType:        uiType,
				AllUsers:      eData["all_users"] == "true",
				Icon:          iconVal,
				DisplayIcon:   displayIcon,
				LocalIconPath: localIcon,
				FileTypes:     fileTypes,
				NoDisplay:     eData["no_display"] == "true",
				Enabled:       enabled,
				IsWatchcow:    true,
			})
		}
	}

	return results, nil
}
