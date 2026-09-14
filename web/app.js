// --- 把 Docker 放到桌面 - 前端应用程序 ---

function getAppSessionToken() {
  return window.__FN_SESSION__ || document.querySelector('meta[name="fn-session-token"]')?.content || '';
}

// Global fetch interceptor to attach anti-bypass frontend session token
(function() {
  const originalFetch = window.fetch;
  window.fetch = function(resource, init = {}) {
    const token = getAppSessionToken();
    if (token) {
      if (!init.headers) {
        init.headers = {};
      }
      if (init.headers instanceof Headers) {
        if (!init.headers.has('X-App-Session')) {
          init.headers.set('X-App-Session', token);
        }
      } else if (Array.isArray(init.headers)) {
        init.headers.push(['X-App-Session', token]);
      } else {
        init.headers['X-App-Session'] = token;
      }
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
  if (!icon || icon === 'icon.png' || icon === '/icon.png' || icon === 'default_item_icon.png' || icon === '/default_item_icon.png') {
    return apiUrl('/default_item_icon.png');
  }
  if (icon.startsWith('http://') || icon.startsWith('https://') || icon.startsWith('data:')) {
    return icon;
  }
  if (icon.startsWith('/api/')) {
    return apiUrl(icon);
  }
  const clean = icon.replace(/^\/?icons\//, '').replace(/^\/+/, '');
  if (!clean || clean === 'icon.png' || clean === 'default_item_icon.png') {
    return apiUrl('/default_item_icon.png');
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
  watchcowItems: [],
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
  logDate: '',
  logLevel: 'ALL',
  logSearch: '',
  logs: [],
  appNameDirty: false,
  appShortId: '',
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
    b.classList.toggle('active', b.dataset.tab === tab);
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
  }
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

async function fetchWatchcowItems() {
  try {
    const res = await fetch(apiUrl('/api/desktop/watchcow'));
    if (res.status === 401) return;
    if (res.ok) {
      state.watchcowItems = await res.json();
      renderDesktopTable();
      updateDesktopCountBadge();
    }
  } catch (err) {
    console.error('Fetch watchcow items error:', err);
  }
}

async function fetchDesktopItems() {
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
      renderDesktopTable();
      updateDesktopCountBadge();

      // If any item is reconciling or updating, poll again in 3s to live-update status
      if (state.reconcilePollTimer) {
        clearTimeout(state.reconcilePollTimer);
        state.reconcilePollTimer = null;
      }
      const hasActive = merged.some(item => item.reconciling || item._updating);
      if (hasActive) {
        state.reconcilePollTimer = setTimeout(fetchDesktopItems, 3000);
      }
    }
  } catch (err) {
    console.error('Fetch desktop items error:', err);
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

function updateSettingsForm() {
  const portalName = '把 Docker 放到桌面';
  const elName = document.getElementById('setting-portal-name');
  if (elName) elName.value = portalName;

  const titleEl = document.getElementById('settings-card-title');
  if (titleEl) {
    const ver = state.settings?.version || '1.1.18';
    titleEl.textContent = `v${ver} - 系统设置`;
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
  if (!iconType) {
    if (state.originalSettings?.portal_icon_text) {
      iconType = 'text';
    } else {
      iconType = 'upload';
    }
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

  const previewName = document.getElementById('setting-icon-preview-name');
  if (previewName) previewName.textContent = '把 Docker 放到桌面';

  setSettingIconTab(iconType);
  if (iconType === 'text') {
    renderSettingTextIconCanvas();
  }

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

  const curIconType = state.activeSettingIconTab || 'upload';
  let iconChanged = curIconType !== (state.originalSettings.portal_icon_type || 'upload');
  if (curIconType === 'text') {
    const curText = (document.getElementById('setting-icon-text-input')?.value || '').trim();
    const curTextColor = (document.getElementById('setting-icon-text-color-hex')?.value || '#ffffff').trim();
    const curBgColor = (document.getElementById('setting-icon-bg-color-hex')?.value || '#1e293b').trim();
    if (curText !== (state.originalSettings.portal_icon_text || '') ||
        curTextColor.toLowerCase() !== (state.originalSettings.portal_icon_text_color || '#ffffff').toLowerCase() ||
        curBgColor.toLowerCase() !== (state.originalSettings.portal_icon_bg_color || '#1e293b').toLowerCase()) {
      iconChanged = true;
    }
  } else if (curIconType === 'url') {
    const curUrl = (document.getElementById('setting-icon-url-input')?.value || '').trim();
    if (curUrl !== (state.originalSettings.portal_icon || '')) {
      iconChanged = true;
    }
  } else if (curIconType === 'upload') {
    const curIcon = (document.getElementById('setting-portal-icon')?.value || '').trim();
    if (curIcon !== (state.originalSettings.portal_icon || '')) {
      iconChanged = true;
    }
  }

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
    } catch (err) {
      console.error('SSE parse error:', err);
    }
  };
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
  document.getElementById('host-name').textContent = host.hostname || '-';
  document.getElementById('host-os').textContent = host.os_pretty || host.os_name || '-';
  document.getElementById('host-kernel').textContent = host.kernel || '-';
  document.getElementById('host-arch').textContent = host.arch || '-';
  document.getElementById('host-ip').textContent = host.primary_ip || '-';

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
    const ips = (iface.ipv4 || []).join(', ') || '-';
    const status = iface.is_up ? '<span class="status-badge active">活跃</span>' : '<span class="status-badge paused">未激活</span>';
    html += `<tr>
      <td><code>${escapeHtml(iface.name)}</code></td>
      <td>${escapeHtml(iface.type_label || iface.type)}</td>
      <td><code>${escapeHtml(iface.mac || '-')}</code></td>
      <td><code>${escapeHtml(ips)}</code></td>
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
          <span class="btn-text">已在桌面</span><span class="btn-badge">${count}</span>
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

  const cpuText = p.cpu_percent > 0.1 ? `${p.cpu_percent.toFixed(1)}%` : '-';
  const rssText = p.mem_rss_bytes > 0 ? formatBytes(p.mem_rss_bytes) : '-';
  const resText = (cpuText === '-' && rssText === '-') ? '-' : `${cpuText} / ${rssText}`;

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
}

// --- Render Desktop Items Table ---
function renderDesktopTable() {
  const tbody = document.getElementById('desktop-tbody');
  if (!tbody) return;

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

  if (filtered.length === 0 && filteredWatchcow.length === 0) {
    tbody.innerHTML = '<tr><td colspan="10" class="empty-state">' + (query ? '未找到匹配的桌面图标' : '暂无已创建的桌面图标') + '</td></tr>';
    return;
  }

  let html = '';

  if (filtered.length === 0 && filteredWatchcow.length > 0) {
    html += '<tr><td colspan="10" class="empty-state" style="padding: 1.5rem 1rem;">暂无手动添加的桌面图标</td></tr>';
  } else {
    for (const item of filtered) {
      let modeText = '本机端口';
      let modeClass = 'text-type-local';
      let targetText = `:${item.port}`;
      if (item.mode === 'proxy') {
        modeText = '端口映射';
        modeClass = 'text-type-proxy';
        targetText = `${item.target_url} ➔ :${item.port}`;
      } else if (item.mode === 'shortcut') {
        modeText = '网页链接';
        modeClass = 'text-type-shortcut';
        targetText = item.target_url;
      }

      const hasIcon = !item.no_display;
      const hasContextMenu = Array.isArray(item.file_types) && item.file_types.length > 0;
      let entryText = '图标';
      if (hasIcon && hasContextMenu) {
        entryText = '图标 / 右键';
      } else if (hasIcon && !hasContextMenu) {
        entryText = '图标';
      } else if (!hasIcon && hasContextMenu) {
        entryText = '右键';
      } else {
        entryText = '-';
      }
      const entryHtml = `<span style="font-size: 0.88rem; color: var(--text-main);">${entryText}</span>`;

      const openModeText = item.ui_type === 'iframe' ? '内部弹窗' : '新标签页';
      const openModeClass = item.ui_type === 'iframe' ? 'text-open-modal' : 'text-open-tab';
      const permText = item.all_users ? '所有用户' : '仅管理员';
      const permClass = item.all_users ? 'text-perm-all' : 'text-perm-admin';
      const toggleHtml = `
        <div class="status-toggle-wrapper">
          <label class="toggle-switch" title="${item.enabled ? '点击停用' : '点击启用'}">
            <input type="checkbox" class="desktop-toggle-checkbox" data-id="${item.id}" ${item.enabled ? 'checked' : ''}>
            <span class="toggle-slider"></span>
          </label>
          <span class="status-toggle-label ${item.enabled ? 'active' : 'paused'}">
            ${item.enabled ? '就绪' : '已停用'}
          </span>
        </div>`;

      const iconSrc = getIconUrl(item.icon);

      let statusColHtml = toggleHtml;
      const isReconciling = !!item.reconciling;
      if (isReconciling || item._updating) {
        const statusText = item._statusText || item.status_text || (isReconciling ? '恢复中...' : '正在更新中...');
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
      const fileTypesBadge = (Array.isArray(item.file_types) && item.file_types.length > 0)
        ? `<span class="badge badge-secondary" style="font-size: 11px; margin-left: 6px; font-weight: normal; vertical-align: middle; background: rgba(100, 116, 139, 0.12); color: #64748b; border: 1px solid rgba(100, 116, 139, 0.3); padding: 1px 6px; border-radius: 4px;" title="支持右键打开文件扩展名: ${escapeHtml(item.file_types.join(', '))}">📄 ${escapeHtml(item.file_types.slice(0, 3).join(','))}${item.file_types.length > 3 ? '...' : ''}</span>`
        : '';

      html += `<tr class="${isUpdating ? 'row-updating' : ''}">
        <td>
          <img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/default_item_icon.png')}'" alt="图标">
        </td>
        <td><strong>${escapeHtml(item.name)}</strong>${fileTypesBadge}</td>
        <td><span class="${modeClass}">${modeText}</span></td>
        <td>${entryHtml}</td>
        <td><code>${escapeHtml(targetText)}</code></td>
        <td><span class="${openModeClass}">${openModeText}</span></td>
        <td><span class="${permClass}">${permText}</span></td>
        <td>${statusColHtml}</td>
        <td>
          <div class="table-actions">
            <button class="btn btn-sm btn-secondary btn-edit-desktop" data-id="${item.id}" ${isUpdating ? 'disabled style="opacity: 0.5; pointer-events: none;"' : ''}>
              <span>编辑</span>
            </button>
          </div>
        </td>
        <td class="filler-col"></td>
      </tr>`;
    }
  }

  if (filteredWatchcow.length > 0) {
    html += `
      <tr class="table-sink-divider-row" aria-hidden="true">
        <td colspan="10" class="table-sink-divider-cell">
          <span class="table-sink-title">以下内容读取自 Docker 项目的 Watchcow 标签</span>
        </td>
      </tr>`;

    for (const item of filteredWatchcow) {
      const iconSrc = getIconUrl(item.display_icon || item.icon);
      let modeText = '本机端口';
      let modeClass = 'text-type-local';
      let targetText = `:${item.port}${item.path && item.path !== '/' ? item.path : ''}`;
      if (item.mode === 'shortcut') {
        modeText = '网页链接';
        modeClass = 'text-type-shortcut';
        targetText = item.target_url || item.redirect || `:${item.port}`;
      } else if (item.mode === 'proxy') {
        modeText = '端口映射';
        modeClass = 'text-type-proxy';
        targetText = `${item.target_url} ➔ :${item.port}`;
      }

      const hasIcon = !item.no_display;
      const hasContextMenu = Array.isArray(item.file_types) && item.file_types.length > 0;
      let entryText = '图标';
      if (hasIcon && hasContextMenu) {
        entryText = '图标 / 右键';
      } else if (hasIcon && !hasContextMenu) {
        entryText = '图标';
      } else if (!hasIcon && hasContextMenu) {
        entryText = '右键';
      } else {
        entryText = '-';
      }
      const entryHtml = `<span style="font-size: 0.88rem; color: var(--text-main);">${entryText}</span>`;

      const openModeText = item.ui_type === 'iframe' ? '内部弹窗' : '新标签页';
      const openModeClass = item.ui_type === 'iframe' ? 'text-open-modal' : 'text-open-tab';
      const permText = item.all_users ? '所有用户' : '仅管理员';
      const permClass = item.all_users ? 'text-perm-all' : 'text-perm-admin';
      const fileTypesBadge = (Array.isArray(item.file_types) && item.file_types.length > 0)
        ? `<span class="badge badge-secondary" style="font-size: 11px; margin-left: 6px; font-weight: normal; vertical-align: middle; background: rgba(100, 116, 139, 0.12); color: #64748b; border: 1px solid rgba(100, 116, 139, 0.3); padding: 1px 6px; border-radius: 4px;" title="支持右键打开文件扩展名: ${escapeHtml(item.file_types.join(', '))}">📄 ${escapeHtml(item.file_types.slice(0, 3).join(','))}${item.file_types.length > 3 ? '...' : ''}</span>`
        : '';
      const containerHint = item.container_name
        ? `<div style="font-size: 0.76rem; color: var(--text-muted); font-weight: normal; margin-top: 2px;">${escapeHtml(item.container_name)}</div>`
        : '';

      const toggleHtml = `
        <div class="status-toggle-wrapper">
          <label class="toggle-switch" title="${item.enabled ? '点击停用' : '点击启用'}">
            <input type="checkbox" class="watchcow-toggle-checkbox" data-id="${escapeHtml(item.id)}" ${item.enabled ? 'checked' : ''}>
            <span class="toggle-slider"></span>
          </label>
          <span class="status-toggle-label ${item.enabled ? 'active' : 'paused'}">
            ${item.enabled ? '就绪' : '已停用'}
          </span>
        </div>`;

      html += `<tr>
        <td>
          <img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/default_item_icon.png')}'" alt="图标">
        </td>
        <td><strong>${escapeHtml(item.name)}</strong>${containerHint}${fileTypesBadge}</td>
        <td><span class="${modeClass}">${modeText}</span></td>
        <td>${entryHtml}</td>
        <td><code>${escapeHtml(targetText)}</code></td>
        <td><span class="${openModeClass}">${openModeText}</span></td>
        <td><span class="${permClass}">${permText}</span></td>
        <td>${toggleHtml}</td>
        <td>
          <span style="color: var(--text-muted); font-size: 0.82rem; user-select: none;">不可编辑</span>
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

      const wrapper = chk.closest('.status-toggle-wrapper');
      const label = wrapper ? wrapper.querySelector('.status-toggle-label') : null;
      const originalText = label ? label.textContent.trim() : '';

      reportClientLog('action', '用户切换桌面图标状态', `ID: ${id}, 目标状态: ${chk.checked ? '启用' : '停用'}`, { id, checked: chk.checked });

      // Immediately disable checkbox and show loading state
      chk.disabled = true;
      if (label) {
        label.className = 'status-toggle-label pending';
        label.textContent = '处理中...';
      }

      try {
        const res = await fetch(apiUrl(`/api/desktop/items/${id}/toggle`), { method: 'POST' });
        if (res.ok) {
          const updated = await res.json();
          const item = state.desktopItems.find(i => i.id === id);
          if (item) item.enabled = updated.enabled;
          showToast(`已成功${updated.enabled ? '启用' : '停用'}桌面图标`, 'success');
          renderDesktopTable();
          fetchPorts();
        } else {
          const errData = await res.json().catch(() => ({}));
          const errMsg = errData.error || res.statusText;
          showToast('切换状态失败: ' + errMsg, 'error', 5000);
          chk.checked = !chk.checked;
          if (label) {
            label.className = `status-toggle-label ${chk.checked ? 'active' : 'paused'}`;
            label.textContent = originalText;
          }
          chk.disabled = false;
        }
      } catch (e) {
        showToast('网络请求异常: ' + e.message, 'error', 5000);
        chk.checked = !chk.checked;
        if (label) {
          label.className = `status-toggle-label ${chk.checked ? 'active' : 'paused'}`;
          label.textContent = originalText;
        }
        chk.disabled = false;
      }
    });
  });

  tbody.querySelectorAll('.watchcow-toggle-checkbox').forEach(chk => {
    chk.addEventListener('change', async () => {
      const id = chk.dataset.id;
      if (chk.disabled) return;

      const wrapper = chk.closest('.status-toggle-wrapper');
      const label = wrapper ? wrapper.querySelector('.status-toggle-label') : null;
      const originalText = label ? label.textContent.trim() : '';

      reportClientLog('action', '用户切换Watchcow条目状态', `ID: ${id}, 目标状态: ${chk.checked ? '启用' : '停用'}`, { id, checked: chk.checked });

      chk.disabled = true;
      if (label) {
        label.className = 'status-toggle-label pending';
        label.textContent = '处理中...';
      }

      try {
        const res = await fetch(apiUrl(`/api/desktop/watchcow/${encodeURIComponent(id)}/toggle`), { method: 'POST' });
        if (res.ok) {
          const updated = await res.json();
          const item = (state.watchcowItems || []).find(i => i.id === id);
          if (item) item.enabled = updated.enabled;
          showToast(`已成功${updated.enabled ? '启用' : '停用'} Watchcow 图标`, 'success');
          renderDesktopTable();
          fetchPorts();
        } else {
          const errData = await res.json().catch(() => ({}));
          const errMsg = errData.error || res.statusText;
          showToast('切换状态失败: ' + errMsg, 'error', 5000);
          chk.checked = !chk.checked;
          if (label) {
            label.className = `status-toggle-label ${chk.checked ? 'active' : 'paused'}`;
            label.textContent = originalText;
          }
          chk.disabled = false;
        }
      } catch (e) {
        showToast('网络请求异常: ' + e.message, 'error', 5000);
        chk.checked = !chk.checked;
        if (label) {
          label.className = `status-toggle-label ${chk.checked ? 'active' : 'paused'}`;
          label.textContent = originalText;
        }
        chk.disabled = false;
      }
    });
  });

  tbody.querySelectorAll('.btn-edit-desktop').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = btn.dataset.id;
      openEditDesktopModal(id);
    });
  });
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
    iconTab: state.activeIconTab || 'text',
    iconText: document.getElementById('icon-text-input')?.value || '',
    iconTextColor: (document.getElementById('icon-text-color')?.value || '#ffffff').toLowerCase(),
    iconBgColor: (document.getElementById('icon-bg-color')?.value || '#1e293b').toLowerCase(),
    iconUrl: document.getElementById('icon-url-input')?.value || '',
    iconHidden: document.getElementById('item-icon')?.value || '',
    noticeContent: document.getElementById('item-notice-content')?.value || '',
    fileTypes: document.getElementById('item-file-types')?.value || '',
    noDisplay: !!document.getElementById('item-no-display')?.checked
  });
}

