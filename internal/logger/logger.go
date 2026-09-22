package logger

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// LogEntry represents a structured log line for the Web UI.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Raw       string `json:"raw"`
}

// LogResponse is returned by GetLogs for API serialization.
type LogResponse struct {
	Success     bool       `json:"success"`
	Dates       []string   `json:"dates,omitempty"`
	CurrentDate string     `json:"current_date,omitempty"`
	LogPath     string     `json:"log_path"`
	FileSize    int64      `json:"file_size"`
	TotalLines  int        `json:"total_lines"`
	Lines       []LogEntry `json:"lines"`
}

const (
	DefaultRetentionDays = 28
	MaxLogSizeBytes      = 28 * 1024 * 1024 // 28MB 最大保留体积
	TargetPruneSizeBytes = 24 * 1024 * 1024 // 24MB 淘汰目标体积 (超限后淘汰最老日志并预留缓冲空间)
)

// Logger manages multi-destination streaming logging and auto-pruning.
type Logger struct {
	mu            sync.Mutex
	logDir        string
	currentFile   *os.File
	fallbackFile  *os.File
	outWriter     io.Writer
	retentionDays int
	stopChan      chan struct{}
}

var (
	defaultLogger *Logger
)

// Init initializes the streaming logger and configures slog default handler.
func Init(dataDir string, retentionDays int) (*Logger, error) {
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}

	// Determine log directory:
	// 1. If TRIM_PKGVAR is defined (fnOS environment), use ${TRIM_PKGVAR}/logs
	// 2. Otherwise use ${dataDir}/logs
	logDir := ""
	if trimPkgVar := os.Getenv("TRIM_PKGVAR"); trimPkgVar != "" {
		logDir = filepath.Join(trimPkgVar, "logs")
	} else {
		logDir = filepath.Join(dataDir, "logs")
	}

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}

	// Also ensure dataDir/logs exists or mirrors if TRIM_PKGVAR is used
	if dataDir != "" {
		altDir := filepath.Join(dataDir, "logs")
		if altDir != logDir {
			_ = os.MkdirAll(altDir, 0755)
		}
	}

	fallbackFile, _ := os.OpenFile("/tmp/fn-docker-to-desktop.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)

	filePath := filepath.Join(logDir, "app.log")
	currentFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("初始化日志文件失败: %w", err)
	}

	l := &Logger{
		logDir:        logDir,
		retentionDays: retentionDays,
		outWriter:     os.Stdout,
		fallbackFile:  fallbackFile,
		currentFile:   currentFile,
		stopChan:      make(chan struct{}),
	}

	// Prune old logs immediately
	l.PruneOldLogs()

	// Start background pruning ticker (runs every hour)
	go l.runPruningLoop()

	defaultLogger = l

	// Set up slog custom handler
	handler := newCustomSlogHandler(l)
	slog.SetDefault(slog.New(handler))

	return l, nil
}

// GetDefault returns the singleton Logger instance.
func GetDefault() *Logger {
	return defaultLogger
}

// Close flushes and closes active log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	select {
	case <-l.stopChan:
	default:
		close(l.stopChan)
	}

	if l.fallbackFile != nil {
		_ = l.fallbackFile.Sync()
		_ = l.fallbackFile.Close()
		l.fallbackFile = nil
	}

	if l.currentFile != nil {
		_ = l.currentFile.Sync()
		err := l.currentFile.Close()
		l.currentFile = nil
		return err
	}
	return nil
}

// LogDir returns the active log directory on the filesystem.
func (l *Logger) LogDir() string {
	return l.logDir
}

// Write writes raw bytes to stdout and streaming log file with thread safety.
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Filter out lone spinner progress lines
	trimmed := strings.TrimSpace(string(p))
	if isCliSpinnerLine(trimmed) {
		return len(p), nil
	}

	// Write to stdout
	if l.outWriter != nil {
		_, _ = l.outWriter.Write(p)
	}

	// Write to fallback /tmp log for universal troubleshooting
	if l.fallbackFile != nil {
		_, _ = l.fallbackFile.Write(p)
		_ = l.fallbackFile.Sync()
	}

	// Write to streaming log file
	if l.currentFile != nil {
		n, err = l.currentFile.Write(p)
		_ = l.currentFile.Sync()

		// Size-based auto-pruning (> 28MB) to keep within 28MB limit
		if fi, err := l.currentFile.Stat(); err == nil && fi.Size() > MaxLogSizeBytes {
			l.pruneCurrentFileLocked()
		}

		return n, err
	}

	return len(p), nil
}

