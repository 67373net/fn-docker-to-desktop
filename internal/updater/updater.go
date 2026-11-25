package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// UpdateStatus represents the live status of the self-update process.
type UpdateStatus struct {
	State           string  `json:"state"` // "idle", "downloading", "installing", "restarting", "success", "error", "cancelled"
	Progress        float64 `json:"progress"` // 0.0 to 100.0
	Speed           string  `json:"speed"` // e.g. "2.4 MB/s"
	DownloadedBytes int64   `json:"downloaded_bytes"`
	TotalBytes      int64   `json:"total_bytes"`
	Message         string  `json:"message"`
	Error           string  `json:"error,omitempty"`
	TargetVersion   string  `json:"target_version,omitempty"`
	StartedAt       string  `json:"started_at,omitempty"`
	UpdatedAt       string  `json:"updated_at,omitempty"`
}

// Updater manages the self-updating process including package downloading and detached installation.
type Updater struct {
	mu          sync.RWMutex
	status      UpdateStatus
	cancelFunc  context.CancelFunc
	cliPath     string
	statusFile  string
	fpkPath     string
	scriptPath  string
	logPath     string
	httpClient  *http.Client
	execCommand func(name string, arg ...string) *exec.Cmd
}

// NewUpdater creates a new Updater instance.
func NewUpdater(cliPath string) *Updater {
	return &Updater{
		status: UpdateStatus{
			State:   "idle",
			Message: "空闲中",
		},
		cliPath:    cliPath,
		statusFile: "/tmp/fn-docker-to-desktop-update-status.json",
		fpkPath:    "/tmp/fn-docker-to-desktop-upgrade.fpk",
		scriptPath: "/tmp/fn-docker-to-desktop-upgrade.sh",
		logPath:    "/tmp/fn-docker-to-desktop-upgrade.log",
		httpClient: &http.Client{
			Timeout: 30 * time.Minute,
		},
		execCommand: exec.Command,
	}
}

// SetPaths configures custom file paths (primarily used in unit testing).
func (u *Updater) SetPaths(statusFile, fpkPath, scriptPath, logPath string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if statusFile != "" {
		u.statusFile = statusFile
	}
	if fpkPath != "" {
		u.fpkPath = fpkPath
	}
	if scriptPath != "" {
		u.scriptPath = scriptPath
	}
	if logPath != "" {
		u.logPath = logPath
	}
}

// SetExecCommand overrides the command runner (used for testing).
func (u *Updater) SetExecCommand(fn func(name string, arg ...string) *exec.Cmd) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.execCommand = fn
}

// GetStatus returns the current status, checking disk if memory is idle or installing/restarting.
func (u *Updater) GetStatus() UpdateStatus {
	u.mu.RLock()
	memStatus := u.status
	statusFile := u.statusFile
	u.mu.RUnlock()

	// If memory is idle or in progress, check if there's a fresh status on disk (e.g. from background script or after restart)
	if memStatus.State == "idle" || memStatus.State == "installing" || memStatus.State == "restarting" {
		if diskStatus, err := u.readDiskStatus(statusFile); err == nil {
			// Check if file is recent (within 10 minutes)
			if diskStatus.UpdatedAt != "" {
				if t, err := time.Parse(time.RFC3339, diskStatus.UpdatedAt); err == nil {
					if time.Since(t) < 10*time.Minute {
						return diskStatus
					}
				}
			}
		}
	}
	return memStatus
}

// StartUpdate triggers the background self-update.
func (u *Updater) StartUpdate(downloadURL, targetVersion string) error {
	downloadURL = strings.TrimSpace(downloadURL)
	targetVersion = strings.TrimSpace(targetVersion)

	if downloadURL == "" {
		return errors.New("下载地址不能为空")
	}
	if !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://") {
		return errors.New("非法下载地址")
	}

	u.mu.Lock()
	if u.status.State == "downloading" || u.status.State == "installing" {
		u.mu.Unlock()
		return errors.New("更新任务已在进行中，请勿重复操作")
	}

	ctx, cancel := context.WithCancel(context.Background())
	u.cancelFunc = cancel
	nowStr := time.Now().Format(time.RFC3339)
	u.status = UpdateStatus{
		State:         "downloading",
		Progress:      0,
		Speed:         "0 B/s",
		Message:       "正在准备下载安装包...",
		TargetVersion: targetVersion,
		StartedAt:     nowStr,
		UpdatedAt:     nowStr,
	}
	statusCopy := u.status
	u.mu.Unlock()

	_ = u.writeDiskStatus(statusCopy)

	go u.runUpdateLoop(ctx, downloadURL, targetVersion)
	return nil
}

