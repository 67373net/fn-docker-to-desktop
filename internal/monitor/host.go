package monitor

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NetworkInterfaceInfo represents a single network interface and its IP addresses.
type NetworkInterfaceInfo struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`       // "physical", "tailscale", "zerotier", "docker", "loopback", "virtual"
	TypeLabel string   `json:"type_label"` // "物理网卡", "Tailscale VPN", "ZeroTier VPN", "Docker 网桥", "本地环回", "虚拟网卡"
	MAC       string   `json:"mac"`
	IPv4      []string `json:"ipv4"`
	IPv6      []string `json:"ipv6"`
	IsUp      bool     `json:"is_up"`
	MTU       int      `json:"mtu"`
}

// HostInfo contains host metadata and network interface details.
type HostInfo struct {
	Hostname      string                 `json:"hostname"`
	OSName        string                 `json:"os_name"`
	OSPretty      string                 `json:"os_pretty"`
	Kernel        string                 `json:"kernel"`
	Arch          string                 `json:"arch"`
	UptimeSeconds int64                  `json:"uptime_seconds"`
	PrimaryIP     string                 `json:"primary_ip"`
	TotalIPv4     int                    `json:"total_ipv4"`
	TotalIPv6     int                    `json:"total_ipv6"`
	Interfaces    []NetworkInterfaceInfo `json:"interfaces"`
}

var (
	cachedHostInfo     HostInfo
	lastHostInfoSample time.Time
	hostInfoLock       sync.RWMutex
)

// GetHostInfo returns host information, caching for 10 seconds to eliminate CPU overhead.
func GetHostInfo(procPath string) HostInfo {
	hostInfoLock.RLock()
	if time.Since(lastHostInfoSample) < 10*time.Second && cachedHostInfo.Hostname != "" {
		res := cachedHostInfo
		res.UptimeSeconds = readUptime(procPath)
		hostInfoLock.RUnlock()
		return res
	}
	hostInfoLock.RUnlock()

	hostInfoLock.Lock()
	defer hostInfoLock.Unlock()

	if time.Since(lastHostInfoSample) < 10*time.Second && cachedHostInfo.Hostname != "" {
		res := cachedHostInfo
		res.UptimeSeconds = readUptime(procPath)
		return res
	}

	info := collectHostInfo(procPath)
	cachedHostInfo = info
	lastHostInfoSample = time.Now()
	return info
}

func collectHostInfo(procPath string) HostInfo {
	if procPath == "" {
		procPath = "/proc"
	}

	info := HostInfo{
		Hostname:      readHostname(procPath),
		Kernel:        readKernel(procPath),
		Arch:          formatArch(runtime.GOARCH),
		UptimeSeconds: readUptime(procPath),
	}

	info.OSName, info.OSPretty = readOSRelease()
	info.Interfaces, info.PrimaryIP, info.TotalIPv4, info.TotalIPv6 = collectNetworkInterfaces()

	return info
}

func readHostname(procPath string) string {
	// Try /proc/sys/kernel/hostname
	if b, err := os.ReadFile(filepath.Join(procPath, "sys", "kernel", "hostname")); err == nil {
		h := strings.TrimSpace(string(b))
		if h != "" {
			return h
		}
	}
	// Try /host/root/etc/hostname
	if b, err := os.ReadFile("/host/root/etc/hostname"); err == nil {
		h := strings.TrimSpace(string(b))
		if h != "" {
			return h
		}
	}
	// Fallback standard library
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}

func readOSRelease() (string, string) {
	// Try host root os-release first, then container os-release
	paths := []string{
		"/host/root/etc/os-release",
		"/host/root/usr/lib/os-release",
		"/etc/os-release",
		"/usr/lib/os-release",
	}

	for _, p := range paths {
		if f, err := os.Open(p); err == nil {
			scanner := bufio.NewScanner(f)
			var prettyName, name, version string
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					prettyName = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"'`)
				} else if strings.HasPrefix(line, "NAME=") {
					name = strings.Trim(strings.TrimPrefix(line, "NAME="), `"'`)
				} else if strings.HasPrefix(line, "VERSION_ID=") {
					version = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"'`)
				}
			}
			f.Close()

			if prettyName != "" {
				return name, prettyName
			}
			if name != "" {
				if version != "" {
					return name, name + " " + version
				}
				return name, name
			}
		}
	}

	return runtime.GOOS, runtime.GOOS
}

