package remote

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fn-docker-to-desktop/internal/monitor"
	"golang.org/x/crypto/ssh"
)

// SSHManager manages SSH client connections and remote inspection.
type SSHManager struct {
	mu      sync.RWMutex
	clients map[string]*ssh.Client // key: host ID
}

// NewSSHManager creates a new SSHManager.
func NewSSHManager() *SSHManager {
	return &SSHManager{
		clients: make(map[string]*ssh.Client),
	}
}

func buildClientConfig(user, authType, password, privateKey, passphrase string, timeout time.Duration) (*ssh.ClientConfig, error) {
	if user == "" {
		user = "root"
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	var authMethods []ssh.AuthMethod
	if authType == "key" && strings.TrimSpace(privateKey) != "" {
		var signer ssh.Signer
		var err error
		if passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(privateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 私钥失败: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	} else {
		return nil, fmt.Errorf("未提供有效的 SSH 密码或私钥凭据")
	}

	return &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}, nil
}

// TestConnection tests SSH connectivity to a remote host.
func (m *SSHManager) TestConnection(req TestHostRequest) TestHostResponse {
	cfg, err := buildClientConfig(req.User, req.AuthType, req.Password, req.PrivateKey, req.Passphrase, 6*time.Second)
	if err != nil {
		return TestHostResponse{Success: false, Error: err.Error()}
	}

	addr := net.JoinHostPort(req.Host, strconv.Itoa(req.SSHPort))
	start := time.Now()
	client, err := ssh.Dial("tcp", addr, cfg)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestHostResponse{
			Success:   false,
			LatencyMs: latency,
			Error:     fmt.Sprintf("SSH 连接失败: %v", err),
		}
	}
	defer client.Close()

	serverVer := string(client.ServerVersion())
	return TestHostResponse{
		Success:       true,
		LatencyMs:     latency,
		ServerVersion: serverVer,
	}
}

// GetClient returns a cached connected SSH client or establishes a new one.
func (m *SSHManager) GetClient(h HostConfig) (*ssh.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, ok := m.clients[h.ID]; ok && client != nil {
		// Test client liveness with a quick channel probe
		_, _, err := client.SendRequest("keepalive@fn-docker", true, nil)
		if err == nil {
			return client, nil
		}
		// Stale client, close and discard
		_ = client.Close()
		delete(m.clients, h.ID)
	}

	cfg, err := buildClientConfig(h.User, h.AuthType, h.Password, h.PrivateKey, h.Passphrase, 8*time.Second)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(h.Host, strconv.Itoa(h.SSHPort))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("连接远程主机 %s:%d 失败: %w", h.Host, h.SSHPort, err)
	}

	m.clients[h.ID] = client
	return client, nil
}

// CloseClient closes and removes the cached SSH client for a host.
func (m *SSHManager) CloseClient(hostID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, ok := m.clients[hostID]; ok && client != nil {
		_ = client.Close()
		delete(m.clients, hostID)
	}
}

// Dial opens a direct-tcpip connection through the SSH client to targetAddr.
func (m *SSHManager) Dial(h HostConfig, targetAddr string) (net.Conn, error) {
	client, err := m.GetClient(h)
	if err != nil {
		return nil, err
	}

	conn, err := client.Dial("tcp", targetAddr)
	if err != nil {
		// Connection might be stale, retry once with fresh connection
		m.CloseClient(h.ID)
		client, err = m.GetClient(h)
		if err != nil {
			return nil, err
		}
		return client.Dial("tcp", targetAddr)
	}
	return conn, nil
}

// FetchRemotePorts connects via SSH and parses listening ports and containers.
func (m *SSHManager) FetchRemotePorts(h HostConfig) ([]monitor.PortEntry, error) {
	client, err := m.GetClient(h)
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		m.CloseClient(h.ID)
		return nil, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	cmd := `(ss -tulpn 2>/dev/null || netstat -tulnp 2>/dev/null); echo "---DOCKER_SEP---"; (docker ps --format '{{json .}}' 2>/dev/null || true)`
	out, err := session.CombinedOutput(cmd)
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("执行远程探针命令失败: %w", err)
	}

	return parseRemoteCommandOutput(string(out)), nil
}

type rawDockerContainer struct {
	ID      string `json:"ID"`
	Names   string `json:"Names"`
	Image   string `json:"Image"`
	Status  string `json:"Status"`
	Ports   string `json:"Ports"`
	Command string `json:"Command"`
}

var (
	ssUserRegex   = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)
	netstatPIDReg = regexp.MustCompile(`(\d+)/([^\s]+)`)
)

