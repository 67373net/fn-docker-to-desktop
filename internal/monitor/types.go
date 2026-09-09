package monitor

// SocketAddress represents an individual socket endpoint bound to a port.
type SocketAddress struct {
	Protocol  string `json:"protocol"`   // "tcp", "tcp6", "udp", "udp6"
	IPVersion string `json:"ip_version"` // "IPv4" or "IPv6"
	IP        string `json:"ip"`         // "0.0.0.0", "::", "127.0.0.1", etc.
	Port      int    `json:"port"`       // Port number
	State     string `json:"state"`      // "LISTEN", "UNCONN", "ESTABLISHED", etc.
	Inode     string `json:"inode"`      // Socket inode
	PID       int    `json:"pid"`        // Bound PID
}

// PortEntry represents an aggregated open/listening port, its associated process(es), and resource usage.
type PortEntry struct {
	LocalPort    int             `json:"local_port"`              // e.g. 80, 443, 8080
	Protocol     string          `json:"protocol"`                // Summary: "tcp", "udp", "tcp, udp"
	Protocols    []string        `json:"protocols"`               // ["tcp", "udp"]
	IPVersion    string          `json:"ip_version"`              // Summary: "IPv4", "IPv6", "IPv4 / IPv6"
	IPVersions   []string        `json:"ip_versions"`             // ["IPv4", "IPv6"]
	LocalIP      string          `json:"local_ip"`                // Summary: "0.0.0.0, [::]" or "127.0.0.1"
	LocalIPs     []string        `json:"local_ips"`               // All distinct local IPs
	RemoteIP     string          `json:"remote_ip,omitempty"`      // e.g. "0.0.0.0", "*"
	RemotePort   int             `json:"remote_port,omitempty"`    // e.g. 0
	State        string          `json:"state"`                   // Primary state: "LISTEN", "UNCONN", etc.
	States       []string        `json:"states,omitempty"`        // All distinct states
	Inode        string          `json:"inode"`                   // Primary or joined socket inode
	Inodes       []string        `json:"inodes"`                  // All socket inodes
	PID          int             `json:"pid"`                     // Primary PID
	PIDs         []int           `json:"pids"`                    // All associated PIDs
	ProcessName  string          `json:"process_name"`            // Primary process name
	Cmdline      string          `json:"cmdline"`                 // Full commandline
	Exe          string          `json:"exe"`                     // Path to executable
	User         string          `json:"user"`                    // Process owner username
	UID          int             `json:"uid"`                     // User ID
	CPUPercent   float64         `json:"cpu_percent"`             // Aggregated CPU percentage
	MemRSSBytes  uint64          `json:"mem_rss_bytes"`           // Aggregated Resident Set Size (RSS) in bytes
	IOReadBytes  uint64          `json:"io_read_bytes"`           // Process total bytes read from disk
	IOWriteBytes uint64          `json:"io_write_bytes"`          // Process total bytes written to disk
	IOReadRate   float64         `json:"io_read_rate"`            // Process disk read rate in B/s
	IOWriteRate  float64         `json:"io_write_rate"`           // Process disk write rate in B/s
	NetRxRate    float64         `json:"net_rx_rate"`             // Process network Rx rate in B/s (containers)
	NetTxRate    float64         `json:"net_tx_rate"`             // Process network Tx rate in B/s (containers)
	Docker       DockerInfo      `json:"docker"`                  // Docker container information
	Addresses    []SocketAddress `json:"addresses"`               // All detailed socket endpoints
	HasDesktop   bool            `json:"has_desktop"`             // Whether this port has a desktop icon configured
	DesktopCount int             `json:"desktop_count"`           // Number of desktop icons for this port
	DesktopName  string          `json:"desktop_name,omitempty"`  // Desktop icon title if configured
}

// ProcessDetail represents a full system process with detailed resource usage.
type ProcessDetail struct {
	PID          int        `json:"pid"`
	Name         string     `json:"name"`
	Cmdline      string     `json:"cmdline"`
	Exe          string     `json:"exe"`
	State        string     `json:"state"` // "R", "S", "D", "Z", etc.
	User         string     `json:"user"`
	UID          int        `json:"uid"`
	Threads      int        `json:"threads"`
	CPUPercent   float64    `json:"cpu_percent"`
	MemRSSBytes  uint64     `json:"mem_rss_bytes"`
	MemPercent   float64    `json:"mem_percent"`
	IOReadBytes  uint64     `json:"io_read_bytes"`
	IOWriteBytes uint64     `json:"io_write_bytes"`
	IOReadRate   float64    `json:"io_read_rate"`  // Bytes/sec
	IOWriteRate  float64    `json:"io_write_rate"` // Bytes/sec
	NetRxRate    float64    `json:"net_rx_rate"`   // Bytes/sec
	NetTxRate    float64    `json:"net_tx_rate"`   // Bytes/sec
	Docker       DockerInfo `json:"docker"`
}

// DiffResult represents the diff between two port snapshots.
type DiffResult struct {
	Added       []PortEntry   `json:"added"`
	Removed     []PortEntry   `json:"removed"`
	Snapshot    []PortEntry   `json:"snapshot"`
	TotalTCP    int           `json:"total_tcp"`
	TotalUDP    int           `json:"total_udp"`
	TotalDocker int           `json:"total_docker"`
	TotalProc   int           `json:"total_proc"`
	System      SystemMetrics `json:"system"`
	Timestamp   int64         `json:"timestamp"`
}

// ProcessInfo holds cached details and resource usage of a process.
type ProcessInfo struct {
	PID          int
	Name         string
	Cmdline      string
	Exe          string
	State        string
	UID          int
	Username     string
	ContainerID  string
	Threads      int
	CPUPercent   float64
	MemRSSBytes  uint64
	MemPercent   float64
	IOReadBytes  uint64
	IOWriteBytes uint64
	IOReadRate   float64
	IOWriteRate  float64
	NetRxRate    float64
	NetTxRate    float64
}