func (l *Logger) pruneCurrentFileLocked() {
	curPath := filepath.Join(l.logDir, "app.log")
	if l.currentFile != nil {
		_ = l.currentFile.Sync()
		_ = l.currentFile.Close()
		l.currentFile = nil
	}

	_ = pruneLogFile(curPath, l.retentionDays, MaxLogSizeBytes, TargetPruneSizeBytes)

	var err error
	l.currentFile, err = os.OpenFile(curPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil && l.outWriter != nil {
		_, _ = fmt.Fprintf(l.outWriter, "[ERROR] [logger] 重新打开日志文件失败: %v\n", err)
	}
}

// PruneOldLogs scans the log directory and trims logs exceeding 28 days or 28MB.
func (l *Logger) PruneOldLogs() {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. Prune primary streaming app.log (both > 28 days and > 28MB)
	l.pruneCurrentFileLocked()

	// 2. Prune legacy app-*.log files
	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)
	entries, err := os.ReadDir(l.logDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".log") {
				datePart := strings.TrimSuffix(strings.TrimPrefix(name, "app-"), ".log")
				if fileDate, err := time.Parse("2006-01-02", datePart); err == nil {
					if fileDate.Before(cutoff) {
						_ = os.Remove(filepath.Join(l.logDir, name))
						if l.outWriter != nil {
							_, _ = fmt.Fprintf(l.outWriter, "%s [INFO] [logger] 已清理超过 %d 天的历史日志: %s\n",
								time.Now().Format("2006-01-02 15:04:05"), l.retentionDays, name)
						}
					}
				} else {
					if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
						_ = os.Remove(filepath.Join(l.logDir, name))
					}
				}
			}
			// Clean up stale temporary files
			if strings.HasSuffix(name, ".prune.tmp") {
				_ = os.Remove(filepath.Join(l.logDir, name))
			}
		}
	}

	// 3. Prune fallback and lifecycle logs
	if l.fallbackFile != nil {
		_ = l.fallbackFile.Sync()
		_ = l.fallbackFile.Close()
		l.fallbackFile = nil
	}
	_ = pruneLogFile("/tmp/fn-docker-to-desktop.log", l.retentionDays, MaxLogSizeBytes, TargetPruneSizeBytes)
	l.fallbackFile, _ = os.OpenFile("/tmp/fn-docker-to-desktop.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)

	_ = pruneLogFile("/tmp/fn-docker-to-desktop-lifecycle.log", l.retentionDays, MaxLogSizeBytes, TargetPruneSizeBytes)
	_ = pruneLogFile(filepath.Join(l.logDir, "lifecycle.log"), l.retentionDays, MaxLogSizeBytes, TargetPruneSizeBytes)

	// Clean up legacy noisy uninstall log if it exists in /tmp
	_ = os.Remove("/tmp/fn-docker-to-desktop-uninstall.log")
}

func (l *Logger) runPruningLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.PruneOldLogs()
		case <-l.stopChan:
			return
		}
	}
}

// ListAvailableDates returns available log dates in descending order for legacy support.
func (l *Logger) ListAvailableDates() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	dates := []string{}
	entries, err := os.ReadDir(l.logDir)
	if err != nil {
		return dates
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".log") {
			datePart := strings.TrimPrefix(name, "app-")
			datePart = strings.TrimSuffix(datePart, ".log")
			if _, err := time.Parse("2006-01-02", datePart); err == nil {
				dates = append(dates, datePart)
			}
		}
	}

	sort.Slice(dates, func(i, j int) bool {
		return dates[i] > dates[j]
	})

	return dates
}

// GetLogFilePath returns the absolute path of the primary streaming log file.
func (l *Logger) GetLogFilePath(dateStr ...string) string {
	if len(dateStr) > 0 && dateStr[0] != "" {
		legacy := filepath.Join(l.logDir, fmt.Sprintf("app-%s.log", dateStr[0]))
		if fi, err := os.Stat(legacy); err == nil && !fi.IsDir() {
			return legacy
		}
	}
	return filepath.Join(l.logDir, "app.log")
}