function saveDesktopItemFormSnapshot() {
  state.desktopItemFormSnapshot = getDesktopItemFormSnapshot();
}

function isDesktopItemFormDirty() {
  const modal = document.getElementById('modal-desktop-item');
  if (!modal || !modal.classList.contains('active')) return false;
  if (!state.desktopItemFormSnapshot) return false;
  return getDesktopItemFormSnapshot() !== state.desktopItemFormSnapshot;
}

function tryCloseDesktopItemModal() {
  if (isDesktopItemFormDirty()) {
    if (!confirm('当前图标内容已修改但尚未保存，确定要放弃修改并退出吗？')) {
      return;
    }
  }
  state.desktopItemFormSnapshot = null;
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

  // Icon upload
  const fileInput = document.getElementById('icon-file-input');
  if (fileInput) {
    fileInput.addEventListener('change', handleIconUpload);
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
  if (mode === 'shortcut') {
    const elUiType = document.getElementById('item-ui-type');
    if (elUiType) elUiType.value = 'url';
  }
}

// --- Icon 3-Tab Editor ---
function setIconModalTab(tabName) {
  state.activeIconTab = tabName;
  document.querySelectorAll('#modal-desktop-item .icon-tab').forEach(b => {
    b.classList.toggle('active', b.dataset.iconTab === tabName);
  });
  document.querySelectorAll('#modal-desktop-item .icon-tab-pane').forEach(p => {
    p.classList.toggle('active', p.id === `icon-pane-${tabName}`);
  });

  const previewImg = document.getElementById('icon-preview-img');
  const previewName = document.getElementById('icon-preview-name');

  if (tabName === 'text') {
    const text = document.getElementById('icon-text-input')?.value || '';
    if (text.trim()) {
      renderTextIconCanvas();
    } else {
      if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
      if (previewName) previewName.textContent = '默认图标';
    }
  } else if (tabName === 'url') {
    const url = document.getElementById('icon-url-input')?.value.trim() || '';
    if (url) {
      if (previewImg) {
        previewImg.src = url;
        previewImg.onerror = () => {
          previewImg.src = apiUrl('/default_item_icon.png');
          if (previewName) previewName.textContent = '图片载入失败';
        };
      }
      if (previewName) previewName.textContent = '网络图标';
    } else {
      if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
      if (previewName) previewName.textContent = '默认图标';
    }
  } else if (tabName === 'upload') {
    const val = document.getElementById('item-icon')?.value.trim() || '';
    if (val) {
      if (previewImg) previewImg.src = getIconUrl(val);
      if (previewName) previewName.textContent = val.split('/').pop();
    }
  }
}

function renderTextIconCanvas() {
  const textInput = document.getElementById('icon-text-input');
  const text = (textInput ? textInput.value : '').trim();
  const textColor = document.getElementById('icon-text-color')?.value || '#ffffff';
  const bgColor = document.getElementById('icon-bg-color')?.value || '#1e293b';
  const previewImg = document.getElementById('icon-preview-img');
  const previewName = document.getElementById('icon-preview-name');

  if (!text) {
    if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
    if (previewName) previewName.textContent = '默认图标';
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
  if (previewName) previewName.textContent = `文字: ${lines[0]}`;
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

function initIconEditor() {
  document.querySelectorAll('.icon-tab').forEach(tab => {
    tab.addEventListener('click', () => setIconModalTab(tab.dataset.iconTab));
  });

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
      const previewName = document.getElementById('icon-preview-name');
      if (!val) {
        if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
        if (previewName) previewName.textContent = '默认图标';
      } else {
        if (previewImg) {
          previewImg.src = val;
          previewImg.onerror = () => {
            previewImg.src = apiUrl('/default_item_icon.png');
            if (previewName) previewName.textContent = '图片载入失败';
          };
        }
        if (previewName) previewName.textContent = '网络图标';
      }
    });
  }

  const btnReset = document.getElementById('btn-reset-icon');
  if (btnReset) {
    btnReset.addEventListener('click', () => {
      document.getElementById('item-icon').value = '';
      if (textInput) textInput.value = '';
      if (urlInput) urlInput.value = '';
      updateTextColorUI('#ffffff');
      updateBgColorUI('#1e293b');
      closeColorPopovers();
      state.currentTextIconDataUrl = null;
      const previewImg = document.getElementById('icon-preview-img');
      const previewName = document.getElementById('icon-preview-name');
      if (previewImg) previewImg.src = apiUrl('/default_item_icon.png');
      if (previewName) previewName.textContent = '默认图标';
      showToast('已恢复为默认图标', 'info');
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

  const previewImg = document.getElementById('setting-icon-preview-img');
  const previewName = document.getElementById('setting-icon-preview-name');
  const displayName = '把 Docker 放到桌面';

  if (tabName === 'text') {
    const text = document.getElementById('setting-icon-text-input')?.value || '';
    if (text.trim()) {
      renderSettingTextIconCanvas();
    } else {
      if (previewImg) previewImg.src = apiUrl('/icon.png');
      if (previewName) previewName.textContent = displayName;
    }
  } else if (tabName === 'url') {
    const url = document.getElementById('setting-icon-url-input')?.value.trim() || '';
    if (url) {
      if (previewImg) {
        previewImg.src = url;
        previewImg.onerror = () => {
          previewImg.src = apiUrl('/icon.png');
          if (previewName) previewName.textContent = '图片载入失败';
        };
      }
      if (previewName) previewName.textContent = displayName;
    } else {
      if (previewImg) previewImg.src = apiUrl('/icon.png');
      if (previewName) previewName.textContent = displayName;
    }
  } else if (tabName === 'upload') {
    const val = document.getElementById('setting-portal-icon')?.value.trim() || '';
    if (val && val !== 'icon.png') {
      if (previewImg) previewImg.src = getIconUrl(val);
      if (previewName) previewName.textContent = displayName;
    } else {
      if (previewImg) previewImg.src = apiUrl('/icon.png');
      if (previewName) previewName.textContent = displayName;
    }
  }
}

function renderSettingTextIconCanvas() {
  const textInput = document.getElementById('setting-icon-text-input');
  const text = (textInput ? textInput.value : '').trim();
  const textColor = document.getElementById('setting-icon-text-color')?.value || '#ffffff';
  const bgColor = document.getElementById('setting-icon-bg-color')?.value || '#1e293b';
  const previewImg = document.getElementById('setting-icon-preview-img');
  const previewName = document.getElementById('setting-icon-preview-name');
  const displayName = '把 Docker 放到桌面';

  if (!text) {
    if (previewImg) previewImg.src = apiUrl('/icon.png');
    if (previewName) previewName.textContent = displayName;
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
  if (previewName) previewName.textContent = displayName;
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

  const textInput = document.getElementById('setting-icon-text-input');
  if (textInput) {
    textInput.addEventListener('input', () => {
      renderSettingTextIconCanvas();
      checkSettingsDirty();
    });
  }

  // Live update preview name when portal name input changes
  const nameInput = document.getElementById('setting-portal-name');
  if (nameInput) {
    nameInput.addEventListener('input', () => {
      const previewName = document.getElementById('setting-icon-preview-name');
      if (previewName) previewName.textContent = nameInput.value.trim() || '把 Docker 放到桌面';
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
      const previewName = document.getElementById('setting-icon-preview-name');
      const displayName = '把 Docker 放到桌面';
      if (!val) {
        if (previewImg) previewImg.src = apiUrl('/icon.png');
        if (previewName) previewName.textContent = displayName;
      } else {
        if (previewImg) {
          previewImg.src = val;
          previewImg.onerror = () => {
            previewImg.src = apiUrl('/icon.png');
            if (previewName) previewName.textContent = '图片载入失败';
          };
        }
        if (previewName) previewName.textContent = displayName;
      }
      checkSettingsDirty();
    });
  }

  const fileInput = document.getElementById('setting-icon-file-input');
  if (fileInput) {
    fileInput.addEventListener('change', async (e) => {
      const originalFile = e.target.files[0];
      if (!originalFile) return;

      if (originalFile.size > 10 * 1024 * 1024) {
        showToast('上传图标文件不能超过 10MB', 'error');
        e.target.value = '';
        return;
      }

      const elIcon = document.getElementById('setting-portal-icon');
      const imgEl = document.getElementById('setting-icon-preview-img');
      const statusEl = document.getElementById('setting-icon-upload-status');

      if (statusEl) statusEl.textContent = '正在处理并上传图标...';

      let file = originalFile;
      try {
        file = await compressIconFile(originalFile, 256);
      } catch (err) {
        showToast(err.message, 'error');
        if (statusEl) statusEl.textContent = '上传失败';
        e.target.value = '';
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
          if (elIcon) elIcon.value = data.url;
          if (imgEl) imgEl.src = apiUrl(data.url);
          if (statusEl) statusEl.textContent = `已选择: ${file.name}`;
          checkSettingsDirty();
          showToast(`自身桌面图标「${file.name}」上传成功`, 'success');
        } else {
          if (statusEl) statusEl.textContent = '上传失败';
          showToast(data.error || '上传图标失败', 'error');
        }
      } catch (err) {
        if (statusEl) statusEl.textContent = '网络异常';
        showToast('上传图标网络异常: ' + err.message, 'error');
      }
    });
  }

  const btnReset = document.getElementById('setting-btn-reset-icon');
  if (btnReset) {
    btnReset.addEventListener('click', () => {
      const elSettingIcon = document.getElementById('setting-portal-icon');
      if (elSettingIcon) elSettingIcon.value = 'icon.png';
      if (textInput) textInput.value = '';
      if (urlInput) urlInput.value = '';
      const statusEl = document.getElementById('setting-icon-upload-status');
      if (statusEl) statusEl.textContent = '支持 PNG、JPG、SVG、ICO 格式（限 10MB 内，位图自动压缩）';
      const fileInp = document.getElementById('setting-icon-file-input');
      if (fileInp) fileInp.value = '';
      updateSettingTextColorUI('#ffffff');
      updateSettingBgColorUI('#1e293b');
      closeSettingColorPopovers();
      state.currentSettingTextIconDataUrl = null;
      const previewImg = document.getElementById('setting-icon-preview-img');
      const previewName = document.getElementById('setting-icon-preview-name');
      if (previewImg) previewImg.src = apiUrl('/icon.png?t=' + Date.now());
      if (previewName) previewName.textContent = '把 Docker 放到桌面';
      setSettingIconTab('upload');
      checkSettingsDirty();
      showToast('已恢复为默认图标，请点击下方「保存」生效', 'info');
    });
  }
}

function resetDesktopForm() {
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
  const uploadStatus = document.getElementById('icon-upload-status');
  if (uploadStatus) uploadStatus.textContent = '支持 PNG、JPG、SVG、ICO 格式（限 10MB 内，位图自动压缩）';
  state.currentTextIconDataUrl = null;
  setIconModalTab('text');

  document.getElementById('icon-preview-img').src = apiUrl('/default_item_icon.png');
  document.getElementById('icon-preview-name').textContent = '默认图标';
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
    if (cleanName) {
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
        if (elIcon && elIcon.value === cdnUrl) elIcon.value = '';
        document.getElementById('icon-preview-name').textContent = '默认快捷方式图标';
      };
      document.getElementById('icon-preview-name').textContent = `${cleanName}.png (官方推荐)`;
    }
  }

  setDesktopModalMode('local');
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
  const itemIconType = item.icon_type || (item.icon && (item.icon.includes('text-icon-') ? 'text' : (item.icon.startsWith('http://') || item.icon.startsWith('https://') ? 'url' : 'upload'))) || 'text';

  const textInput = document.getElementById('icon-text-input');
  const urlInput = document.getElementById('icon-url-input');
  const textColorInput = document.getElementById('icon-text-color');
  const textColorHex = document.getElementById('icon-text-color-hex');
  const bgColorInput = document.getElementById('icon-bg-color');
  const bgColorHex = document.getElementById('icon-bg-color-hex');
  const uploadStatus = document.getElementById('icon-upload-status');
  const previewImg = document.getElementById('icon-preview-img');
  const previewName = document.getElementById('icon-preview-name');

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
      if (previewName) previewName.textContent = item.icon.split('/').pop();
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
        if (previewName) previewName.textContent = '图片载入失败';
      };
    }
    if (previewName) previewName.textContent = item.icon ? '网络图标' : '默认图标';
  } else if (itemIconType === 'upload') {
    setIconModalTab('upload');
    document.getElementById('item-icon').value = item.icon || '';
    if (uploadStatus) uploadStatus.textContent = item.icon ? `已使用: ${item.icon}` : '支持 PNG、JPG、SVG、ICO 格式';
    if (previewImg) {
      previewImg.src = getIconUrl(item.icon);
      previewImg.onerror = () => {
        previewImg.src = apiUrl('/default_item_icon.png');
        if (previewName) previewName.textContent = '图片载入失败';
      };
    }
    if (previewName) previewName.textContent = item.icon ? item.icon.split('/').pop() : '默认图标';
  }

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
    const openModeClass = item.ui_type === 'iframe' ? 'text-open-modal' : 'text-open-tab';
    const statusText = item.enabled ? '<span class="status-badge active">就绪</span>' : '<span class="status-badge paused">已停用</span>';

    html += `<tr>
      <td><img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/default_item_icon.png')}'" alt="图标"></td>
      <td><strong>${escapeHtml(item.name)}</strong>${item.notice_enabled && (item.notice_content || '').trim() ? ' <span style="font-size: 11px; color: #3b82f6;" title="已开启启动前提醒公告">📢</span>' : ''}</td>
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
    } else if (state.activeIconTab === 'upload') {
      iconType = 'upload';
      icon = (document.getElementById('item-icon')?.value || '').trim();
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
    closeModal('modal-desktop-item');

    // Update in-memory state FIRST so table and badges have it before any tab switch or fetch
    if (id) {
      const existing = state.desktopItems.find(i => i.id === id);
      if (existing) {
        existing._updating = true;
        existing._error = false;
        existing._statusText = '正在更新中...';
        existing.name = name;
        existing.port = port;
        existing.app_name = appName;
        if (icon) existing.icon = icon;
        existing.icon_type = iconType;
        existing.icon_text = iconText;
        existing.icon_text_color = iconTextColor;
        existing.icon_bg_color = iconBgColor;
        existing.notice_enabled = noticeEnabled;
        existing.notice_content = noticeContent;
      }
    } else {
      state.desktopItems.unshift({
        ...payload,
        _updating: true,
        _error: false,
        _statusText: '正在创建中...',
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

async function handleIconUpload(e) {
  const originalFile = e.target.files[0];
  if (!originalFile) return;

  if (originalFile.size > 10 * 1024 * 1024) {
    showToast('上传图标文件不能超过 10MB', 'error');
    e.target.value = '';
    return;
  }

  const elIcon = document.getElementById('item-icon');
  const imgEl = document.getElementById('icon-preview-img');
  const nameEl = document.getElementById('icon-preview-name');

  if (nameEl) nameEl.textContent = '正在处理并上传图标...';

  let file = originalFile;
  try {
    file = await compressIconFile(originalFile, 256);
  } catch (err) {
    showToast(err.message, 'error');
    if (nameEl) nameEl.textContent = '上传失败';
    e.target.value = '';
    return;
  }

  reportClientLog('action', '用户上传本地图标文件', `文件名: ${file.name}, 大小: ${file.size}字节`, { name: file.name, size: file.size, type: file.type });

  const formData = new FormData();
  formData.append('icon', file);

  try {
    const res = await fetch(apiUrl('/api/icons/upload'), {
      method: 'POST',
      body: formData,
    });
    const data = await res.json();
    if (res.ok && data.url) {
      if (elIcon) elIcon.value = data.url;
      if (imgEl) imgEl.src = apiUrl(data.url);
      if (nameEl) nameEl.textContent = file.name;
      showToast(`本地图标「${file.name}」已成功保存`, 'success');
    } else {
      if (nameEl) nameEl.textContent = '上传失败';
      showToast(data.error || '上传图标失败', 'error');
    }
  } catch (err) {
    if (nameEl) nameEl.textContent = '网络异常';
    showToast('上传图标网络异常: ' + err.message, 'error');
  }
}

function handleCancelSettings() {
  if (state.isSettingsDirty) {
    if (!confirm('当前设置有未保存的修改，确定要放弃修改并恢复吗？')) {
      return;
    }
  }
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
  let iconType = activeTabEl?.dataset.settingIconTab || state.activeSettingIconTab || 'upload';
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
  } else if (iconType === 'upload') {
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
      state.isSettingsDirty = false;
      const savedName = '把 Docker 放到桌面';
      document.title = `${savedName} - 容器与端口管理`;
      const titleEl = document.getElementById('settings-card-title');
      if (titleEl) {
        const ver = state.settings?.version || '1.1.18';
        titleEl.textContent = `v${ver} - 系统设置`;
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
      <td>${addr.pid > 0 ? addr.pid : '-'}</td>
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
      <div><strong>${escapeHtml(port.process_name || '-')}</strong> (PID: ${port.pid || '-'})</div>
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
          </tr>
        </thead>
        <tbody>
          ${endpointsHtml || '<tr><td colspan="4">无详细端点</td></tr>'}
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
    });
  }

  fetchPorts();
  fetchDesktopItems();
  fetchHost();
  initEventSource();
  initLogViewer();
}

// --- System Logs Viewer (8 Days Retention) ---
function initLogViewer() {
  const dateSelect = document.getElementById('log-date-select');
  if (dateSelect) {
    dateSelect.addEventListener('change', (e) => {
      state.logDate = e.target.value;
      fetchLogs();
    });
  }

  document.querySelectorAll('#log-level-chips .chip').forEach(chip => {
    chip.addEventListener('click', () => {
      document.querySelectorAll('#log-level-chips .chip').forEach(c => c.classList.remove('active'));
      chip.classList.add('active');
      state.logLevel = chip.dataset.logLevel || 'ALL';
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
}

async function fetchLogs(isAutoPoll = false) {
  try {
    let url = apiUrl(`/api/logs?level=${encodeURIComponent(state.logLevel || 'ALL')}`);
    if (state.logDate) {
      url += `&date=${encodeURIComponent(state.logDate)}`;
    }
    if (state.logSearch) {
      url += `&search=${encodeURIComponent(state.logSearch)}`;
    }

    const res = await fetch(url);
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      const data = await res.json();
      state.logs = data.lines || [];

      updateDateDropdown(data.dates, data.current_date);

      const pathEl = document.getElementById('log-path-display');
      if (pathEl && data.log_path) {
        pathEl.textContent = data.log_path;
      }
      const titleEl = document.getElementById('terminal-title');
      if (titleEl && data.current_date) {
        titleEl.textContent = `app-${data.current_date}.log (${formatBytes(data.file_size || 0)})`;
      }
      const totalEl = document.getElementById('log-total-count');
      if (totalEl) {
        totalEl.textContent = data.total_lines || 0;
      }
      const displayEl = document.getElementById('log-display-count');
      if (displayEl) {
        displayEl.textContent = state.logs.length;
      }

      renderLogs(isAutoPoll);
    }
  } catch (err) {
    console.error('Fetch logs error:', err);
  }
}

function updateDateDropdown(dates, currentDate) {
  const select = document.getElementById('log-date-select');
  if (!select || !dates || dates.length === 0) return;

  const currentVal = state.logDate || select.value || currentDate;
  const existingOptions = Array.from(select.options).map(o => o.value);
  const isSame = dates.length === existingOptions.length && dates.every((d, i) => d === existingOptions[i]);

  if (!isSame) {
    select.innerHTML = '';
    dates.forEach(d => {
      const opt = document.createElement('option');
      opt.value = d;
      opt.textContent = d;
      if (d === currentVal) {
        opt.selected = true;
      }
      select.appendChild(opt);
    });
  } else if (currentVal && select.value !== currentVal) {
    select.value = currentVal;
  }
}

function renderLogs(isAutoPoll = false) {
  const body = document.getElementById('terminal-log-body');
  if (!body) return;

  if (!state.logs || state.logs.length === 0) {
    body.innerHTML = '<div class="log-empty-state">暂无日志记录</div>';
    return;
  }

  const wasAtBottom = body.scrollHeight - body.scrollTop - body.clientHeight < 80;

  const html = state.logs.map((entry, idx) => {
    const lineNum = idx + 1;
    const level = entry.level || 'INFO';
    const badgeClass = `log-badge-${level.toLowerCase()}`;
    return `<div class="log-line">
      <span class="log-num">${lineNum}</span>
      <span class="log-time">${escapeHtml(entry.timestamp || '')}</span>
      <span class="log-badge ${badgeClass}">${escapeHtml(level)}</span>
      <span class="log-text">${escapeHtml(entry.message || entry.raw)}</span>
    </div>`;
  }).join('');

  body.innerHTML = html;

  if (!isAutoPoll || wasAtBottom) {
    body.scrollTop = body.scrollHeight;
  }
}

function scrollLogsToBottom() {
  const body = document.getElementById('terminal-log-body');
  if (body) {
    body.scrollTop = body.scrollHeight;
  }
}

function downloadLogFile() {
  const select = document.getElementById('log-date-select');
  const date = select ? select.value : '';
  window.open(apiUrl(`/api/logs/download?date=${encodeURIComponent(date)}`), '_blank');
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
