package remote

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fn-docker-to-desktop/internal/monitor"
)

type probedCacheEntry struct {
	ports     []monitor.PortEntry
	timestamp time.Time
}

// LANScanner scans local area network for active hosts.
type LANScanner struct {
	storage     *Storage
	mu          sync.RWMutex
	scanning    atomic.Bool
	lastScan    time.Time
	cachedHosts []DiscoveredLANHost
	probedMu    sync.RWMutex
	probedCache map[string]probedCacheEntry
}

// NewLANScanner creates a new LANScanner.
func NewLANScanner(storage *Storage) *LANScanner {
	s := &LANScanner{
		storage:     storage,
		probedCache: make(map[string]probedCacheEntry),
	}
	// Initial fast harvest from ARP table
	s.cachedHosts = s.readARPTable()
	return s
}

// readARPTable parses /proc/net/arp to quickly harvest devices without sending packets.
func (s *LANScanner) readARPTable() []DiscoveredLANHost {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseARPReader(f)
}

func parseARPReader(r io.Reader) []DiscoveredLANHost {
	var result []DiscoveredLANHost
	scanner := bufio.NewScanner(r)
	// Skip header line: IP address HW type Flags HW address Mask Device
	if scanner.Scan() {
		_ = scanner.Text()
	}

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 4 {
			ip := fields[0]
			flags := fields[2]
			mac := fields[3]
			// Flags "0x2" means completed/valid ARP entry, skip "0x0" (incomplete)
			if flags != "0x0" && mac != "00:00:00:00:00:00" && !strings.HasPrefix(ip, "127.") {
				result = append(result, DiscoveredLANHost{
					IP:     ip,
					MAC:    mac,
					Status: "unconfigured",
				})
			}
		}
	}
	return result
}

// TriggerScan starts an asynchronous background LAN scan.
func (s *LANScanner) TriggerScan() {
	if s.scanning.Swap(true) {
		return // Already scanning
	}

	go func() {
		defer s.scanning.Store(false)
		s.doScan()
	}()
}

func (s *LANScanner) doScan() {
	// 1. Gather all candidates from ARP table first
	arpHosts := s.readARPTable()
	aliveMap := make(map[string]DiscoveredLANHost)
	for _, h := range arpHosts {
		aliveMap[h.IP] = h
	}

	// 2. Discover local subnet IPs
	subnets := getLocalSubnets()
	probePorts := []int{22, 80, 443, 8080, 5000, 3000, 9000}

	for _, sub := range subnets {
		// Limit to /24 networks (<= 256 IPs) to avoid scanning giant networks
		ones, bits := sub.Mask.Size()
		if bits-ones > 8 {
			continue // Subnet too large, skip active brute-force scan
		}

		ips := generateSubnetIPs(sub)
		type probeJob struct {
			ip   string
			port int
		}
		jobs := make(chan probeJob, 200)
		var wg sync.WaitGroup

		// Worker pool
		numWorkers := 35
		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					addr := fmt.Sprintf("%s:%d", job.ip, job.port)
					conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
					if err == nil {
						_ = conn.Close()
						s.mu.Lock()
						if _, ok := aliveMap[job.ip]; !ok {
							aliveMap[job.ip] = DiscoveredLANHost{
								IP:     job.ip,
								Status: "unconfigured",
							}
						}
						s.mu.Unlock()
					}
				}
			}()
		}

		// Enqueue jobs
		for _, ip := range ips {
			for _, port := range probePorts {
				jobs <- probeJob{ip: ip, port: port}
			}
		}
		close(jobs)
		wg.Wait()
	}

	// 3. De-duplicate and match against configured hosts in storage
	configured := s.storage.GetAllHosts()
	configuredMap := make(map[string]HostConfig, len(configured)*2)
	for _, ch := range configured {
		configuredMap[ch.Host] = ch
		configuredMap[CleanHostAddress(ch.Host)] = ch
	}

	var finalHosts []DiscoveredLANHost
	for _, h := range aliveMap {
		cleanIP := CleanHostAddress(h.IP)
		if ch, exists := configuredMap[cleanIP]; exists {
			h.Status = "configured"
			h.HostID = ch.ID
			h.Name = ch.Name
		} else if ch, exists := configuredMap[h.IP]; exists {
			h.Status = "configured"
			h.HostID = ch.ID
			h.Name = ch.Name
		}
		finalHosts = append(finalHosts, h)
	}

	s.mu.Lock()
	s.cachedHosts = finalHosts
	s.lastScan = time.Now()
	s.mu.Unlock()
}

