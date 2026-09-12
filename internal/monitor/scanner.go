package monitor

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type procStatSample struct {
	ticks uint64
	time  time.Time
}

type procIOSample struct {
	readBytes  uint64
	writeBytes uint64
	time       time.Time
}

type procNetSample struct {
	rxBytes uint64
	txBytes uint64
	time    time.Time
}

var (
	userCache     = make(map[int]string)
	userCacheLock sync.RWMutex

	procStatLock sync.Mutex
	procStatMap  = make(map[int]procStatSample)
	procIOLock   sync.Mutex
	procIOMap    = make(map[int]procIOSample)
	procNetLock  sync.Mutex
	procNetMap   = make(map[int]procNetSample)
	numCPU       = float64(runtime.NumCPU())
	totalMemOnce sync.Once
	totalMemSys  uint64
)

func getTotalSystemMemory(procPath string) uint64 {
	totalMemOnce.Do(func() {
		if f, err := os.Open(filepath.Join(procPath, "meminfo")); err == nil {
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						kb, _ := strconv.ParseUint(fields[1], 10, 64)
						totalMemSys = kb * 1024
					}
					break
				}
			}
			f.Close()
		}
	})
	return totalMemSys
}

func init() {
	if numCPU < 1 {
		numCPU = 1
	}
}

func openPasswdFile() (*os.File, error) {
	if f, err := os.Open("/host/etc/passwd"); err == nil {
		return f, nil
	}
	return os.Open("/etc/passwd")
}

// InitUserCache reads host /etc/passwd or fallback to resolve UIDs to usernames.
func InitUserCache() {
	file, err := openPasswdFile()
	if err != nil {
		return
	}
	defer file.Close()

	userCacheLock.Lock()
	defer userCacheLock.Unlock()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 3 {
			if uid, err := strconv.Atoi(parts[2]); err == nil {
				userCache[uid] = parts[0]
			}
		}
	}
}

// GetUsername returns the username for a given UID.
func GetUsername(uid int) string {
	userCacheLock.RLock()
	name, ok := userCache[uid]
	userCacheLock.RUnlock()
	if ok {
		return name
	}
	if uid == 0 {
		return "root"
	}
	return fmt.Sprintf("uid(%d)", uid)
}

// tcpStateMap maps Linux hex tcp state to human-readable string.
var tcpStateMap = map[string]string{
	"01": "ESTABLISHED",
	"02": "SYN_SENT",
	"03": "SYN_RECV",
	"04": "FIN_WAIT1",
	"05": "FIN_WAIT2",
	"06": "TIME_WAIT",
	"07": "CLOSE",
	"08": "CLOSE_WAIT",
	"09": "LAST_ACK",
	"0A": "LISTEN",
	"0B": "CLOSING",
}

// parseIPv4 parses 8-character little-endian hex string into IP.
func parseIPv4(hexIP string) string {
	if len(hexIP) != 8 {
		return hexIP
	}
	b, err := hex.DecodeString(hexIP)
	if err != nil || len(b) != 4 {
		return hexIP
	}
	return net.IPv4(b[3], b[2], b[1], b[0]).String()
}

// parseIPv6 parses 32-character hex string into IPv6 canonical string.
func parseIPv6(hexIP string) string {
	if len(hexIP) != 32 {
		return hexIP
	}
	ipBytes := make([]byte, 16)
	for i := 0; i < 4; i++ {
		chunk, err := hex.DecodeString(hexIP[i*8 : (i+1)*8])
		if err != nil || len(chunk) != 4 {
			return hexIP
		}
		ipBytes[i*4+0] = chunk[3]
		ipBytes[i*4+1] = chunk[2]
		ipBytes[i*4+2] = chunk[1]
		ipBytes[i*4+3] = chunk[0]
	}
	ip := net.IP(ipBytes)
	if ip == nil {
		return hexIP
	}
	return ip.String()
}

// parseAddress parses "HEX_IP:HEX_PORT" into ip string, port int.
func parseAddress(addrStr string, isIPv6 bool) (string, int) {
	parts := strings.Split(addrStr, ":")
	if len(parts) != 2 {
		return "*", 0
	}
	port64, _ := strconv.ParseInt(parts[1], 16, 32)
	port := int(port64)

	var ip string
	if isIPv6 {
		ip = parseIPv6(parts[0])
	} else {
		ip = parseIPv4(parts[0])
	}

	return ip, port
}