func readKernel(procPath string) string {
	if b, err := os.ReadFile(filepath.Join(procPath, "sys", "kernel", "osrelease")); err == nil {
		k := strings.TrimSpace(string(b))
		if k != "" {
			return k
		}
	}
	return "Linux"
}

func formatArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "x86"
	case "arm":
		return "arm"
	default:
		return goarch
	}
}

func readUptime(procPath string) int64 {
	if b, err := os.ReadFile(filepath.Join(procPath, "uptime")); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if sec, err := strconv.ParseFloat(fields[0], 64); err == nil {
				return int64(sec)
			}
		}
	}
	return 0
}

func collectNetworkInterfaces() ([]NetworkInterfaceInfo, string, int, int) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, "127.0.0.1", 1, 0
	}

	var results []NetworkInterfaceInfo
	var primaryIP string
	totalIPv4 := 0
	totalIPv6 := 0

	var physicalIPv4 string
	var vpnIPv4 string
	var otherIPv4 string

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		var ipv4List []string
		var ipv6List []string

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP

			if ip4 := ip.To4(); ip4 != nil {
				ipv4List = append(ipv4List, ip4.String())
				if !ip.IsLoopback() {
					totalIPv4++
				}
			} else {
				ipv6List = append(ipv6List, ip.String())
				if !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
					totalIPv6++
				}
			}
		}

		if len(ipv4List) == 0 && len(ipv6List) == 0 {
			continue
		}

		ifaceType, typeLabel := classifyInterface(iface.Name)

		item := NetworkInterfaceInfo{
			Name:      iface.Name,
			Type:      ifaceType,
			TypeLabel: typeLabel,
			MAC:       iface.HardwareAddr.String(),
			IPv4:      ipv4List,
			IPv6:      ipv6List,
			IsUp:      iface.Flags&net.FlagUp != 0,
			MTU:       iface.MTU,
		}
		results = append(results, item)

		// Primary IP Selection logic
		if iface.Flags&net.FlagUp != 0 && len(ipv4List) > 0 {
			firstIP := ipv4List[0]
			if ifaceType == "physical" && physicalIPv4 == "" {
				physicalIPv4 = firstIP
			} else if (ifaceType == "tailscale" || ifaceType == "zerotier") && vpnIPv4 == "" {
				vpnIPv4 = firstIP
			} else if ifaceType != "loopback" && otherIPv4 == "" {
				otherIPv4 = firstIP
			}
		}
	}

	if physicalIPv4 != "" {
		primaryIP = physicalIPv4
	} else if vpnIPv4 != "" {
		primaryIP = vpnIPv4
	} else if otherIPv4 != "" {
		primaryIP = otherIPv4
	} else {
		primaryIP = "127.0.0.1"
	}

	return results, primaryIP, totalIPv4, totalIPv6
}

func classifyInterface(name string) (string, string) {
	n := strings.ToLower(name)
	if n == "lo" {
		return "loopback", "本地环回"
	}
	if strings.HasPrefix(n, "tailscale") || strings.HasPrefix(n, "wg") {
		return "tailscale", "Tailscale VPN"
	}
	if strings.HasPrefix(n, "zt") {
		return "zerotier", "ZeroTier VPN"
	}
	if strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "veth") {
		return "docker", "Docker 虚拟网卡"
	}
	if strings.HasPrefix(n, "eth") || strings.HasPrefix(n, "en") || strings.HasPrefix(n, "wl") || strings.HasPrefix(n, "wlan") {
		return "physical", "物理网卡"
	}
	return "virtual", "虚拟网络接口"
}
