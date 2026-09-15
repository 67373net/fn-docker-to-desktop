package monitor

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// Watcher monitors port changes and broadcasts updates via SSE.
type Watcher struct {
	procPath         string
	interval         time.Duration
	subscribers      map[chan []byte]string // maps channel to active view tab: "ports", "processes"
	subLock          sync.RWMutex
	lastSnapshot     []PortEntry
	lastSnapshotTime time.Time
	snapshotLock     sync.RWMutex
	stopChan         chan struct{}
	sampler          *SystemSampler
	lastProcesses    []ProcessDetail
	lastProcTime     time.Time
	procLock         sync.RWMutex
}

// NewWatcher creates a new Watcher instance.
func NewWatcher(procPath string, interval time.Duration) *Watcher {
	if interval < 500*time.Millisecond {
		interval = 1500 * time.Millisecond
	}
	InitUserCache()
	return &Watcher{
		procPath:    procPath,
		interval:    interval,
		subscribers: make(map[chan []byte]string),
		stopChan:    make(chan struct{}),
		sampler:     NewSystemSampler(procPath),
	}
}

// Start begins the background polling loop with adaptive sleep.
func (w *Watcher) Start() {
	ticker := time.NewTicker(w.interval)
	heartbeatTicker := time.NewTicker(15 * time.Second)
	sysTicker := time.NewTicker(2 * time.Second)
	procTicker := time.NewTicker(3500 * time.Millisecond)

	go func() {
		defer ticker.Stop()
		defer heartbeatTicker.Stop()
		defer sysTicker.Stop()
		defer procTicker.Stop()

		for {
			select {
			case <-w.stopChan:
				return

			case <-heartbeatTicker.C:
				w.subLock.RLock()
				hasSubs := len(w.subscribers) > 0
				w.subLock.RUnlock()
				if !hasSubs {
					continue
				}
				w.broadcastMessage("heartbeat", map[string]interface{}{
					"timestamp": time.Now().Unix(),
				})

			case <-sysTicker.C:
				w.subLock.RLock()
				hasSubs := len(w.subscribers) > 0
				w.subLock.RUnlock()
				if !hasSubs {
					continue
				}
				sysMetrics := w.sampler.Sample()
				w.broadcastMessage("sys_metrics", sysMetrics)

			case <-procTicker.C:
				// Only scan all processes if at least one client is viewing the "processes" tab
				w.subLock.RLock()
				hasProcSubs := false
				for _, view := range w.subscribers {
					if view == "processes" {
						hasProcSubs = true
						break
					}
				}
				w.subLock.RUnlock()

				if hasProcSubs {
					procs := ScanAllProcesses(w.procPath)
					w.procLock.Lock()
					w.lastProcesses = procs
					w.lastProcTime = time.Now()
					w.procLock.Unlock()
					w.broadcastMessageToView("proc_update", procs, "processes")
				}

			case <-ticker.C:
				// Only scan ports if at least one client is viewing "ports"
				w.subLock.RLock()
				hasPortSubs := false
				for _, view := range w.subscribers {
					if view == "ports" || view == "all" {
						hasPortSubs = true
						break
					}
				}
				w.subLock.RUnlock()

				if hasPortSubs {
					w.checkChanges()
				}
			}
		}
	}()
}

// Stop terminates the watcher loop.
func (w *Watcher) Stop() {
	close(w.stopChan)
}

// ProcPath returns the procfs path used by the watcher.
func (w *Watcher) ProcPath() string {
	return w.procPath
}

func entryKey(e *PortEntry) string {
	return fmt.Sprintf("%d|%s|%s|%s|%d|%s", e.LocalPort, e.Protocol, e.LocalIP, e.State, e.PID, e.Docker.ContainerID)
}

func (w *Watcher) checkChanges() {
	current := ScanPorts(w.procPath, false)
	sysMetrics := w.sampler.GetLatest()

	w.snapshotLock.Lock()
	prev := w.lastSnapshot
	w.lastSnapshot = current
	w.lastSnapshotTime = time.Now()
	w.snapshotLock.Unlock()

	prevMap := make(map[string]PortEntry, len(prev))
	for i := range prev {
		prevMap[entryKey(&prev[i])] = prev[i]
	}

	currMap := make(map[string]PortEntry, len(current))
	for i := range current {
		currMap[entryKey(&current[i])] = current[i]
	}

	var added []PortEntry
	var removed []PortEntry

	for k, v := range currMap {
		if _, exists := prevMap[k]; !exists {
			added = append(added, v)
		}
	}

	for k, v := range prevMap {
		if _, exists := currMap[k]; !exists {
			removed = append(removed, v)
		}
	}

	if len(added) > 0 || len(removed) > 0 || len(prev) == 0 {
		var totalTCP, totalUDP, totalDocker int
		procSet := make(map[int]struct{})

		for _, e := range current {
			for _, proto := range e.Protocols {
				if proto == "tcp" {
					totalTCP++
					break
				}
			}
			for _, proto := range e.Protocols {
				if proto == "udp" {
					totalUDP++
					break
				}
			}
			if e.Docker.IsDocker {
				totalDocker++
			}
			for _, pid := range e.PIDs {
				if pid > 0 {
					procSet[pid] = struct{}{}
				}
			}
		}

		diff := DiffResult{
			Added:       added,
			Removed:     removed,
			Snapshot:    current,
			TotalTCP:    totalTCP,
			TotalUDP:    totalUDP,
			TotalDocker: totalDocker,
			TotalProc:   len(procSet),
			System:      sysMetrics,
			Timestamp:   time.Now().Unix(),
		}

		w.broadcastMessageToView("port_change", diff, "ports")
	}
}