// GetStatus returns the current scanning status and discovered hosts.
func (s *LANScanner) GetStatus() LANScanStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	configured := s.storage.GetAllHosts()
	configuredMap := make(map[string]HostConfig, len(configured)*2)
	for _, ch := range configured {
		configuredMap[ch.Host] = ch
		configuredMap[CleanHostAddress(ch.Host)] = ch
		if ch.ID != "" {
			configuredMap[ch.ID] = ch
		}
	}

	result := make([]DiscoveredLANHost, len(s.cachedHosts))
	for i, h := range s.cachedHosts {
		cleanIP := CleanHostAddress(h.IP)
		if ch, ok := configuredMap[cleanIP]; ok {
			h.Status = "configured"
			h.HostID = ch.ID
			h.Name = ch.Name
		} else if ch, ok := configuredMap[h.IP]; ok {
			h.Status = "configured"
			h.HostID = ch.ID
			h.Name = ch.Name
		} else {
			h.Status = "unconfigured"
			h.HostID = ""
			h.Name = ""
		}
		result[i] = h
	}

	return LANScanStatus{
		Scanning: s.scanning.Load(),
		Hosts:    result,
	}
}

// UnmarkConfiguredHost clears the configured status of a host address or ID in cached LAN hosts.
func (s *LANScanner) UnmarkConfiguredHost(hostAddr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cleanAddr := CleanHostAddress(hostAddr)
	for i := range s.cachedHosts {
		hClean := CleanHostAddress(s.cachedHosts[i].IP)
		if s.cachedHosts[i].IP == hostAddr || hClean == cleanAddr || s.cachedHosts[i].HostID == hostAddr || s.cachedHosts[i].HostID == cleanAddr {
			s.cachedHosts[i].Status = "unconfigured"
			s.cachedHosts[i].HostID = ""
			s.cachedHosts[i].Name = ""
		}
	}
	s.InvalidateProbedCache(hostAddr)
}

// InvalidateProbedCache clears the cached ports for a host.
func (s *LANScanner) InvalidateProbedCache(hostAddr string) {
	s.probedMu.Lock()
	defer s.probedMu.Unlock()
	cleanAddr := CleanHostAddress(hostAddr)
	delete(s.probedCache, hostAddr)
	delete(s.probedCache, cleanAddr)
}

func getLocalSubnets() []*net.IPNet {
	var subnets []*net.IPNet
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := iface.Name
		if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth") {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ip4 := ipNet.IP.To4(); ip4 != nil {
					// Private LAN ranges (192.168.x.x, 10.x.x.x, 172.16-31.x.x)
					if ip4[0] == 192 && ip4[1] == 168 || ip4[0] == 10 || (ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) {
						subnets = append(subnets, ipNet)
					}
				}
			}
		}
	}
	return subnets
}

func generateSubnetIPs(sub *net.IPNet) []string {
	var ips []string
	ip := sub.IP.To4()
	if ip == nil {
		return nil
	}

	mask := sub.Mask
	// For /24 (255.255.255.0)
	if mask[0] == 255 && mask[1] == 255 && mask[2] == 255 {
		prefix := fmt.Sprintf("%d.%d.%d.", ip[0], ip[1], ip[2])
		for i := 1; i <= 254; i++ {
			if byte(i) == ip[3] {
				continue // Skip self
			}
			ips = append(ips, fmt.Sprintf("%s%d", prefix, i))
		}
	}
	return ips
}

// commonProbePorts lists standard web, container, NAS and database ports to probe when SSH is not available.
var commonProbePorts = []int{
	// Popular Web & Reverse Proxy
	80, 81, 443, 2019, 8000, 8008, 8080, 8081, 8082, 8083, 8084, 8085, 8088, 8089, 8090, 8443, 8888,
	// Popular Container Apps & Tools
	3000, 3001, 3002, 4000, 5000, 5001, 5002, 5005, 5173, 5174, 5244, 5678, 6080, 6800, 7860, 7890, 7891, 7897,
	8006, 8096, 8123, 8554, 8555, 8686, 8920, 8989, 7878, 9000, 9001, 9080, 9090, 9091, 9100, 9200, 9300, 9443, 9696, 9999,
	10000, 10086, 10808, 11434, 21115, 21116, 21117, 21118, 21119, 32400, 50000, 51820,
	// Databases / MQ / Cache
	1080, 1194, 1433, 1521, 1883, 2379, 2380, 3306, 5432, 5672, 6379, 8086, 27017,
	// System / File Sharing / Remote
	21, 22, 23, 25, 53, 110, 139, 143, 445, 548, 902, 993, 995, 2049, 2375, 2376, 3389, 5666, 5900, 5901, 6443, 6881, 6882, 7000, 7001,
}

