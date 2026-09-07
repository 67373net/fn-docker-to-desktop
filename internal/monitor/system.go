package monitor

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SystemSamplePoint represents a single lightweight historical timestamped sample.
type SystemSamplePoint struct {
	Timestamp     int64   `json:"t"`
	CPUPercent    float64 `json:"cpu"`
	MemPercent    float64 `json:"mem"`
	MemUsedBytes  uint64  `json:"mem_used"`
	NetRxBytesSec float64 `json:"net_rx"`
	NetTxBytesSec float64 `json:"net_tx"`
	DiskReadSec   float64 `json:"disk_r"`
	DiskWriteSec  float64 `json:"disk_w"`
	DiskPercent   float64 `json:"disk_pct"`
	DiskUsedBytes uint64  `json:"disk_used"`
}

// SystemMetrics contains CPU, Memory, Disk Usage, Network, and Disk I/O metrics.
type SystemMetrics struct {
	CPUPercent     float64             `json:"cpu_percent"`
	MemTotalBytes  uint64              `json:"mem_total_bytes"`
	MemUsedBytes   uint64              `json:"mem_used_bytes"`
	MemPercent     float64             `json:"mem_percent"`
	DiskTotalBytes uint64              `json:"disk_total_bytes"`
	DiskUsedBytes  uint64              `json:"disk_used_bytes"`
	DiskPercent    float64             `json:"disk_percent"`
	NetRxBytesSec  float64             `json:"net_rx_bytes_sec"`
	NetTxBytesSec  float64             `json:"net_tx_bytes_sec"`
	DiskReadSec    float64             `json:"disk_read_sec"`
	DiskWriteSec   float64             `json:"disk_write_sec"`
	History        []SystemSamplePoint `json:"history,omitempty"`
}

// SystemSampler samples system stats from procfs.
type SystemSampler struct {
	procPath       string
	lock           sync.Mutex
	lastSampleTime time.Time

	lastTotalCPU uint64
	lastIdleCPU  uint64

	lastNetRx uint64
	lastNetTx uint64

	lastDiskRead  uint64
	lastDiskWrite uint64

	lastMetrics SystemMetrics
	diskPattern *regexp.Regexp

	history    []SystemSamplePoint
	maxHistory int
}