// Cancel cancels any ongoing download task.
func (u *Updater) Cancel() {
	u.mu.Lock()
	if u.cancelFunc != nil {
		u.cancelFunc()
		u.cancelFunc = nil
	}
	if u.status.State == "downloading" {
		nowStr := time.Now().Format(time.RFC3339)
		u.status.State = "cancelled"
		u.status.Message = "已取消下载"
		u.status.UpdatedAt = nowStr
		statusCopy := u.status
		u.mu.Unlock()
		_ = u.writeDiskStatus(statusCopy)
	} else {
		u.mu.Unlock()
	}
	_ = os.Remove(u.fpkPath + ".tmp")
}

func (u *Updater) runUpdateLoop(ctx context.Context, downloadURL, targetVersion string) {
	slog.Info("[UPDATER] 开始在线更新任务", "targetVersion", targetVersion, "url", downloadURL)

	err := u.downloadPackage(ctx, downloadURL)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("[UPDATER] 更新下载已被用户取消")
			return
		}
		slog.Error("[UPDATER] 下载安装包失败", "error", err)
		u.mu.Lock()
		u.status.State = "error"
		u.status.Error = err.Error()
		u.status.Message = "下载安装包失败: " + err.Error()
		u.status.UpdatedAt = time.Now().Format(time.RFC3339)
		statusCopy := u.status
		u.mu.Unlock()
		_ = u.writeDiskStatus(statusCopy)
		return
	}

	// Download successful, prepare installation
	slog.Info("[UPDATER] 安装包下载完成，准备启动独立安装脚本", "fpk", u.fpkPath)
	u.mu.Lock()
	u.status.State = "installing"
	u.status.Progress = 100
	u.status.Speed = ""
	u.status.Message = "安装包下载完成，正在准备安装..."
	u.status.UpdatedAt = time.Now().Format(time.RFC3339)
	statusCopy := u.status
	u.mu.Unlock()
	_ = u.writeDiskStatus(statusCopy)

	if err := u.launchDetachedInstaller(targetVersion); err != nil {
		slog.Error("[UPDATER] 启动独立安装脚本失败", "error", err)
		u.mu.Lock()
		u.status.State = "error"
		u.status.Error = err.Error()
		u.status.Message = "启动更新脚本失败: " + err.Error()
		u.status.UpdatedAt = time.Now().Format(time.RFC3339)
		statusCopy := u.status
		u.mu.Unlock()
		_ = u.writeDiskStatus(statusCopy)
	}
}

func (u *Updater) downloadPackage(ctx context.Context, downloadURL string) error {
	tmpFile := u.fpkPath + ".tmp"
	_ = os.Remove(tmpFile)

	urlsToTry := []string{downloadURL}
	if strings.Contains(downloadURL, "github.com") && !strings.Contains(downloadURL, "ghproxy") {
		urlsToTry = append(urlsToTry, "https://ghproxy.net/"+downloadURL)
	}

	var lastErr error
	for idx, targetURL := range urlsToTry {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if idx > 0 {
			slog.Info("[UPDATER] 尝试使用镜像加速下载安装包", "url", targetURL)
		}

		err := u.tryDownloadURL(ctx, targetURL, tmpFile)
		if err == nil {
			// Validate downloaded file
			fi, statErr := os.Stat(tmpFile)
			if statErr != nil || fi.Size() < 100*1024 {
				_ = os.Remove(tmpFile)
				lastErr = fmt.Errorf("下载的文件大小异常 (小于 100KB)")
				continue
			}

			// Rename to destination
			_ = os.Remove(u.fpkPath)
			if renameErr := os.Rename(tmpFile, u.fpkPath); renameErr != nil {
				return fmt.Errorf("保存安装包文件失败: %w", renameErr)
			}
			return nil
		}

		lastErr = err
		if errors.Is(err, context.Canceled) {
			_ = os.Remove(tmpFile)
			return err
		}
		slog.Warn("[UPDATER] 从当前源下载失败，尝试下一个源", "url", targetURL, "error", err)
	}

	_ = os.Remove(tmpFile)
	return fmt.Errorf("所有下载源均失败: %w", lastErr)
}