type rawSocketEntry struct {
	protocol   string
	ipVersion  string
	localIP    string
	localPort  int
	remoteIP   string
	remotePort int
	state      string
	inode      string
	uid        int
}

func scanProcNetFile(procPath, filename, proto, ipVersion string, isIPv6 bool) ([]rawSocketEntry, map[string]struct{}) {
	filePath := filepath.Join(procPath, "net", filename)
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil
	}
	defer file.Close()

	var entries []rawSocketEntry
	inodeSet := make(map[string]struct{})
	scanner := bufio.NewScanner(file)

	if scanner.Scan() {
		// skip header
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		localAddr := fields[1]
		remAddr := fields[2]
		stHex := strings.ToUpper(fields[3])
		uid, _ := strconv.Atoi(fields[7])
		inode := fields[9]

		var state string
		if strings.HasPrefix(proto, "tcp") {
			if s, ok := tcpStateMap[stHex]; ok {
				state = s
			} else {
				state = stHex
			}
		} else {
			if stHex == "07" {
				state = "UNCONN"
			} else if stHex == "01" {
				state = "ESTABLISHED"
			} else {
				state = stHex
			}
		}

		localIP, localPort := parseAddress(localAddr, isIPv6)
		remoteIP, remotePort := parseAddress(remAddr, isIPv6)

		entries = append(entries, rawSocketEntry{
			protocol:   proto,
			ipVersion:  ipVersion,
			localIP:    localIP,
			localPort:  localPort,
			remoteIP:   remoteIP,
			remotePort: remotePort,
			state:      state,
			inode:      inode,
			uid:        uid,
		})

		if inode != "0" && inode != "" {
			inodeSet[inode] = struct{}{}
		}
	}

	return entries, inodeSet
}

func extractContainerID(cgroupText string) string {
	for _, line := range strings.Split(cgroupText, "\n") {
		if idx := strings.Index(line, "docker-"); idx != -1 {
			rest := line[idx+7:]
			if dot := strings.Index(rest, ".scope"); dot != -1 {
				return rest[:dot]
			}
			fields := strings.FieldsFunc(rest, func(r rune) bool {
				return r == '/' || r == '.' || r == ' ' || r == '\n'
			})
			if len(fields) > 0 && len(fields[0]) >= 12 {
				return fields[0]
			}
		}
		if idx := strings.Index(line, "/docker/"); idx != -1 {
			rest := line[idx+8:]
			fields := strings.FieldsFunc(rest, func(r rune) bool {
				return r == '/' || r == '.' || r == ' ' || r == '\n'
			})
			if len(fields) > 0 && len(fields[0]) >= 12 {
				return fields[0]
			}
		}
		if idx := strings.Index(line, "containerd-"); idx != -1 {
			rest := line[idx+11:]
			if dot := strings.Index(rest, ".scope"); dot != -1 {
				return rest[:dot]
			}
			fields := strings.FieldsFunc(rest, func(r rune) bool {
				return r == '/' || r == '.' || r == ' ' || r == '\n'
			})
			if len(fields) > 0 && len(fields[0]) >= 12 {
				return fields[0]
			}
		}
	}
	return ""
}

func buildInodeProcessMap(procPath string, targetInodes map[string]struct{}) map[string]ProcessInfo {
	result := make(map[string]ProcessInfo)
	if len(targetInodes) == 0 {
		return result
	}

	entries, err := os.ReadDir(procPath)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}

		fdDir := filepath.Join(procPath, name, "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}

		var pInfo *ProcessInfo

		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}

			if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
				inode := link[8 : len(link)-1]
				if _, needed := targetInodes[inode]; needed {
					if pInfo == nil {
						pInfo = resolveProcessInfo(procPath, pid)
					}
					result[inode] = *pInfo
				}
			}
		}

		if len(result) >= len(targetInodes) {
			break
		}
	}

	return result
}

// PruneDeadRateSamples cleans up rate tracking samples for terminated processes.
func PruneDeadRateSamples() {
	now := time.Now()
	procStatLock.Lock()
	if len(procStatMap) > 400 {
		for pid, sample := range procStatMap {
			if now.Sub(sample.time) > 30*time.Second {
				delete(procStatMap, pid)
			}
		}
	}
	procStatLock.Unlock()

	procIOLock.Lock()
	if len(procIOMap) > 400 {
		for pid, sample := range procIOMap {
			if now.Sub(sample.time) > 30*time.Second {
				delete(procIOMap, pid)
			}
		}
	}
	procIOLock.Unlock()

	procNetLock.Lock()
	if len(procNetMap) > 400 {
		for pid, sample := range procNetMap {
			if now.Sub(sample.time) > 30*time.Second {
				delete(procNetMap, pid)
			}
		}
	}
	procNetLock.Unlock()
}