// GetCurrentSnapshot returns the most recent port list with stats and system metrics.
func (w *Watcher) GetCurrentSnapshot(listenOnly bool) ([]PortEntry, int, int, int, int, SystemMetrics) {
	w.snapshotLock.Lock()
	if len(w.lastSnapshot) == 0 || time.Since(w.lastSnapshotTime) > 2*time.Second {
		w.lastSnapshot = ScanPorts(w.procPath, false)
		w.lastSnapshotTime = time.Now()
	}
	snapshot := w.lastSnapshot
	w.snapshotLock.Unlock()

	var filtered []PortEntry
	var totalTCP, totalUDP, totalDocker int
	procSet := make(map[int]struct{})

	for _, e := range snapshot {
		if listenOnly && (e.State != "LISTEN" && e.State != "UNCONN") {
			continue
		}
		filtered = append(filtered, e)
		for _, proto := range e.Protocols {
			if proto == "tcp" {
				totalTCP++
				break
			}
		}
		for _, proto := range e.Protocols {
			if proto == "udp" {
				totalUDP++
				break
			}
		}
		if e.Docker.IsDocker {
			totalDocker++
		}
		for _, pid := range e.PIDs {
			if pid > 0 {
				procSet[pid] = struct{}{}
			}
		}
	}

	return filtered, totalTCP, totalUDP, totalDocker, len(procSet), w.sampler.SampleWithHistory()
}

// GetCurrentProcesses returns the latest process snapshot (or scans if none or older than 2s).
func (w *Watcher) GetCurrentProcesses() ([]ProcessDetail, SystemMetrics) {
	w.procLock.Lock()
	if len(w.lastProcesses) == 0 || time.Since(w.lastProcTime) > 2*time.Second {
		w.lastProcesses = ScanAllProcesses(w.procPath)
		w.lastProcTime = time.Now()
	}
	procs := w.lastProcesses
	w.procLock.Unlock()

	return procs, w.sampler.SampleWithHistory()
}

// Subscribe registers a client channel to receive SSE event payloads for a specific view tab.
func (w *Watcher) Subscribe(view string) chan []byte {
	if view == "" {
		view = "ports"
	}
	ch := make(chan []byte, 32)
	w.subLock.Lock()
	w.subscribers[ch] = view
	isFirst := len(w.subscribers) == 1
	w.subLock.Unlock()

	// If this was the first subscriber, immediately trigger an update
	if isFirst {
		go func() {
			if view == "processes" {
				procs, _ := w.GetCurrentProcesses()
				w.broadcastMessageToView("proc_update", procs, "processes")
			} else {
				w.checkChanges()
			}
			w.broadcastMessage("sys_metrics", w.sampler.Sample())
		}()
	}

	return ch
}

// Unsubscribe unregisters a client channel. If all subscribers disconnect, immediately frees OS memory.
func (w *Watcher) Unsubscribe(ch chan []byte) {
	w.subLock.Lock()
	delete(w.subscribers, ch)
	rem := len(w.subscribers)
	w.subLock.Unlock()

	close(ch)

	// If zero subscribers remain, release OS memory to enter ultra-low power sleep
	if rem == 0 {
		go debug.FreeOSMemory()
	}
}

func (w *Watcher) broadcastMessage(event string, data interface{}) {
	payload, err := json.Marshal(map[string]interface{}{
		"event": event,
		"data":  data,
	})
	if err != nil {
		return
	}

	w.subLock.RLock()
	defer w.subLock.RUnlock()

	for ch := range w.subscribers {
		select {
		case ch <- payload:
		default:
			// Client slow, skip to prevent blocking
		}
	}
}

func (w *Watcher) broadcastMessageToView(event string, data interface{}, targetView string) {
	payload, err := json.Marshal(map[string]interface{}{
		"event": event,
		"data":  data,
	})
	if err != nil {
		return
	}

	w.subLock.RLock()
	defer w.subLock.RUnlock()

	for ch, view := range w.subscribers {
		if view == targetView || view == "all" {
			select {
			case ch <- payload:
			default:
			}
		}
	}
}

// BroadcastDockLabelChange notifies connected SSE clients that Docker containers/labels have changed.
func (w *Watcher) BroadcastDockLabelChange() {
	w.broadcastMessage("docklabel_update", map[string]interface{}{
		"time": time.Now().Unix(),
	})
}