// ReadLogs reads and filters logs from app.log and/or lifecycle logs.
// sourceFilter can be "app", "lifecycle", or "all" / "".
// Entries are returned in descending chronological order (newest logs at top).
func (l *Logger) ReadLogs(sourceFilter, levelFilter, search string, limit int) (*LogResponse, error) {
	if limit <= 0 || limit > 10000 {
		limit = 5000
	}

	sourceFilter = strings.ToLower(strings.TrimSpace(sourceFilter))
	levelFilter = strings.ToLower(strings.TrimSpace(levelFilter))
	search = strings.ToLower(strings.TrimSpace(search))

	filePath := filepath.Join(l.logDir, "app.log")
	resp := &LogResponse{
		Success: true,
		LogPath: filePath,
		Lines:   []LogEntry{},
	}

	type orderedEntry struct {
		entry LogEntry
		order int
	}
	var allEntries []orderedEntry
	orderCounter := 0

	matchAndAdd := func(entry LogEntry) {
		if entry.Raw == "" {
			return
		}
		if levelFilter != "" && levelFilter != "all" {
			if strings.ToLower(entry.Level) != levelFilter {
				return
			}
		}
		if search != "" {
			if !strings.Contains(strings.ToLower(entry.Raw), search) &&
				!strings.Contains(strings.ToLower(entry.Message), search) {
				return
			}
		}
		orderCounter++
		allEntries = append(allEntries, orderedEntry{
			entry: entry,
			order: orderCounter,
		})
	}

	// Read app running logs if sourceFilter is "app", "all", or ""
	if sourceFilter == "app" || sourceFilter == "all" || sourceFilter == "" {
		// Read legacy app-*.log files first so past entries are preserved
		entries, _ := os.ReadDir(l.logDir)
		var legacyFiles []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "app-") && strings.HasSuffix(e.Name(), ".log") {
				legacyFiles = append(legacyFiles, filepath.Join(l.logDir, e.Name()))
			}
		}
		sort.Strings(legacyFiles)
		for _, f := range legacyFiles {
			readLinesFromFile(f, "app", matchAndAdd)
		}

		// Also check rotated app.log.old
		oldAppLog := filepath.Join(l.logDir, "app.log.old")
		if fi, err := os.Stat(oldAppLog); err == nil && !fi.IsDir() {
			readLinesFromFile(oldAppLog, "app", matchAndAdd)
		}

		// Read current streaming app.log
		if fi, err := os.Stat(filePath); err == nil {
			resp.FileSize += fi.Size()
			readLinesFromFile(filePath, "app", matchAndAdd)
		} else if fallbackFi, err := os.Stat("/tmp/fn-docker-to-desktop.log"); err == nil {
			resp.FileSize += fallbackFi.Size()
			// Fallback if app.log not yet created
			readLinesFromFile("/tmp/fn-docker-to-desktop.log", "app", matchAndAdd)
		}
	}

	// Read lifecycle logs if sourceFilter is "lifecycle" or "all"
	if sourceFilter == "lifecycle" || sourceFilter == "all" {
		candidates := []string{
			filepath.Join(l.logDir, "lifecycle.log"),
			"/tmp/fn-docker-to-desktop-lifecycle.log",
		}
		seenFiles := make(map[string]bool)
		for _, c := range candidates {
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 && !seenFiles[c] {
				seenFiles[c] = true
				if sourceFilter == "lifecycle" {
					resp.LogPath = c
					resp.FileSize = fi.Size()
				}
				readLinesFromFile(c, "lifecycle", matchAndAdd)
			}
		}
	}

	// Sort descending by timestamp: newer logs displayed at top
	sort.SliceStable(allEntries, func(i, j int) bool {
		t1 := allEntries[i].entry.Timestamp
		t2 := allEntries[j].entry.Timestamp
		if t1 != "" && t2 != "" {
			if t1 != t2 {
				return t1 > t2
			}
			return allEntries[i].order > allEntries[j].order
		} else if t1 != "" {
			return true
		} else if t2 != "" {
			return false
		}
		return allEntries[i].order > allEntries[j].order
	})

	resp.TotalLines = len(allEntries)
	resultLines := make([]LogEntry, 0, len(allEntries))
	for _, item := range allEntries {
		resultLines = append(resultLines, item.entry)
	}

	if len(resultLines) > limit {
		resp.Lines = resultLines[:limit]
	} else {
		resp.Lines = resultLines
	}

	return resp, nil
}