func (u *Updater) tryDownloadURL(ctx context.Context, downloadURL, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "fn-docker-to-desktop-updater")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP 状态码异常 %d", resp.StatusCode)
	}

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	totalBytes := resp.ContentLength
	var downloadedBytes int64
	buf := make([]byte, 32*1024)

	lastTime := time.Now()
	var lastBytes int64

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return wErr
			}
			downloadedBytes += int64(n)

			now := time.Now()
			elapsed := now.Sub(lastTime).Seconds()
			if elapsed >= 0.3 {
				bytesSec := float64(downloadedBytes-lastBytes) / elapsed
				speed := formatSpeed(bytesSec)
				progress := 0.0
				if totalBytes > 0 {
					progress = float64(downloadedBytes) / float64(totalBytes) * 100.0
					if progress > 100.0 {
						progress = 100.0
					}
				}

				msg := fmt.Sprintf("正在下载安装包... (%.1f MB / %.1f MB)",
					float64(downloadedBytes)/(1024*1024), float64(totalBytes)/(1024*1024))
				if totalBytes <= 0 {
					msg = fmt.Sprintf("正在下载安装包... (%.1f MB)", float64(downloadedBytes)/(1024*1024))
				}

				u.mu.Lock()
				u.status.Progress = progress
				u.status.Speed = speed
				u.status.DownloadedBytes = downloadedBytes
				u.status.TotalBytes = totalBytes
				u.status.Message = msg
				u.status.UpdatedAt = now.Format(time.RFC3339)
				u.mu.Unlock()

				lastTime = now
				lastBytes = downloadedBytes
			}
		}

		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return rErr
		}
	}

	return nil
}

func (u *Updater) launchDetachedInstaller(targetVersion string) error {
	scriptContent := u.generateScriptContent(targetVersion)
	if err := os.WriteFile(u.scriptPath, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("写入更新执行脚本失败: %w", err)
	}

	shellPath := "/bin/bash"
	if _, err := os.Stat(shellPath); err != nil {
		shellPath = "/bin/sh"
	}

	cmd := u.execCommand(shellPath, u.scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动后台脱离进程失败: %w", err)
	}

	slog.Info("[UPDATER] 成功启动独立安装升级脚本", "script", u.scriptPath, "pid", cmd.Process.Pid)
	return nil
}

