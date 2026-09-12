package monitor

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var sensitiveArgRegex = regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|key|auth|api[-_]?key)[=:\s]+([^\s]+)`)

func maskSensitiveCmdline(cmd string) string {
	if cmd == "" {
		return ""
	}
	return sensitiveArgRegex.ReplaceAllStringFunc(cmd, func(match string) string {
		parts := strings.FieldsFunc(match, func(r rune) bool {
			return r == '=' || r == ':' || r == ' '
		})
		if len(parts) >= 2 {
			sepIdx := strings.IndexAny(match, "=:\t ")
			if sepIdx != -1 {
				return match[:sepIdx+1] + "******"
			}
		}
		return match
	})
}

// ScanAllProcesses scans all active processes in procPath and returns a list sorted by CPU% descending.
func ScanAllProcesses(procPath string) []ProcessDetail {
	if procPath == "" {
		procPath = "/proc"
	}

	RefreshDockerContainers()

	entries, err := os.ReadDir(procPath)
	if err != nil {
		return nil
	}

	results := make([]ProcessDetail, 0, 128)

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

		p := resolveProcessInfo(procPath, pid)
		if p == nil {
			continue
		}

		detail := ProcessDetail{
			PID:          p.PID,
			Name:         p.Name,
			Cmdline:      maskSensitiveCmdline(p.Cmdline),
			Exe:          p.Exe,
			State:        p.State,
			User:         p.Username,
			UID:          p.UID,
			Threads:      p.Threads,
			CPUPercent:   p.CPUPercent,
			MemRSSBytes:  p.MemRSSBytes,
			MemPercent:   p.MemPercent,
			IOReadBytes:  p.IOReadBytes,
			IOWriteBytes: p.IOWriteBytes,
			IOReadRate:   p.IOReadRate,
			IOWriteRate:  p.IOWriteRate,
			NetRxRate:    p.NetRxRate,
			NetTxRate:    p.NetTxRate,
		}

		if dInfo, ok := ResolveDockerByContainerID(p.ContainerID); ok {
			detail.Docker = dInfo
		} else if p.Name == "docker-proxy" {
			detail.Docker = DockerInfo{
				IsDocker:      true,
				ContainerName: "docker-proxy",
				Summary:       "Docker 端口映射代理",
			}
		}

		results = append(results, detail)
	}

	// Sort by CPU% descending by default (to quickly identify CPU hogs)
	sort.Slice(results, func(i, j int) bool {
		if results[i].CPUPercent != results[j].CPUPercent {
			return results[i].CPUPercent > results[j].CPUPercent
		}
		return results[i].MemRSSBytes > results[j].MemRSSBytes
	})

	PruneDeadRateSamples()
	return results
}