func readLinesFromFile(path string, source string, handler func(LogEntry)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 128*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isCliSpinnerLine(trimmed) {
			continue
		}

		var entry LogEntry
		if source == "lifecycle" {
			entry = parseLifecycleLogLine(line)
		} else {
			entry = parseLogLine(line)
		}
		if entry.Raw == "" {
			continue
		}
		entry.Source = source
		handler(entry)
	}
}

// GetLifecycleLogFilePath returns the path to the lifecycle log file.
func (l *Logger) GetLifecycleLogFilePath() string {
	candidates := []string{
		"/tmp/fn-docker-to-desktop-lifecycle.log",
		filepath.Join(l.logDir, "lifecycle.log"),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return c
		}
	}
	return "/tmp/fn-docker-to-desktop-lifecycle.log"
}

// ReadLifecycleLogs reads and filters the system lifecycle and startup logs.
func (l *Logger) ReadLifecycleLogs(levelFilter, search string, limit int) (*LogResponse, error) {
	return l.ReadLogs("lifecycle", levelFilter, search, limit)
}

func parseLifecycleLogLine(raw string) LogEntry {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isCliSpinnerLine(trimmed) {
		return LogEntry{}
	}

	// Structured lifecycle log line MUST have a timestamp prefix: [202... or 202...
	if !strings.HasPrefix(trimmed, "[202") && !strings.HasPrefix(trimmed, "202") {
		return LogEntry{}
	}

	entry := LogEntry{
		Raw:    raw,
		Source: "lifecycle",
		Level:  "info",
	}

	if strings.HasPrefix(trimmed, "[") {
		idx := strings.Index(trimmed, "]")
		if idx > 1 {
			entry.Timestamp = trimmed[1:idx]
			entry.Message = strings.TrimSpace(trimmed[idx+1:])
		}
	} else {
		parts := strings.SplitN(trimmed, " ", 3)
		if len(parts) >= 2 {
			entry.Timestamp = parts[0] + " " + parts[1]
		}
		if len(parts) >= 3 {
			entry.Message = parts[2]
		} else {
			entry.Message = raw
		}
	}

	upper := strings.ToUpper(raw)
	if strings.Contains(upper, "ERROR") || strings.Contains(raw, "失败") || strings.Contains(upper, "FATAL") {
		entry.Level = "error"
	} else if strings.Contains(upper, "WARN") || strings.Contains(raw, "警告") {
		entry.Level = "warn"
	} else if strings.Contains(upper, "DEBUG") {
		entry.Level = "debug"
	} else {
		entry.Level = "info"
	}

	if entry.Message == "" {
		entry.Message = raw
	}
	return entry
}

func parseLogLine(raw string) LogEntry {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isCliSpinnerLine(trimmed) {
		return LogEntry{}
	}

	entry := LogEntry{
		Raw:    raw,
		Source: "app",
		Level:  "info",
	}

	// Format: 2026-09-08 17:15:30 [LEVEL] message...
	parts := strings.SplitN(raw, " ", 3)
	if len(parts) >= 2 {
		entry.Timestamp = parts[0] + " " + parts[1]
	}

	upper := strings.ToUpper(raw)
	if strings.Contains(upper, "[ERROR]") || strings.Contains(upper, "LEVEL=ERROR") || strings.Contains(upper, "[PANIC]") {
		entry.Level = "error"
	} else if strings.Contains(upper, "[WARN]") || strings.Contains(upper, "LEVEL=WARN") {
		entry.Level = "warn"
	} else if strings.Contains(upper, "[DEBUG]") || strings.Contains(upper, "LEVEL=DEBUG") {
		entry.Level = "debug"
	} else {
		entry.Level = "info"
	}

	if len(parts) >= 3 {
		entry.Message = parts[2]
	} else {
		entry.Message = raw
	}

	return entry
}

var timeLayouts = []string{
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05.00",
	"2006-01-02 15:04:05.0",
	"2006-01-02 15:04:05",
	time.RFC3339,
	time.RFC3339Nano,
}