func NewSystemSampler(procPath string) *SystemSampler {
	if procPath == "" {
		procPath = "/proc"
	}
	s := &SystemSampler{
		procPath:    procPath,
		diskPattern: regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|nvme[0-9]+n[0-9]+|xvd[a-z]+|mmcblk[0-9]+)$`),
		maxHistory:  30,
		history:     make([]SystemSamplePoint, 0, 30),
	}
	s.Sample()
	return s
}

// Sample reads procfs and calculates current rates with jitter prevention.
func (s *SystemSampler) Sample() SystemMetrics {
	s.lock.Lock()
	defer s.lock.Unlock()

	now := time.Now()
	var dt float64
	if !s.lastSampleTime.IsZero() {
		dt = now.Sub(s.lastSampleTime).Seconds()
	}

	// Avoid recalculating if sampled within a very short timeframe (< 300ms)
	if dt < 0.3 && !s.lastSampleTime.IsZero() {
		return s.lastMetrics
	}

	// Start with previous metrics as fallback to prevent sudden 0 drops
	metrics := s.lastMetrics

	// 1. CPU Usage (/proc/stat)
	if f, err := os.Open(filepath.Join(s.procPath, "stat")); err == nil {
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 5 && fields[0] == "cpu" {
				var total, idle uint64
				for i := 1; i < len(fields); i++ {
					v, _ := strconv.ParseUint(fields[i], 10, 64)
					total += v
					if i == 4 || i == 5 { // idle + iowait
						idle += v
					}
				}

				if s.lastTotalCPU > 0 && total > s.lastTotalCPU {
					dTotal := float64(total - s.lastTotalCPU)
					dIdle := float64(idle - s.lastIdleCPU)
					if dTotal > 0 {
						pct := (1.0 - (dIdle / dTotal)) * 100
						if pct < 0 {
							pct = 0
						}
						if pct > 100 {
							pct = 100
						}
						metrics.CPUPercent = pct
						s.lastTotalCPU = total
						s.lastIdleCPU = idle
					}
				} else if s.lastTotalCPU == 0 {
					s.lastTotalCPU = total
					s.lastIdleCPU = idle
				}
			}
		}
		f.Close()
	}

	// 2. Memory Usage (/proc/meminfo)
	if f, err := os.Open(filepath.Join(s.procPath, "meminfo")); err == nil {
		scanner := bufio.NewScanner(f)
		var memTotal, memAvail uint64
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseUint(fields[1], 10, 64)
					memTotal = kb * 1024
				}
			} else if strings.HasPrefix(line, "MemAvailable:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseUint(fields[1], 10, 64)
					memAvail = kb * 1024
				}
			}
		}
		f.Close()

		if memTotal > 0 {
			metrics.MemTotalBytes = memTotal
			metrics.MemUsedBytes = memTotal - memAvail
			metrics.MemPercent = (float64(metrics.MemUsedBytes) / float64(memTotal)) * 100
		}
	}

	// 3. Network Rates (/proc/net/dev)
	if f, err := os.Open(filepath.Join(s.procPath, "net", "dev")); err == nil {
		scanner := bufio.NewScanner(f)
		var curRx, curTx uint64
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
		f.Close()

		if dt > 0 && s.lastNetRx > 0 && curRx >= s.lastNetRx {
			metrics.NetRxBytesSec = float64(curRx-s.lastNetRx) / dt
			metrics.NetTxBytesSec = float64(curTx-s.lastNetTx) / dt
			s.lastNetRx = curRx
			s.lastNetTx = curTx
		} else if s.lastNetRx == 0 {
			s.lastNetRx = curRx
			s.lastNetTx = curTx
		}
	}

	// 4. Disk I/O Rates (/proc/diskstats)
	if f, err := os.Open(filepath.Join(s.procPath, "diskstats")); err == nil {
		scanner := bufio.NewScanner(f)
		var curDiskRead, curDiskWrite uint64
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 10 {
				dev := fields[2]
				if s.diskPattern.MatchString(dev) {
					secRead, _ := strconv.ParseUint(fields[5], 10, 64)
					secWrite, _ := strconv.ParseUint(fields[9], 10, 64)
					curDiskRead += secRead * 512
					curDiskWrite += secWrite * 512
				}
			}
		}
		f.Close()

		if dt > 0 && s.lastDiskRead > 0 && curDiskRead >= s.lastDiskRead {
			metrics.DiskReadSec = float64(curDiskRead-s.lastDiskRead) / dt
			metrics.DiskWriteSec = float64(curDiskWrite-s.lastDiskWrite) / dt
			s.lastDiskRead = curDiskRead
			s.lastDiskWrite = curDiskWrite
		} else if s.lastDiskRead == 0 {
			s.lastDiskRead = curDiskRead
			s.lastDiskWrite = curDiskWrite
		}
	}

	// 5. Disk Usage (Capacity & Used Space via statfs)
	statPath := "/host/root"
	if _, err := os.Stat(statPath); err != nil {
		statPath = "/host/etc/passwd"
		if _, err := os.Stat(statPath); err != nil {
			statPath = "/"
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(statPath, &stat); err == nil && stat.Blocks > 0 {
		totalDisk := stat.Blocks * uint64(stat.Bsize)
		freeDisk := stat.Bfree * uint64(stat.Bsize)
		if totalDisk > 0 {
			metrics.DiskTotalBytes = totalDisk
			metrics.DiskUsedBytes = totalDisk - freeDisk
			metrics.DiskPercent = (float64(metrics.DiskUsedBytes) / float64(totalDisk)) * 100
		}
	}

	s.lastSampleTime = now
	s.lastMetrics = metrics

	// Append to history ring buffer (keep last maxHistory points)
	pt := SystemSamplePoint{
		Timestamp:     now.Unix(),
		CPUPercent:    metrics.CPUPercent,
		MemPercent:    metrics.MemPercent,
		MemUsedBytes:  metrics.MemUsedBytes,
		NetRxBytesSec: metrics.NetRxBytesSec,
		NetTxBytesSec: metrics.NetTxBytesSec,
		DiskReadSec:   metrics.DiskReadSec,
		DiskWriteSec:  metrics.DiskWriteSec,
		DiskPercent:   metrics.DiskPercent,
		DiskUsedBytes: metrics.DiskUsedBytes,
	}
	s.history = append(s.history, pt)
	if len(s.history) > s.maxHistory {
		s.history = s.history[len(s.history)-s.maxHistory:]
	}

	return metrics
}

func (s *SystemSampler) GetLatest() SystemMetrics {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.lastMetrics
}

// SampleWithHistory executes a sample and returns current metrics along with the historical trend points.
func (s *SystemSampler) SampleWithHistory() SystemMetrics {
	s.Sample()
	s.lock.Lock()
	defer s.lock.Unlock()
	res := s.lastMetrics
	res.History = make([]SystemSamplePoint, len(s.history))
	copy(res.History, s.history)
	return res
}

// GetWithHistory returns the latest cached metrics along with the historical trend points.
func (s *SystemSampler) GetWithHistory() SystemMetrics {
	s.lock.Lock()
	defer s.lock.Unlock()
	res := s.lastMetrics
	res.History = make([]SystemSamplePoint, len(s.history))
	copy(res.History, s.history)
	return res
}
