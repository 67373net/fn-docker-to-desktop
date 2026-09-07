package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DockerInfo holds metadata about a Docker container backing a port.
type DockerInfo struct {
	IsDocker      bool   `json:"is_docker"`
	ContainerID   string `json:"container_id,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	Image         string `json:"image,omitempty"`
	Service       string `json:"service,omitempty"`
	Status        string `json:"status,omitempty"`
	PrivatePort   int    `json:"private_port,omitempty"`
	Summary       string `json:"summary,omitempty"`
}

type dockerContainerRaw struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	Status string            `json:"Status"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

var (
	dockerCacheLock     sync.RWMutex
	dockerPortMap       = make(map[string]DockerInfo)
	dockerCIDMap        = make(map[string]DockerInfo)
	lastDockerFetch     time.Time
	dockerSockPath      = "/var/run/docker.sock"
	dockerClientOnce    sync.Once
	sharedDockerClient  *http.Client
)

// SetDockerSocketPath allows overriding the docker socket path.
func SetDockerSocketPath(path string) {
	dockerSockPath = path
}

func getDockerClient() *http.Client {
	dockerClientOnce.Do(func() {
		sharedDockerClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", dockerSockPath)
				},
				DisableKeepAlives: false,
				MaxIdleConns:      2,
				IdleConnTimeout:   60 * time.Second,
			},
			Timeout: 3 * time.Second,
		}
	})
	return sharedDockerClient
}

// RefreshDockerContainers updates the container cache if older than 8 seconds.
func RefreshDockerContainers() {
	if _, err := os.Stat(dockerSockPath); err != nil {
		return
	}

	dockerCacheLock.RLock()
	if time.Since(lastDockerFetch) < 8*time.Second {
		dockerCacheLock.RUnlock()
		return
	}
	dockerCacheLock.RUnlock()

	client := getDockerClient()
	resp, err := client.Get("http://localhost/containers/json")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var containers []dockerContainerRaw
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return
	}

	newPortMap := make(map[string]DockerInfo)
	newCIDMap := make(map[string]DockerInfo)

	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}

		service := c.Labels["com.docker.compose.service"]
		shortID := c.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		// Human-friendly summary
		summaryParts := []string{fmt.Sprintf("容器: %s", name)}
		if service != "" {
			summaryParts = append(summaryParts, fmt.Sprintf("服务: %s", service))
		}
		summaryParts = append(summaryParts, fmt.Sprintf("镜像: %s", c.Image))
		summary := strings.Join(summaryParts, " | ")

		baseInfo := DockerInfo{
			IsDocker:      true,
			ContainerID:   shortID,
			ContainerName: name,
			Image:         c.Image,
			Service:       service,
			Status:        c.Status,
			Summary:       summary,
		}

		newCIDMap[c.ID] = baseInfo
		newCIDMap[shortID] = baseInfo

		// Map published ports
		for _, p := range c.Ports {
			if p.PublicPort > 0 && p.Type != "" {
				portInfo := baseInfo
				portInfo.PrivatePort = p.PrivatePort
				portInfo.Summary = fmt.Sprintf("%s (映射: %d->%d/%s)", summary, p.PublicPort, p.PrivatePort, p.Type)

				key := fmt.Sprintf("%d/%s", p.PublicPort, strings.ToLower(p.Type))
				newPortMap[key] = portInfo
			}
		}
	}

	dockerCacheLock.Lock()
	dockerPortMap = newPortMap
	dockerCIDMap = newCIDMap
	lastDockerFetch = time.Now()
	dockerCacheLock.Unlock()
}

// ResolveDockerInfo finds Docker metadata by port/proto or by container ID.
func ResolveDockerInfo(port int, proto string, containerID string) (DockerInfo, bool) {
	dockerCacheLock.RLock()
	defer dockerCacheLock.RUnlock()

	// 1. By Container ID if present
	if containerID != "" {
		if info, ok := dockerCIDMap[containerID]; ok {
			return info, true
		}
		if len(containerID) >= 12 {
			if info, ok := dockerCIDMap[containerID[:12]]; ok {
				return info, true
			}
		}
	}

	// 2. By Public Port & Protocol
	// Normalize proto: "tcp6" -> "tcp", "udp6" -> "udp"
	cleanProto := strings.TrimSuffix(strings.ToLower(proto), "6")
	key := fmt.Sprintf("%d/%s", port, cleanProto)
	if info, ok := dockerPortMap[key]; ok {
		return info, true
	}

	return DockerInfo{}, false
}

// ResolveDockerByContainerID finds Docker metadata directly by container ID.
func ResolveDockerByContainerID(containerID string) (DockerInfo, bool) {
	if containerID == "" {
		return DockerInfo{}, false
	}
	dockerCacheLock.RLock()
	defer dockerCacheLock.RUnlock()

	if info, ok := dockerCIDMap[containerID]; ok {
		return info, true
	}
	if len(containerID) >= 12 {
		if info, ok := dockerCIDMap[containerID[:12]]; ok {
			return info, true
		}
	}
	return DockerInfo{}, false
}