// resolveProcessInfo fetches details and resource stats for a given PID.
func resolveProcessInfo(procPath string, pid int) *ProcessInfo {
	pidDir := filepath.Join(procPath, strconv.Itoa(pid))

	// Cmdline
	cmdBytes, _ := os.ReadFile(filepath.Join(pidDir, "cmdline"))
	cmdline := strings.ReplaceAll(string(cmdBytes), "\x00", " ")
	cmdline = strings.TrimSpace(cmdline)

	isKernel := cmdline == ""

	// Exe (only resolve if non-kernel process with cmdline)
	var exe string
	if !isKernel {
		exe, _ = os.Readlink(filepath.Join(pidDir, "exe"))
	}

	// UID (directly from directory stat, avoiding /proc/[pid]/status parsing)
	uid := 0
	if fi, err := os.Stat(pidDir); err == nil {
		if s, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid = int(s.Uid)
		}
	}

	// Cgroup (only for non-kernel processes)
	var containerID string
	if !isKernel {
		cgroupBytes, _ := os.ReadFile(filepath.Join(pidDir, "cgroup"))
		containerID = extractContainerID(string(cgroupBytes))
	}

	// 1. Memory RSS (/proc/[pid]/statm) - skip for kernel threads
	var memRSS uint64
	var memPct float64
	if !isKernel {
		if statmBytes, err := os.ReadFile(filepath.Join(pidDir, "statm")); err == nil {
			fields := strings.Fields(string(statmBytes))
			if len(fields) >= 2 {
				if pages, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
					memRSS = pages * 4096
				}
			}
		}
		totMem := getTotalSystemMemory(procPath)
		if totMem > 0 && memRSS > 0 {
			memPct = (float64(memRSS) / float64(totMem)) * 100.0
		}
	}

	// 2. CPU % and State (/proc/[pid]/stat)
	var comm string
	var cpuPercent float64
	var state string = "S"
	var threads int = 1
	if statBytes, err := os.ReadFile(filepath.Join(pidDir, "stat")); err == nil {
		content := string(statBytes)
		if lastParen := strings.LastIndex(content, ")"); lastParen != -1 {
			if firstParen := strings.Index(content, "("); firstParen != -1 && lastParen > firstParen {
				comm = content[firstParen+1 : lastParen]
			}
			rest := strings.Fields(content[lastParen+1:])
			if len(rest) >= 1 {
				state = rest[0]
			}
			if len(rest) >= 18 {
				if t, err := strconv.Atoi(rest[17]); err == nil && t > 0 {
					threads = t
				}
			}
			if len(rest) >= 13 {
				utime, _ := strconv.ParseUint(rest[11], 10, 64)
				stime, _ := strconv.ParseUint(rest[12], 10, 64)
				totalTicks := utime + stime

				now := time.Now()
				procStatLock.Lock()
				prev, exists := procStatMap[pid]
				procStatMap[pid] = procStatSample{ticks: totalTicks, time: now}
				procStatLock.Unlock()

				if exists && !prev.time.IsZero() {
					dt := now.Sub(prev.time).Seconds()
					if dt > 0.1 && totalTicks >= prev.ticks {
						dTicks := float64(totalTicks - prev.ticks)
						pct := (dTicks / (dt * 100.0 * numCPU)) * 100.0
						if pct > 100.0 {
							pct = 100.0
						}
						cpuPercent = pct
					}
				}
			}
		}
	}

	if comm == "" && exe != "" {
		comm = filepath.Base(exe)
	}

	// 3. Disk I/O & Rate (/proc/[pid]/io) - skip for kernel threads
	var ioRead, ioWrite uint64
	var ioReadRate, ioWriteRate float64
	if !isKernel {
		if ioBytes, err := os.ReadFile(filepath.Join(pidDir, "io")); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(ioBytes)))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "read_bytes:") {
					f := strings.Fields(line)
					if len(f) >= 2 {
						ioRead, _ = strconv.ParseUint(f[1], 10, 64)
					}
				} else if strings.HasPrefix(line, "write_bytes:") {
					f := strings.Fields(line)
					if len(f) >= 2 {
						ioWrite, _ = strconv.ParseUint(f[1], 10, 64)
					}
				}
			}

			now := time.Now()
			procIOLock.Lock()
			prevIO, existsIO := procIOMap[pid]
			procIOMap[pid] = procIOSample{readBytes: ioRead, writeBytes: ioWrite, time: now}
			procIOLock.Unlock()

			if existsIO && !prevIO.time.IsZero() {
				dt := now.Sub(prevIO.time).Seconds()
				if dt > 0.1 {
					if ioRead >= prevIO.readBytes {
						ioReadRate = float64(ioRead-prevIO.readBytes) / dt
					}
					if ioWrite >= prevIO.writeBytes {
						ioWriteRate = float64(ioWrite-prevIO.writeBytes) / dt
					}
				}
			}
		}
	}

	// 4. Container Network Rate (/proc/[pid]/net/dev)
	var netRxRate, netTxRate float64
	if containerID != "" {
		if netBytes, err := os.ReadFile(filepath.Join(pidDir, "net", "dev")); err == nil {
			var curRx, curTx uint64
			scanner := bufio.NewScanner(strings.NewReader(string(netBytes)))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if !strings.Contains(line, ":") {
					continue
				}
				parts := strings.SplitN(line, ":", 2)
				if len(parts) != 2 {
					continue
				}
				iface := strings.TrimSpace(parts[0])
				if iface == "lo" {
					continue
				}
				fields := strings.Fields(parts[1])
				if len(fields) >= 9 {
					rx, _ := strconv.ParseUint(fields[0], 10, 64)
					tx, _ := strconv.ParseUint(fields[8], 10, 64)
					curRx += rx
					curTx += tx
				}
			}

			now := time.Now()
			procNetLock.Lock()
			prevNet, existsNet := procNetMap[pid]
			procNetMap[pid] = procNetSample{rxBytes: curRx, txBytes: curTx, time: now}
			procNetLock.Unlock()

			if existsNet && !prevNet.time.IsZero() {
				dt := now.Sub(prevNet.time).Seconds()
				if dt > 0.1 {
					if curRx >= prevNet.rxBytes {
						netRxRate = float64(curRx-prevNet.rxBytes) / dt
					}
					if curTx >= prevNet.txBytes {
						netTxRate = float64(curTx-prevNet.txBytes) / dt
					}
				}
			}
		}
	}

	return &ProcessInfo{
		PID:          pid,
		Name:         comm,
		Cmdline:      cmdline,
		Exe:          exe,
		State:        state,
		UID:          uid,
		Username:     GetUsername(uid),
		ContainerID:  containerID,
		Threads:      threads,
		CPUPercent:   cpuPercent,
		MemRSSBytes:  memRSS,
		MemPercent:   memPct,
		IOReadBytes:  ioRead,
		IOWriteBytes: ioWrite,
		IOReadRate:   ioReadRate,
		IOWriteRate:  ioWriteRate,
		NetRxRate:    netRxRate,
		NetTxRate:    netTxRate,
	}
}

