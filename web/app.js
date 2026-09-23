// --- 把 Docker 放到桌面 - 前端应用程序 ---

function getAppSessionToken() {
  return window.__FN_SESSION__ || document.querySelector('meta[name="fn-session-token"]')?.content || '';
}

// Global fetch interceptor to attach anti-bypass frontend session token
(function() {
  const originalFetch = window.fetch;
  window.fetch = function(resource, init = {}) {
    const token = getAppSessionToken();
    if (!init.headers) {
      init.headers = {};
    }
    if (init.headers instanceof Headers) {
      if (token && !init.headers.has('X-App-Session')) {
        init.headers.set('X-App-Session', token);
      }
      if (!init.headers.has('X-Requested-With')) {
        init.headers.set('X-Requested-With', 'XMLHttpRequest');
      }
    } else if (Array.isArray(init.headers)) {
      if (token) init.headers.push(['X-App-Session', token]);
      init.headers.push(['X-Requested-With', 'XMLHttpRequest']);
    } else {
      if (token) init.headers['X-App-Session'] = token;
      init.headers['X-Requested-With'] = 'XMLHttpRequest';
    }
    return originalFetch.call(this, resource, init);
  };
})();

const BASE_PATH = window.location.pathname.startsWith('/app/fn-docker-to-desktop')
  ? '/app/fn-docker-to-desktop'
  : '';

function apiUrl(path) {
  return BASE_PATH + path;
}

function getIconUrl(icon) {
  if (!icon || icon === 'default_item_icon.png' || icon === '/default_item_icon.png') {
    return apiUrl('/default_item_icon.png');
  }
  if (icon === 'icon.png' || icon === '/icon.png') {
    return apiUrl('/icon.png');
  }
  if (icon.startsWith('http://') || icon.startsWith('https://') || icon.startsWith('data:')) {
    return icon;
  }
  if (icon.startsWith('/api/')) {
    return apiUrl(icon);
  }
  const clean = icon.replace(/^\/?icons\//, '').replace(/^\/+/, '');
  if (!clean || clean === 'default_item_icon.png') {
    return apiUrl('/default_item_icon.png');
  }
  if (clean === 'icon.png') {
    return apiUrl('/icon.png');
  }
  return apiUrl(`/icons/${clean}`);
}

function normalizeHexColor(val) {
  if (!val) return null;
  val = val.trim();
  if (val.startsWith('#')) {
    val = val.slice(1);
  }
  if (/^[0-9a-fA-F]{6}$/.test(val)) {
    return '#' + val.toLowerCase();
  }
  if (/^[0-9a-fA-F]{3}$/.test(val)) {
    return '#' + (val[0] + val[0] + val[1] + val[1] + val[2] + val[2]).toLowerCase();
  }
  return null;
}

// Client-side audit & error reporting to backend logs
function reportClientLog(type, action, message, details, stack) {
  try {
    const payload = JSON.stringify({
      level: type === 'error' ? 'error' : 'info',
      type: type || 'action',
      action: action || '',
      message: message || '',
      details: details || {},
      stack: stack || ''
    });
    if (navigator.sendBeacon) {
      const blob = new Blob([payload], { type: 'application/json' });
      const token = getAppSessionToken();
      const beaconUrl = token ? apiUrl(`/api/logs/client?session=${encodeURIComponent(token)}`) : apiUrl('/api/logs/client');
      navigator.sendBeacon(beaconUrl, blob);
    } else {
      fetch(apiUrl('/api/logs/client'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: payload,
        keepalive: true
      }).catch(() => {});
    }
  } catch (e) {
    console.warn('Failed to send client log:', e);
  }
}

// Automatically report runtime JS errors to backend logs
window.addEventListener('error', function (event) {
  reportClientLog(
    'error',
    'WindowError',
    event.message || '前端脚本错误',
    { filename: event.filename, lineno: event.lineno, colno: event.colno },
    event.error ? event.error.stack : ''
  );
});

// Automatically report unhandled Promise rejections to backend logs
window.addEventListener('unhandledrejection', function (event) {
  const reason = event.reason;
  const msg = reason ? (reason.message || String(reason)) : '未处理的Promise拒绝';
  reportClientLog(
    'error',
    'UnhandledPromiseRejection',
    msg,
    {},
    reason && reason.stack ? reason.stack : ''
  );
});

let state = {
  currentTab: 'ports',
  ports: [],
  desktopItems: [],
  desktopItemsLoaded: false,
  watchcowItems: [],
  watchcowItemsLoaded: false,
  processes: [],
  system: null,
  host: null,
  portFilterSource: 'docker', // 'all' | 'docker' | 'host' (默认: Docker 容器)
  portFilterProto: 'all',     // 'all' | 'tcp' | 'udp' (默认: 全部)
  portSearch: '',
  desktopSearch: '',
  procSearch: '',
  procSort: 'cpu',
  procSortDir: 'desc',
  activeIconTab: 'text',
  currentTextIconDataUrl: null,
  desktopItemFormSnapshot: null,
  originalSettings: null,
  isSettingsDirty: false,
  eventSource: null,
  activeMode: 'local',
  logSource: 'ALL',
  logLevel: 'ALL',
  logSearch: '',
  logPage: 1,
  logPageSize: 200,
  logs: [],
  appNameDirty: false,
  appShortId: '',
  iconLibrary: [],
  iconLibraryLoaded: false,
  initialModalIcon: null,
  initialSettingIcon: null,
};

// --- Utilities ---
function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function formatRate(bytesSec) {
  if (!bytesSec || bytesSec < 0.1) return '0 B/s';
  return formatBytes(bytesSec) + '/s';
}

function escapeHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function getHostTargetUrl(port, protocol = 'http', path = '/') {
  let hostname = window.location.hostname;
  if (!hostname || hostname === '0.0.0.0') hostname = '127.0.0.1';
  if (hostname.includes(':') && !hostname.startsWith('[')) hostname = `[${hostname}]`;
  if (!path.startsWith('/')) path = '/' + path;
  return `${protocol}://${hostname}:${port}${path}`;
}

// --- Tab Navigation ---
function initNavigation() {
  document.querySelectorAll('.nav-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      const tab = btn.dataset.tab;
      switchTab(tab);
    });
  });
}

function switchTab(tab) {
  if (state.currentTab === 'settings' && tab !== 'settings' && state.isSettingsDirty) {
    if (!confirm('设置有未保存的修改，离开将丢失未保存的设置，确定要切换标签页吗？')) {
      return;
    }
    updateSettingsForm();
  }

  state.currentTab = tab;
  document.querySelectorAll('.nav-tab').forEach(b => {
    const isActive = b.dataset.tab === tab;
    b.classList.toggle('active', isActive);
    if (isActive && typeof b.scrollIntoView === 'function') {
      b.scrollIntoView({ behavior: 'smooth', inline: 'nearest', block: 'nearest' });
    }
  });
  document.querySelectorAll('.tab-pane').forEach(p => {
    p.classList.toggle('active', p.id === `pane-${tab}`);
  });

  if (tab === 'ports') {
    fetchPorts();
  } else if (tab === 'desktop') {
    fetchDesktopItems();
  } else if (tab === 'processes') {
    fetchProcesses();
  } else if (tab === 'logs') {
    fetchLogs();
  } else if (tab === 'settings') {
    fetchSettings();
    fetchHost();
  } else if (tab === 'about') {
    checkAppUpdate(false);
  }

  requestAnimationFrame(adjustAllTableWrapping);
}

// --- Data Fetching ---
async function fetchPorts() {
  try {
    const res = await fetch(apiUrl('/api/ports'));
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      state.ports = await res.json();
      renderPortsTable();
      updatePortCountBadge();
    }
  } catch (err) {
    console.error('Fetch ports error:', err);
  }
}

let watchcowFetchSeq = 0;

function scheduleReconcilePolling() {
  if (state.reconcilePollTimer) {
    clearTimeout(state.reconcilePollTimer);
    state.reconcilePollTimer = null;
  }
  const hasDesktopActive = (state.desktopItems || []).some(item => item && (item.reconciling || item._updating));
  const hasWatchcowActive = (state.watchcowItems || []).some(item => item && (item.reconciling || item._updating));
  if (hasDesktopActive || hasWatchcowActive) {
    state.reconcilePollTimer = setTimeout(() => {
      if (hasDesktopActive) fetchDesktopItems();
      if (hasWatchcowActive) fetchWatchcowItems();
    }, 2000);
  }
}

async function fetchWatchcowItems() {
  const fetchStart = Date.now();
  const seq = ++watchcowFetchSeq;
  try {
    let res = await fetch(apiUrl('/api/desktop/docklabel'));
    if (!res.ok && res.status !== 401) {
      res = await fetch(apiUrl('/api/desktop/watchcow'));
    }
    if (res.status === 401) return;
    if (seq < watchcowFetchSeq) {
      // Discard outdated response
      return;
    }
    if (res.ok) {
      const serverItems = await res.json();
      const pendingMap = new Map();
      (state.watchcowItems || []).forEach(item => {
        if (item && (item._updating || item._error)) {
          pendingMap.set(item.id, item);
        }
      });
      state.watchcowItems = serverItems.map(item => {
        const pending = pendingMap.get(item.id);
        if (pending && pending._updating) {
          return {
            ...item,
            _updating: true,
            _statusText: pending._statusText || (item.enabled ? '停用中...' : '启用中...')
          };
        }
        return item;
      });
      state.watchcowItemsLoaded = true;
      renderDesktopTable();
      updateDesktopCountBadge();
      scheduleReconcilePolling();
    } else {
      state.watchcowItemsLoaded = true;
      renderDesktopTable();
    }
  } catch (err) {
    console.error('Fetch docklabel items error:', err);
    state.watchcowItemsLoaded = true;
    renderDesktopTable();
  } finally {
    const dur = Date.now() - fetchStart;
    if (dur > 1500) {
      console.warn(`[PERF] fetchWatchcowItems 扫描耗时过长: ${dur}ms`);
      reportClientLog('warn', 'fetchWatchcowItems 扫描耗时过长', `${dur}ms`);
    }
  }
}

async function fetchDesktopItems() {
  const fetchStart = Date.now();
  fetchWatchcowItems();
  try {
    const res = await fetch(apiUrl('/api/desktop/items'));
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      const serverItems = await res.json();

      // Collect existing pending / in-flight items
      const pendingMap = new Map();
      (state.desktopItems || []).forEach(item => {
        if (item && (item._updating || item._error)) {
          pendingMap.set(item.id, item);
        }
      });

      const merged = [];
      const seenIds = new Set();

      // 1. Preserve any newly added items still in progress that server has not yet returned
      pendingMap.forEach((pendingItem, id) => {
        const onServer = serverItems.find(s => s.id === id);
        if (!onServer && pendingItem._statusText !== '正在移出中...') {
          merged.push(pendingItem);
          seenIds.add(id);
        }
      });

      // 2. Merge server items with any active in-flight status
      serverItems.forEach(serverItem => {
        const pendingItem = pendingMap.get(serverItem.id);
        if (pendingItem) {
          if (pendingItem._statusText === '正在移出中...') {
            merged.push({
              ...serverItem,
              _updating: true,
              _statusText: pendingItem._statusText,
              _error: pendingItem._error,
            });
          } else {
            merged.push({
              ...serverItem,
              ...pendingItem,
              _updating: pendingItem._updating,
              _statusText: pendingItem._statusText,
              _error: pendingItem._error,
            });
          }
        } else {
          merged.push(serverItem);
        }
        seenIds.add(serverItem.id);
      });

      state.desktopItems = merged;
      state.desktopItemsLoaded = true;
      renderDesktopTable();
      updateDesktopCountBadge();
      scheduleReconcilePolling();
    } else {
      state.desktopItemsLoaded = true;
      renderDesktopTable();
    }
  } catch (err) {
    console.error('Fetch desktop items error:', err);
    state.desktopItemsLoaded = true;
    renderDesktopTable();
  } finally {
    const dur = Date.now() - fetchStart;
    if (dur > 1000) {
      console.warn(`[PERF] fetchDesktopItems 响应耗时过长: ${dur}ms`);
      reportClientLog('warn', 'fetchDesktopItems 响应耗时过长', `${dur}ms`);
    }
  }
}

async function handleExportDesktopItems() {
  const items = state.desktopItems || [];
  if (items.length === 0) {
    showToast('当前没有可导出的桌面图标', 'info');
    return;
  }
  try {
    showToast('正在生成包含图标与配置的备份压缩包...', 'info');
    const headers = {};
    if (state.sessionToken) {
      headers['X-App-Session'] = state.sessionToken;
    }
    const res = await fetch(apiUrl('/api/desktop/export'), { headers });
    if (!res.ok) {
      const errJson = await res.json().catch(() => ({}));
      throw new Error(errJson.error || `HTTP ${res.status}`);
    }
    const blob = await res.blob();
    const d = new Date();
    const dateStr = d.getFullYear() +
      String(d.getMonth() + 1).padStart(2, '0') +
      String(d.getDate()).padStart(2, '0') + '-' +
      String(d.getHours()).padStart(2, '0') +
      String(d.getMinutes()).padStart(2, '0') +
      String(d.getSeconds()).padStart(2, '0');
    const filename = `fn-desktop-icons-${dateStr}.zip`;
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    showToast(`已成功导出 ${items.length} 个桌面图标及图片资源压缩包 (.zip)`, 'success');
    reportClientLog('action', '用户导出桌面图标ZIP备份', `导出数量: ${items.length}, 文件名: ${filename}`);
  } catch (err) {
    console.error('Export error:', err);
    showToast('导出桌面图标失败: ' + err.message, 'error');
  }
}

async function fetchProcesses() {
  try {
    const res = await fetch(apiUrl('/api/processes'));
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      state.processes = await res.json();
      renderProcessesTable();
    }
  } catch (err) {
    console.error('Fetch processes error:', err);
  }
}

async function fetchSystem() {
  try {
    const res = await fetch(apiUrl('/api/system'));
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      state.system = await res.json();
      updateSystemMetrics(state.system);
    }
  } catch (err) {
    console.error('Fetch system error:', err);
  }
}

async function fetchHost() {
  try {
    const res = await fetch(apiUrl('/api/host'));
    if (res.ok) {
      state.host = await res.json();
      renderHostInfo(state.host);
    }
  } catch (err) {
    console.error('Fetch host error:', err);
  }
}

async function fetchSettings() {
  try {
    const res = await fetch(apiUrl('/api/settings'));
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      const settings = await res.json();
      state.settings = settings;
      state.originalSettings = {
        portal_name: settings.portal_name || '把 Docker 放到桌面',
        portal_all_users: settings.portal_all_users === true,
        portal_icon: settings.portal_icon || 'icon.png',
        portal_icon_type: settings.portal_icon_type || 'upload',
        portal_icon_text: settings.portal_icon_text || '',
        portal_icon_text_color: settings.portal_icon_text_color || '#ffffff',
        portal_icon_bg_color: settings.portal_icon_bg_color || '#1e293b',
      };
      state.isSettingsDirty = false;
      updateSettingsForm();
    }
  } catch (err) {
    console.error('Fetch settings error:', err);
  }
}