// getStandardAndCommonPorts returns a deduplicated and sorted list of ports containing:
// 1) All standard well-known ports (1..1024)
// 2) All popular container, web, NAS, proxy, and database ports (commonProbePorts)
func getStandardAndCommonPorts() []int {
	portMap := make(map[int]bool, 1200)
	for p := 1; p <= 1024; p++ {
		portMap[p] = true
	}
	for _, p := range commonProbePorts {
		if p >= 1 && p <= 65535 {
			portMap[p] = true
		}
	}
	result := make([]int, 0, len(portMap))
	for p := range portMap {
		result = append(result, p)
	}
	sort.Ints(result)
	return result
}

// ProbeSpecificPorts scans a specific list of ports on a target IP concurrently with low timeout.
func (s *LANScanner) ProbeSpecificPorts(ip string, ports []int) []monitor.PortEntry {
	if ip == "" || len(ports) == 0 {
		return nil
	}
	workers := 150
	if len(ports) < workers {
		workers = len(ports)
	}
	timeout := 120 * time.Millisecond

	portChan := make(chan int, len(ports))
	var foundPorts []int
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for port := range portChan {
				addr := net.JoinHostPort(ip, strconv.Itoa(port))
				conn, err := net.DialTimeout("tcp", addr, timeout)
				if err == nil {
					_ = conn.Close()
					mu.Lock()
					foundPorts = append(foundPorts, port)
					mu.Unlock()
				}
			}
		}()
	}

	for _, p := range ports {
		if p >= 1 && p <= 65535 {
			portChan <- p
		}
	}
	close(portChan)

	wg.Wait()
	sort.Ints(foundPorts)

	var entries []monitor.PortEntry
	for _, p := range foundPorts {
		entries = append(entries, monitor.PortEntry{
			LocalPort:   p,
			Protocol:    "tcp",
			Protocols:   []string{"tcp"},
			LocalIP:     ip,
			LocalIPs:    []string{ip},
			IPVersion:   "IPv4",
			State:       "LISTEN",
			ProcessName: "",
			NeedsSSH:    true,
		})
	}
	return entries
}

// ProbeHostPorts probes standard well-known ports (1..1024) and popular container/web/NAS ports
// on a target IP when SSH is unconfigured or failed. Finishes in ~0.5s and caches for 30s.
func (s *LANScanner) ProbeHostPorts(ip string) []monitor.PortEntry {
	if ip == "" {
		return nil
	}
	cleanIP := CleanHostAddress(ip)
	s.probedMu.RLock()
	if entry, ok := s.probedCache[cleanIP]; ok {
		if time.Since(entry.timestamp) < 30*time.Second {
			s.probedMu.RUnlock()
			return entry.ports
		}
	}
	s.probedMu.RUnlock()

	portsToProbe := getStandardAndCommonPorts()
	ports := s.ProbeSpecificPorts(cleanIP, portsToProbe)

	s.probedMu.Lock()
	if s.probedCache == nil {
		s.probedCache = make(map[string]probedCacheEntry)
	}
	s.probedCache[cleanIP] = probedCacheEntry{
		ports:     ports,
		timestamp: time.Now(),
	}
	s.probedMu.Unlock()

	return ports
}

// ProbeHostPortsRange scans ports within a given range on a target IP.
func (s *LANScanner) ProbeHostPortsRange(ip string, startPort, endPort int) []monitor.PortEntry {
	if ip == "" {
		return nil
	}
	if startPort < 1 {
		startPort = 1
	}
	if endPort > 65535 {
		endPort = 65535
	}
	if startPort > endPort {
		return nil
	}

	totalPorts := endPort - startPort + 1
	workers := 300
	if totalPorts < workers {
		workers = totalPorts
	}
	timeout := 200 * time.Millisecond

	portChan := make(chan int, 2000)
	var foundPorts []int
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for port := range portChan {
				addr := net.JoinHostPort(ip, strconv.Itoa(port))
				conn, err := net.DialTimeout("tcp", addr, timeout)
				if err != nil && !strings.Contains(err.Error(), "refused") {
					// In case of transient packet loss or listen queue saturation, retry once
					conn, err = net.DialTimeout("tcp", addr, 250*time.Millisecond)
				}
				if err == nil {
					_ = conn.Close()
					mu.Lock()
					foundPorts = append(foundPorts, port)
					mu.Unlock()
				}
			}
		}()
	}

	for p := startPort; p <= endPort; p++ {
		portChan <- p
	}
	close(portChan)

	wg.Wait()
	sort.Ints(foundPorts)

	var entries []monitor.PortEntry
	for _, p := range foundPorts {
		entries = append(entries, monitor.PortEntry{
			LocalPort:   p,
			Protocol:    "tcp",
			Protocols:   []string{"tcp"},
			LocalIP:     ip,
			LocalIPs:    []string{ip},
			IPVersion:   "IPv4",
			State:       "LISTEN",
			ProcessName: "",
			NeedsSSH:    true,
		})
	}
	return entries
}

