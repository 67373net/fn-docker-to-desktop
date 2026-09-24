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

// LANScanner scans local area network for active hosts.
type LANScanner struct {
	storage     *Storage
	mu          sync.RWMutex
	scanning    atomic.Bool
	lastScan    time.Time
	cachedHosts []DiscoveredLANHost
}

// NewLANScanner creates a new LANScanner.
func NewLANScanner(storage *Storage) *LANScanner {
	s := &LANScanner{
		storage: storage,
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
	configuredMap := make(map[string]HostConfig, len(configured))
	for _, ch := range configured {
		configuredMap[ch.Host] = ch
	}

	var finalHosts []DiscoveredLANHost
	for _, h := range aliveMap {
		if ch, exists := configuredMap[h.IP]; exists {
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
	configuredMap := make(map[string]HostConfig, len(configured))
	for _, ch := range configured {
		configuredMap[ch.Host] = ch
	}

	result := make([]DiscoveredLANHost, len(s.cachedHosts))
	for i, h := range s.cachedHosts {
		if ch, ok := configuredMap[h.IP]; ok {
			h.Status = "configured"
			h.HostID = ch.ID
			h.Name = ch.Name
		}
		result[i] = h
	}

	return LANScanStatus{
		Scanning: s.scanning.Load(),
		Hosts:    result,
	}
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
	80, 443, 8080, 8443, 8000, 8008, 8081, 8082, 8083, 8084, 8085, 8088, 8090, 8888,
	// Popular Container Apps & Tools
	3000, 3001, 3002, 5000, 5001, 5002, 5005, 5173, 5174, 5244, 5678, 6080, 7860, 7890, 7897,
	8096, 8123, 8989, 7878, 8686, 9696, 9000, 9080, 9090, 9091, 9100, 9443, 9999, 10000, 10086, 11434, 32400, 50000,
	// Databases / MQ / Cache
	1433, 1521, 1883, 2379, 2380, 3306, 5432, 5672, 6379, 27017,
	// System / File Sharing / Remote
	21, 22, 23, 25, 53, 110, 139, 143, 445, 548, 993, 995, 2049, 2375, 2376, 3389, 5666, 5900, 5901, 6443, 6881, 6882, 7000, 7001,
}

// ProbeHostPorts scans common ports on a target IP when SSH is unconfigured or failed.
func (s *LANScanner) ProbeHostPorts(ip string) []monitor.PortEntry {
	if ip == "" {
		return nil
	}

	portSet := make(map[int]struct{})
	// Include 1..1024 well-known ports
	for p := 1; p <= 1024; p++ {
		portSet[p] = struct{}{}
	}
	// Include popular container & NAS ports
	for _, p := range commonProbePorts {
		portSet[p] = struct{}{}
	}

	portsToScan := make([]int, 0, len(portSet))
	for p := range portSet {
		portsToScan = append(portsToScan, p)
	}
	sort.Ints(portsToScan)

	var foundPorts []int
	var mu sync.Mutex

	workers := 80
	timeout := 350 * time.Millisecond

	portChan := make(chan int, len(portsToScan))
	for _, p := range portsToScan {
		portChan <- p
	}
	close(portChan)

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