// ScanPorts scans host ports and maps them to processes and resource stats.
func ScanPorts(procPath string, listenOnly bool) []PortEntry {
	if procPath == "" {
		procPath = "/proc"
	}

	RefreshDockerContainers()

	var allRaw []rawSocketEntry
	allInodes := make(map[string]struct{})

	files := []struct {
		filename  string
		proto     string
		ipVersion string
		isIPv6    bool
	}{
		{"tcp", "tcp", "IPv4", false},
		{"tcp6", "tcp6", "IPv6", true},
		{"udp", "udp", "IPv4", false},
		{"udp6", "udp6", "IPv6", true},
	}

	for _, f := range files {
		raw, inodes := scanProcNetFile(procPath, f.filename, f.proto, f.ipVersion, f.isIPv6)
		for _, r := range raw {
			if listenOnly {
				if strings.HasPrefix(r.protocol, "tcp") && r.state != "LISTEN" {
					continue
				}
			}
			allRaw = append(allRaw, r)
		}
		for inode := range inodes {
			allInodes[inode] = struct{}{}
		}
	}

	inodeMap := buildInodeProcessMap(procPath, allInodes)

	results := aggregatePortEntries(allRaw, inodeMap)
	PruneDeadRateSamples()
	return results
}

func aggregatePortEntries(allRaw []rawSocketEntry, inodeMap map[string]ProcessInfo) []PortEntry {
	portGroups := make(map[int][]rawSocketEntry)
	var portOrder []int
	for _, r := range allRaw {
		if r.localPort <= 0 {
			continue
		}
		if _, exists := portGroups[r.localPort]; !exists {
			portOrder = append(portOrder, r.localPort)
		}
		portGroups[r.localPort] = append(portGroups[r.localPort], r)
	}
	sort.Ints(portOrder)

	results := make([]PortEntry, 0, len(portOrder))
	for _, port := range portOrder {
		rawList := portGroups[port]

		addresses := make([]SocketAddress, 0, len(rawList))
		hasTCP := false
		hasUDP := false
		hasIPv4 := false
		hasIPv6 := false

		var localIPs []string
		ipSet := make(map[string]struct{})

		hasListen := false
		hasUnconn := false
		hasEstablished := false
		stateSet := make(map[string]struct{})
		var states []string

		var inodes []string
		inodeSet := make(map[string]struct{})

		var pids []int
		pidSet := make(map[int]struct{})

		var primaryPInfo *ProcessInfo
		var primaryUID int
		var primaryUser string
		var dockerInfo DockerInfo
		hasDocker := false

		seenPIDMetrics := make(map[int]struct{})
		var totalCPU float64
		var totalRSS uint64
		var totalIORead, totalIOWrite uint64
		var totalIOReadRate, totalIOWriteRate float64
		var totalNetRxRate, totalNetTxRate float64

		for _, r := range rawList {
			cleanProto := strings.ToLower(strings.TrimSuffix(r.protocol, "6"))
			if cleanProto == "tcp" {
				hasTCP = true
			} else if cleanProto == "udp" {
				hasUDP = true
			}

			if r.ipVersion == "IPv4" {
				hasIPv4 = true
			} else if r.ipVersion == "IPv6" {
				hasIPv6 = true
			}

			if _, exists := ipSet[r.localIP]; !exists {
				ipSet[r.localIP] = struct{}{}
				localIPs = append(localIPs, r.localIP)
			}

			if r.state == "LISTEN" {
				hasListen = true
			} else if r.state == "UNCONN" {
				hasUnconn = true
			} else if r.state == "ESTABLISHED" {
				hasEstablished = true
			}
			if _, exists := stateSet[r.state]; !exists {
				stateSet[r.state] = struct{}{}
				states = append(states, r.state)
			}

			if r.inode != "" && r.inode != "0" {
				if _, exists := inodeSet[r.inode]; !exists {
					inodeSet[r.inode] = struct{}{}
					inodes = append(inodes, r.inode)
				}
			}

			pid := 0
			var pInfo *ProcessInfo
			if p, ok := inodeMap[r.inode]; ok {
				pInfo = &p
				pid = p.PID
				if pid > 0 {
					if _, exists := pidSet[pid]; !exists {
						pidSet[pid] = struct{}{}
						pids = append(pids, pid)
					}
					if _, seen := seenPIDMetrics[pid]; !seen {
						seenPIDMetrics[pid] = struct{}{}
						totalCPU += p.CPUPercent
						totalRSS += p.MemRSSBytes
						totalIORead += p.IOReadBytes
						totalIOWrite += p.IOWriteBytes
						totalIOReadRate += p.IOReadRate
						totalIOWriteRate += p.IOWriteRate
						totalNetRxRate += p.NetRxRate
						totalNetTxRate += p.NetTxRate
					}
				}
				if primaryPInfo == nil && p.Name != "" {
					primaryPInfo = &p
					primaryUID = p.UID
					primaryUser = p.Username
				}
			}

			if primaryUID == 0 && r.uid != 0 {
				primaryUID = r.uid
				primaryUser = GetUsername(r.uid)
			}

			// Resolve Docker info
			if !hasDocker {
				cid := ""
				if pInfo != nil {
					cid = pInfo.ContainerID
				}
				if dInfo, ok := ResolveDockerInfo(port, r.protocol, cid); ok {
					dockerInfo = dInfo
					hasDocker = true
				} else if pInfo != nil && pInfo.Name == "docker-proxy" {
					if dInfo, ok := ResolveDockerInfo(port, r.protocol, ""); ok {
						dockerInfo = dInfo
						hasDocker = true
					}
				}
			}

			addresses = append(addresses, SocketAddress{
				Protocol:  r.protocol,
				IPVersion: r.ipVersion,
				IP:        r.localIP,
				Port:      r.localPort,
				State:     r.state,
				Inode:     r.inode,
				PID:       pid,
			})
		}

		// Fallback docker resolution by port alone if not found yet
		if !hasDocker {
			if dInfo, ok := ResolveDockerInfo(port, "tcp", ""); ok {
				dockerInfo = dInfo
				hasDocker = true
			} else if dInfo, ok := ResolveDockerInfo(port, "udp", ""); ok {
				dockerInfo = dInfo
				hasDocker = true
			}
		}
		if !hasDocker && primaryPInfo != nil && primaryPInfo.Name == "docker-proxy" {
			dockerInfo = DockerInfo{
				IsDocker:      true,
				ContainerName: "docker-proxy",
				Summary:       "Docker 端口映射代理",
			}
			hasDocker = true
		}

		// Protocols
		var protocols []string
		if hasTCP {
			protocols = append(protocols, "tcp")
		}
		if hasUDP {
			protocols = append(protocols, "udp")
		}
		summaryProto := strings.Join(protocols, ", ")

		// IP Versions
		var ipVersions []string
		if hasIPv4 {
			ipVersions = append(ipVersions, "IPv4")
		}
		if hasIPv6 {
			ipVersions = append(ipVersions, "IPv6")
		}
		summaryIPVersion := strings.Join(ipVersions, " / ")

		// Summary Local IP
		has0000 := false
		hasColonColon := false
		for _, ip := range localIPs {
			if ip == "0.0.0.0" {
				has0000 = true
			} else if ip == "::" {
				hasColonColon = true
			}
		}

		var summaryLocalIP string
		if has0000 && hasColonColon {
			if len(localIPs) == 2 {
				summaryLocalIP = "0.0.0.0, [::]"
			} else {
				summaryLocalIP = fmt.Sprintf("0.0.0.0, [::] 等 %d 个地址", len(localIPs))
			}
		} else if len(localIPs) == 1 {
			if strings.Contains(localIPs[0], ":") {
				summaryLocalIP = fmt.Sprintf("[%s]", localIPs[0])
			} else {
				summaryLocalIP = localIPs[0]
			}
		} else if len(localIPs) == 2 {
			ip1 := localIPs[0]
			if strings.Contains(ip1, ":") {
				ip1 = fmt.Sprintf("[%s]", ip1)
			}
			ip2 := localIPs[1]
			if strings.Contains(ip2, ":") {
				ip2 = fmt.Sprintf("[%s]", ip2)
			}
			summaryLocalIP = fmt.Sprintf("%s, %s", ip1, ip2)
		} else if len(localIPs) > 2 {
			firstIP := localIPs[0]
			if strings.Contains(firstIP, ":") {
				firstIP = fmt.Sprintf("[%s]", firstIP)
			}
			summaryLocalIP = fmt.Sprintf("%s 等 %d 个地址", firstIP, len(localIPs))
		}

		// State
		var summaryState string
		if hasListen {
			summaryState = "LISTEN"
		} else if hasUnconn {
			summaryState = "UNCONN"
		} else if hasEstablished {
			summaryState = "ESTABLISHED"
		} else if len(states) > 0 {
			summaryState = states[0]
		} else {
			summaryState = "UNKNOWN"
		}

		var primaryPID int
		if len(pids) > 0 {
			primaryPID = pids[0]
		}

		var procName, cmdline, exe string
		if primaryPInfo != nil {
			procName = primaryPInfo.Name
			cmdline = primaryPInfo.Cmdline
			exe = primaryPInfo.Exe
		} else if hasDocker && dockerInfo.ContainerName != "" {
			procName = dockerInfo.ContainerName
		}
		if primaryUser == "" {
			primaryUser = GetUsername(primaryUID)
		}

		entry := PortEntry{
			LocalPort:    port,
			Protocol:     summaryProto,
			Protocols:    protocols,
			IPVersion:    summaryIPVersion,
			IPVersions:   ipVersions,
			LocalIP:      summaryLocalIP,
			LocalIPs:     localIPs,
			State:        summaryState,
			States:       states,
			Inode:        strings.Join(inodes, ","),
			Inodes:       inodes,
			PID:          primaryPID,
			PIDs:         pids,
			ProcessName:  procName,
			Cmdline:      cmdline,
			Exe:          exe,
			User:         primaryUser,
			UID:          primaryUID,
			CPUPercent:   totalCPU,
			MemRSSBytes:  totalRSS,
			IOReadBytes:  totalIORead,
			IOWriteBytes: totalIOWrite,
			IOReadRate:   totalIOReadRate,
			IOWriteRate:  totalIOWriteRate,
			NetRxRate:    totalNetRxRate,
			NetTxRate:    totalNetTxRate,
			Docker:       dockerInfo,
			Addresses:    addresses,
		}

		results = append(results, entry)
	}

	return results
}