func parseLineTimestamp(line string) (time.Time, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[") {
		idx := strings.Index(trimmed, "]")
		if idx > 1 {
			tsStr := trimmed[1:idx]
			for _, layout := range timeLayouts {
				if t, err := time.ParseInLocation(layout, tsStr, time.Local); err == nil {
					return t, true
				}
			}
		}
	} else {
		parts := strings.SplitN(trimmed, " ", 3)
		if len(parts) >= 2 {
			tsStr := parts[0] + " " + parts[1]
			for _, layout := range timeLayouts {
				if t, err := time.ParseInLocation(layout, tsStr, time.Local); err == nil {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

func isCliSpinnerLine(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)

	// Filter common CLI progress indicators
	if strings.Contains(lower, "uninstalling") ||
		strings.Contains(lower, "installing") ||
		strings.Contains(lower, "verifying files") ||
		strings.Contains(lower, "installation complete") {
		return true
	}
	if strings.Contains(lower, "checking") && (strings.Contains(lower, "uninstall") || strings.Contains(lower, "install") || len(trimmed) <= 20) {
		return true
	}

	// Filter single spinner characters or spinner prefix like "| uninstalling." or "- uninstalling.."
	if len(trimmed) <= 2 && strings.ContainsAny(trimmed, "|/\\-") {
		return true
	}
	if len(trimmed) >= 2 && (trimmed[0] == '/' || trimmed[0] == '\\' || trimmed[0] == '|' || trimmed[0] == '-') {
		if trimmed[1] == ' ' || trimmed[1] == '\t' || strings.Contains(lower, "uninstalling") {
			return true
		}
	}

	return false
}

func pruneLogFile(filePath string, retentionDays int, maxBytes int64, targetBytes int64) error {
	fi, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	if fi.Size() == 0 {
		return nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	type lineMeta struct {
		text string
		size int
	}

	var keptLines []lineMeta
	var totalKeptBytes int64
	var lastTime time.Time
	var droppedOldLines bool

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 128*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isCliSpinnerLine(trimmed) {
			droppedOldLines = true
			continue
		}

		if t, ok := parseLineTimestamp(trimmed); ok {
			lastTime = t
		}

		// Check if older than retentionDays (28 days)
		if !lastTime.IsZero() && lastTime.Before(cutoff) {
			droppedOldLines = true
			continue
		}

		lineLen := len(line) + 1 // newline
		keptLines = append(keptLines, lineMeta{
			text: line,
			size: lineLen,
		})
		totalKeptBytes += int64(lineLen)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}

	// If no lines were dropped and size does not exceed maxBytes, no rewrite needed
	if !droppedOldLines && totalKeptBytes <= maxBytes {
		return nil
	}

	// If totalKeptBytes exceeds targetBytes, drop oldest lines from head
	startIndex := 0
	for totalKeptBytes > targetBytes && startIndex < len(keptLines) {
		totalKeptBytes -= int64(keptLines[startIndex].size)
		startIndex++
	}

	tmpPath := filePath + ".prune.tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	w := bufio.NewWriter(tmpFile)
	for i := startIndex; i < len(keptLines); i++ {
		_, _ = w.WriteString(keptLines[i].text)
		_ = w.WriteByte('\n')
	}
	_ = w.Flush()
	_ = tmpFile.Sync()
	_ = tmpFile.Close()

	return os.Rename(tmpPath, filePath)
}

// LogDiagnostic outputs detailed startup diagnostics to help investigate environment and startup issues.
func LogDiagnostic(version string, port int, host, dataDir, iconPath string, socketPath ...string) {
	hostname, _ := os.Hostname()
	wd, _ := os.Getwd()
	exePath, _ := os.Executable()

	var ipList []string
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			if addrs, err := iface.Addrs(); err == nil {
				for _, addr := range addrs {
					if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
						if ipNet.IP.To4() != nil {
							ipList = append(ipList, fmt.Sprintf("%s:%s", iface.Name, ipNet.IP.String()))
						}
					}
				}
			}
		}
	}

	banner := strings.Repeat("=", 78)
	slog.Info(banner)
	slog.Info(fmt.Sprintf("把 Docker 放到桌面 (fn-docker-to-desktop) 服务启动诊断信息 [版本: %s]", version))
	slog.Info("------------------------------------------------------------------------------")
	slog.Info("应用信息",
		"版本号", version,
		"程序标识", "fn-docker-to-desktop",
		"系统架构", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		"Go版本", runtime.Version(),
	)
	slog.Info("基础环境",
		"PID", os.Getpid(),
		"UID/GID", fmt.Sprintf("%d/%d", os.Getuid(), os.Getgid()),
		"主机名", hostname,
	)
	slog.Info("运行路径",
		"工作目录", wd,
		"程序文件", exePath,
		"数据目录", dataDir,
		"图标路径", iconPath,
	)
	slog.Info("飞牛系统变量",
		"TRIM_APPDEST", os.Getenv("TRIM_APPDEST"),
		"TRIM_PKGVAR", os.Getenv("TRIM_PKGVAR"),
		"PORT_ENV", os.Getenv("PORT"),
	)
	if port > 0 {
		slog.Info("网络服务",
			"运行模式", "TCP 端口独立模式",
			"绑定端口", port,
			"监听主机", host,
			"网卡IP", strings.Join(ipList, ", "),
		)
	} else {
		sock := ""
		if len(socketPath) > 0 {
			sock = socketPath[0]
		}
		slog.Info("网络服务",
			"运行模式", "飞牛统一网关模式 (免端口模式)",
			"Unix Socket", sock,
			"说明", "零端口占用，免端口配置，告别冲突",
		)
	}
	if defaultLogger != nil {
		slog.Info("日志系统",
			"日志存储路径", filepath.Join(defaultLogger.LogDir(), "app.log"),
			"存储模式", "单文件流式存储",
			"保留策略", fmt.Sprintf("最长 %d 天或最大 %d MB (满足其一即淘汰超限日志)", defaultLogger.retentionDays, MaxLogSizeBytes/(1024*1024)),
		)
	}
	slog.Info(banner)
}

// RecoverAndLog handles panics, logs full stack trace, flushes files, and prevents silent exit.
func RecoverAndLog(contextDesc string, panicVal any) {
	stack := string(debug.Stack())
	msg := fmt.Sprintf("=== 捕获到严重 Panic 异常 [%s] ===\n异常详情: %v\n堆栈跟踪:\n%s\n", contextDesc, panicVal, stack)
	if defaultLogger != nil {
		_, _ = defaultLogger.Write([]byte(msg))
	} else {
		_, _ = fmt.Fprintln(os.Stderr, msg)
		if f, err := os.OpenFile("/tmp/fn-docker-to-desktop.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666); err == nil {
			_, _ = f.WriteString(msg)
			_ = f.Close()
		}
	}
}

// --- Custom slog Handler implementation ---
type customSlogHandler struct {
	logger *Logger
	opts   slog.HandlerOptions
}

func newCustomSlogHandler(l *Logger) *customSlogHandler {
	lvl := slog.LevelInfo
	if envLvl := os.Getenv("LOG_LEVEL"); strings.EqualFold(envLvl, "DEBUG") {
		lvl = slog.LevelDebug
	}
	return &customSlogHandler{
		logger: l,
		opts: slog.HandlerOptions{
			Level: lvl,
		},
	}
}

func (h *customSlogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *customSlogHandler) Handle(_ context.Context, r slog.Record) error {
	levelStr := "INFO"
	switch {
	case r.Level >= slog.LevelError:
		levelStr = "ERROR"
	case r.Level >= slog.LevelWarn:
		levelStr = "WARN"
	case r.Level <= slog.LevelDebug:
		levelStr = "DEBUG"
	}

	timeStr := r.Time.Format("2006-01-02 15:04:05")

	var attrs []string
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, fmt.Sprintf("%s=%v", a.Key, a.Value.Any()))
		return true
	})

	attrText := ""
	if len(attrs) > 0 {
		attrText = " " + strings.Join(attrs, " ")
	}

	line := fmt.Sprintf("%s [%s] %s%s\n", timeStr, levelStr, r.Message, attrText)
	_, err := h.logger.Write([]byte(line))
	return err
}

func (h *customSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *customSlogHandler) WithGroup(name string) slog.Handler {
	return h
}