func (u *Updater) generateScriptContent(targetVersion string) string {
	return fmt.Sprintf(`#!/bin/bash
STATUS_FILE=%q
FPK_FILE=%q
LOG_FILE=%q
TARGET_VERSION=%q

write_status() {
  local state="$1"
  local progress="$2"
  local msg="$3"
  local err="$4"
  local now
  now=$(date -u +"%%Y-%%m-%%dT%%H:%%M:%%SZ" 2>/dev/null || date +"%%Y-%%m-%%dT%%H:%%M:%%SZ")
  cat <<EOF > "${STATUS_FILE}"
{
  "state": "${state}",
  "progress": ${progress},
  "message": "${msg}",
  "error": "${err}",
  "target_version": "${TARGET_VERSION}",
  "updated_at": "${now}"
}
EOF
}

exec >> "${LOG_FILE}" 2>&1
echo "=========================================="
echo "Upgrade script started at $(date)"
echo "Target version: ${TARGET_VERSION}"
echo "FPK file: ${FPK_FILE}"

# Wait 1s to allow the HTTP response of update status to return to browser
sleep 1

write_status "installing" 100 "正在通过飞牛应用中心安装新版本..." ""

# Locate appcenter-cli
CLI=%q
if [ -z "$CLI" ] || [ ! -x "$CLI" ]; then
  for p in /var/apps/appcenter/target/bin/appcenter-cli /usr/bin/appcenter-cli /usr/local/bin/appcenter-cli /host/root/usr/bin/appcenter-cli /host/root/var/apps/appcenter/target/bin/appcenter-cli; do
    if [ -x "$p" ]; then
      CLI="$p"
      break
    fi
  done
fi
if [ -z "$CLI" ]; then
  CLI=$(command -v appcenter-cli 2>/dev/null || true)
fi

echo "Appcenter CLI: ${CLI}"

# Determine default volume
VOLUME="1"
if [ -n "$CLI" ]; then
  DEF_VOL=$("$CLI" default-volume 2>/dev/null || true)
  if [ -n "$DEF_VOL" ]; then
    NUM=$(echo "$DEF_VOL" | grep -o '[0-9]\+' | head -n1)
    if [ -n "$NUM" ]; then
      VOLUME="$NUM"
    fi
  fi
fi
echo "Install volume: ${VOLUME}"

SUCCESS=0

# Step 1: Try appcenter-cli install-fpk
if [ -n "$CLI" ]; then
  echo "Attempting: $CLI install-fpk ${FPK_FILE} --volume ${VOLUME}"
  if "$CLI" install-fpk "${FPK_FILE}" --volume "${VOLUME}"; then
    echo "install-fpk succeeded"
    SUCCESS=1
  else
    echo "install-fpk failed, will try extracting and install-local..."
  fi
fi

# Step 2: Fallback to extract and install-local
if [ "$SUCCESS" -eq 0 ] && [ -n "$CLI" ]; then
  EXTRACT_DIR="/tmp/fn-update-extract-$$"
  mkdir -p "${EXTRACT_DIR}"
  if tar -tf "${FPK_FILE}" >/dev/null 2>&1; then
    tar -xf "${FPK_FILE}" -C "${EXTRACT_DIR}" 2>/dev/null || true
  elif unzip -t "${FPK_FILE}" >/dev/null 2>&1; then
    unzip -q -o "${FPK_FILE}" -d "${EXTRACT_DIR}" 2>/dev/null || true
  fi
  if [ -f "${EXTRACT_DIR}/manifest" ]; then
    echo "Extracted package found manifest, running install-local..."
    cd "${EXTRACT_DIR}"
    if "$CLI" install-local --volume "${VOLUME}"; then
      echo "install-local succeeded"
      SUCCESS=1
    fi
    cd /
  fi
  rm -rf "${EXTRACT_DIR}"
fi

# Step 3: Standalone fallback if CLI not found or standard install failed
if [ "$SUCCESS" -eq 0 ]; then
  APP_DIR="/var/apps/fn-docker-to-desktop/target"
  if [ -d "$APP_DIR" ]; then
    echo "Attempting direct extraction fallback into ${APP_DIR}..."
    EXTRACT_DIR="/tmp/fn-update-extract-$$"
    mkdir -p "${EXTRACT_DIR}"
    if tar -tf "${FPK_FILE}" >/dev/null 2>&1; then
      tar -xf "${FPK_FILE}" -C "${EXTRACT_DIR}" 2>/dev/null || true
    fi
    if [ -f "${EXTRACT_DIR}/app.tgz" ]; then
      tar -xzf "${EXTRACT_DIR}/app.tgz" -C "${APP_DIR}" 2>/dev/null || true
      if [ -f "${APP_DIR}/fn-docker-to-desktop" ]; then
        chmod +x "${APP_DIR}/fn-docker-to-desktop"* 2>/dev/null || true
        SUCCESS=1
        echo "Direct extraction succeeded"
      fi
    fi
    rm -rf "${EXTRACT_DIR}"
  fi
fi

if [ "$SUCCESS" -eq 1 ]; then
  echo "Installation successful, ensuring service is started..."
  write_status "restarting" 100 "安装完成，正在启动服务..." ""
  if [ -n "$CLI" ]; then
    "$CLI" start fn-docker-to-desktop 2>/dev/null || true
  elif [ -x "/var/apps/fn-docker-to-desktop/target/fn-docker-to-desktop" ]; then
    systemctl restart fn-docker-to-desktop 2>/dev/null || true
  fi
  sleep 2
  write_status "success" 100 "更新成功，服务已重启" ""
  rm -f "${FPK_FILE}"
  echo "Upgrade finished successfully at $(date)"
  exit 0
else
  echo "All upgrade methods failed at $(date)"
  write_status "error" 100 "应用更新失败，请查看 /tmp/fn-docker-to-desktop-upgrade.log 或手动下载安装包安装" "安装命令执行异常"
  exit 1
fi
`, u.statusFile, u.fpkPath, u.logPath, targetVersion, u.cliPath)
}

func (u *Updater) writeDiskStatus(st UpdateStatus) error {
	dir := filepath.Dir(u.statusFile)
	_ = os.MkdirAll(dir, 0755)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(u.statusFile, b, 0644)
}

func (u *Updater) readDiskStatus(file string) (UpdateStatus, error) {
	var st UpdateStatus
	b, err := os.ReadFile(file)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func formatSpeed(bytesSec float64) string {
	if bytesSec >= 1024*1024 {
		return fmt.Sprintf("%.1f MB/s", bytesSec/(1024*1024))
	} else if bytesSec >= 1024 {
		return fmt.Sprintf("%.0f KB/s", bytesSec/1024)
	}
	return fmt.Sprintf("%.0f B/s", bytesSec)
}
