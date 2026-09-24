package remote

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
