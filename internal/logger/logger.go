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
		retentionDays = 8
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
	if strings.Contains(trimmed, "Verifying files") && !strings.Contains(trimmed, "appcenter-cli") {
		return len(p), nil
	}

	// Size-based auto-rotation (> 20MB) to prevent disk exhaustion
	if l.currentFile != nil {
		if fi, err := l.currentFile.Stat(); err == nil && fi.Size() > 20*1024*1024 {
			_ = l.currentFile.Close()
			oldPath := filepath.Join(l.logDir, "app.log.old")
			curPath := filepath.Join(l.logDir, "app.log")
			_ = os.Remove(oldPath)
			_ = os.Rename(curPath, oldPath)
			l.currentFile, _ = os.OpenFile(curPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		}
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
		return n, err
	}

	return len(p), nil
}

// PruneOldLogs scans the log directory and deletes files older than retentionDays.
func (l *Logger) PruneOldLogs() {
	l.mu.Lock()
	defer l.mu.Unlock()

	entries, err := os.ReadDir(l.logDir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "app-") || !strings.HasSuffix(name, ".log") {
			continue
		}

		// Extract date: app-YYYY-MM-DD.log
		datePart := strings.TrimPrefix(name, "app-")
		datePart = strings.TrimSuffix(datePart, ".log")

		fileDate, err := time.Parse("2006-01-02", datePart)
		if err != nil {
			// Fallback to ModTime
			if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(l.logDir, name))
			}
			continue
		}

		if fileDate.Before(cutoff) {
			targetPath := filepath.Join(l.logDir, name)
			_ = os.Remove(targetPath)
			if l.outWriter != nil {
				_, _ = fmt.Fprintf(l.outWriter, "%s [INFO] [logger] 已自动清理超过 %d 天的历史日志: %s\n",
					time.Now().Format("2006-01-02 15:04:05"), l.retentionDays, name)
			}
		}
	}
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
// sourceFilter can be "app", "lifecycle", or ""/"ALL" (for unified combined stream).
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

	var allEntries []LogEntry

	matchAndAdd := func(entry LogEntry) {
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
		allEntries = append(allEntries, entry)
	}

	// Read app running logs if sourceFilter is "" or "all" or "app"
	if sourceFilter == "" || sourceFilter == "all" || sourceFilter == "app" {
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

	// Read lifecycle logs if sourceFilter is "" or "all" or "lifecycle"
	if sourceFilter == "" || sourceFilter == "all" || sourceFilter == "lifecycle" {
		candidates := []string{
			filepath.Join(l.logDir, "lifecycle.log"),
			"/tmp/fn-docker-to-desktop-uninstall.log",
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
		t1 := allEntries[i].Timestamp
		t2 := allEntries[j].Timestamp
		if t1 != "" && t2 != "" {
			if t1 != t2 {
				return t1 > t2
			}
		} else if t1 != "" {
			return true
		} else if t2 != "" {
			return false
		}
		return i < j
	})

	resp.TotalLines = len(allEntries)
	if len(allEntries) > limit {
		resp.Lines = allEntries[:limit]
	} else {
		resp.Lines = allEntries
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
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "Verifying files") || (len(trimmed) <= 2 && strings.ContainsAny(trimmed, "|/\\-")) {
			continue
		}

		var entry LogEntry
		if source == "lifecycle" {
			entry = parseLifecycleLogLine(line)
		} else {
			entry = parseLogLine(line)
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
		"/tmp/fn-docker-to-desktop-uninstall.log",
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
	entry := LogEntry{
		Raw:    raw,
		Source: "lifecycle",
		Level:  "info",
	}

	trimmed := strings.TrimSpace(raw)
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
