package remote

import (
	"fn-docker-to-desktop/internal/monitor"
	"time"
)

// HostConfig represents a managed remote host (LAN machine or public VPS).
type HostConfig struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`        // 别名
	Host       string    `json:"host"`        // IP 或域名
	SSHPort    int       `json:"ssh_port"`    // 默认 22
	User       string    `json:"user"`        // 默认 root
	AuthType   string    `json:"auth_type"`   // "password" or "key"
	Password   string    `json:"password,omitempty"`
	PrivateKey string    `json:"private_key,omitempty"`
	Passphrase string    `json:"passphrase,omitempty"`
	Status     string    `json:"status"`      // "connected", "unconfigured", "failed"
	Error      string    `json:"error,omitempty"`
	IsLAN      bool      `json:"is_lan,omitempty"` // 是否为局域网发现的主机
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DiscoveredLANHost represents a host discovered on local subnet.
type DiscoveredLANHost struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Status   string `json:"status"`            // "configured" or "unconfigured"
	HostID   string `json:"host_id,omitempty"` // If already configured, its HostConfig.ID
	Name     string `json:"name,omitempty"`    // If already configured, its alias
}

// TestHostRequest represents request to test SSH connectivity.
type TestHostRequest struct {
	Host       string `json:"host"`
	SSHPort    int    `json:"ssh_port"`
	User       string `json:"user"`
	AuthType   string `json:"auth_type"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// TestHostResponse represents result of SSH connectivity test.
type TestHostResponse struct {
	Success       bool   `json:"success"`
	LatencyMs     int64  `json:"latency_ms"`
	ServerVersion string `json:"server_version,omitempty"`
	Error         string `json:"error,omitempty"`
}

// RemoteHostPortsResponse represents ports returned for a remote host.
type RemoteHostPortsResponse struct {
	HostID    string              `json:"host_id"`
	HostName  string              `json:"host_name"`
	Status    string              `json:"status"`
	Error     string              `json:"error,omitempty"`
	Ports     []monitor.PortEntry `json:"ports"`
	Timestamp int64               `json:"timestamp"`
}

// LANScanStatus represents current LAN scan progress.
type LANScanStatus struct {
	Scanning bool                `json:"scanning"`
	Hosts    []DiscoveredLANHost `json:"hosts"`
}