func parseRemoteCommandOutput(output string) []monitor.PortEntry {
	parts := strings.Split(output, "---DOCKER_SEP---")
	portLines := ""
	dockerLines := ""
	if len(parts) > 0 {
		portLines = parts[0]
	}
	if len(parts) > 1 {
		dockerLines = parts[1]
	}

	// 1. Parse Docker containers
	dockerMap := make(map[int]monitor.DockerInfo) // key: public port
	for _, line := range strings.Split(dockerLines, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var dc rawDockerContainer
		if err := json.Unmarshal([]byte(trimmed), &dc); err == nil {
			info := monitor.DockerInfo{
				IsDocker:      true,
				ContainerID:   dc.ID,
				ContainerName: dc.Names,
				Image:         dc.Image,
				Status:        dc.Status,
			}
			// Parse mapped ports: e.g. "0.0.0.0:80->80/tcp, :::80->80/tcp, 0.0.0.0:3000->3000/tcp"
			for _, pToken := range strings.Split(dc.Ports, ",") {
				pToken = strings.TrimSpace(pToken)
				if idx := strings.Index(pToken, "->"); idx != -1 {
					left := pToken[:idx]
					if colon := strings.LastIndex(left, ":"); colon != -1 {
						if pNum, err := strconv.Atoi(left[colon+1:]); err == nil && pNum > 0 {
							dockerMap[pNum] = info
						}
					}
				}
			}
		}
	}

	// 2. Parse listening ports (ss or netstat)
	portMap := make(map[int]*monitor.PortEntry)

	for _, line := range strings.Split(portLines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Netid") || strings.HasPrefix(line, "Active") || strings.HasPrefix(line, "Proto") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		proto := strings.ToLower(fields[0])
		if !strings.HasPrefix(proto, "tcp") && !strings.HasPrefix(proto, "udp") {
			continue
		}

		var localAddrStr string
		var procName string
		var pid int

		if strings.HasPrefix(fields[0], "tcp") || strings.HasPrefix(fields[0], "udp") {
			if len(fields) >= 5 && (fields[1] == "LISTEN" || fields[1] == "UNCONN" || fields[1] == "0") {
				// ss format: tcp LISTEN 0 128 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1234,fd=6))
				localAddrStr = fields[4]
				if len(fields) >= 7 {
					userField := strings.Join(fields[6:], " ")
					if m := ssUserRegex.FindStringSubmatch(userField); len(m) >= 3 {
						procName = m[1]
						pid, _ = strconv.Atoi(m[2])
					}
				}
			} else {
				// netstat format: tcp 0 0 0.0.0.0:80 0.0.0.0:* LISTEN 1234/nginx
				localAddrStr = fields[3]
				lastField := fields[len(fields)-1]
				if m := netstatPIDReg.FindStringSubmatch(lastField); len(m) >= 3 {
					pid, _ = strconv.Atoi(m[1])
					procName = m[2]
				}
			}
		}

		if localAddrStr == "" {
			continue
		}

		// Parse IP and Port from localAddrStr
		colonIdx := strings.LastIndex(localAddrStr, ":")
		if colonIdx == -1 {
			continue
		}
		portStr := localAddrStr[colonIdx+1:]
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 {
			continue
		}
		localIP := localAddrStr[:colonIdx]
		if strings.HasPrefix(localIP, "[") && strings.HasSuffix(localIP, "]") {
			localIP = localIP[1 : len(localIP)-1]
		}
		if localIP == "" || localIP == "*" {
			localIP = "0.0.0.0"
		}

		cleanProto := "tcp"
		if strings.Contains(proto, "udp") {
			cleanProto = "udp"
		}

		// Aggregate entry
		entry, exists := portMap[port]
		if !exists {
			entry = &monitor.PortEntry{
				LocalPort:   port,
				Protocol:    cleanProto,
				Protocols:   []string{cleanProto},
				LocalIP:     localIP,
				LocalIPs:    []string{localIP},
				State:       "LISTEN",
				PID:         pid,
				PIDs:        []int{pid},
				ProcessName: procName,
			}
			if cleanProto == "udp" {
				entry.State = "UNCONN"
			}
			portMap[port] = entry
		} else {
			// Merge protocols
			hasProto := false
			for _, pr := range entry.Protocols {
				if pr == cleanProto {
					hasProto = true
					break
				}
			}
			if !hasProto {
				entry.Protocols = append(entry.Protocols, cleanProto)
				entry.Protocol = strings.Join(entry.Protocols, ", ")
			}

			// Merge local IPs
			hasIP := false
			for _, ip := range entry.LocalIPs {
				if ip == localIP {
					hasIP = true
					break
				}
			}
			if !hasIP {
				entry.LocalIPs = append(entry.LocalIPs, localIP)
				entry.LocalIP = strings.Join(entry.LocalIPs, ", ")
			}

			if entry.ProcessName == "" && procName != "" {
				entry.ProcessName = procName
				entry.PID = pid
			}
		}

		// Check Docker match
		if dInfo, ok := dockerMap[port]; ok {
			entry.Docker = dInfo
			if entry.ProcessName == "" || entry.ProcessName == "docker-proxy" {
				entry.ProcessName = dInfo.ContainerName
			}
		}
	}

	result := make([]monitor.PortEntry, 0, len(portMap))
	for _, entry := range portMap {
		result = append(result, *entry)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].LocalPort < result[j].LocalPort
	})

	return result
}