function formatRelativeTime(dateInput) {
  if (!dateInput) return '';
  const date = new Date(dateInput);
  if (isNaN(date.getTime())) return '';
  const now = new Date();
  const diffSec = Math.floor((now.getTime() - date.getTime()) / 1000);
  if (diffSec < 0) return '刚刚';
  if (diffSec < 60) return `${Math.max(1, diffSec)}秒前`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}分钟前`;
  const diffHour = Math.floor(diffMin / 60);
  if (diffHour < 24) return `${diffHour}小时前`;
  const diffDay = Math.floor(diffHour / 24);
  if (diffDay < 30) return `${diffDay}天前`;
  const diffMonth = Math.floor(diffDay / 30);
  if (diffMonth < 12) return `${diffMonth}个月前`;
  const diffYear = Math.floor(diffDay / 365);
  return `${Math.max(1, diffYear)}年前`;
}

let isCheckingUpdate = false;
let updateCheckResult = null;

async function checkAppUpdate(force = false) {
  if (isCheckingUpdate) return;
  const btn = document.getElementById('btn-check-update');
  const badge = document.getElementById('version-status-badge');
  const dot = document.getElementById('about-update-dot');
  const card = document.getElementById('update-notice-card');

  isCheckingUpdate = true;
  if (btn) {
    btn.disabled = true;
    btn.textContent = '正在检查...';
  }
  if (badge) {
    badge.style.display = 'inline-flex';
    badge.className = 'version-status-badge';
    badge.textContent = '正在检查更新...';
  }

  try {
    const res = await fetch(apiUrl('/api/system/version-check' + (force ? '?force=true' : '')));
    if (res.ok) {
      const data = await res.json();
      updateCheckResult = data;
      if (data.has_update) {
        if (dot) dot.style.display = 'inline-block';
        if (badge) {
          badge.style.display = 'inline-flex';
          badge.className = 'version-status-badge has-update';
          badge.textContent = `发现新版本 v${data.latest_version}`;
        }
        if (card) {
          card.style.display = 'block';
          const newVerEl = document.getElementById('update-new-version');
          if (newVerEl) newVerEl.textContent = `v${data.latest_version}`;
          const timeEl = document.getElementById('update-published-time');
          if (timeEl) {
            const relTime = formatRelativeTime(data.published_at);
            timeEl.textContent = relTime ? `发布于 ${relTime}` : '';
          }
          const dlBtn = document.getElementById('btn-download-fpk');
          if (dlBtn && data.download_url) {
            dlBtn.href = data.download_url;
          }
          const changelogEl = document.getElementById('update-changelog-body');
          if (changelogEl) {
            changelogEl.textContent = (data.release_notes || '').trim() || '暂无更新日志说明';
          }
        }
      } else {
        if (dot) dot.style.display = 'none';
        if (card) card.style.display = 'none';
        if (badge) {
          badge.style.display = 'inline-flex';
          if (data.error) {
            badge.className = 'version-status-badge';
            badge.textContent = data.error;
          } else {
            badge.className = 'version-status-badge up-to-date';
            badge.textContent = '当前已是最新版本';
          }
        }
      }
    } else {
      if (badge) {
        badge.style.display = 'inline-flex';
        badge.className = 'version-status-badge';
        badge.textContent = '检查更新失败';
      }
    }
  } catch (err) {
    console.error('Check update error:', err);
    if (badge) {
      badge.className = 'version-status-badge';
      badge.textContent = '网络连接异常';
    }
  } finally {
    isCheckingUpdate = false;
    if (btn) {
      btn.disabled = false;
      btn.textContent = '检查更新';
    }
  }
}

function updateSettingsForm() {
  const portalName = '把 Docker 放到桌面';
  const elName = document.getElementById('setting-portal-name');
  if (elName) elName.value = portalName;

  const ver = state.settings?.version || '1.1.44';
  const titleEl = document.getElementById('settings-card-title');
  if (titleEl) {
    titleEl.textContent = `v${ver} - 系统设置`;
  }
  const aboutVerEl = document.getElementById('about-app-version');
  if (aboutVerEl) {
    aboutVerEl.textContent = `v${ver}`;
  }
  document.title = `${portalName} - 容器与端口管理`;

  const allUsers = state.originalSettings?.portal_all_users ? 'true' : 'false';
  const rAll = document.querySelector(`input[name="setting-portal-all-users"][value="${allUsers}"]`);
  if (rAll) rAll.checked = true;

  const pwdEl = document.getElementById('setting-portal-password');
  const pwdConfirmEl = document.getElementById('setting-portal-password-confirm');
  if (pwdEl) pwdEl.value = '';
  if (pwdConfirmEl) pwdConfirmEl.value = '';
  const tipEl = document.getElementById('password-match-tip');
  if (tipEl) tipEl.textContent = '';
  const statusEl = document.getElementById('settings-status');
  if (statusEl) statusEl.textContent = '';

  // Update setting icon editor form fields and preview
  let iconType = state.originalSettings?.portal_icon_type;
  if (!iconType || iconType === 'upload') {
    iconType = 'lib';
  }
  const icon = state.originalSettings?.portal_icon || 'icon.png';
  const iconText = state.originalSettings?.portal_icon_text || '';
  const iconTextColor = state.originalSettings?.portal_icon_text_color || '#ffffff';
  const iconBgColor = state.originalSettings?.portal_icon_bg_color || '#1e293b';

  const elSettingIcon = document.getElementById('setting-portal-icon');
  if (elSettingIcon) elSettingIcon.value = icon;

  const elSettingTextInput = document.getElementById('setting-icon-text-input');
  if (elSettingTextInput) elSettingTextInput.value = iconText;

  updateSettingTextColorUI(iconTextColor);
  updateSettingBgColorUI(iconBgColor);

  const elSettingUrlInput = document.getElementById('setting-icon-url-input');
  if (elSettingUrlInput) {
    elSettingUrlInput.value = (iconType === 'url') ? icon : '';
  }

  const previewImg = document.getElementById('setting-icon-preview-img');
  if (iconType === 'text' && iconText) {
    setSettingIconTab('text');
    renderSettingTextIconCanvas();
  } else if (iconType === 'url' && icon) {
    setSettingIconTab('url');
    if (previewImg) previewImg.src = icon;
  } else {
    setSettingIconTab('lib');
    if (previewImg) previewImg.src = getIconUrl(icon);
  }

  collapseIconPicker('setting');
  saveIconSnapshot('setting');
  state.isSettingsDirty = false;
}

function checkSettingsDirty() {
  if (!state.originalSettings) return;
  const rAll = document.querySelector('input[name="setting-portal-all-users"]:checked');
  const curAll = rAll ? rAll.value === 'true' : false;
  const curPwd = document.getElementById('setting-portal-password')?.value || '';
  const curPwdConfirm = document.getElementById('setting-portal-password-confirm')?.value || '';

  const allUsersChanged = curAll !== state.originalSettings.portal_all_users;
  const pwdChanged = curPwd !== '' || curPwdConfirm !== '';
  const iconChanged = isIconModified('setting');

  state.isSettingsDirty = allUsersChanged || pwdChanged || iconChanged;

  const statusEl = document.getElementById('settings-status');
  if (statusEl) {
    if (state.isSettingsDirty) {
      statusEl.textContent = '有未保存的修改';
      statusEl.style.color = 'var(--text-muted)';
    } else {
      statusEl.textContent = '';
    }
  }

  const btnSave = document.getElementById('btn-save-settings');
  const btnCancel = document.getElementById('btn-cancel-settings');
  if (btnSave) btnSave.disabled = !state.isSettingsDirty;
  if (btnCancel) btnCancel.disabled = !state.isSettingsDirty;
}

// --- SSE Real-time Updates ---
function initEventSource() {
  if (state.eventSource) {
    state.eventSource.close();
  }
  const token = getAppSessionToken();
  const eventUrl = token ? apiUrl(`/api/events?session=${encodeURIComponent(token)}`) : apiUrl('/api/events');
  state.eventSource = new EventSource(eventUrl);
  state.eventSource.onmessage = (e) => {
    try {
      if (!e.data || typeof e.data !== 'string') return;
      let jsonStr = e.data.trim();
      if (jsonStr.startsWith('event:')) {
        const dataIdx = jsonStr.indexOf('data:');
        if (dataIdx !== -1) {
          jsonStr = jsonStr.slice(dataIdx + 5).trim();
        }
      }
      const parsed = JSON.parse(jsonStr);
      const data = (parsed && typeof parsed === 'object' && parsed.data) ? parsed.data : parsed;
      if (data.system) {
        state.system = data.system;
        updateSystemMetrics(data.system);
      } else if (data.cpu_percent !== undefined) {
        state.system = data;
        updateSystemMetrics(data);
      }
      if (data.snapshot) {
        state.ports = data.snapshot;
        if (state.currentTab === 'ports') {
          renderPortsTable();
        }
        updatePortCountBadge();
      }
      if (data && data.type === 'docklabel_update') {
        fetchDesktopItems();
        fetchWatchcowItems();
      }
    } catch (err) {
      console.error('SSE parse error:', err);
    }
  };
  state.eventSource.addEventListener('docklabel_update', () => {
    fetchDesktopItems();
    fetchWatchcowItems();
  });
  state.eventSource.onerror = () => {
    // Retry on failure
  };
}

function updatePortCountBadge() {
  const badge = document.getElementById('port-count-badge');
  if (badge) badge.textContent = state.ports.length;
}

function updateDesktopCountBadge() {
  const badge = document.getElementById('desktop-count-badge');
  if (badge) badge.textContent = (state.desktopItems ? state.desktopItems.length : 0) + (state.watchcowItems ? state.watchcowItems.length : 0);
}

function updateDesktopBadge() {
  updateDesktopCountBadge();
}

function updateSystemMetrics(sys) {
  if (!sys) return;
  const cpuPct = Math.round(sys.cpu_percent || 0);
  const memPct = Math.round(sys.mem_percent || 0);



  // System pane
  const cardCpu = document.getElementById('card-cpu');
  const fillCpu = document.getElementById('fill-cpu');
  if (cardCpu) cardCpu.textContent = `${cpuPct}%`;
  if (fillCpu) fillCpu.style.width = `${Math.min(100, cpuPct)}%`;

  const cardMem = document.getElementById('card-mem');
  const fillMem = document.getElementById('fill-mem');
  if (cardMem) {
    const usedGB = (sys.mem_used_bytes / (1024 ** 3)).toFixed(1);
    const totalGB = (sys.mem_total_bytes / (1024 ** 3)).toFixed(1);
    cardMem.textContent = `${usedGB} / ${totalGB} GB (${memPct}%)`;
  }
  if (fillMem) fillMem.style.width = `${Math.min(100, memPct)}%`;

  const cardDisk = document.getElementById('card-disk');
  const fillDisk = document.getElementById('fill-disk');
  if (cardDisk) {
    const usedGB = (sys.disk_used_bytes / (1024 ** 3)).toFixed(1);
    const totalGB = (sys.disk_total_bytes / (1024 ** 3)).toFixed(1);
    const diskPct = Math.round(sys.disk_percent || 0);
    cardDisk.textContent = `${usedGB} / ${totalGB} GB (${diskPct}%)`;
    if (fillDisk) fillDisk.style.width = `${Math.min(100, diskPct)}%`;
  }

  const cardNet = document.getElementById('card-net');
  if (cardNet) {
    cardNet.textContent = `↓ ${formatRate(sys.net_rx_bytes_sec)}  ↑ ${formatRate(sys.net_tx_bytes_sec)}`;
  }
}

function renderHostInfo(host) {
  if (!host) return;
  document.getElementById('host-name').textContent = host.hostname || '';
  document.getElementById('host-os').textContent = host.os_pretty || host.os_name || '';
  document.getElementById('host-kernel').textContent = host.kernel || '';
  document.getElementById('host-arch').textContent = host.arch || '';
  document.getElementById('host-ip').textContent = host.primary_ip || '';

  if (host.uptime_seconds) {
    const days = Math.floor(host.uptime_seconds / 86400);
    const hours = Math.floor((host.uptime_seconds % 86400) / 3600);
    document.getElementById('host-uptime').textContent = `${days} 天 ${hours} 小时`;
  }

  const ifaceTbody = document.getElementById('iface-tbody');
  if (!ifaceTbody) return;
  if (!host.interfaces || host.interfaces.length === 0) {
    ifaceTbody.innerHTML = '<tr><td colspan="6" class="empty-state">未获取到网络接口</td></tr>';
    return;
  }

  let html = '';
  for (const iface of host.interfaces) {
    const ips = (iface.ipv4 || []).join(', ') || '';
    const status = iface.is_up ? '<span class="status-badge active">活跃</span>' : '<span class="status-badge paused">未激活</span>';
    html += `<tr>
      <td><code>${escapeHtml(iface.name)}</code></td>
      <td>${escapeHtml(iface.type_label || iface.type)}</td>
      <td>${iface.mac ? `<code>${escapeHtml(iface.mac)}</code>` : ''}</td>
      <td>${ips ? `<code>${escapeHtml(ips)}</code>` : ''}</td>
      <td>${status}</td>
      <td class="filler-col"></td>
    </tr>`;
  }
  ifaceTbody.innerHTML = html;
}

// --- Sink Rules Management ---
const DEFAULT_SINK_RULES = [
  'zerotier',
  'tailscale',
  'cloudflared',
  'frpc',
  'frps',
  'wireguard',
  'wg-easy',
  'easytier',
  'headscale',
  'nps',
  'npc'
];

function getSinkRules() {
  const stored = localStorage.getItem('fn_docker_sink_rules');
  if (stored !== null) {
    try {
      const parsed = JSON.parse(stored);
      if (Array.isArray(parsed)) {
        return parsed.map(s => String(s).trim().toLowerCase()).filter(Boolean);
      }
    } catch (_) {}
  }
  return [...DEFAULT_SINK_RULES];
}

function saveSinkRules(rules) {
  localStorage.setItem('fn_docker_sink_rules', JSON.stringify(rules));
}

function matchSinkRule(targetStr, rule) {
  if (!targetStr || !rule) return false;
  // If rule is short (<= 4 chars, e.g. nps, npc, frpc), require word/punctuation boundary
  // so that "synps", "jsonps", etc. won't be falsely matched
  if (rule.length <= 4) {
    const escaped = rule.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const regex = new RegExp(`(^|[^a-z0-9])${escaped}([^a-z0-9]|$)`, 'i');
    return regex.test(targetStr);
  }
  // For longer distinctive names (>= 5 chars, e.g. tailscale, zerotier, cloudflared), substring match is safe
  return targetStr.includes(rule);
}

function renderPortRowHtml(p) {
  const portUrl = getHostTargetUrl(p.local_port, 'http', '/');
  const isDocker = p.docker && p.docker.is_docker;
  const procDisplayName = isDocker ? p.docker.container_name : (p.process_name || '系统服务');
  const procTag = isDocker
    ? `<span class="proc-tag tag-docker" title="Docker 容器: ${escapeHtml(p.docker.image || procDisplayName)}">${escapeHtml(procDisplayName)}</span>`
    : `<span class="proc-tag tag-host" title="系统进程: ${escapeHtml(p.exe || procDisplayName)}">${escapeHtml(procDisplayName)}</span>`;

  const matchingItems = (state.desktopItems || []).filter(item => item.port === p.local_port);
  const count = matchingItems.length || p.desktop_count || (p.has_desktop ? 1 : 0);

  let desktopCell = '';
  if (count > 0) {
    desktopCell = `
      <div class="desktop-btn-group">
        <button class="btn btn-sm btn-success btn-manage-desktop-port" data-port="${p.local_port}" data-count="${count}" title="点击查看或编辑已创建的桌面图标">
          <span class="btn-text">已配置</span><span class="btn-badge">${count}</span>
        </button>
        <button class="btn btn-sm btn-outline-primary btn-add-another-desktop" data-port="${p.local_port}" data-name="${escapeHtml(procDisplayName)}" data-container="${escapeHtml(isDocker ? p.docker.container_name : '')}" data-image="${escapeHtml(isDocker && p.docker.image ? p.docker.image : '')}" title="为此端口添加另一个不同路径或名称的桌面图标">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"></line><line x1="5" y1="12" x2="19" y2="12"></line></svg>
        </button>
      </div>`;
  } else {
    desktopCell = `
      <button class="btn btn-sm btn-primary btn-add-port-to-desktop" data-port="${p.local_port}" data-name="${escapeHtml(procDisplayName)}" data-container="${escapeHtml(isDocker ? p.docker.container_name : '')}" data-image="${escapeHtml(isDocker && p.docker.image ? p.docker.image : '')}">
        <span>放到桌面</span>
      </button>`;
  }

  const cpuText = p.cpu_percent > 0.1 ? `${p.cpu_percent.toFixed(1)}%` : '';
  const rssText = p.mem_rss_bytes > 0 ? formatBytes(p.mem_rss_bytes) : '';
  let resText = '';
  if (cpuText && rssText) {
    resText = `${cpuText} / ${rssText}`;
  } else if (cpuText) {
    resText = cpuText;
  } else if (rssText) {
    resText = rssText;
  }

  const protoUpper = (p.protocol || 'TCP').toUpperCase();
  const isPureUdp = protoUpper === 'UDP';
  const protoClass = isPureUdp ? 'text-proto-udp' : 'text-proto-tcp';
  const portClass = isPureUdp ? 'port-link-udp' : 'port-link-tcp';

  let protoPrefix = protoUpper;
  if (protoUpper.includes('TCP') && protoUpper.includes('UDP')) {
    protoPrefix = 'TCP/UDP';
  } else if (protoUpper.includes('UDP')) {
    protoPrefix = 'UDP';
  } else {
    protoPrefix = 'TCP';
  }
  const portTitle = `（${protoPrefix}）在浏览器新窗口打开 ${portUrl}`;

  return `<tr>
    <td>
      <a href="${portUrl}" target="_blank" rel="noopener noreferrer" class="port-link ${portClass}" title="${portTitle}">
        <span>${p.local_port}</span>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg>
      </a>
    </td>
    <td class="col-hide-minimal">
      <span class="${protoClass}">${escapeHtml(p.protocol)}</span>
    </td>
    <td class="col-hide-minimal"><code>${escapeHtml(p.local_ip || '0.0.0.0')}</code></td>
    <td>${procTag}</td>
    <td class="col-hide-minimal" style="font-variant-numeric: tabular-nums;">${resText}</td>
    <td>${desktopCell}</td>
    <td>
      <div class="table-actions">
        <button class="btn btn-sm btn-secondary btn-view-port-detail" data-port="${p.local_port}">
          <span>详情</span>
        </button>
      </div>
    </td>
    <td class="filler-col"></td>
  </tr>`;
}

// --- Table Layout Auto-Wrapping ---
function adjustTableWrapping(table) {
  if (!table) return;
  const container = table.closest('.table-container');
  if (!container || container.clientWidth <= 0) return;

  table.classList.remove('table-wrap');
  if (table.scrollWidth > container.clientWidth) {
    table.classList.add('table-wrap');
  }
}

function adjustAllTableWrapping() {
  document.querySelectorAll('.table-container table.data-table').forEach(table => {
    adjustTableWrapping(table);
  });
}

function initTableAutoWrapObservers() {
  if (typeof ResizeObserver === 'undefined') {
    window.addEventListener('resize', adjustAllTableWrapping);
    return;
  }
  const ro = new ResizeObserver((entries) => {
    for (const entry of entries) {
      const table = entry.target.querySelector('table.data-table');
      if (table) {
        adjustTableWrapping(table);
      }
    }
  });
  document.querySelectorAll('.table-container').forEach(container => {
    ro.observe(container);
  });
  window.addEventListener('resize', adjustAllTableWrapping);
}

// --- Render Ports Table ---
function renderPortsTable() {
  const tbody = document.getElementById('ports-tbody');
  if (!tbody) return;

  const query = state.portSearch.trim().toLowerCase();

  const filtered = state.ports.filter(p => {
    // Search matching
    if (query) {
      const matchPort = String(p.local_port).includes(query);
      const matchProc = (p.process_name || '').toLowerCase().includes(query);
      const matchDocker = (p.docker && p.docker.container_name || '').toLowerCase().includes(query);
      const matchExe = (p.exe || '').toLowerCase().includes(query);
      if (!matchPort && !matchProc && !matchDocker && !matchExe) return false;
    }

    // Dropdown 1: Source filter ('all' | 'docker' | 'host')
    const isDocker = !!(p.docker && p.docker.is_docker);
    if (state.portFilterSource === 'docker' && !isDocker) return false;
    if (state.portFilterSource === 'host' && isDocker) return false;

    // Dropdown 2: Protocol filter ('all' | 'tcp' | 'udp')
    const protoStr = (p.protocol || '').toLowerCase();
    const isTcp = (p.protocols || []).some(x => x.toLowerCase().includes('tcp')) || protoStr.includes('tcp');
    const isUdp = (p.protocols || []).some(x => x.toLowerCase().includes('udp')) || protoStr.includes('udp');
    if (state.portFilterProto === 'tcp' && !isTcp) return false;
    if (state.portFilterProto === 'udp' && !isUdp) return false;

    return true;
  });

  if (filtered.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty-state">没有符合条件的端口</td></tr>';
    return;
  }

  // Split into normal items and sink items
  const sinkRules = getSinkRules();
  const isSinkItem = (p) => {
    if (!sinkRules || !sinkRules.length) return false;
    const containerName = (p.docker && p.docker.container_name ? p.docker.container_name : '').toLowerCase();
    const imageName = (p.docker && p.docker.image ? p.docker.image : '').toLowerCase();
    const procName = (p.process_name || '').toLowerCase();
    const exeName = (p.exe || '').toLowerCase();
    return sinkRules.some(r =>
      matchSinkRule(containerName, r) ||
      matchSinkRule(imageName, r) ||
      matchSinkRule(procName, r) ||
      matchSinkRule(exeName, r)
    );
  };

  const normalItems = [];
  const sinkItems = [];
  for (const p of filtered) {
    if (isSinkItem(p)) {
      sinkItems.push(p);
    } else {
      normalItems.push(p);
    }
  }

  let html = '';
  for (const p of normalItems) {
    html += renderPortRowHtml(p);
  }

  if (sinkItems.length > 0) {
    if (normalItems.length > 0) {
      html += `
        <tr class="table-sink-divider-row" aria-hidden="true">
          <td colspan="8" class="table-sink-divider-cell">
            <span class="table-sink-title">置底</span>
          </td>
        </tr>`;
    }
    for (const p of sinkItems) {
      html += renderPortRowHtml(p);
    }
  }

  tbody.innerHTML = html;

  // Bind actions
  tbody.querySelectorAll('.btn-add-port-to-desktop').forEach(btn => {
    btn.addEventListener('click', () => {
      const port = parseInt(btn.dataset.port, 10);
      const name = btn.dataset.name || `端口-${port}`;
      const container = btn.dataset.container || '';
      const image = btn.dataset.image || '';
      openCreateDesktopModalWithPort(port, name, container, image);
    });
  });

  tbody.querySelectorAll('.btn-manage-desktop-port').forEach(btn => {
    btn.addEventListener('click', () => {
      const port = parseInt(btn.dataset.port, 10);
      const matching = (state.desktopItems || []).filter(i => i.port === port);
      if (matching.length === 1) {
        openEditDesktopModal(matching[0].id);
      } else if (matching.length > 1) {
        const portObj = state.ports.find(x => x.local_port === port);
        const name = portObj && portObj.docker && portObj.docker.is_docker
          ? portObj.docker.container_name
          : (portObj && portObj.process_name ? portObj.process_name : `端口-${port}`);
        openPortDesktopListModal(port, name, matching);
      } else {
        openCreateDesktopModalWithPort(port, `端口-${port}`);
      }
    });
  });

  tbody.querySelectorAll('.btn-add-another-desktop').forEach(btn => {
    btn.addEventListener('click', () => {
      const port = parseInt(btn.dataset.port, 10);
      const name = btn.dataset.name || `端口-${port}`;
      const container = btn.dataset.container || '';
      const image = btn.dataset.image || '';
      openCreateDesktopModalWithPort(port, name, container, image);
    });
  });

  tbody.querySelectorAll('.btn-view-port-detail').forEach(btn => {
    btn.addEventListener('click', () => {
      const port = parseInt(btn.dataset.port, 10);
      openPortDetailModal(port);
    });
  });

  adjustTableWrapping(document.getElementById('ports-table'));
}

// --- Render Desktop Items Table ---
function renderDesktopTable() {
  const tbody = document.getElementById('desktop-tbody');
  if (!tbody) return;

  if (!state.desktopItemsLoaded && !state.watchcowItemsLoaded) {
    tbody.innerHTML = '<tr><td colspan="7" class="empty-state"><span class="spinner-small" style="margin-right: 8px;"></span>正在载入桌面图标...</td></tr>';
    return;
  }

  const query = state.desktopSearch.trim().toLowerCase();
  const filtered = state.desktopItems.filter(item => {
    if (!query) return true;
    return (item.name || '').toLowerCase().includes(query) ||
      (item.target_url || '').toLowerCase().includes(query) ||
      (item.container_name || '').toLowerCase().includes(query) ||
      String(item.port || '').includes(query);
  });

  const filteredWatchcow = (state.watchcowItems || []).filter(item => {
    if (!query) return true;
    return (item.name || '').toLowerCase().includes(query) ||
      (item.container_name || '').toLowerCase().includes(query) ||
      String(item.port || '').includes(query);
  });

  let html = '';

  if (!state.desktopItemsLoaded) {
    html += '<tr><td colspan="7" class="empty-state" style="padding: 1.5rem 1rem; color: var(--text-muted);"><span class="spinner-small" style="margin-right: 8px;"></span>正在载入手动桌面图标...</td></tr>';
  } else if (filtered.length === 0) {
    if (state.watchcowItemsLoaded && filteredWatchcow.length === 0) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-state">' + (query ? '未找到匹配的桌面图标' : '暂无已创建的桌面图标') + '</td></tr>';
      return;
    }
    html += '<tr><td colspan="7" class="empty-state" style="padding: 1.5rem 1rem;">暂无手动添加的桌面图标</td></tr>';
  } else {
    for (const item of filtered) {
      const pathSuffix = (item.path && item.path !== '/') ? item.path : '';
      let modeText = '本机端口';
      let targetText = `:${item.port}${pathSuffix}`;
      if (item.mode === 'proxy') {
        modeText = '端口映射';
        targetText = `${item.target_url}${pathSuffix} ➔ :${item.port}`;
      } else if (item.mode === 'shortcut') {
        modeText = '网页链接';
        targetText = item.target_url;
      }

      const openModeText = item.ui_type === 'iframe' ? '内部弹窗' : '新标签页';
      const openModeClass = item.ui_type === 'iframe' ? 'type-sub-iframe' : 'type-sub-tab';
      const typeColHtml = `
        <div>
          <div style="font-size: 0.85rem; color: var(--text-main); font-weight: 500;">${escapeHtml(modeText)}</div>
          <div class="${openModeClass}" style="font-size: 0.85rem; margin-top: 2px;">${escapeHtml(openModeText)}</div>
        </div>`;

      const hasIcon = !item.no_display;
      const hasContextMenu = Array.isArray(item.file_types) && item.file_types.length > 0;
      let entryText = '图标';
      if (hasIcon && hasContextMenu) {
        entryText = '图标/右键';
      } else if (hasIcon && !hasContextMenu) {
        entryText = '图标';
      } else if (!hasIcon && hasContextMenu) {
        entryText = '右键';
      } else {
        entryText = '';
      }
      const permText = item.all_users ? '所有用户' : '仅管理员';
      const permClass = item.all_users ? 'perm-sub-all' : 'perm-sub-admin';
      const entryColHtml = `
        <div>
          <div style="font-size: 0.85rem; color: var(--text-main); font-weight: 500;">${entryText}</div>
          <div class="${permClass}" style="font-size: 0.85rem; margin-top: 2px;">${permText}</div>
        </div>`;

      const toggleHtml = `
        <div class="status-toggle-wrapper">
          <label class="toggle-switch" title="${item.enabled ? '点击停用' : '点击启用'}">
            <input type="checkbox" class="desktop-toggle-checkbox" data-id="${item.id}" ${item.enabled ? 'checked' : ''}>
            <span class="toggle-slider"></span>
          </label>
        </div>`;

      const iconSrc = getIconUrl(item.icon);

      let statusColHtml = toggleHtml;
      const isReconciling = !!item.reconciling;
      if (isReconciling || item._updating) {
        const statusText = item._statusText || item.status_text || (isReconciling ? '恢复中...' : '更新中...');
        statusColHtml = `
          <div class="status-updating-badge">
            <span class="spinner-small"></span>
            <span>${escapeHtml(statusText)}</span>
          </div>`;
      } else if (item._error) {
        statusColHtml = `
          <div class="status-error-badge" style="display: inline-flex; align-items: center; gap: 4px; color: #ef4444; font-size: 0.82rem; font-weight: 500;" title="${escapeHtml(item._statusText || '')}">
            <span>⚠️</span>
            <span>${escapeHtml(item._statusText || '操作失败')}</span>
          </div>`;
      }

      const isUpdating = !!item._updating || isReconciling;

      const containerHint = item.container_name
        ? `<div style="font-size: 0.76rem; color: var(--text-muted); font-weight: normal; margin-top: 2px;">${escapeHtml(item.container_name)}</div>`
        : '';

      html += `
        <tr data-id="${item.id}" class="${isUpdating ? 'row-updating' : ''}">
          <td>
            <div class="name-with-icon">
              <img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/default_item_icon.png')}'" alt="图标">
              <div class="name-with-icon-text">
                <div style="font-weight: 600; color: var(--text-main); font-size: 0.92rem;">
                  ${escapeHtml(item.name)}
                </div>
                ${containerHint}
              </div>
            </div>
          </td>
          <td>${typeColHtml}</td>
          <td>${entryColHtml}</td>
          <td><code>${escapeHtml(targetText)}</code></td>
          <td>${statusColHtml}</td>
          <td>
            <div class="table-actions">
              <button class="btn btn-sm btn-secondary btn-edit-desktop" data-id="${item.id}" ${isUpdating ? 'disabled' : ''}>
                <span>编辑</span>
              </button>
            </div>
          </td>
          <td class="filler-col"></td>
        </tr>`;
    }
  }

  if (!state.watchcowItemsLoaded) {
    html += `
      <tr class="table-sink-divider-row" aria-hidden="true">
        <td colspan="7" class="table-sink-divider-cell">
          <span class="table-sink-title">以下图标读取自 Compose 中的 Watchcow 标签；如需修改，请编辑 Compose 配置后刷新</span>
        </td>
      </tr>
      <tr>
        <td colspan="7" class="empty-state" style="padding: 1.5rem 1rem; color: var(--text-muted);">
          <span class="spinner-small" style="margin-right: 8px;"></span>正在扫描 Watchcow 标签条目...
        </td>
      </tr>`;
  } else if (filteredWatchcow.length > 0) {
    html += `
      <tr class="table-sink-divider-row" aria-hidden="true">
        <td colspan="7" class="table-sink-divider-cell">
          <span class="table-sink-title">以下图标读取自 Compose 中的 Watchcow 标签；如需修改，请编辑 Compose 配置后刷新</span>
        </td>
      </tr>`;

    for (const item of filteredWatchcow) {
      const iconSrc = getIconUrl(item.display_icon || item.icon);
      let modeText = '本机端口';
      let targetText = `:${item.port}${item.path && item.path !== '/' ? item.path : ''}`;
      if (item.mode === 'shortcut') {
        modeText = '网页链接';
        targetText = item.target_url || item.redirect || `:${item.port}`;
      } else if (item.mode === 'proxy') {
        modeText = '端口映射';
        targetText = `${item.target_url} ➔ :${item.port}`;
      }

      const openModeText = item.ui_type === 'iframe' ? '内部弹窗' : '新标签页';
      const openModeClass = item.ui_type === 'iframe' ? 'type-sub-iframe' : 'type-sub-tab';
      const typeColHtml = `
        <div>
          <div style="font-size: 0.85rem; color: var(--text-main); font-weight: 500;">${escapeHtml(modeText)}</div>
          <div class="${openModeClass}" style="font-size: 0.85rem; margin-top: 2px;">${escapeHtml(openModeText)}</div>
        </div>`;

      const hasIcon = !item.no_display;
      const hasContextMenu = Array.isArray(item.file_types) && item.file_types.length > 0;
      let entryText = '图标';
      if (hasIcon && hasContextMenu) {
        entryText = '图标/右键';
      } else if (hasIcon && !hasContextMenu) {
        entryText = '图标';
      } else if (!hasIcon && hasContextMenu) {
        entryText = '右键';
      } else {
        entryText = '';
      }
      const permText = item.all_users ? '所有用户' : '仅管理员';
      const permClass = item.all_users ? 'perm-sub-all' : 'perm-sub-admin';
      const entryColHtml = `
        <div>
          <div style="font-size: 0.85rem; color: var(--text-main); font-weight: 500;">${entryText}</div>
          <div class="${permClass}" style="font-size: 0.85rem; margin-top: 2px;">${permText}</div>
        </div>`;

      const containerHint = item.container_name
        ? `<div style="font-size: 0.76rem; color: var(--text-muted); font-weight: normal; margin-top: 2px;">${escapeHtml(item.container_name)}</div>`
        : '';

      const toggleHtml = `
        <div class="status-toggle-wrapper">
          <label class="toggle-switch" title="${item.enabled ? '点击停用' : '点击启用'}">
            <input type="checkbox" class="watchcow-toggle-checkbox" data-id="${escapeHtml(item.id)}" ${item.enabled ? 'checked' : ''}>
            <span class="toggle-slider"></span>
          </label>
        </div>`;

      let statusColHtml = toggleHtml;
      const isReconciling = !!item.reconciling;
      if (isReconciling || item._updating) {
        const statusText = item._statusText || item.status_text || (isReconciling ? '恢复中...' : '更新中...');
        statusColHtml = `
          <div class="status-updating-badge">
            <span class="spinner-small"></span>
            <span>${escapeHtml(statusText)}</span>
          </div>`;
      } else if (item._error) {
        statusColHtml = `
          <div class="status-error-badge" style="display: inline-flex; align-items: center; gap: 4px; color: #ef4444; font-size: 0.82rem; font-weight: 500;" title="${escapeHtml(item._statusText || '')}">
            <span>⚠️</span>
            <span>${escapeHtml(item._statusText || '操作失败')}</span>
          </div>`;
      }

      const isUpdating = !!item._updating || isReconciling;

      html += `<tr class="${isUpdating ? 'row-updating' : ''}">
        <td>
          <div class="name-with-icon">
            <img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/default_item_icon.png')}'" alt="图标">
            <div class="name-with-icon-text">
              <strong>${escapeHtml(item.name)}</strong>
              ${containerHint}
            </div>
          </div>
        </td>
        <td>${typeColHtml}</td>
        <td>${entryColHtml}</td>
        <td><code>${escapeHtml(targetText)}</code></td>
        <td>${statusColHtml}</td>
        <td>
          <div class="table-actions">
            <button class="btn btn-sm btn-secondary btn-copy-watchcow" data-id="${escapeHtml(item.id)}" title="基于此配置新建桌面图标">
              <span>复制</span>
            </button>
          </div>
        </td>
        <td class="filler-col"></td>
      </tr>`;
    }
  }

  tbody.innerHTML = html;

  tbody.querySelectorAll('.desktop-toggle-checkbox').forEach(chk => {
    chk.addEventListener('change', async () => {
      const id = chk.dataset.id;
      if (chk.disabled) return;

      const item = state.desktopItems.find(i => i.id === id);
      if (item) {
        item._updating = true;
        item._statusText = chk.checked ? '启用中...' : '停用中...';
        item._error = false;
        renderDesktopTable();
      }

      reportClientLog('action', '用户切换桌面图标状态', `ID: ${id}, 目标状态: ${chk.checked ? '启用' : '停用'}`, { id, checked: chk.checked });

      try {
        const res = await fetch(apiUrl(`/api/desktop/items/${id}/toggle`), { method: 'POST' });
        if (res.ok) {
          const updated = await res.json();
          const targetItem = state.desktopItems.find(i => i.id === id);
          if (targetItem) {
            targetItem.enabled = updated.enabled;
            targetItem._updating = false;
            targetItem._statusText = null;
          }
          showToast(`已成功${updated.enabled ? '启用' : '停用'}桌面图标`, 'success');
          renderDesktopTable();
          fetchPorts();
        } else {
          const errData = await res.json().catch(() => ({}));
          const errMsg = errData.error || res.statusText;
          showToast('切换状态失败: ' + errMsg, 'error', 5000);
          const targetItem = state.desktopItems.find(i => i.id === id);
          if (targetItem) {
            targetItem._updating = false;
            targetItem._error = true;
            targetItem._statusText = errMsg;
          }
          renderDesktopTable();
        }
      } catch (e) {
        showToast('网络请求异常: ' + e.message, 'error', 5000);
        const targetItem = state.desktopItems.find(i => i.id === id);
        if (targetItem) {
          targetItem._updating = false;
          targetItem._error = true;
          targetItem._statusText = e.message;
        }
        renderDesktopTable();
      }
    });
  });

  tbody.querySelectorAll('.watchcow-toggle-checkbox').forEach(chk => {
    chk.addEventListener('change', async () => {
      const id = chk.dataset.id;
      if (chk.disabled) return;

      const item = (state.watchcowItems || []).find(i => i.id === id);
      if (item) {
        item._updating = true;
        item._statusText = chk.checked ? '启用中...' : '停用中...';
        item._error = false;
        renderDesktopTable();
      }

      reportClientLog('action', '用户切换容器标签条目状态', `ID: ${id}, 目标状态: ${chk.checked ? '启用' : '停用'}`, { id, checked: chk.checked });

      try {
        const res = await fetch(apiUrl(`/api/desktop/docklabel/${encodeURIComponent(id)}/toggle`), { method: 'POST' });
        if (res.ok) {
          const updated = await res.json();
          const targetItem = (state.watchcowItems || []).find(i => i.id === id);
          if (targetItem) {
            targetItem.enabled = updated.enabled;
            targetItem._updating = false;
            targetItem.reconciling = false;
            targetItem.status_text = null;
            targetItem._statusText = null;
          }
          showToast(`已成功${updated.enabled ? '启用' : '停用'}桌面图标`, 'success');
          renderDesktopTable();
          fetchPorts();
          await fetchWatchcowItems();
          scheduleReconcilePolling();
        } else {
          const errData = await res.json().catch(() => ({}));
          const errMsg = errData.error || res.statusText;
          showToast('切换状态失败: ' + errMsg, 'error', 5000);
          const targetItem = (state.watchcowItems || []).find(i => i.id === id);
          if (targetItem) {
            targetItem._updating = false;
            targetItem.reconciling = false;
            targetItem._error = true;
            targetItem._statusText = errMsg;
          }
          renderDesktopTable();
        }
      } catch (e) {
        showToast('网络请求异常: ' + e.message, 'error', 5000);
        const targetItem = (state.watchcowItems || []).find(i => i.id === id);
        if (targetItem) {
          targetItem._updating = false;
          targetItem.reconciling = false;
          targetItem._error = true;
          targetItem._statusText = e.message;
        }
        renderDesktopTable();
      }
    });
  });

  tbody.querySelectorAll('.btn-edit-desktop').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = btn.dataset.id;
      openEditDesktopModal(id);
    });
  });

  tbody.querySelectorAll('.btn-copy-watchcow').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = btn.dataset.id;
      openCreateDesktopModalFromWatchcow(id);
    });
  });

  adjustTableWrapping(document.getElementById('desktop-table'));
}

// --- Render Processes Table ---
function renderProcessesTable() {
  const tbody = document.getElementById('proc-tbody');
  if (!tbody) return;

  const query = state.procSearch.trim().toLowerCase();
  let list = [...state.processes];

  if (query) {
    list = list.filter(p => {
      return String(p.pid).includes(query) ||
        p.name.toLowerCase().includes(query) ||
        p.user.toLowerCase().includes(query);
    });
  }

  // Update header indicators
  document.querySelectorAll('#proc-table thead .sortable-th').forEach(th => {
    const isCur = th.dataset.sort === state.procSort;
    th.classList.toggle('sort-active', isCur);
    const icon = th.querySelector('.sort-icon');
    if (icon) {
      icon.textContent = isCur ? (state.procSortDir === 'asc' ? ' ▲' : ' ▼') : '';
    }
  });

  list.sort((a, b) => {
    let cmp = 0;
    switch (state.procSort) {
      case 'pid':
        cmp = a.pid - b.pid;
        break;
      case 'name':
        cmp = (a.name || '').localeCompare(b.name || '');
        break;
      case 'user':
        cmp = (a.user || '').localeCompare(b.user || '');
        break;
      case 'state':
        cmp = (a.state || '').localeCompare(b.state || '');
        break;
      case 'cpu':
        cmp = a.cpu_percent - b.cpu_percent;
        break;
      case 'mem':
        cmp = a.mem_rss_bytes - b.mem_rss_bytes;
        break;
      case 'io':
        cmp = (a.io_read_rate + a.io_write_rate) - (b.io_read_rate + b.io_write_rate);
        break;
      case 'net':
        cmp = ((a.net_rx_rate || 0) + (a.net_tx_rate || 0)) - ((b.net_rx_rate || 0) + (b.net_tx_rate || 0));
        break;
      case 'docker': {
        const d1 = (a.docker && a.docker.container_name) || '';
        const d2 = (b.docker && b.docker.container_name) || '';
        cmp = d1.localeCompare(d2);
        break;
      }
      default:
        cmp = a.cpu_percent - b.cpu_percent;
    }
    return state.procSortDir === 'desc' ? -cmp : cmp;
  });

  if (list.length === 0) {
    tbody.innerHTML = '<tr><td colspan="10" class="empty-state">没有符合条件的进程</td></tr>';
    return;
  }

  let html = '';
  for (const p of list.slice(0, 150)) { // Limit to top 150 for peak DOM performance
    const hasDocker = !!(p.docker && p.docker.is_docker && p.docker.container_name);
    const dockerTag = hasDocker
      ? `<span class="proc-container-name" title="Docker 容器: ${escapeHtml(p.docker.image || p.docker.container_name)}">${escapeHtml(p.docker.container_name)}</span>`
      : '';
    html += `<tr>
      <td><code>${p.pid}</code></td>
      <td><strong>${escapeHtml(p.name)}</strong></td>
      <td>${escapeHtml(p.user || 'root')}</td>
      <td>${escapeHtml(p.state)}</td>
      <td style="font-variant-numeric: tabular-nums;">${p.cpu_percent.toFixed(1)}%</td>
      <td style="font-variant-numeric: tabular-nums;">${formatBytes(p.mem_rss_bytes)}</td>
      <td style="font-variant-numeric: tabular-nums;">${formatRate(p.io_read_rate + p.io_write_rate)}</td>
      <td style="font-variant-numeric: tabular-nums; white-space: nowrap;">
        <span title="下载网速">↓ ${formatRate(p.net_rx_rate || 0)}</span>
        <span title="上传网速" style="margin-left: 6px;">↑ ${formatRate(p.net_tx_rate || 0)}</span>
      </td>
      <td>${dockerTag}</td>
      <td class="filler-col"></td>
    </tr>`;
  }
  tbody.innerHTML = html;
  adjustTableWrapping(document.getElementById('proc-table'));
}

function initProcTableSort() {
  document.querySelectorAll('#proc-table thead .sortable-th').forEach(th => {
    th.addEventListener('click', () => {
      const field = th.dataset.sort;
      if (state.procSort === field) {
        state.procSortDir = state.procSortDir === 'asc' ? 'desc' : 'asc';
      } else {
        state.procSort = field;
        state.procSortDir = (field === 'pid' || field === 'name' || field === 'user' || field === 'state' || field === 'docker') ? 'asc' : 'desc';
      }
      renderProcessesTable();
    });
  });
}

// --- Modals & Desktop Item Forms ---
function openModal(id) {
  const el = document.getElementById(id);
  if (el) el.classList.add('active');
}

function closeModal(id) {
  const el = document.getElementById(id);
  if (el) el.classList.remove('active');
}

function getDesktopItemFormSnapshot() {
  return JSON.stringify({
    mode: document.getElementById('item-mode')?.value || '',
    name: document.getElementById('item-name')?.value || '',
    appName: document.getElementById('item-app-name')?.value || '',
    containerName: document.getElementById('item-container-name')?.value || '',
    localPort: document.getElementById('item-local-port')?.value || '',
    targetUrl: document.getElementById('item-target-url')?.value || '',
    proxyPort: document.getElementById('item-proxy-port')?.value || '',
    skipTls: !!document.getElementById('item-skip-tls')?.checked,
    shortcutUrl: document.getElementById('item-shortcut-url')?.value || '',
    protocol: document.getElementById('item-protocol')?.value || 'http',
    path: document.getElementById('item-path')?.value || '/',
    uiType: document.getElementById('item-ui-type')?.value || 'url',
    allUsers: document.getElementById('item-all-users')?.value || 'false',
    noticeContent: document.getElementById('item-notice-content')?.value || '',
    fileTypes: document.getElementById('item-file-types')?.value || '',
    noDisplay: !!document.getElementById('item-no-display')?.checked,
    iconVal: document.getElementById('item-icon')?.value || ''
  });
}

function saveDesktopItemFormSnapshot() {
  state.desktopItemFormSnapshot = getDesktopItemFormSnapshot();
}

function isDesktopItemFormDirty() {
  const modal = document.getElementById('modal-desktop-item');
  if (!modal || !modal.classList.contains('active')) return false;

  const isIconPickerOpen = document.getElementById('modal-icon-picker-expanded')?.style.display !== 'none';
  if (isIconPickerOpen && isIconModified('modal')) {
    console.log('[DIRTY-CHECK] Icon modified in expanded picker');
    return true;
  }

  if (!state.desktopItemFormSnapshot) return false;
  let snap = {};
  let cur = {};
  try {
    snap = JSON.parse(state.desktopItemFormSnapshot || '{}');
    cur = JSON.parse(getDesktopItemFormSnapshot() || '{}');
  } catch (_) {
    return false;
  }

  // Check common fields against snapshot
  if ((cur.name || '').trim() !== (snap.name || '').trim()) {
    console.log('[DIRTY-CHECK] name modified:', snap.name, '->', cur.name);
    return true;
  }
  if ((cur.appName || '').trim() !== (snap.appName || '').trim()) {
    console.log('[DIRTY-CHECK] appName modified:', snap.appName, '->', cur.appName);
    return true;
  }
  if ((cur.containerName || '').trim() !== (snap.containerName || '').trim()) {
    console.log('[DIRTY-CHECK] containerName modified:', snap.containerName, '->', cur.containerName);
    return true;
  }
  if (String(cur.allUsers) !== String(snap.allUsers)) {
    console.log('[DIRTY-CHECK] allUsers modified:', snap.allUsers, '->', cur.allUsers);
    return true;
  }
  if ((cur.noticeContent || '').trim() !== (snap.noticeContent || '').trim()) {
    console.log('[DIRTY-CHECK] noticeContent modified:', snap.noticeContent, '->', cur.noticeContent);
    return true;
  }
  if (!!cur.noDisplay !== !!snap.noDisplay) {
    console.log('[DIRTY-CHECK] noDisplay modified:', snap.noDisplay, '->', cur.noDisplay);
    return true;
  }
  if ((cur.iconVal || '').trim() !== (snap.iconVal || '').trim()) {
    console.log('[DIRTY-CHECK] iconVal modified:', snap.iconVal, '->', cur.iconVal);
    return true;
  }

  // Mode check:
  if (cur.mode !== snap.mode) {
    // User switched mode tab. Check if user actually entered/modified content for the new mode!
    if (cur.mode === 'local') {
      const p = (cur.localPort || '').trim();
      if (p && p !== (snap.localPort || '').trim()) {
        console.log('[DIRTY-CHECK] localPort dirty in switched mode:', snap.localPort, '->', p);
        return true;
      }
    } else if (cur.mode === 'proxy') {
      const u = (cur.targetUrl || '').trim();
      const p = (cur.proxyPort || '').trim();
      if (u && u !== (snap.targetUrl || '').trim()) {
        console.log('[DIRTY-CHECK] targetUrl dirty in switched mode:', snap.targetUrl, '->', u);
        return true;
      }
      if (p && p !== (snap.proxyPort || '').trim()) {
        console.log('[DIRTY-CHECK] proxyPort dirty in switched mode:', snap.proxyPort, '->', p);
        return true;
      }
    } else if (cur.mode === 'shortcut') {
      const s = (cur.shortcutUrl || '').trim();
      if (s && s !== (snap.shortcutUrl || '').trim()) {
        console.log('[DIRTY-CHECK] shortcutUrl dirty in switched mode:', snap.shortcutUrl, '->', s);
        return true;
      }
    }
    // If no meaningful content entered for new mode, switching mode tab was just navigation!
    return false;
  }

  // cur.mode === snap.mode: check mode-specific fields
  if (cur.mode === 'local') {
    if ((cur.localPort || '').trim() !== (snap.localPort || '').trim()) {
      console.log('[DIRTY-CHECK] localPort modified:', snap.localPort, '->', cur.localPort);
      return true;
    }
    if (cur.protocol !== snap.protocol) {
      console.log('[DIRTY-CHECK] protocol modified:', snap.protocol, '->', cur.protocol);
      return true;
    }
    if ((cur.path || '').trim() !== (snap.path || '').trim()) {
      console.log('[DIRTY-CHECK] path modified:', snap.path, '->', cur.path);
      return true;
    }
    if (cur.uiType !== snap.uiType) {
      console.log('[DIRTY-CHECK] uiType modified:', snap.uiType, '->', cur.uiType);
      return true;
    }
    if ((cur.fileTypes || '').trim() !== (snap.fileTypes || '').trim()) {
      console.log('[DIRTY-CHECK] fileTypes modified:', snap.fileTypes, '->', cur.fileTypes);
      return true;
    }
  } else if (cur.mode === 'proxy') {
    if ((cur.targetUrl || '').trim() !== (snap.targetUrl || '').trim()) {
      console.log('[DIRTY-CHECK] targetUrl modified:', snap.targetUrl, '->', cur.targetUrl);
      return true;
    }
    if ((cur.proxyPort || '').trim() !== (snap.proxyPort || '').trim()) {
      console.log('[DIRTY-CHECK] proxyPort modified:', snap.proxyPort, '->', cur.proxyPort);
      return true;
    }
    if (!!cur.skipTls !== !!snap.skipTls) {
      console.log('[DIRTY-CHECK] skipTls modified:', snap.skipTls, '->', cur.skipTls);
      return true;
    }
    if ((cur.path || '').trim() !== (snap.path || '').trim()) {
      console.log('[DIRTY-CHECK] path modified:', snap.path, '->', cur.path);
      return true;
    }
    if (cur.uiType !== snap.uiType) {
      console.log('[DIRTY-CHECK] uiType modified:', snap.uiType, '->', cur.uiType);
      return true;
    }
    if ((cur.fileTypes || '').trim() !== (snap.fileTypes || '').trim()) {
      console.log('[DIRTY-CHECK] fileTypes modified:', snap.fileTypes, '->', cur.fileTypes);
      return true;
    }
  } else if (cur.mode === 'shortcut') {
    if ((cur.shortcutUrl || '').trim() !== (snap.shortcutUrl || '').trim()) {
      console.log('[DIRTY-CHECK] shortcutUrl modified:', snap.shortcutUrl, '->', cur.shortcutUrl);
      return true;
    }
  }

  return false;
}

function tryCloseDesktopItemModal() {
  if (isDesktopItemFormDirty()) {
    const isIconPickerOpen = document.getElementById('modal-icon-picker-expanded')?.style.display !== 'none';
    const msg = (isIconPickerOpen && isIconModified('modal'))
      ? '当前图标已修改但尚未保存，确定要放弃修改并退出吗？'
      : '当前内容已修改但尚未保存，确定要放弃修改并退出吗？';
    if (!confirm(msg)) {
      return;
    }
  }
  state.desktopItemFormSnapshot = null;
  state.initialModalIcon = null;
  collapseIconPicker('modal');
  closeModal('modal-desktop-item');
}

function initModals() {
  document.querySelectorAll('.modal-close').forEach(btn => {
    btn.addEventListener('click', (e) => {
      const target = btn.dataset.close;
      if (target === 'modal-desktop-item') {
        e.preventDefault();
        tryCloseDesktopItemModal();
      } else if (target) {
        closeModal(target);
      }
    });
  });

  // Modal backdrop click outside dialog
  const modalDesktop = document.getElementById('modal-desktop-item');
  if (modalDesktop) {
    modalDesktop.addEventListener('click', (e) => {
      if (e.target === modalDesktop) {
        tryCloseDesktopItemModal();
      }
    });
  }

  // Global ESC key to close modal with dirty check
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      const elDesktop = document.getElementById('modal-desktop-item');
      if (elDesktop && elDesktop.classList.contains('active')) {
        tryCloseDesktopItemModal();
        return;
      }
      const elSink = document.getElementById('modal-sink-settings');
      if (elSink && elSink.classList.contains('active')) {
        const btnCancelSink = document.getElementById('btn-cancel-sink-modal');
        if (btnCancelSink) {
          btnCancelSink.click();
        } else {
          closeModal('modal-sink-settings');
        }
        return;
      }
    }
  });

  // File types help toggle
  const btnFileTypesHelp = document.getElementById('btn-file-types-help');
  const helpFileTypes = document.getElementById('item-file-types-help');
  if (btnFileTypesHelp && helpFileTypes) {
    btnFileTypesHelp.addEventListener('click', () => {
      helpFileTypes.style.display = helpFileTypes.style.display === 'none' ? 'block' : 'none';
    });
  }

  // Mode tabs in Desktop Item Modal
  document.querySelectorAll('.mode-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      setDesktopModalMode(btn.dataset.mode);
    });
  });

  // Open modal button
  const btnAdd = document.getElementById('btn-open-create-modal');
  if (btnAdd) {
    btnAdd.addEventListener('click', () => {
      resetDesktopForm();
      openModal('modal-desktop-item');
      saveDesktopItemFormSnapshot();
    });
  }

  const btnQuickAdd = document.getElementById('btn-quick-add-desktop');
  if (btnQuickAdd) {
    btnQuickAdd.addEventListener('click', () => {
      resetDesktopForm();
      openModal('modal-desktop-item');
      saveDesktopItemFormSnapshot();
    });
  }

  // Form submit
  const formItem = document.getElementById('form-desktop-item');
  if (formItem) {
    formItem.addEventListener('submit', handleSaveDesktopItem);
  }

  // Save as new button in edit modal
  const btnSaveAsNew = document.getElementById('btn-save-as-new');
  if (btnSaveAsNew) {
    btnSaveAsNew.addEventListener('click', () => {
      // Clear ID so handleSaveDesktopItem treats it as a brand new item
      document.getElementById('item-id').value = '';
      if (formItem) {
        if (formItem.requestSubmit) {
          formItem.requestSubmit();
        } else {
          formItem.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
        }
      }
    });
  }

  // Test target button
  const btnTest = document.getElementById('btn-test-target');
  if (btnTest) {
    btnTest.addEventListener('click', handleTestTarget);
  }

  // Recommend port button
  const btnRec = document.getElementById('btn-recommend-port');
  if (btnRec) {
    btnRec.addEventListener('click', handleRecommendPort);
  }

  // Dynamic package name synchronization with item name
  const elItemName = document.getElementById('item-name');
  if (elItemName) {
    elItemName.addEventListener('input', () => {
      if (!state.appNameDirty) {
        const val = elItemName.value.trim();
        const baseCandidate = (val || 'app').toLowerCase().replace(/[^a-z0-9]/g, '-').replace(/-+/g, '-').replace(/^-+|-+$/g, '').slice(0, 14);
        const elAppName = document.getElementById('item-app-name');
        if (elAppName) {
          const shortId = state.appShortId || Math.floor(100000 + Math.random() * 900000).toString();
          state.appShortId = shortId;
          elAppName.value = ('fndocker.' + (baseCandidate || 'app') + '-' + shortId).slice(0, 32);
          checkAppNameDuplicate();
        }
      }
    });
  }

  const elAppName = document.getElementById('item-app-name');
  if (elAppName) {
    elAppName.addEventListener('input', () => {
      state.appNameDirty = true;
      checkAppNameDuplicate();
    });
  }

  // Settings listeners (dirty tracking and manual Save/Cancel)
  const elPortalName = document.getElementById('setting-portal-name');
  if (elPortalName) {
    elPortalName.addEventListener('input', checkSettingsDirty);
  }
  document.querySelectorAll('input[name="setting-portal-all-users"]').forEach(r => {
    r.addEventListener('change', checkSettingsDirty);
  });
  const elPwd = document.getElementById('setting-portal-password');
  const elPwdConfirm = document.getElementById('setting-portal-password-confirm');
  if (elPwd) {
    elPwd.addEventListener('input', checkSettingsDirty);
  }
  if (elPwdConfirm) {
    elPwdConfirm.addEventListener('input', checkSettingsDirty);
  }

  const btnCancelSettings = document.getElementById('btn-cancel-settings');
  if (btnCancelSettings) {
    btnCancelSettings.addEventListener('click', handleCancelSettings);
  }

  const btnSaveSettings = document.getElementById('btn-save-settings');
  if (btnSaveSettings) {
    btnSaveSettings.addEventListener('click', handleSaveSettingsManual);
  }

  // Icon 3-Tab Editor Initialization
  initIconEditor();
  initSettingIconEditor();

  // Auth form
  const formAuth = document.getElementById('form-auth');
  if (formAuth) {
    formAuth.addEventListener('submit', handleAuthLogin);
  }
}

function checkAppNameDuplicate() {
  const elAppName = document.getElementById('item-app-name');
  const tipEl = document.getElementById('item-app-name-duplicate-tip');
  if (!elAppName || !tipEl) return null;

  const currentId = (document.getElementById('item-id') ? document.getElementById('item-id').value.trim() : '');
  const val = elAppName.value.trim();
  if (!val) {
    tipEl.style.display = 'none';
    tipEl.textContent = '';
    elAppName.style.borderColor = '';
    return null;
  }

  const conflict = (state.desktopItems || []).find(item => item.id !== currentId && item.app_name === val);
  if (conflict) {
    tipEl.style.display = 'block';
    tipEl.textContent = `⚠️ 该应用包名已被桌面图标「${conflict.name}」占用，保存时将产生冲突，请修改！`;
    elAppName.style.borderColor = 'var(--danger, #ef4444)';
    return conflict;
  } else {
    tipEl.style.display = 'none';
    tipEl.textContent = '';
    elAppName.style.borderColor = '';
    return null;
  }
}

function setDesktopModalMode(mode) {
  state.activeMode = mode;
  document.getElementById('item-mode').value = mode;

  document.querySelectorAll('.mode-tab').forEach(b => {
    b.classList.toggle('active', b.dataset.mode === mode);
  });

  document.getElementById('fields-local').style.display = mode === 'local' ? 'block' : 'none';
  document.getElementById('fields-proxy').style.display = mode === 'proxy' ? 'block' : 'none';
  document.getElementById('fields-shortcut').style.display = mode === 'shortcut' ? 'block' : 'none';

  const rowProtoPath = document.getElementById('row-protocol-path');
  if (rowProtoPath) {
    rowProtoPath.style.display = mode === 'shortcut' ? 'none' : 'flex';
  }
  const groupUiType = document.getElementById('group-ui-type');
  if (groupUiType) {
    groupUiType.style.display = mode === 'shortcut' ? 'none' : 'block';
  }
  const groupFileTypes = document.getElementById('form-group-file-types');
  if (groupFileTypes) {
    groupFileTypes.style.display = mode === 'shortcut' ? 'none' : 'block';
  }
}

// --- Icon 3-Tab Editor ---
function setIconModalTab(tabName) {
  state.activeIconTab = tabName;
  document.querySelectorAll('#modal-icon-tabs .icon-tab').forEach(b => {
    b.classList.toggle('active', b.dataset.iconTab === tabName);
  });
  document.querySelectorAll('#modal-desktop-item .icon-tab-pane').forEach(p => {
    p.classList.toggle('active', p.id === `icon-pane-${tabName}`);
  });

  if (tabName === 'lib' || tabName === 'upload') {
    renderIconLibraryGrid('modal');
  }
}

function renderTextIconCanvas() {
  const textInput = document.getElementById('icon-text-input');
  const text = (textInput ? textInput.value : '').trim();
  const textColor = document.getElementById('icon-text-color')?.value || '#ffffff';
  const bgColor = document.getElementById('icon-bg-color')?.value || '#1e293b';
  const previewImg = document.getElementById('icon-preview-img');

  if (!text) {
    if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
    state.currentTextIconDataUrl = null;
    return;
  }

  let canvas = document.getElementById('icon-text-canvas');
  if (!canvas) {
    canvas = document.createElement('canvas');
    canvas.id = 'icon-text-canvas';
    canvas.width = 256;
    canvas.height = 256;
    canvas.style.display = 'none';
    document.body.appendChild(canvas);
  }

  const ctx = canvas.getContext('2d');
  ctx.clearRect(0, 0, 256, 256);

  // Background squircle
  ctx.fillStyle = bgColor;
  const r = 50;
  ctx.beginPath();
  if (ctx.roundRect) {
    ctx.roundRect(0, 0, 256, 256, r);
  } else {
    ctx.moveTo(r, 0);
    ctx.lineTo(256 - r, 0);
    ctx.quadraticCurveTo(256, 0, 256, r);
    ctx.lineTo(256, 256 - r);
    ctx.quadraticCurveTo(256, 256, 256 - r, 256);
    ctx.lineTo(r, 256);
    ctx.quadraticCurveTo(0, 256, 0, 256 - r);
    ctx.lineTo(0, r);
    ctx.quadraticCurveTo(0, 0, r, 0);
    ctx.closePath();
  }
  ctx.fill();

  // Multi-line adaptive font sizing
  const rawLines = text.split('\n').map(l => l.trim()).filter(l => l.length > 0);
  const lines = rawLines.length > 0 ? rawLines : [text];

  const maxW = 200; // Padded inner width
  const maxH = 200; // Padded inner height

  let fontSize = 140;
  const fontFam = '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif';

  while (fontSize > 16) {
    ctx.font = `bold ${fontSize}px ${fontFam}`;
    const lineHeight = fontSize * 1.16;
    const totalH = (lines.length - 1) * lineHeight + fontSize;
    if (totalH <= maxH) {
      let allFit = true;
      for (const line of lines) {
        if (ctx.measureText(line).width > maxW) {
          allFit = false;
          break;
        }
      }
      if (allFit) break;
    }
    fontSize -= 2;
  }

  ctx.font = `bold ${fontSize}px ${fontFam}`;
  ctx.fillStyle = textColor;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';

  const lineHeight = fontSize * 1.16;
  const totalH = (lines.length - 1) * lineHeight;
  const startY = 128 - totalH / 2;

  lines.forEach((line, idx) => {
    ctx.fillText(line, 128, startY + idx * lineHeight);
  });

  const dataUrl = canvas.toDataURL('image/png');
  state.currentTextIconDataUrl = dataUrl;
  if (previewImg) previewImg.src = dataUrl;
}

async function uploadTextIconBlob() {
  const canvas = document.getElementById('icon-text-canvas');
  if (!canvas) return null;
  return new Promise((resolve) => {
    canvas.toBlob(async (blob) => {
      if (!blob) return resolve(null);
      try {
        const formData = new FormData();
        formData.append('icon', blob, `text-icon-${Date.now()}.png`);
        const res = await fetch(apiUrl('/api/icons/upload'), {
          method: 'POST',
          body: formData,
        });
        const data = await res.json();
        if (res.ok && data.url) {
          resolve(data.url);
        } else {
          resolve(null);
        }
      } catch (err) {
        console.error('Upload text icon error:', err);
        resolve(null);
      }
    }, 'image/png');
  });
}

// --- Color Math & Spectrum Picker Helpers ---
function hexToRgb(hex) {
  const norm = normalizeHexColor(hex);
  if (!norm) return [255, 255, 255];
  const num = parseInt(norm.slice(1), 16);
  return [(num >> 16) & 255, (num >> 8) & 255, num & 255];
}

function rgbToHex(r, g, b) {
  const toHex = (c) => Math.max(0, Math.min(255, Math.round(c))).toString(16).padStart(2, '0');
  return '#' + toHex(r) + toHex(g) + toHex(b);
}

function rgbToHsv(r, g, b) {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  const d = max - min;
  let h = 0;
  const s = max === 0 ? 0 : d / max;
  const v = max;

  if (max !== min) {
    switch (max) {
      case r: h = (g - b) / d + (g < b ? 6 : 0); break;
      case g: h = (b - r) / d + 2; break;
      case b: h = (r - g) / d + 4; break;
    }
    h *= 60;
  }
  return [h, s, v];
}

function hsvToRgb(h, s, v) {
  h = (h % 360 + 360) % 360;
  let r, g, b;
  const i = Math.floor(h / 60);
  const f = (h / 60) - i;
  const p = v * (1 - s);
  const q = v * (1 - f * s);
  const t = v * (1 - (1 - f) * s);

  switch (i) {
    case 0: r = v; g = t; b = p; break;
    case 1: r = q; g = v; b = p; break;
    case 2: r = p; g = v; b = t; break;
    case 3: r = p; g = q; b = v; break;
    case 4: r = t; g = p; b = v; break;
    default: r = v; g = p; b = q; break;
  }
  return [Math.round(r * 255), Math.round(g * 255), Math.round(b * 255)];
}

function drawColorSpectrum(canvas, hue) {
  if (!canvas) return;
  const ctx = canvas.getContext('2d');
  const w = canvas.width;
  const h = canvas.height;

  const horizGrad = ctx.createLinearGradient(0, 0, w, 0);
  horizGrad.addColorStop(0, '#ffffff');
  horizGrad.addColorStop(1, `hsl(${hue}, 100%, 50%)`);
  ctx.fillStyle = horizGrad;
  ctx.fillRect(0, 0, w, h);

  const vertGrad = ctx.createLinearGradient(0, 0, 0, h);
  vertGrad.addColorStop(0, 'rgba(0,0,0,0)');
  vertGrad.addColorStop(1, '#000000');
  ctx.fillStyle = vertGrad;
  ctx.fillRect(0, 0, w, h);
}

const spectrumPickers = {
  text: { hue: 0, s: 0, v: 1, isDragging: false },
  bg: { hue: 215, s: 0.44, v: 0.23, isDragging: false },
  'setting-text': { hue: 0, s: 0, v: 1, isDragging: false },
  'setting-bg': { hue: 215, s: 0.44, v: 0.23, isDragging: false },
};

function setupSpectrumPicker(type) {
  const canvas = document.getElementById(`spectrum-canvas-${type}`);
  const cursor = document.getElementById(`spectrum-cursor-${type}`);
  const hueSlider = document.getElementById(`hue-slider-${type}`);
  const wrap = document.getElementById(`spectrum-wrap-${type}`);
  if (!canvas || !cursor || !hueSlider || !wrap) return;

  const spState = spectrumPickers[type];

  function redraw() {
    drawColorSpectrum(canvas, spState.hue);
    cursor.style.left = (spState.s * 100) + '%';
    cursor.style.top = ((1 - spState.v) * 100) + '%';
  }

  function pickFromCoords(clientX, clientY) {
    const rect = canvas.getBoundingClientRect();
    const x = Math.max(0, Math.min(rect.width, clientX - rect.left));
    const y = Math.max(0, Math.min(rect.height, clientY - rect.top));
    spState.s = x / rect.width;
    spState.v = 1 - (y / rect.height);
    cursor.style.left = x + 'px';
    cursor.style.top = y + 'px';

    const rgb = hsvToRgb(spState.hue, spState.s, spState.v);
    const hex = rgbToHex(rgb[0], rgb[1], rgb[2]);
    if (type === 'text') {
      updateTextColorUI(hex, false);
      renderTextIconCanvas();
    } else if (type === 'bg') {
      updateBgColorUI(hex, false);
      renderTextIconCanvas();
    } else if (type === 'setting-text') {
      updateSettingTextColorUI(hex, false);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    } else if (type === 'setting-bg') {
      updateSettingBgColorUI(hex, false);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    }
  }

  wrap.addEventListener('mousedown', (e) => {
    spState.isDragging = true;
    pickFromCoords(e.clientX, e.clientY);
  });

  document.addEventListener('mousemove', (e) => {
    if (spState.isDragging) {
      pickFromCoords(e.clientX, e.clientY);
    }
  });

  document.addEventListener('mouseup', () => {
    spState.isDragging = false;
  });

  wrap.addEventListener('touchstart', (e) => {
    if (e.touches && e.touches[0]) {
      spState.isDragging = true;
      pickFromCoords(e.touches[0].clientX, e.touches[0].clientY);
    }
  }, { passive: true });

  document.addEventListener('touchmove', (e) => {
    if (spState.isDragging && e.touches && e.touches[0]) {
      pickFromCoords(e.touches[0].clientX, e.touches[0].clientY);
    }
  }, { passive: true });

  document.addEventListener('touchend', () => {
    spState.isDragging = false;
  });

  hueSlider.addEventListener('input', () => {
    spState.hue = parseFloat(hueSlider.value);
    drawColorSpectrum(canvas, spState.hue);
    const rgb = hsvToRgb(spState.hue, spState.s, spState.v);
    const hex = rgbToHex(rgb[0], rgb[1], rgb[2]);
    if (type === 'text') {
      updateTextColorUI(hex, false);
      renderTextIconCanvas();
    } else if (type === 'bg') {
      updateBgColorUI(hex, false);
      renderTextIconCanvas();
    } else if (type === 'setting-text') {
      updateSettingTextColorUI(hex, false);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    } else if (type === 'setting-bg') {
      updateSettingBgColorUI(hex, false);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    }
  });

  redraw();
}

function updateTextColorUI(val, updateSpectrum = true) {
  if (!val) return;
  const hex = normalizeHexColor(val) || val;
  const picker = document.getElementById('icon-text-color');
  const input = document.getElementById('icon-text-color-hex');
  const preview = document.getElementById('preview-text-color-block');
  const display = document.getElementById('display-text-color-hex');
  if (picker) picker.value = hex;
  if (input) input.value = hex;
  if (preview) preview.style.backgroundColor = hex;
  if (display) display.textContent = hex;
  document.querySelectorAll('#text-color-swatches .color-swatch').forEach(s => {
    s.classList.toggle('active', s.dataset.color.toLowerCase() === hex.toLowerCase());
  });

  if (updateSpectrum) {
    const [r, g, b] = hexToRgb(hex);
    const [h, s, v] = rgbToHsv(r, g, b);
    const sp = spectrumPickers.text;
    sp.hue = h; sp.s = s; sp.v = v;
    const hueSlider = document.getElementById('hue-slider-text');
    if (hueSlider) hueSlider.value = Math.round(h);
    const canvas = document.getElementById('spectrum-canvas-text');
    const cursor = document.getElementById('spectrum-cursor-text');
    if (canvas) drawColorSpectrum(canvas, h);
    if (cursor) {
      cursor.style.left = (s * 100) + '%';
      cursor.style.top = ((1 - v) * 100) + '%';
    }
  }
}

function updateBgColorUI(val, updateSpectrum = true) {
  if (!val) return;
  const hex = normalizeHexColor(val) || val;
  const picker = document.getElementById('icon-bg-color');
  const input = document.getElementById('icon-bg-color-hex');
  const preview = document.getElementById('preview-bg-color-block');
  const display = document.getElementById('display-bg-color-hex');
  if (picker) picker.value = hex;
  if (input) input.value = hex;
  if (preview) preview.style.backgroundColor = hex;
  if (display) display.textContent = hex;
  document.querySelectorAll('#bg-color-swatches .color-swatch').forEach(s => {
    s.classList.toggle('active', s.dataset.color.toLowerCase() === hex.toLowerCase());
  });

  if (updateSpectrum) {
    const [r, g, b] = hexToRgb(hex);
    const [h, s, v] = rgbToHsv(r, g, b);
    const sp = spectrumPickers.bg;
    sp.hue = h; sp.s = s; sp.v = v;
    const hueSlider = document.getElementById('hue-slider-bg');
    if (hueSlider) hueSlider.value = Math.round(h);
    const canvas = document.getElementById('spectrum-canvas-bg');
    const cursor = document.getElementById('spectrum-cursor-bg');
    if (canvas) drawColorSpectrum(canvas, h);
    if (cursor) {
      cursor.style.left = (s * 100) + '%';
      cursor.style.top = ((1 - v) * 100) + '%';
    }
  }
}

function closeColorPopovers() {
  const popText = document.getElementById('popover-text-color');
  const popBg = document.getElementById('popover-bg-color');
  if (popText) popText.style.display = 'none';
  if (popBg) popBg.style.display = 'none';
}

// --- Unified Icon Picker Core Helpers ---

async function fetchIconLibrary(force = false) {
  if (state.iconLibraryLoaded && !force && state.iconLibrary.length > 0) {
    return state.iconLibrary;
  }
  try {
    const res = await fetch(apiUrl('/api/icons'));
    if (res.ok) {
      const data = await res.json();
      if (Array.isArray(data)) {
        state.iconLibrary = data;
        state.iconLibraryLoaded = true;
      }
    }
  } catch (err) {
    console.error('Failed to fetch icon library:', err);
  }
  return state.iconLibrary;
}

async function renderIconLibraryGrid(scope) {
  const gridId = scope === 'setting' ? 'setting-icon-library-grid' : 'modal-icon-library-grid';
  const grid = document.getElementById(gridId);
  if (!grid) return;

  await fetchIconLibrary();

  // Keep only the first child (the upload card)
  const uploadCard = grid.querySelector('.icon-lib-upload-card');
  grid.querySelectorAll('.icon-lib-card:not(.icon-lib-upload-card)').forEach(el => el.remove());

  const curVal = (scope === 'setting'
    ? (document.getElementById('setting-portal-icon')?.value || '')
    : (document.getElementById('item-icon')?.value || '')
  ).trim();

  // 1. Prepend built-in app icons (default container icon and product icon)
  const builtInIcons = [
    { name: '默认图标', url: 'default_item_icon.png', title: '系统默认图标', in_use: true, isBuiltIn: true },
    { name: '产品图标', url: 'icon.png', title: '把 Docker 放到桌面 产品图标', in_use: true, isBuiltIn: true },
  ];

  const allIcons = [...builtInIcons, ...state.iconLibrary];

  allIcons.forEach(icon => {
    const card = document.createElement('div');
    card.className = 'icon-lib-card';
    card.dataset.url = icon.url;
    card.title = icon.title || icon.name;

    let isSelected = false;
    if (icon.url === 'default_item_icon.png') {
      if (scope === 'modal') {
        isSelected = !curVal || curVal === 'default_item_icon.png' || curVal === '/default_item_icon.png';
      } else {
        isSelected = curVal === 'default_item_icon.png' || curVal === '/default_item_icon.png';
      }
    } else if (icon.url === 'icon.png') {
      if (scope === 'setting') {
        isSelected = !curVal || curVal === 'icon.png' || curVal === '/icon.png';
      } else {
        isSelected = curVal === 'icon.png' || curVal === '/icon.png';
      }
    } else if (curVal) {
      isSelected = curVal === icon.url || curVal === icon.name || curVal === `/icons/${icon.name}` || icon.url === `/icons/${curVal}`;
    }

    if (isSelected) {
      card.classList.add('selected');
    }

    const img = document.createElement('img');
    img.src = getIconUrl(icon.url);
    img.alt = icon.name;
    img.loading = 'lazy';
    card.appendChild(img);

    const isBuiltIn = !!icon.isBuiltIn || icon.url === 'default_item_icon.png' || icon.url === 'icon.png';
    const isInUse = isBuiltIn || !!icon.in_use;

    if (isInUse) {
      const badge = document.createElement('span');
      badge.className = 'icon-card-badge badge-in-use';
      badge.textContent = '使用中';
      card.appendChild(badge);
    } else {
      const delBtn = document.createElement('button');
      delBtn.type = 'button';
      delBtn.className = 'icon-card-badge btn-delete-icon';
      delBtn.textContent = '删除';
      delBtn.title = '删除此图标';
      delBtn.addEventListener('click', async (e) => {
        e.stopPropagation();
        e.preventDefault();
        if (!confirm(`确定要删除图标 "${icon.name}" 吗？`)) return;
        try {
          const res = await fetch(apiUrl('/api/icons/' + encodeURIComponent(icon.name)), {
            method: 'DELETE'
          });
          const data = await res.json();
          if (res.ok) {
            showToast('图标已删除', 'success');
            await fetchIconLibrary(true);
            renderIconLibraryGrid('modal');
            renderIconLibraryGrid('setting');
          } else {
            showToast(data.error || '删除图标失败', 'error');
          }
        } catch (err) {
          showToast('删除请求失败: ' + err.message, 'error');
        }
      });
      card.appendChild(delBtn);
    }

    card.addEventListener('click', () => {
      selectLibraryIcon(scope, icon.url, card);
    });

    grid.appendChild(card);
  });
}

function selectLibraryIcon(scope, url, cardEl) {
  const gridId = scope === 'setting' ? 'setting-icon-library-grid' : 'modal-icon-library-grid';
  const grid = document.getElementById(gridId);
  if (grid) {
    grid.querySelectorAll('.icon-lib-card').forEach(c => c.classList.remove('selected'));
  }
  if (cardEl) {
    cardEl.classList.add('selected');
  }

  let saveVal = url;
  if (url === '/default_item_icon.png' || url === 'default_item_icon.png') {
    saveVal = 'default_item_icon.png';
  } else if (url === '/icon.png' || url === 'icon.png') {
    saveVal = 'icon.png';
  }

  if (scope === 'setting') {
    const elIcon = document.getElementById('setting-portal-icon');
    if (elIcon) elIcon.value = saveVal;
    const imgEl = document.getElementById('setting-icon-preview-img');
    if (imgEl) imgEl.src = getIconUrl(saveVal);
    checkSettingsDirty();
  } else {
    const elIcon = document.getElementById('item-icon');
    if (elIcon) elIcon.value = saveVal;
    const imgEl = document.getElementById('icon-preview-img');
    if (imgEl) imgEl.src = getIconUrl(saveVal);
  }
}

async function handleUploadLibraryIcon(scope, fileInput) {
  const originalFile = fileInput.files[0];
  if (!originalFile) return;

  if (originalFile.size > 10 * 1024 * 1024) {
    showToast('上传图标文件不能超过 10MB', 'error');
    fileInput.value = '';
    return;
  }

  let file = originalFile;
  try {
    file = await compressIconFile(originalFile, 256);
  } catch (err) {
    showToast(err.message || '图标压缩失败', 'error');
    fileInput.value = '';
    return;
  }

  const formData = new FormData();
  formData.append('icon', file);

  try {
    const res = await fetch(apiUrl('/api/icons/upload'), {
      method: 'POST',
      body: formData,
    });
    const data = await res.json();
    if (res.ok && data.url) {
      const newIcon = {
        name: file.name,
        url: data.url,
        last_used: Math.floor(Date.now() / 1000),
      };
      state.iconLibrary = [newIcon, ...state.iconLibrary.filter(i => i.url !== data.url)];

      const gridId = scope === 'setting' ? 'setting-icon-library-grid' : 'modal-icon-library-grid';
      const grid = document.getElementById(gridId);
      if (grid) {
        grid.querySelectorAll('.icon-lib-card').forEach(c => c.classList.remove('selected'));
        const uploadCard = grid.querySelector('.icon-lib-upload-card');

        grid.querySelectorAll(`.icon-lib-card[data-url="${data.url}"]`).forEach(c => c.remove());

        const newCard = document.createElement('div');
        newCard.className = 'icon-lib-card selected';
        newCard.dataset.url = data.url;
        newCard.title = file.name;

        const img = document.createElement('img');
        img.src = apiUrl(data.url);
        img.alt = file.name;
        newCard.appendChild(img);

        newCard.addEventListener('click', () => {
          selectLibraryIcon(scope, data.url, newCard);
        });

        if (uploadCard && uploadCard.nextSibling) {
          uploadCard.after(newCard);
        } else if (uploadCard) {
          grid.appendChild(newCard);
        }
      }

      if (scope === 'setting') {
        const elIcon = document.getElementById('setting-portal-icon');
        if (elIcon) elIcon.value = data.url;
        const imgEl = document.getElementById('setting-icon-preview-img');
        if (imgEl) imgEl.src = apiUrl(data.url);
        checkSettingsDirty();
      } else {
        const elIcon = document.getElementById('item-icon');
        if (elIcon) elIcon.value = data.url;
        const imgEl = document.getElementById('icon-preview-img');
        if (imgEl) imgEl.src = apiUrl(data.url);
      }

      showToast(`图标「${file.name}」上传成功并已选择`, 'success');
    } else {
      showToast(data.error || '上传图标失败', 'error');
    }
  } catch (err) {
    showToast('上传图标网络异常: ' + err.message, 'error');
  } finally {
    fileInput.value = '';
  }
}

function expandIconPicker(scope) {
  const prefix = scope === 'setting' ? 'setting-' : 'modal-';
  const collapsed = document.getElementById(`${prefix}icon-picker-collapsed`);
  const expanded = document.getElementById(`${prefix}icon-picker-expanded`);
  if (collapsed) collapsed.style.display = 'none';
  if (expanded) expanded.style.display = 'block';

  renderIconLibraryGrid(scope);

  if (scope === 'setting') {
    if (spectrumPickers['setting-text']) drawColorSpectrum(document.getElementById('spectrum-canvas-setting-text'), spectrumPickers['setting-text'].hue);
    if (spectrumPickers['setting-bg']) drawColorSpectrum(document.getElementById('spectrum-canvas-setting-bg'), spectrumPickers['setting-bg'].hue);
  } else {
    if (spectrumPickers['text']) drawColorSpectrum(document.getElementById('spectrum-canvas-text'), spectrumPickers['text'].hue);
    if (spectrumPickers['bg']) drawColorSpectrum(document.getElementById('spectrum-canvas-bg'), spectrumPickers['bg'].hue);
  }
}

function collapseIconPicker(scope) {
  const prefix = scope === 'setting' ? 'setting-' : 'modal-';
  const collapsed = document.getElementById(`${prefix}icon-picker-collapsed`);
  const expanded = document.getElementById(`${prefix}icon-picker-expanded`);
  if (expanded) expanded.style.display = 'none';
  if (collapsed) collapsed.style.display = 'flex';
}

function handleCollapseClick(scope) {
  if (isIconModified(scope)) {
    if (!confirm('已修改图标，确定要放弃本次修改并收起吗？')) {
      return;
    }
    revertIcon(scope);
  }
  collapseIconPicker(scope);
}

function saveIconSnapshot(scope) {
  if (scope === 'setting') {
    const tab = state.activeSettingIconTab || 'lib';
    const text = (document.getElementById('setting-icon-text-input')?.value || '').trim();
    const textColor = (document.getElementById('setting-icon-text-color-hex')?.value || '#ffffff').trim().toLowerCase();
    const bgColor = (document.getElementById('setting-icon-bg-color-hex')?.value || '#1e293b').trim().toLowerCase();
    const url = (document.getElementById('setting-icon-url-input')?.value || '').trim();
    const iconVal = (document.getElementById('setting-portal-icon')?.value || '').trim();
    const previewImg = document.getElementById('setting-icon-preview-img');
    const previewSrc = previewImg ? previewImg.src : '';
    state.initialSettingIcon = { tab, text, textColor, bgColor, url, iconVal, previewSrc };
  } else {
    const tab = state.activeIconTab || 'text';
    const text = (document.getElementById('icon-text-input')?.value || '').trim();
    const textColor = (document.getElementById('icon-text-color-hex')?.value || '#ffffff').trim().toLowerCase();
    const bgColor = (document.getElementById('icon-bg-color-hex')?.value || '#1e293b').trim().toLowerCase();
    const url = (document.getElementById('icon-url-input')?.value || '').trim();
    const iconVal = (document.getElementById('item-icon')?.value || '').trim();
    const previewImg = document.getElementById('icon-preview-img');
    const previewSrc = previewImg ? previewImg.src : '';
    state.initialModalIcon = { tab, text, textColor, bgColor, url, iconVal, previewSrc };
  }
}

function isIconModified(scope) {
  const snap = scope === 'setting' ? state.initialSettingIcon : state.initialModalIcon;
  if (!snap) return false;

  const isSetting = scope === 'setting';
  const curTab = isSetting ? (state.activeSettingIconTab || 'lib') : (state.activeIconTab || 'text');

  if (curTab === 'text') {
    const textInputId = isSetting ? 'setting-icon-text-input' : 'icon-text-input';
    const textColorId = isSetting ? 'setting-icon-text-color-hex' : 'icon-text-color-hex';
    const bgColorId = isSetting ? 'setting-icon-bg-color-hex' : 'icon-bg-color-hex';
    const curText = (document.getElementById(textInputId)?.value || '').trim();
    const curTextColor = (document.getElementById(textColorId)?.value || '#ffffff').trim().toLowerCase();
    const curBgColor = (document.getElementById(bgColorId)?.value || '#1e293b').trim().toLowerCase();

    if (curText) {
      if (curText !== (snap.text || '') || curTextColor !== (snap.textColor || '#ffffff') || curBgColor !== (snap.bgColor || '#1e293b')) {
        return true;
      }
    } else {
      if (snap.tab === 'text' && snap.text) {
        return true;
      }
    }
    return false;
  }

  if (curTab === 'url') {
    const urlInputId = isSetting ? 'setting-icon-url-input' : 'icon-url-input';
    const curUrl = (document.getElementById(urlInputId)?.value || '').trim();
    if (curUrl) {
      if (curUrl !== (snap.url || '')) return true;
    } else {
      if (snap.tab === 'url' && snap.url) {
        return true;
      }
    }
    return false;
  }

  if (curTab === 'lib' || curTab === 'upload') {
    const iconInputId = isSetting ? 'setting-portal-icon' : 'item-icon';
    const curIcon = (document.getElementById(iconInputId)?.value || '').trim();
    if (curIcon !== (snap.iconVal || '')) return true;
    return false;
  }

  return false;
}

function revertIcon(scope) {
  const snap = scope === 'setting' ? state.initialSettingIcon : state.initialModalIcon;
  if (!snap) return;

  if (scope === 'setting') {
    const textInput = document.getElementById('setting-icon-text-input');
    if (textInput) textInput.value = snap.text;
    updateSettingTextColorUI(snap.textColor);
    updateSettingBgColorUI(snap.bgColor);
    const urlInput = document.getElementById('setting-icon-url-input');
    if (urlInput) urlInput.value = snap.url;
    const elIcon = document.getElementById('setting-portal-icon');
    if (elIcon) elIcon.value = snap.iconVal;
    setSettingIconTab(snap.tab);
    const previewImg = document.getElementById('setting-icon-preview-img');
    if (previewImg && snap.previewSrc) previewImg.src = snap.previewSrc;
    renderIconLibraryGrid('setting');
    checkSettingsDirty();
  } else {
    const textInput = document.getElementById('icon-text-input');
    if (textInput) textInput.value = snap.text;
    updateTextColorUI(snap.textColor);
    updateBgColorUI(snap.bgColor);
    const urlInput = document.getElementById('icon-url-input');
    if (urlInput) urlInput.value = snap.url;
    const elIcon = document.getElementById('item-icon');
    if (elIcon) elIcon.value = snap.iconVal;
    setIconModalTab(snap.tab);
    const previewImg = document.getElementById('icon-preview-img');
    if (previewImg && snap.previewSrc) previewImg.src = snap.previewSrc;
    renderIconLibraryGrid('modal');
  }
}

function initIconEditor() {
  document.querySelectorAll('#modal-icon-tabs .icon-tab').forEach(tab => {
    tab.addEventListener('click', () => setIconModalTab(tab.dataset.iconTab));
  });

  const btnToggle = document.getElementById('modal-btn-toggle-icon-edit');
  if (btnToggle) {
    btnToggle.addEventListener('click', () => expandIconPicker('modal'));
  }

  const btnCollapse = document.getElementById('modal-btn-collapse-icon');
  if (btnCollapse) {
    btnCollapse.addEventListener('click', () => handleCollapseClick('modal'));
  }

  const fileInput = document.getElementById('icon-file-input');
  if (fileInput) {
    fileInput.addEventListener('change', () => handleUploadLibraryIcon('modal', fileInput));
  }

  const textInput = document.getElementById('icon-text-input');
  if (textInput) {
    textInput.addEventListener('input', renderTextIconCanvas);
  }

  // Initialize 2D spectrum pickers for text and bg
  setupSpectrumPicker('text');
  setupSpectrumPicker('bg');

  // Popover toggle buttons
  const btnTextTrigger = document.getElementById('btn-text-color-trigger');
  const popoverText = document.getElementById('popover-text-color');
  if (btnTextTrigger && popoverText) {
    btnTextTrigger.addEventListener('click', (e) => {
      e.stopPropagation();
      const isVisible = popoverText.style.display === 'block';
      closeColorPopovers();
      if (!isVisible) {
        popoverText.style.display = 'block';
        const canvas = document.getElementById('spectrum-canvas-text');
        if (canvas) drawColorSpectrum(canvas, spectrumPickers.text.hue);
      }
    });
    popoverText.addEventListener('click', (e) => e.stopPropagation());
  }

  const btnBgTrigger = document.getElementById('btn-bg-color-trigger');
  const popoverBg = document.getElementById('popover-bg-color');
  if (btnBgTrigger && popoverBg) {
    btnBgTrigger.addEventListener('click', (e) => {
      e.stopPropagation();
      const isVisible = popoverBg.style.display === 'block';
      closeColorPopovers();
      if (!isVisible) {
        popoverBg.style.display = 'block';
        const canvas = document.getElementById('spectrum-canvas-bg');
        if (canvas) drawColorSpectrum(canvas, spectrumPickers.bg.hue);
      }
    });
    popoverBg.addEventListener('click', (e) => e.stopPropagation());
  }

  document.addEventListener('click', () => {
    closeColorPopovers();
  });

  const textColorInput = document.getElementById('icon-text-color');
  const textColorHex = document.getElementById('icon-text-color-hex');
  if (textColorInput) {
    textColorInput.addEventListener('input', () => {
      updateTextColorUI(textColorInput.value);
      renderTextIconCanvas();
    });
  }
  if (textColorHex) {
    textColorHex.addEventListener('input', () => {
      const hex = normalizeHexColor(textColorHex.value);
      if (hex) {
        updateTextColorUI(hex);
        renderTextIconCanvas();
      }
    });
    textColorHex.addEventListener('blur', () => {
      const hex = normalizeHexColor(textColorHex.value);
      updateTextColorUI(hex || '#ffffff');
    });
  }

  const bgColorInput = document.getElementById('icon-bg-color');
  const bgColorHex = document.getElementById('icon-bg-color-hex');
  if (bgColorInput) {
    bgColorInput.addEventListener('input', () => {
      updateBgColorUI(bgColorInput.value);
      renderTextIconCanvas();
    });
  }
  if (bgColorHex) {
    bgColorHex.addEventListener('input', () => {
      const hex = normalizeHexColor(bgColorHex.value);
      if (hex) {
        updateBgColorUI(hex);
        renderTextIconCanvas();
      }
    });
    bgColorHex.addEventListener('blur', () => {
      const hex = normalizeHexColor(bgColorHex.value);
      updateBgColorUI(hex || '#1e293b');
    });
  }

  document.querySelectorAll('#text-color-swatches .color-swatch').forEach(swatch => {
    swatch.addEventListener('click', () => {
      updateTextColorUI(swatch.dataset.color);
      renderTextIconCanvas();
    });
  });

  document.querySelectorAll('#bg-color-swatches .color-swatch').forEach(swatch => {
    swatch.addEventListener('click', () => {
      updateBgColorUI(swatch.dataset.color);
      renderTextIconCanvas();
    });
  });

  const urlInput = document.getElementById('icon-url-input');
  if (urlInput) {
    urlInput.addEventListener('input', () => {
      const val = urlInput.value.trim();
      const previewImg = document.getElementById('icon-preview-img');
      if (!val) {
        if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
      } else {
        if (previewImg) {
          previewImg.src = val;
          previewImg.onerror = () => {
            previewImg.src = apiUrl('/default_item_icon.png');
          };
        }
      }
    });
  }
}

// --- Setting Icon 3-Tab Editor ---
function setSettingIconTab(tabName) {
  state.activeSettingIconTab = tabName;
  document.querySelectorAll('#setting-icon-tabs .icon-tab').forEach(b => {
    b.classList.toggle('active', b.dataset.settingIconTab === tabName);
  });
  document.querySelectorAll('#pane-settings .icon-tab-pane').forEach(p => {
    p.classList.toggle('active', p.id === `setting-icon-pane-${tabName}`);
  });

  if (tabName === 'lib' || tabName === 'upload') {
    renderIconLibraryGrid('setting');
  }
}

function renderSettingTextIconCanvas() {
  const textInput = document.getElementById('setting-icon-text-input');
  const text = (textInput ? textInput.value : '').trim();
  const textColor = document.getElementById('setting-icon-text-color')?.value || '#ffffff';
  const bgColor = document.getElementById('setting-icon-bg-color')?.value || '#1e293b';
  const previewImg = document.getElementById('setting-icon-preview-img');

  if (!text) {
    if (previewImg) previewImg.src = apiUrl('/icon.png');
    state.currentSettingTextIconDataUrl = null;
    return;
  }

  let canvas = document.getElementById('setting-icon-text-canvas');
  if (!canvas) {
    canvas = document.createElement('canvas');
    canvas.id = 'setting-icon-text-canvas';
    canvas.width = 256;
    canvas.height = 256;
    canvas.style.display = 'none';
    document.body.appendChild(canvas);
  }

  const ctx = canvas.getContext('2d');
  ctx.clearRect(0, 0, 256, 256);

  // Background squircle
  ctx.fillStyle = bgColor;
  const r = 50;
  ctx.beginPath();
  if (ctx.roundRect) {
    ctx.roundRect(0, 0, 256, 256, r);
  } else {
    ctx.moveTo(r, 0);
    ctx.lineTo(256 - r, 0);
    ctx.quadraticCurveTo(256, 0, 256, r);
    ctx.lineTo(256, 256 - r);
    ctx.quadraticCurveTo(256, 256, 256 - r, 256);
    ctx.lineTo(r, 256);
    ctx.quadraticCurveTo(0, 256, 0, 256 - r);
    ctx.lineTo(0, r);
    ctx.quadraticCurveTo(0, 0, r, 0);
    ctx.closePath();
  }
  ctx.fill();

  // Multi-line adaptive font sizing
  const rawLines = text.split('\n').map(l => l.trim()).filter(l => l.length > 0);
  const lines = rawLines.length > 0 ? rawLines : [text];

  const maxW = 200;
  const maxH = 200;

  let fontSize = 140;
  const fontFam = '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif';

  while (fontSize > 16) {
    ctx.font = `bold ${fontSize}px ${fontFam}`;
    const lineHeight = fontSize * 1.16;
    const totalH = (lines.length - 1) * lineHeight + fontSize;
    if (totalH <= maxH) {
      let allFit = true;
      for (const line of lines) {
        if (ctx.measureText(line).width > maxW) {
          allFit = false;
          break;
        }
      }
      if (allFit) break;
    }
    fontSize -= 2;
  }

  ctx.font = `bold ${fontSize}px ${fontFam}`;
  ctx.fillStyle = textColor;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';

  const lineHeight = fontSize * 1.16;
  const totalH = (lines.length - 1) * lineHeight;
  const startY = 128 - totalH / 2;

  lines.forEach((line, idx) => {
    ctx.fillText(line, 128, startY + idx * lineHeight);
  });

  const dataUrl = canvas.toDataURL('image/png');
  state.currentSettingTextIconDataUrl = dataUrl;
  if (previewImg) previewImg.src = dataUrl;
}

async function uploadSettingTextIconBlob() {
  const canvas = document.getElementById('setting-icon-text-canvas');
  if (!canvas) return null;
  return new Promise((resolve) => {
    canvas.toBlob(async (blob) => {
      if (!blob) return resolve(null);
      try {
        const formData = new FormData();
        formData.append('icon', blob, `self-icon-${Date.now()}.png`);
        const res = await fetch(apiUrl('/api/icons/upload'), {
          method: 'POST',
          body: formData,
        });
        const data = await res.json();
        if (res.ok && data.url) {
          resolve(data.url);
        } else {
          resolve(null);
        }
      } catch (err) {
        console.error('Upload setting text icon error:', err);
        resolve(null);
      }
    }, 'image/png');
  });
}

function updateSettingTextColorUI(val, updateSpectrum = true) {
  if (!val) return;
  const hex = normalizeHexColor(val) || val;
  const picker = document.getElementById('setting-icon-text-color');
  const input = document.getElementById('setting-icon-text-color-hex');
  const preview = document.getElementById('setting-preview-text-color-block');
  const display = document.getElementById('setting-display-text-color-hex');
  if (picker) picker.value = hex;
  if (input) input.value = hex;
  if (preview) preview.style.backgroundColor = hex;
  if (display) display.textContent = hex;
  document.querySelectorAll('#setting-text-color-swatches .color-swatch').forEach(s => {
    s.classList.toggle('active', s.dataset.color.toLowerCase() === hex.toLowerCase());
  });

  if (updateSpectrum) {
    const [r, g, b] = hexToRgb(hex);
    const [h, s, v] = rgbToHsv(r, g, b);
    const sp = spectrumPickers['setting-text'];
    if (sp) {
      sp.hue = h; sp.s = s; sp.v = v;
      const hueSlider = document.getElementById('hue-slider-setting-text');
      if (hueSlider) hueSlider.value = Math.round(h);
      const canvas = document.getElementById('spectrum-canvas-setting-text');
      const cursor = document.getElementById('spectrum-cursor-setting-text');
      if (canvas) drawColorSpectrum(canvas, h);
      if (cursor) {
        cursor.style.left = (s * 100) + '%';
        cursor.style.top = ((1 - v) * 100) + '%';
      }
    }
  }
}

function updateSettingBgColorUI(val, updateSpectrum = true) {
  if (!val) return;
  const hex = normalizeHexColor(val) || val;
  const picker = document.getElementById('setting-icon-bg-color');
  const input = document.getElementById('setting-icon-bg-color-hex');
  const preview = document.getElementById('setting-preview-bg-color-block');
  const display = document.getElementById('setting-display-bg-color-hex');
  if (picker) picker.value = hex;
  if (input) input.value = hex;
  if (preview) preview.style.backgroundColor = hex;
  if (display) display.textContent = hex;
  document.querySelectorAll('#setting-bg-color-swatches .color-swatch').forEach(s => {
    s.classList.toggle('active', s.dataset.color.toLowerCase() === hex.toLowerCase());
  });

  if (updateSpectrum) {
    const [r, g, b] = hexToRgb(hex);
    const [h, s, v] = rgbToHsv(r, g, b);
    const sp = spectrumPickers['setting-bg'];
    if (sp) {
      sp.hue = h; sp.s = s; sp.v = v;
      const hueSlider = document.getElementById('hue-slider-setting-bg');
      if (hueSlider) hueSlider.value = Math.round(h);
      const canvas = document.getElementById('spectrum-canvas-setting-bg');
      const cursor = document.getElementById('spectrum-cursor-setting-bg');
      if (canvas) drawColorSpectrum(canvas, h);
      if (cursor) {
        cursor.style.left = (s * 100) + '%';
        cursor.style.top = ((1 - v) * 100) + '%';
      }
    }
  }
}

function closeSettingColorPopovers() {
  const popText = document.getElementById('setting-popover-text-color');
  const popBg = document.getElementById('setting-popover-bg-color');
  if (popText) popText.style.display = 'none';
  if (popBg) popBg.style.display = 'none';
}

function initSettingIconEditor() {
  document.querySelectorAll('#setting-icon-tabs .icon-tab').forEach(tab => {
    tab.addEventListener('click', () => {
      setSettingIconTab(tab.dataset.settingIconTab);
      checkSettingsDirty();
    });
  });

  const btnToggle = document.getElementById('setting-btn-toggle-icon-edit');
  if (btnToggle) {
    btnToggle.addEventListener('click', () => expandIconPicker('setting'));
  }

  const btnCollapse = document.getElementById('setting-btn-collapse-icon');
  if (btnCollapse) {
    btnCollapse.addEventListener('click', () => handleCollapseClick('setting'));
  }

  const fileInput = document.getElementById('setting-icon-file-input');
  if (fileInput) {
    fileInput.addEventListener('change', () => handleUploadLibraryIcon('setting', fileInput));
  }

  const textInput = document.getElementById('setting-icon-text-input');
  if (textInput) {
    textInput.addEventListener('input', () => {
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    });
  }

  // Live update portal name
  const nameInput = document.getElementById('setting-portal-name');
  if (nameInput) {
    nameInput.addEventListener('input', () => {
      checkSettingsDirty();
    });
  }

  // Initialize 2D spectrum pickers for setting-text and setting-bg
  setupSpectrumPicker('setting-text');
  setupSpectrumPicker('setting-bg');

  // Popover toggle buttons
  const btnTextTrigger = document.getElementById('setting-btn-text-color-trigger');
  const popoverText = document.getElementById('setting-popover-text-color');
  if (btnTextTrigger && popoverText) {
    btnTextTrigger.addEventListener('click', (e) => {
      e.stopPropagation();
      const isVisible = popoverText.style.display === 'block';
      closeSettingColorPopovers();
      if (!isVisible) {
        popoverText.style.display = 'block';
        const canvas = document.getElementById('spectrum-canvas-setting-text');
        if (canvas && spectrumPickers['setting-text']) drawColorSpectrum(canvas, spectrumPickers['setting-text'].hue);
      }
    });
    popoverText.addEventListener('click', (e) => e.stopPropagation());
  }

  const btnBgTrigger = document.getElementById('setting-btn-bg-color-trigger');
  const popoverBg = document.getElementById('setting-popover-bg-color');
  if (btnBgTrigger && popoverBg) {
    btnBgTrigger.addEventListener('click', (e) => {
      e.stopPropagation();
      const isVisible = popoverBg.style.display === 'block';
      closeSettingColorPopovers();
      if (!isVisible) {
        popoverBg.style.display = 'block';
        const canvas = document.getElementById('spectrum-canvas-setting-bg');
        if (canvas && spectrumPickers['setting-bg']) drawColorSpectrum(canvas, spectrumPickers['setting-bg'].hue);
      }
    });
    popoverBg.addEventListener('click', (e) => e.stopPropagation());
  }

  document.addEventListener('click', () => {
    closeSettingColorPopovers();
  });

  const textColorInput = document.getElementById('setting-icon-text-color');
  const textColorHex = document.getElementById('setting-icon-text-color-hex');
  if (textColorInput) {
    textColorInput.addEventListener('input', () => {
      updateSettingTextColorUI(textColorInput.value);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    });
  }
  if (textColorHex) {
    textColorHex.addEventListener('input', () => {
      const hex = normalizeHexColor(textColorHex.value);
      if (hex) {
        updateSettingTextColorUI(hex);
        renderSettingTextIconCanvas();
        checkSettingsDirty();
      }
    });
    textColorHex.addEventListener('blur', () => {
      const hex = normalizeHexColor(textColorHex.value);
      updateSettingTextColorUI(hex || '#ffffff');
      checkSettingsDirty();
    });
  }

  const bgColorInput = document.getElementById('setting-icon-bg-color');
  const bgColorHex = document.getElementById('setting-icon-bg-color-hex');
  if (bgColorInput) {
    bgColorInput.addEventListener('input', () => {
      updateSettingBgColorUI(bgColorInput.value);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    });
  }
  if (bgColorHex) {
    bgColorHex.addEventListener('input', () => {
      const hex = normalizeHexColor(bgColorHex.value);
      if (hex) {
        updateSettingBgColorUI(hex);
        renderSettingTextIconCanvas();
        checkSettingsDirty();
      }
    });
    bgColorHex.addEventListener('blur', () => {
      const hex = normalizeHexColor(bgColorHex.value);
      updateSettingBgColorUI(hex || '#1e293b');
      checkSettingsDirty();
    });
  }

  document.querySelectorAll('#setting-text-color-swatches .color-swatch').forEach(swatch => {
    swatch.addEventListener('click', () => {
      updateSettingTextColorUI(swatch.dataset.color);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    });
  });

  document.querySelectorAll('#setting-bg-color-swatches .color-swatch').forEach(swatch => {
    swatch.addEventListener('click', () => {
      updateSettingBgColorUI(swatch.dataset.color);
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    });
  });

  const urlInput = document.getElementById('setting-icon-url-input');
  if (urlInput) {
    urlInput.addEventListener('input', () => {
      const val = urlInput.value.trim();
      const previewImg = document.getElementById('setting-icon-preview-img');
      if (!val) {
        if (previewImg) previewImg.src = apiUrl('/icon.png');
      } else {
        if (previewImg) {
          previewImg.src = val;
          previewImg.onerror = () => {
            previewImg.src = apiUrl('/icon.png');
          };
        }
      }
      checkSettingsDirty();
    });
  }
}

function resetDesktopForm() {
  state.sourceWatchcowId = null;
  document.getElementById('item-id').value = '';
  document.getElementById('item-name').value = '';
  state.appShortId = Math.floor(100000 + Math.random() * 900000).toString();
  const elAppName = document.getElementById('item-app-name');
  if (elAppName) elAppName.value = 'fndocker.app-' + state.appShortId;
  state.appNameDirty = false;
  const formEl = document.getElementById('form-desktop-item');
  if (formEl) delete formEl.dataset.image;

  const tipEl = document.getElementById('item-app-name-duplicate-tip');
  if (tipEl) {
    tipEl.style.display = 'none';
    tipEl.textContent = '';
  }
  if (elAppName) elAppName.style.borderColor = '';

  const elContainer = document.getElementById('item-container-name');
  if (elContainer) elContainer.value = '';
  document.getElementById('item-local-port').value = '';
  document.getElementById('item-target-url').value = '';
  document.getElementById('item-proxy-port').value = '';
  document.getElementById('item-shortcut-url').value = '';
  document.getElementById('item-protocol').value = 'http';
  document.getElementById('item-path').value = '/';
  document.getElementById('item-ui-type').value = 'url';
  document.getElementById('item-all-users').value = 'false';
  document.getElementById('item-icon').value = '';
  document.getElementById('item-skip-tls').checked = false;
  document.getElementById('test-target-result').textContent = '';

  const textInput = document.getElementById('icon-text-input');
  if (textInput) textInput.value = '';
  updateTextColorUI('#ffffff');
  updateBgColorUI('#1e293b');
  closeColorPopovers();

  const urlInput = document.getElementById('icon-url-input');
  if (urlInput) urlInput.value = '';
  state.currentTextIconDataUrl = null;
  setIconModalTab('text');

  const previewImg = document.getElementById('icon-preview-img');
  if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
  document.getElementById('desktop-modal-title').textContent = '添加桌面图标';
  const btnSaveAsNew = document.getElementById('btn-save-as-new');
  if (btnSaveAsNew) btnSaveAsNew.style.display = 'none';
  const btnSave = document.getElementById('btn-save-desktop-item');
  if (btnSave) btnSave.textContent = '保存并放到桌面';
  const btnDel = document.getElementById('btn-delete-from-modal');
  if (btnDel) {
    btnDel.style.display = 'none';
    btnDel.onclick = null;
  }
  const txtNotice = document.getElementById('item-notice-content');
  if (txtNotice) txtNotice.value = '';

  const elFileTypes = document.getElementById('item-file-types');
  if (elFileTypes) elFileTypes.value = '';
  const helpFileTypes = document.getElementById('item-file-types-help');
  if (helpFileTypes) helpFileTypes.style.display = 'none';
  const chkNoDisplay = document.getElementById('item-no-display');
  if (chkNoDisplay) chkNoDisplay.checked = false;

  setDesktopModalMode('local');
  collapseIconPicker('modal');
  saveIconSnapshot('modal');
}

function openCreateDesktopModalWithPort(port, name, containerName, image) {
  resetDesktopForm();
  document.getElementById('item-local-port').value = port;
  document.getElementById('item-name').value = name;
  const elContainer = document.getElementById('item-container-name');
  if (elContainer) elContainer.value = containerName || '';

  const formEl = document.getElementById('form-desktop-item');
  if (formEl && image) {
    formEl.dataset.image = image;
  }

  // Pre-generate unique package identifier for fnOS
  state.appShortId = Math.floor(100000 + Math.random() * 900000).toString();
  const baseCandidate = (containerName || name || 'app').toLowerCase().replace(/[^a-z0-9]/g, '-').replace(/-+/g, '-').replace(/^-+|-+$/g, '').slice(0, 14);
  const defaultAppName = ('fndocker.' + (baseCandidate || 'app') + '-' + state.appShortId).slice(0, 32);
  const elAppName = document.getElementById('item-app-name');
  if (elAppName) elAppName.value = defaultAppName;
  state.appNameDirty = false;
  checkAppNameDuplicate();

  // Auto-resolve or recommend official icon from Homarr CDN for Docker containers or port services
  let iconCandidate = '';
  if (image) {
    let imgPart = image.split('/').pop().split(':')[0].split('@')[0];
    iconCandidate = imgPart;
  }
  if (!iconCandidate) {
    iconCandidate = containerName || name;
  }
  if (iconCandidate) {
    const cleanName = iconCandidate.toLowerCase().replace(/[^a-z0-9_-]/g, '').replace(/^[_-]+|[_-]+$/g, '');
    const genericWords = ['image', 'images', 'icon', 'icons', 'default', 'app', 'apps', 'logo', 'pic', 'picture', 'portal', 'dashboard', 'service', 'server', 'container', 'test'];
    if (cleanName && !genericWords.includes(cleanName) && !cleanName.startsWith('copy_')) {
      const cdnUrl = `https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/${cleanName}.png`;
      setIconModalTab('url');
      const urlInput = document.getElementById('icon-url-input');
      if (urlInput) urlInput.value = cdnUrl;
      const imgEl = document.getElementById('icon-preview-img');
      imgEl.src = cdnUrl;
      const elIcon = document.getElementById('item-icon');
      if (elIcon) elIcon.value = cdnUrl;
      imgEl.onerror = () => {
        imgEl.src = apiUrl('/default_item_icon.png');
        if (urlInput) urlInput.value = '';
        if (elIcon && elIcon.value === cdnUrl) {
          elIcon.value = '';
          if (state.desktopItemFormSnapshot) {
            try {
              let snapObj = JSON.parse(state.desktopItemFormSnapshot);
              if (snapObj.iconVal === cdnUrl) {
                snapObj.iconVal = '';
                state.desktopItemFormSnapshot = JSON.stringify(snapObj);
              }
            } catch (_) {}
          }
          if (state.initialModalIcon && state.initialModalIcon.iconVal === cdnUrl) {
            state.initialModalIcon.iconVal = '';
            state.initialModalIcon.url = '';
          }
        }
      };
    }
  }

  setDesktopModalMode('local');
  collapseIconPicker('modal');
  saveIconSnapshot('modal');
  openModal('modal-desktop-item');
  saveDesktopItemFormSnapshot();
}

function openCreateDesktopModalFromWatchcow(id) {
  const item = (state.watchcowItems || []).find(i => i.id === id);
  if (!item) return;

  resetDesktopForm();
  state.sourceWatchcowId = id;
  document.getElementById('desktop-modal-title').textContent = '新建桌面图标';
  document.getElementById('item-id').value = '';
  document.getElementById('item-name').value = item.name || '';

  const elContainer = document.getElementById('item-container-name');
  if (elContainer) elContainer.value = item.container_name || '';

  const formEl = document.getElementById('form-desktop-item');
  if (formEl && item.image) {
    formEl.dataset.image = item.image;
  }

  // Pre-generate unique package identifier for fnOS
  state.appShortId = Math.floor(100000 + Math.random() * 900000).toString();
  const baseCandidate = (item.container_name || item.name || 'app').toLowerCase().replace(/[^a-z0-9]/g, '-').replace(/-+/g, '-').replace(/^-+|-+$/g, '').slice(0, 14);
  const defaultAppName = ('fndocker.' + (baseCandidate || 'app') + '-' + state.appShortId).slice(0, 32);
  const elAppName = document.getElementById('item-app-name');
  if (elAppName) elAppName.value = defaultAppName;
  state.appNameDirty = false;
  checkAppNameDuplicate();

  const mode = item.mode || 'local';
  if (mode === 'local') {
    document.getElementById('item-local-port').value = item.port || '';
    setDesktopModalMode('local');
  } else if (mode === 'proxy') {
    document.getElementById('item-target-url').value = item.target_url || '';
    document.getElementById('item-proxy-port').value = item.port || '';
    document.getElementById('item-skip-tls').checked = !!item.skip_tls_verify;
    setDesktopModalMode('proxy');
  } else if (mode === 'shortcut') {
    document.getElementById('item-shortcut-url').value = item.target_url || item.redirect || '';
    setDesktopModalMode('shortcut');
  }

  document.getElementById('item-protocol').value = item.protocol || 'http';
  document.getElementById('item-path').value = item.path || '/';
  document.getElementById('item-ui-type').value = item.ui_type || 'url';
  document.getElementById('item-all-users').value = item.all_users ? 'true' : 'false';

  const elFileTypes = document.getElementById('item-file-types');
  if (elFileTypes) elFileTypes.value = Array.isArray(item.file_types) ? item.file_types.join(', ') : '';
  const helpFileTypes = document.getElementById('item-file-types-help');
  if (helpFileTypes) helpFileTypes.style.display = 'none';
  const chkNoDisplay = document.getElementById('item-no-display');
  if (chkNoDisplay) chkNoDisplay.checked = !!item.no_display;

  const btnSaveAsNew = document.getElementById('btn-save-as-new');
  if (btnSaveAsNew) btnSaveAsNew.style.display = 'none';
  const btnSave = document.getElementById('btn-save-desktop-item');
  if (btnSave) btnSave.textContent = '保存并放到桌面';
  const btnDel = document.getElementById('btn-delete-from-modal');
  if (btnDel) {
    btnDel.style.display = 'none';
    btnDel.onclick = null;
  }

  // Pre-fill icon: prioritize display_icon (which is already rendered and fast-cached in Watchcow table)
  // or real icon source / local icon path.
  const iconSource = item.display_icon || item.icon || item.local_icon_path || '';
  const previewImg = document.getElementById('icon-preview-img');
  const elIcon = document.getElementById('item-icon');

  if (iconSource) {
    if (iconSource.startsWith('http://') || iconSource.startsWith('https://')) {
      setIconModalTab('url');
      const urlInput = document.getElementById('icon-url-input');
      if (urlInput) urlInput.value = iconSource;
    } else {
      setIconModalTab('lib');
    }
    if (elIcon) elIcon.value = iconSource;
    if (previewImg) {
      previewImg.src = getIconUrl(iconSource);
      previewImg.onerror = () => {
        previewImg.src = apiUrl('/default_item_icon.png');
      };
    }
  }

  collapseIconPicker('modal');
  saveIconSnapshot('modal');
  openModal('modal-desktop-item');
  saveDesktopItemFormSnapshot();
}

function openEditDesktopModal(id) {
  const item = state.desktopItems.find(i => i.id === id);
  if (!item) return;

  resetDesktopForm();
  document.getElementById('desktop-modal-title').textContent = '编辑桌面图标';
  document.getElementById('item-id').value = item.id;
  document.getElementById('item-name').value = item.name;

  const elAppName = document.getElementById('item-app-name');
  if (elAppName) elAppName.value = item.app_name || '';
  state.appNameDirty = true;
  checkAppNameDuplicate();

  const formEl = document.getElementById('form-desktop-item');
  if (formEl && item.image) {
    formEl.dataset.image = item.image;
  }

  const elContainer = document.getElementById('item-container-name');
  if (elContainer) elContainer.value = item.container_name || '';
  document.getElementById('item-protocol').value = item.protocol || 'http';
  document.getElementById('item-path').value = item.path || '/';
  document.getElementById('item-ui-type').value = item.ui_type || 'url';
  document.getElementById('item-all-users').value = item.all_users ? 'true' : 'false';
  document.getElementById('item-icon').value = item.icon || '';

  const txtNotice = document.getElementById('item-notice-content');
  if (txtNotice) txtNotice.value = item.notice_content || '';

  const elFileTypes = document.getElementById('item-file-types');
  if (elFileTypes) elFileTypes.value = Array.isArray(item.file_types) ? item.file_types.join(', ') : '';
  const helpFileTypes = document.getElementById('item-file-types-help');
  if (helpFileTypes) helpFileTypes.style.display = 'none';
  const chkNoDisplay = document.getElementById('item-no-display');
  if (chkNoDisplay) chkNoDisplay.checked = !!item.no_display;

  const btnSaveAsNew = document.getElementById('btn-save-as-new');
  if (btnSaveAsNew) btnSaveAsNew.style.display = 'inline-flex';
  const btnSave = document.getElementById('btn-save-desktop-item');
  if (btnSave) btnSave.textContent = '保存并更新桌面';

  // Wire up "移出桌面" in the modal footer
  const btnDel = document.getElementById('btn-delete-from-modal');
  if (btnDel) {
    btnDel.style.display = 'inline-flex';
    btnDel.onclick = async () => {
      if (!confirm(`确定从飞牛桌面移出图标「${item.name}」吗？`)) return;
      state.desktopItemFormSnapshot = null;
      state.initialModalIcon = null;
      closeModal('modal-desktop-item');

      reportClientLog('action', '用户确认移出桌面图标', `移出图标: ${item.name} (ID: ${id}, 包名: ${item.app_name})`, { id, name: item.name, app_name: item.app_name });

      // Optimistic row update with loading spinner so user sees immediate feedback
      const target = state.desktopItems.find(i => i.id === id);
      if (target) {
        target._updating = true;
        target._error = false;
        target._statusText = '正在移出中...';
        renderDesktopTable();
      }

      try {
        let res = await fetch(apiUrl(`/api/desktop/items/${id}/delete`), { method: 'POST' });
        if (!res.ok) {
          res = await fetch(apiUrl(`/api/desktop/items/${id}`), { method: 'DELETE' });
        }
        if (res.ok) {
          showToast(`已成功从桌面移出「${item.name}」`, 'success');
          state.desktopItems = state.desktopItems.filter(i => i.id !== id);
          updateDesktopCountBadge();
          renderDesktopTable();
          renderPortsTable();
        } else {
          const errData = await res.json().catch(() => ({}));
          const errMsg = errData.error || `移出失败 (HTTP ${res.status})`;
          showToast(errMsg, 'error', 6000);
          if (target) {
            target._updating = false;
            target._error = true;
            target._statusText = '移出失败: ' + errMsg;
            renderDesktopTable();
          }
        }
      } catch (err) {
        showToast('移出网络异常: ' + err.message, 'error', 6000);
        if (target) {
          target._updating = false;
          target._error = true;
          target._statusText = '网络异常: ' + err.message;
          renderDesktopTable();
        }
      } finally {
        await fetchDesktopItems();
        await fetchPorts();
      }
    };
  }

  if (item.mode === 'local') {
    document.getElementById('item-local-port').value = item.port || '';
    setDesktopModalMode('local');
  } else if (item.mode === 'proxy') {
    document.getElementById('item-target-url').value = item.target_url || '';
    document.getElementById('item-proxy-port').value = item.port || '';
    document.getElementById('item-skip-tls').checked = !!item.skip_tls_verify;
    setDesktopModalMode('proxy');
  } else if (item.mode === 'shortcut') {
    document.getElementById('item-shortcut-url').value = item.target_url || '';
    setDesktopModalMode('shortcut');
  }

  // Restore icon settings based on stored icon_type and metadata
  const itemIconType = item.icon_type || (item.icon && (item.icon.includes('text-icon-') ? 'text' : (item.icon.startsWith('http://') || item.icon.startsWith('https://') ? 'url' : 'lib'))) || 'text';

  const textInput = document.getElementById('icon-text-input');
  const urlInput = document.getElementById('icon-url-input');
  const previewImg = document.getElementById('icon-preview-img');

  if (itemIconType === 'text') {
    setIconModalTab('text');
    if (textInput) textInput.value = item.icon_text || '';
    const textColor = item.icon_text_color || '#ffffff';
    const bgColor = item.icon_bg_color || '#1e293b';
    updateTextColorUI(textColor);
    updateBgColorUI(bgColor);
    closeColorPopovers();

    if (item.icon_text) {
      renderTextIconCanvas();
    } else if (item.icon) {
      if (previewImg) previewImg.src = getIconUrl(item.icon);
    } else {
      renderTextIconCanvas();
    }
  } else if (itemIconType === 'url') {
    setIconModalTab('url');
    if (urlInput) urlInput.value = item.icon || '';
    if (previewImg) {
      previewImg.src = getIconUrl(item.icon);
      previewImg.onerror = () => {
        previewImg.src = apiUrl('/default_item_icon.png');
      };
    }
  } else {
    setIconModalTab('lib');
    document.getElementById('item-icon').value = item.icon || '';
    if (previewImg) {
      previewImg.src = getIconUrl(item.icon);
      previewImg.onerror = () => {
        previewImg.src = apiUrl('/default_item_icon.png');
      };
    }
  }

  collapseIconPicker('modal');
  saveIconSnapshot('modal');
  openModal('modal-desktop-item');
  saveDesktopItemFormSnapshot();
}

// 端口多桌面图标列表管理弹窗
function openPortDesktopListModal(port, procName, items) {
  document.getElementById('port-desktop-list-title').textContent = `端口 ${port} 的桌面图标 (${items.length})`;
  const tbody = document.getElementById('port-desktop-list-tbody');
  if (!tbody) return;

  let html = '';
  for (const item of items) {
    const iconSrc = getIconUrl(item.icon);
    const openModeText = item.ui_type === 'iframe' ? '内部弹窗' : '新标签页';
    const statusText = `
      <div class="status-toggle-wrapper">
        <label class="toggle-switch" style="cursor: default;" title="${item.enabled ? '已启用' : '已停用'}">
          <input type="checkbox" ${item.enabled ? 'checked' : ''} disabled>
          <span class="toggle-slider"></span>
        </label>
      </div>`;

    html += `<tr>
      <td><img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/default_item_icon.png')}'" alt="图标"></td>
      <td><strong>${escapeHtml(item.name)}</strong></td>
      <td><code>${escapeHtml(item.path || '/')}</code></td>
      <td><span class="${openModeClass}">${openModeText}</span></td>
      <td>${statusText}</td>
      <td>
        <div class="table-actions">
          <button class="btn btn-sm btn-secondary btn-edit-from-list" data-id="${item.id}">
            <span>编辑</span>
          </button>
          <button class="btn btn-sm btn-danger btn-delete-from-list" data-id="${item.id}">
            <span>移出</span>
          </button>
        </div>
      </td>
      <td class="filler-col"></td>
    </tr>`;
  }
  tbody.innerHTML = html;

  tbody.querySelectorAll('.btn-edit-from-list').forEach(btn => {
    btn.addEventListener('click', () => {
      closeModal('modal-port-desktop-list');
      openEditDesktopModal(btn.dataset.id);
    });
  });

  tbody.querySelectorAll('.btn-delete-from-list').forEach(btn => {
    btn.addEventListener('click', async () => {
      const id = btn.dataset.id;
      if (confirm('确定从飞牛桌面移出此图标吗？')) {
        reportClientLog('action', '用户从多图标列表移出桌面图标', `ID: ${id}`, { id });
        try {
          let res = await fetch(apiUrl(`/api/desktop/items/${id}/delete`), { method: 'POST' });
          if (!res.ok) {
            res = await fetch(apiUrl(`/api/desktop/items/${id}`), { method: 'DELETE' });
          }
          if (res.ok) {
            showToast('已从桌面移出图标', 'success');
          } else {
            const errData = await res.json().catch(() => ({}));
            showToast('移出失败: ' + (errData.error || res.statusText), 'error');
          }
        } catch (e) {
          showToast('移出网络异常: ' + e.message, 'error');
        } finally {
          await fetchDesktopItems();
          await fetchPorts();
          closeModal('modal-port-desktop-list');
        }
      }
    });
  });

  const btnAdd = document.getElementById('btn-add-from-list');
  if (btnAdd) {
    btnAdd.onclick = () => {
      closeModal('modal-port-desktop-list');
      openCreateDesktopModalWithPort(port, procName);
    };
  }

  openModal('modal-port-desktop-list');
}

async function handleSaveDesktopItem(e) {
  e.preventDefault();
  try {
    const id = document.getElementById('item-id').value.trim();
    const mode = document.getElementById('item-mode').value;
    const name = document.getElementById('item-name').value.trim();
    const protocol = document.getElementById('item-protocol').value;
    const path = document.getElementById('item-path').value.trim() || '/';
    const uiType = document.getElementById('item-ui-type').value;
    const allUsers = document.getElementById('item-all-users').value === 'true';
    let icon = '';
    let iconType = state.activeIconTab || 'text';
    let iconText = '';
    let iconTextColor = '';
    let iconBgColor = '';

    if (state.activeIconTab === 'text') {
      iconType = 'text';
      iconText = (document.getElementById('icon-text-input')?.value || '').trim();
      iconTextColor = (document.getElementById('icon-text-color-hex')?.value || document.getElementById('icon-text-color')?.value || '#ffffff').trim();
      iconBgColor = (document.getElementById('icon-bg-color-hex')?.value || document.getElementById('icon-bg-color')?.value || '#1e293b').trim();
      if (iconText && state.currentTextIconDataUrl) {
        const uploadedUrl = await uploadTextIconBlob();
        if (uploadedUrl) {
          icon = uploadedUrl;
        }
      }
    } else if (state.activeIconTab === 'url') {
      iconType = 'url';
      icon = (document.getElementById('icon-url-input')?.value || '').trim();
    } else if (state.activeIconTab === 'lib' || state.activeIconTab === 'upload') {
      iconType = 'upload';
      icon = (document.getElementById('item-icon')?.value || '').trim();
    }

    if (!icon && !iconText && state.initialModalIcon) {
      icon = state.initialModalIcon.iconVal || '';
    }
    const appNameInput = document.getElementById('item-app-name');
    const appName = appNameInput ? appNameInput.value.trim() : '';

    if (!name) {
      document.getElementById('item-name').focus();
      return showToast('请输入桌面显示名称', 'error');
    }

    if (appName) {
      const validPattern = /^[a-zA-Z0-9][a-zA-Z0-9._-]{2,31}$/;
      if (!validPattern.test(appName)) {
        if (appNameInput) appNameInput.focus();
        showToast('应用包名标识格式不符合规范：必须字母数字开头，仅含字母数字点号横杠，3-32位', 'error');
        return;
      }
    }

    const conflict = checkAppNameDuplicate();
    if (conflict) {
      if (appNameInput) appNameInput.focus();
      return showToast(`应用包名标识已被桌面图标「${conflict.name}」占用，请更改包名！`, 'error');
    }

    let port = 0;
    let targetUrl = '';
    let skipTls = false;

    if (mode === 'local') {
      port = parseInt(document.getElementById('item-local-port').value, 10);
      if (!port || port <= 0) {
        document.getElementById('item-local-port').focus();
        return showToast('请输入有效的本机端口', 'error');
      }
    } else if (mode === 'proxy') {
      targetUrl = document.getElementById('item-target-url').value.trim();
      port = parseInt(document.getElementById('item-proxy-port').value, 10);
      skipTls = document.getElementById('item-skip-tls').checked;
      if (!targetUrl) {
        document.getElementById('item-target-url').focus();
        return showToast('请输入目标地址', 'error');
      }
      if (!port || port <= 0) {
        document.getElementById('item-proxy-port').focus();
        return showToast('请输入本机代理监听端口', 'error');
      }
    } else if (mode === 'shortcut') {
      targetUrl = document.getElementById('item-shortcut-url').value.trim();
      if (!targetUrl) {
        document.getElementById('item-shortcut-url').focus();
        return showToast('请输入目标网址', 'error');
      }
      if (!/^https?:\/\//i.test(targetUrl)) {
        targetUrl = 'https://' + targetUrl;
      }
    }

    let enabled = true;
    if (id) {
      const existing = state.desktopItems.find(i => i.id === id);
      if (existing && existing.enabled !== undefined) {
        enabled = existing.enabled;
      }
    }

    const containerName = document.getElementById('item-container-name') ? document.getElementById('item-container-name').value.trim() : '';
    const formEl = document.getElementById('form-desktop-item');
    const image = formEl && formEl.dataset.image ? formEl.dataset.image : '';

    const noticeContent = document.getElementById('item-notice-content') ? document.getElementById('item-notice-content').value.trim() : '';
    const noticeEnabled = !!noticeContent;

    const rawFileTypes = document.getElementById('item-file-types') ? document.getElementById('item-file-types').value.trim() : '';
    const fileTypes = rawFileTypes
      ? rawFileTypes.split(/[,，\s]+/).map(s => s.trim().toLowerCase().replace(/^\./, '')).filter(Boolean)
      : [];
    const noDisplay = document.getElementById('item-no-display') ? document.getElementById('item-no-display').checked : false;

    const payload = {
      id: id || `item-${Date.now() % 1000000}`,
      name,
      app_name: appName,
      container_name: containerName,
      image,
      mode,
      port,
      target_url: targetUrl,
      protocol,
      path,
      ui_type: uiType,
      all_users: allUsers,
      icon,
      icon_type: iconType,
      icon_text: iconText,
      icon_text_color: iconTextColor,
      icon_bg_color: iconBgColor,
      skip_tls_verify: skipTls,
      notice_enabled: noticeEnabled,
      notice_content: noticeContent,
      file_types: fileTypes,
      no_display: noDisplay,
      enabled,
    };

    // Close modal immediately and clear snapshot so closing won't prompt
    state.desktopItemFormSnapshot = null;
    state.initialModalIcon = null;
    collapseIconPicker('modal');
    closeModal('modal-desktop-item');

    // Update in-memory state FIRST so table and badges have it immediately before any tab switch or fetch
    if (id) {
      const existing = state.desktopItems.find(i => i.id === id);
      if (existing) {
        Object.assign(existing, payload);
        existing._updating = true;
        existing._error = false;
        existing._statusText = '更新中...';
      }
    } else {
      state.desktopItems.unshift({
        ...payload,
        _updating: true,
        _error: false,
        _statusText: '更新中...',
        created_at: new Date().toISOString(),
      });
    }
    updateDesktopCountBadge();
    renderPortsTable();
    renderDesktopTable();

    // Automatically switch to Desktop tab so the user sees the new icon and progress immediately!
    switchTab('desktop');

    const method = id ? 'PUT' : 'POST';
    const url = id ? apiUrl(`/api/desktop/items/${id}`) : apiUrl('/api/desktop/items');

    reportClientLog(
      'action',
      id ? '用户保存更新桌面图标' : '用户创建新桌面图标',
      `名称: ${name}, 包名: ${appName}, 端口: ${port}, 模式: ${mode}`,
      { id, name, app_name: appName, port, mode, url }
    );

    const doFetch = async () => {
      let res = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      // Fallback: if PUT fails on gateway, try POST /api/desktop/items/{id}
      if (!res.ok && id && method === 'PUT') {
        const fallbackRes = await fetch(apiUrl(`/api/desktop/items/${id}`), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        }).catch(() => null);
        if (fallbackRes && fallbackRes.ok) {
          res = fallbackRes;
        }
      }
      return res;
    };

    doFetch()
      .then(async res => {
        const respData = await res.json().catch(() => ({}));
        if (!res.ok) {
          const errMsg = respData.error || `${id ? '更新' : '添加'}桌面图标失败 (HTTP ${res.status})`;
          console.error('[handleSaveDesktopItem] Server rejected:', errMsg, respData);
          showToast(errMsg, 'error', 6000);
          const target = state.desktopItems.find(i => i.id === payload.id);
          if (target) {
            target._updating = false;
            target._error = true;
            target._statusText = '失败: ' + errMsg;
            renderDesktopTable();
          }
        } else {
          showToast(`桌面图标「${name}」已成功同步至飞牛桌面！`, 'success');
          const target = state.desktopItems.find(i => i.id === payload.id);
          if (target) {
            target._updating = false;
            target._statusText = '';
            if (respData && respData.id) {
              Object.assign(target, respData);
            }
          }
          renderDesktopTable();

          // Auto-disable source Watchcow item if copied from Watchcow to prevent duplicate icons
          if (state.sourceWatchcowId) {
            const srcId = state.sourceWatchcowId;
            state.sourceWatchcowId = null;
            const srcItem = (state.watchcowItems || []).find(i => i.id === srcId);
            if (srcItem && srcItem.enabled) {
              srcItem.enabled = false;
              srcItem._updating = true;
              srcItem._statusText = '停用中...';
              renderDesktopTable();
              fetch(apiUrl(`/api/desktop/docklabel/${encodeURIComponent(srcId)}/toggle`), { method: 'POST' })
                .then(r => r.json())
                .then(updated => {
                  srcItem.enabled = updated.enabled;
                  srcItem._updating = false;
                  srcItem._statusText = null;
                  renderDesktopTable();
                })
                .catch(() => {
                  srcItem._updating = false;
                  renderDesktopTable();
                });
            }
          }

          await fetchDesktopItems();
          await fetchPorts();
        }
      })
      .catch(err => {
        console.error('[handleSaveDesktopItem] Network exception:', err);
        showToast('请求异常: ' + err.message, 'error', 6000);
        const target = state.desktopItems.find(i => i.id === payload.id);
        if (target) {
          target._updating = false;
          target._error = true;
          target._statusText = '请求失败: ' + err.message;
          renderDesktopTable();
        }
      });
  } catch (err) {
    console.error('[handleSaveDesktopItem] Unexpected exception:', err);
    showToast('保存异常: ' + err.message, 'error', 6000);
  }
}

async function handleTestTarget() {
  const targetUrl = document.getElementById('item-target-url').value.trim();
  const resEl = document.getElementById('test-target-result');
  if (!targetUrl) {
    resEl.className = 'test-result error';
    resEl.textContent = '请先输入目标地址';
    return;
  }

  resEl.className = 'test-result';
  resEl.textContent = '正在测试连通性...';

  try {
    const res = await fetch(apiUrl('/api/proxy/test'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ target_url: targetUrl }),
    });
    const data = await res.json();
    if (data.success) {
      resEl.className = 'test-result success';
      resEl.textContent = data.message;
    } else {
      resEl.className = 'test-result error';
      resEl.textContent = data.message;
    }
  } catch (err) {
    resEl.className = 'test-result error';
    resEl.textContent = '请求异常: ' + err.message;
  }
}

async function handleRecommendPort() {
  try {
    const res = await fetch(apiUrl('/api/ports/available?start=18000'));
    if (res.ok) {
      const data = await res.json();
      if (data.recommended_port) {
        document.getElementById('item-proxy-port').value = data.recommended_port;
      }
    }
  } catch (err) {
    console.error('Recommend port error:', err);
  }
}

// Compresses and scales bitmap icons to a square (max 256x256) PNG blob
async function compressIconFile(file, maxSize = 256) {
  if (!file) return null;
  if (file.size > 10 * 1024 * 1024) {
    throw new Error('图标文件大小不能超过 10MB');
  }
  const ext = (file.name.split('.').pop() || '').toLowerCase();
  // Vector SVG and ICO formats should retain their native structures
  if (ext === 'svg' || ext === 'ico' || file.type === 'image/svg+xml' || file.type === 'image/x-icon') {
    return file;
  }

  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onerror = () => resolve(file);
    reader.onload = () => {
      const img = new Image();
      img.onerror = () => resolve(file);
      img.onload = () => {
        try {
          const canvas = document.createElement('canvas');
          canvas.width = maxSize;
          canvas.height = maxSize;
          const ctx = canvas.getContext('2d');
          if (!ctx) {
            resolve(file);
            return;
          }
          ctx.clearRect(0, 0, maxSize, maxSize);
          ctx.imageSmoothingEnabled = true;
          ctx.imageSmoothingQuality = 'high';

          const srcW = img.naturalWidth || img.width;
          const srcH = img.naturalHeight || img.height;
          let dstW, dstH;
          if (srcW >= srcH) {
            dstW = maxSize;
            dstH = Math.max(1, Math.round((srcH * maxSize) / srcW));
          } else {
            dstH = maxSize;
            dstW = Math.max(1, Math.round((srcW * maxSize) / srcH));
          }
          const offsetX = Math.round((maxSize - dstW) / 2);
          const offsetY = Math.round((maxSize - dstH) / 2);
          ctx.drawImage(img, 0, 0, srcW, srcH, offsetX, offsetY, dstW, dstH);

          canvas.toBlob((blob) => {
            if (!blob) {
              resolve(file);
              return;
            }
            const cleanName = (file.name.replace(/\.[^.]+$/, '') || 'icon') + '.png';
            const compressedFile = new File([blob], cleanName, { type: 'image/png' });
            resolve(compressedFile);
          }, 'image/png');
        } catch (e) {
          console.warn('Canvas icon compression failed, using original file:', e);
          resolve(file);
        }
      };
      img.src = reader.result;
    };
    reader.readAsDataURL(file);
  });
}

function handleCancelSettings() {
  if (state.isSettingsDirty) {
    if (!confirm('当前设置有未保存的修改，确定要放弃修改并恢复吗？')) {
      return;
    }
  }
  revertIcon('setting');
  collapseIconPicker('setting');
  updateSettingsForm();
}

async function handleSaveSettingsManual() {
  const name = '把 Docker 放到桌面';
  const rAll = document.querySelector('input[name="setting-portal-all-users"]:checked');
  const allUsers = rAll ? rAll.value === 'true' : false;

  reportClientLog('action', '用户保存系统设置', `用户范围: ${allUsers ? '所有用户' : '仅管理员'}`, { allUsers });

  const pwd = document.getElementById('setting-portal-password').value;
  const pwdConfirm = document.getElementById('setting-portal-password-confirm').value;
  const matchTip = document.getElementById('password-match-tip');
  const statusEl = document.getElementById('settings-status');

  let icon = '';
  const activeTabEl = document.querySelector('#setting-icon-tabs .icon-tab.active');
  let iconType = activeTabEl?.dataset.settingIconTab || state.activeSettingIconTab || 'lib';
  let iconText = '';
  let iconTextColor = '';
  let iconBgColor = '';

  if (iconType === 'text') {
    iconText = (document.getElementById('setting-icon-text-input')?.value || '').trim();
    iconTextColor = (document.getElementById('setting-icon-text-color-hex')?.value || document.getElementById('setting-icon-text-color')?.value || '#ffffff').trim();
    iconBgColor = (document.getElementById('setting-icon-bg-color-hex')?.value || document.getElementById('setting-icon-bg-color')?.value || '#1e293b').trim();
    if (iconText) {
      renderSettingTextIconCanvas();
      if (state.currentSettingTextIconDataUrl) {
        const uploadedUrl = await uploadSettingTextIconBlob();
        if (uploadedUrl) {
          icon = uploadedUrl;
        }
      }
    }
  } else if (iconType === 'url') {
    icon = (document.getElementById('setting-icon-url-input')?.value || '').trim();
  } else if (iconType === 'lib' || iconType === 'upload') {
    icon = (document.getElementById('setting-portal-icon')?.value || '').trim() || 'icon.png';
  }

  const payload = {
    portal_name: name,
    portal_ui_type: 'iframe',
    portal_all_users: allUsers,
    portal_icon: icon || state.originalSettings?.portal_icon || 'icon.png',
    portal_icon_type: iconType,
    portal_icon_text: iconText,
    portal_icon_text_color: iconTextColor,
    portal_icon_bg_color: iconBgColor,
  };

  // Password confirmation check
  if (pwd !== '' || pwdConfirm !== '') {
    if (pwd !== pwdConfirm) {
      if (matchTip) {
        matchTip.textContent = '两次输入的密码不一致，无法保存';
        matchTip.style.color = 'var(--danger, #ef4444)';
      }
      return showToast('两次输入的密码不一致，请核对后再保存', 'error');
    } else {
      payload.auth_password = pwd;
    }
  }

  if (statusEl) {
    statusEl.textContent = '正在保存设置...';
    statusEl.style.color = 'var(--primary)';
  }

  try {
    const res = await fetch(apiUrl('/api/settings'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    if (res.ok) {
      state.originalSettings = {
        portal_name: payload.portal_name,
        portal_all_users: payload.portal_all_users,
        portal_icon: payload.portal_icon,
        portal_icon_type: payload.portal_icon_type,
        portal_icon_text: payload.portal_icon_text,
        portal_icon_text_color: payload.portal_icon_text_color,
        portal_icon_bg_color: payload.portal_icon_bg_color,
      };
      collapseIconPicker('setting');
      saveIconSnapshot('setting');
      state.isSettingsDirty = false;
      const savedName = '把 Docker 放到桌面';
      document.title = `${savedName} - 容器与端口管理`;
      const ver = state.settings?.version || '1.1.44';
      const titleEl = document.getElementById('settings-card-title');
      if (titleEl) {
        titleEl.textContent = `v${ver} - 系统设置`;
      }
      const aboutVerEl = document.getElementById('about-app-version');
      if (aboutVerEl) {
        aboutVerEl.textContent = `v${ver}`;
      }
      document.getElementById('setting-portal-password').value = '';
      document.getElementById('setting-portal-password-confirm').value = '';
      if (matchTip) matchTip.textContent = '';
      if (statusEl) {
        statusEl.textContent = '设置已保存并即时生效';
        statusEl.style.color = 'var(--success, #10b981)';
        setTimeout(() => {
          if (!state.isSettingsDirty && statusEl) statusEl.textContent = '';
        }, 3000);
      }
      showToast('设置已成功保存', 'success');
    } else {
      const data = await res.json().catch(() => ({}));
      const errMsg = data.error || '保存失败';
      if (statusEl) {
        statusEl.textContent = errMsg;
        statusEl.style.color = 'var(--danger, #ef4444)';
      }
      showToast(errMsg, 'error');
    }
  } catch (err) {
    if (statusEl) {
      statusEl.textContent = '网络异常: ' + err.message;
      statusEl.style.color = 'var(--danger, #ef4444)';
    }
    showToast('保存设置网络异常: ' + err.message, 'error');
  }
}

// --- Port Detail Modal ---
function openPortDetailModal(portNum) {
  const port = state.ports.find(p => p.local_port === portNum);
  if (!port) return;

  const titleEl = document.getElementById('port-detail-title');
  const bodyEl = document.getElementById('port-detail-body');
  titleEl.textContent = `端口 :${port.local_port} 详细信息`;

  const portUrl = getHostTargetUrl(port.local_port, 'http', '/');

  let endpointsHtml = '';
  for (const addr of (port.addresses || [])) {
    endpointsHtml += `<tr>
      <td>${escapeHtml(addr.protocol)}</td>
      <td><code>${escapeHtml(addr.ip)}:${addr.port}</code></td>
      <td>${escapeHtml(addr.state)}</td>
      <td>${addr.pid > 0 ? addr.pid : ''}</td>
      <td class="filler-col"></td>
    </tr>`;
  }

  bodyEl.innerHTML = `
    <div style="margin-bottom: 1rem;">
      <div style="font-size: 0.9rem; color: var(--text-muted); margin-bottom: 0.3rem;">访问地址</div>
      <a href="${portUrl}" target="_blank" class="port-link" style="font-size: 1.05rem;">
        ${portUrl}
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg>
      </a>
    </div>

    <div style="margin-bottom: 1rem;">
      <div style="font-size: 0.9rem; color: var(--text-muted); margin-bottom: 0.3rem;">关联容器 / 进程</div>
      <div><strong>${escapeHtml(port.process_name || '')}</strong>${port.pid ? ` (PID: ${port.pid})` : ''}</div>
      <div style="font-size: 0.82rem; color: var(--text-muted); margin-top: 0.2rem;">属主: ${escapeHtml(port.user || 'root')}</div>
      ${port.cmdline ? `<div style="font-size: 0.8rem; font-family: monospace; background: var(--bg-surface-subtle); padding: 0.4rem; border-radius: 4px; margin-top: 0.4rem; word-break: break-all;">${escapeHtml(port.cmdline)}</div>` : ''}
    </div>

    <div>
      <div style="font-size: 0.9rem; font-weight: 600; margin-bottom: 0.5rem;">监听端点</div>
      <table class="data-table" style="font-size: 0.82rem;">
        <thead>
          <tr>
            <th>协议</th>
            <th>端点地址</th>
            <th>状态</th>
            <th>PID</th>
            <th class="filler-col"></th>
          </tr>
        </thead>
        <tbody>
          ${endpointsHtml || '<tr><td colspan="5">无详细端点</td></tr>'}
        </tbody>
      </table>
    </div>
  `;

  openModal('modal-port-detail');
}

// --- Auth Handling ---
function showAuthModal() {
  openModal('modal-auth');
}

async function handleAuthLogin(e) {
  e.preventDefault();
  const pwd = document.getElementById('auth-password').value;
  const msgEl = document.getElementById('auth-error-msg');
  msgEl.textContent = '';

  try {
    const res = await fetch(apiUrl('/api/auth/login'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pwd }),
    });
    if (res.ok) {
      closeModal('modal-auth');
      initApp();
    } else {
      msgEl.className = 'test-result error';
      msgEl.textContent = '密码错误，请重试';
    }
  } catch (err) {
    msgEl.className = 'test-result error';
    msgEl.textContent = '登录失败: ' + err.message;
  }
}

// --- Initialization ---
function initApp() {
  initNavigation();
  initModals();
  initTableAutoWrapObservers();

  // Dropdown filters in Ports tab (Docker 容器/系统原生, TCP/UDP)
  const portFilterSource = document.getElementById('port-filter-source');
  if (portFilterSource) {
    portFilterSource.value = state.portFilterSource;
    portFilterSource.addEventListener('change', (e) => {
      state.portFilterSource = e.target.value;
      renderPortsTable();
    });
  }

  const portFilterProto = document.getElementById('port-filter-proto');
  if (portFilterProto) {
    portFilterProto.value = state.portFilterProto;
    portFilterProto.addEventListener('change', (e) => {
      state.portFilterProto = e.target.value;
      renderPortsTable();
    });
  }

  // Export desktop items button
  const btnExportDesktop = document.getElementById('btn-export-desktop');
  if (btnExportDesktop) {
    btnExportDesktop.addEventListener('click', () => {
      handleExportDesktopItems();
    });
  }

  // Search input in Ports tab
  const portSearch = document.getElementById('port-search');
  if (portSearch) {
    portSearch.addEventListener('input', (e) => {
      state.portSearch = e.target.value;
      renderPortsTable();
    });
  }

  // Search input in Desktop tab
  const desktopSearch = document.getElementById('desktop-search');
  if (desktopSearch) {
    desktopSearch.addEventListener('input', (e) => {
      state.desktopSearch = e.target.value;
      renderDesktopTable();
    });
  }

  // Search input in Process tab
  const procSearch = document.getElementById('proc-search');
  if (procSearch) {
    procSearch.addEventListener('input', (e) => {
      state.procSearch = e.target.value;
      renderProcessesTable();
    });
  }

  // Process table sort header initialization
  initProcTableSort();

  // Toolbar Refresh Buttons
  const btnRefreshPorts = document.getElementById('btn-refresh-ports');
  if (btnRefreshPorts) {
    btnRefreshPorts.addEventListener('click', () => {
      fetchPorts();
    });
  }

  const btnRefreshDesktop = document.getElementById('btn-refresh-desktop');
  if (btnRefreshDesktop) {
    btnRefreshDesktop.addEventListener('click', () => {
      fetchDesktopItems();
      fetchWatchcowItems();
    });
  }

  const btnRefreshProcs = document.getElementById('btn-refresh-procs');
  if (btnRefreshProcs) {
    btnRefreshProcs.addEventListener('click', () => {
      fetchProcesses();
    });
  }

  // Sink settings modal listeners
  const btnOpenSink = document.getElementById('btn-open-sink-modal');
  const sinkInput = document.getElementById('sink-rules-input');
  const btnResetSink = document.getElementById('btn-reset-sink-rules');
  const btnSaveSink = document.getElementById('btn-save-sink-rules');
  const btnCancelSink = document.getElementById('btn-cancel-sink-modal');
  const btnCloseSink = document.getElementById('btn-close-sink-modal');
  const modalSink = document.getElementById('modal-sink-settings');

  function isSinkRulesDirty() {
    if (!sinkInput) return false;
    return sinkInput.value.trim() !== (state.initialSinkText || '').trim();
  }

  function tryCloseSinkModal() {
    if (isSinkRulesDirty()) {
      if (!confirm('置底规则已修改但尚未保存，确定要放弃修改并关闭吗？')) {
        return false;
      }
    }
    closeModal('modal-sink-settings');
    return true;
  }

  if (btnOpenSink) {
    btnOpenSink.addEventListener('click', () => {
      const rules = getSinkRules();
      if (sinkInput) {
        sinkInput.value = rules.join('\n');
        state.initialSinkText = sinkInput.value;
      }
      openModal('modal-sink-settings');
    });
  }

  if (btnResetSink) {
    btnResetSink.addEventListener('click', () => {
      if (sinkInput) {
        sinkInput.value = DEFAULT_SINK_RULES.join('\n');
      }
    });
  }

  if (btnCancelSink) {
    btnCancelSink.addEventListener('click', () => {
      tryCloseSinkModal();
    });
  }

  if (btnCloseSink) {
    btnCloseSink.addEventListener('click', () => {
      tryCloseSinkModal();
    });
  }

  if (modalSink) {
    modalSink.addEventListener('click', (e) => {
      if (e.target === modalSink) {
        tryCloseSinkModal();
      }
    });
  }

  if (btnSaveSink) {
    btnSaveSink.addEventListener('click', () => {
      const lines = (sinkInput ? sinkInput.value : '')
        .split('\n')
        .map(s => s.trim().toLowerCase())
        .filter(Boolean);
      saveSinkRules(lines);
      state.initialSinkText = (sinkInput ? sinkInput.value : '');
      closeModal('modal-sink-settings');
      renderPortsTable();
      showToast('置底规则已保存', 'success');
    });
  }

  // Unsaved settings page exit confirmation
  window.addEventListener('beforeunload', (e) => {
    if (state.isSettingsDirty) {
      e.preventDefault();
      e.returnValue = '设置有未保存的修改，确定要离开吗？';
      return e.returnValue;
    }
  });

  // Minimal mode toggle in Ports tab (default: enabled)
  const toggleMinimal = document.getElementById('toggle-minimal-mode');
  const portsTable = document.getElementById('ports-table');
  if (toggleMinimal && portsTable) {
    const saved = localStorage.getItem('fn_ports_minimal_mode');
    const isMinimal = saved !== null ? saved === 'true' : true;
    toggleMinimal.checked = isMinimal;
    portsTable.classList.toggle('minimal-mode', isMinimal);

    toggleMinimal.addEventListener('change', () => {
      const active = toggleMinimal.checked;
      portsTable.classList.toggle('minimal-mode', active);
      localStorage.setItem('fn_ports_minimal_mode', active);
      adjustTableWrapping(portsTable);
    });
    adjustTableWrapping(portsTable);
  }

  const btnCheckUpdate = document.getElementById('btn-check-update');
  if (btnCheckUpdate) {
    btnCheckUpdate.addEventListener('click', () => checkAppUpdate(true));
  }

  fetchPorts();
  fetchDesktopItems();
  fetchHost();
  initEventSource();
  initLogViewer();

  // Automatically check for updates after 2 seconds
  setTimeout(() => {
    checkAppUpdate(false);
  }, 2000);
}

// --- System Logs Viewer (Streaming & Paginated) ---
function initLogViewer() {
  document.querySelectorAll('#log-source-chips .chip').forEach(chip => {
    chip.addEventListener('click', () => {
      document.querySelectorAll('#log-source-chips .chip').forEach(c => c.classList.remove('active'));
      chip.classList.add('active');
      state.logSource = chip.dataset.logSource || 'ALL';
      state.logPage = 1;
      fetchLogs();
    });
  });

  document.querySelectorAll('#log-level-chips .chip').forEach(chip => {
    chip.addEventListener('click', () => {
      document.querySelectorAll('#log-level-chips .chip').forEach(c => c.classList.remove('active'));
      chip.classList.add('active');
      state.logLevel = chip.dataset.logLevel || 'ALL';
      state.logPage = 1;
      fetchLogs();
    });
  });

  let searchTimeout = null;
  const searchInput = document.getElementById('log-search-input');
  if (searchInput) {
    searchInput.addEventListener('input', (e) => {
      clearTimeout(searchTimeout);
      searchTimeout = setTimeout(() => {
        state.logSearch = e.target.value.trim();
        state.logPage = 1;
        fetchLogs();
      }, 300);
    });
  }

  const btnRefresh = document.getElementById('btn-refresh-logs');
  if (btnRefresh) {
    btnRefresh.addEventListener('click', () => {
      fetchLogs();
    });
  }

  const btnDownload = document.getElementById('btn-download-logs');
  if (btnDownload) {
    btnDownload.addEventListener('click', () => {
      downloadLogFile();
    });
  }

  const pageSelect = document.getElementById('log-page-select');
  if (pageSelect) {
    pageSelect.addEventListener('change', (e) => {
      state.logPage = parseInt(e.target.value, 10) || 1;
      renderLogs();
      const body = document.getElementById('terminal-log-body');
      if (body) body.scrollTop = 0;
    });
  }

  const pageSizeInput = document.getElementById('log-page-size');
  if (pageSizeInput) {
    const handlePageSizeChange = (valStr) => {
      let val = parseInt(valStr, 10);
      if (isNaN(val) || val < 20) val = 20;
      if (val > 1000) val = 1000;
      pageSizeInput.value = val;
      if (state.logPageSize !== val) {
        state.logPageSize = val;
        state.logPage = 1;
        renderLogs();
        const body = document.getElementById('terminal-log-body');
        if (body) body.scrollTop = 0;
      }
    };

    pageSizeInput.addEventListener('change', (e) => {
      handlePageSizeChange(e.target.value);
    });
    pageSizeInput.addEventListener('blur', (e) => {
      handlePageSizeChange(e.target.value);
    });
    pageSizeInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        handlePageSizeChange(e.target.value);
        pageSizeInput.blur();
      }
    });
  }
}

async function fetchLogs(isAutoPoll = false) {
  try {
    const source = state.logSource || 'ALL';
    const level = state.logLevel || 'ALL';
    let url = apiUrl(`/api/logs?source=${encodeURIComponent(source)}&level=${encodeURIComponent(level)}`);
    if (state.logSearch) {
      url += `&search=${encodeURIComponent(state.logSearch)}`;
    }

    const res = await fetch(url);
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      const data = await res.json();
      state.logs = data.lines || [];

      renderLogs(isAutoPoll);
    }
  } catch (err) {
    console.error('Fetch logs error:', err);
  }
}

function renderLogs(isAutoPoll = false) {
  const body = document.getElementById('terminal-log-body');
  if (!body) return;

  const totalCount = state.logs ? state.logs.length : 0;
  const pageSize = state.logPageSize || 200;
  const totalPages = Math.max(1, Math.ceil(totalCount / pageSize));

  if (state.logPage > totalPages) {
    state.logPage = totalPages;
  }
  if (state.logPage < 1) {
    state.logPage = 1;
  }

  const totalPagesEl = document.getElementById('log-total-pages');
  if (totalPagesEl) {
    totalPagesEl.textContent = totalPages;
  }

  // Update page select dropdown
  const pageSelect = document.getElementById('log-page-select');
  if (pageSelect) {
    const currentSelected = String(state.logPage);
    if (pageSelect.options.length !== totalPages || pageSelect.value !== currentSelected) {
      pageSelect.innerHTML = '';
      for (let p = 1; p <= totalPages; p++) {
        const opt = document.createElement('option');
        opt.value = p;
        opt.textContent = p;
        if (p === state.logPage) {
          opt.selected = true;
        }
        pageSelect.appendChild(opt);
      }
    }
  }

  if (!state.logs || totalCount === 0) {
    body.innerHTML = '<div class="log-empty-state">暂无日志记录</div>';
    return;
  }

  const startIndex = (state.logPage - 1) * pageSize;
  const endIndex = Math.min(startIndex + pageSize, totalCount);
  const pagedLogs = state.logs.slice(startIndex, endIndex);

  const html = pagedLogs.map((entry, idx) => {
    const lineNum = startIndex + idx + 1;
    const level = (entry.level || 'info').toLowerCase();
    const source = (entry.source || 'app').toLowerCase();
    const badgeClass = `log-badge-${level}`;
    const sourceClass = `log-source-${source}`;
    let timeStr = entry.timestamp || '';
    if (timeStr.includes('.')) {
      timeStr = timeStr.split('.')[0];
    }
    return `<div class="log-line">
      <span class="log-num">${lineNum}</span>
      <span class="log-time">${escapeHtml(timeStr)}</span>
      <span class="log-source-tag ${sourceClass}">${escapeHtml(source)}</span>
      <span class="log-badge ${badgeClass}">${escapeHtml(level)}</span>
      <span class="log-text">${escapeHtml(entry.message || entry.raw)}</span>
    </div>`;
  }).join('');

  body.innerHTML = html;

  if (!isAutoPoll) {
    body.scrollTop = 0;
  }
}

function downloadLogFile() {
  const source = state.logSource || 'app';
  window.open(apiUrl(`/api/logs/download?source=${encodeURIComponent(source)}`), '_blank');
}

// --- Toast Notifications System ---
function showToast(message, type = 'info', duration = 3500) {
  const container = document.getElementById('toast-container');
  if (!container) {
    alert(message);
    return;
  }
  const toast = document.createElement('div');
  toast.className = `toast toast-${type}`;
  toast.innerHTML = `<span class="toast-message">${escapeHtml(message)}</span>`;
  container.appendChild(toast);
  requestAnimationFrame(() => toast.classList.add('show'));
  setTimeout(() => {
    toast.classList.remove('show');
    setTimeout(() => {
      if (toast.parentNode) toast.parentNode.removeChild(toast);
    }, 300);
  }, duration);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initApp);
} else {
  initApp();
}
