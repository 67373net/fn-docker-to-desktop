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
  if (!icon || icon === 'icon.png' || icon === '/icon.png') return apiUrl('/icon.png');
  if (icon.startsWith('http://') || icon.startsWith('https://') || icon.startsWith('data:')) {
    return icon;
  }
  const clean = icon.replace(/^\/?icons\//, '').replace(/^\/+/, '');
  if (!clean || clean === 'icon.png') return apiUrl('/icon.png');
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

async function fetchDesktopItems() {
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
    }
  } catch (err) {
    console.error('Fetch desktop items error:', err);
  }
}

function handleExportDesktopItems() {
  const items = state.desktopItems || [];
  const exportData = {
    version: state.settings?.version || '1.1.12',
    exported_at: new Date().toISOString(),
    total: items.length,
    items: items.map(item => {
      const { _updating, _error, _statusText, ...cleanItem } = item;
      return cleanItem;
    }),
  };
  const jsonStr = JSON.stringify(exportData, null, 2);
  const blob = new Blob([jsonStr], { type: 'application/json;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  const d = new Date();
  const dateStr = d.getFullYear() +
    String(d.getMonth() + 1).padStart(2, '0') +
    String(d.getDate()).padStart(2, '0') + '-' +
    String(d.getHours()).padStart(2, '0') +
    String(d.getMinutes()).padStart(2, '0') +
    String(d.getSeconds()).padStart(2, '0');
  a.href = url;
  a.download = `fn-desktop-icons-${dateStr}.json`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
  showToast(`已成功导出 ${items.length} 个桌面图标配置`, 'success');
  reportClientLog('action', '用户导出桌面图标配置', `导出数量: ${items.length}`);
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
      };
      state.isSettingsDirty = false;
      updateSettingsForm();
    }
  } catch (err) {
    console.error('Fetch settings error:', err);
  }
}

function updateSettingsForm() {
  const elName = document.getElementById('setting-portal-name');
  if (elName) elName.value = state.originalSettings?.portal_name || '把 Docker 放到桌面';

  const titleEl = document.getElementById('settings-card-title');
  if (titleEl && state.settings && state.settings.version) {
    titleEl.textContent = `把 Docker 放到桌面 v${state.settings.version} - 自身桌面图标设置`;
  }

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
  state.isSettingsDirty = false;
}

function checkSettingsDirty() {
  if (!state.originalSettings) return;
  const curName = document.getElementById('setting-portal-name')?.value.trim() || '';
  const rAll = document.querySelector('input[name="setting-portal-all-users"]:checked');
  const curAll = rAll ? rAll.value === 'true' : false;
  const curPwd = document.getElementById('setting-portal-password')?.value || '';
  const curPwdConfirm = document.getElementById('setting-portal-password-confirm')?.value || '';

  const nameChanged = curName !== state.originalSettings.portal_name;
  const allUsersChanged = curAll !== state.originalSettings.portal_all_users;
  const pwdChanged = curPwd !== '' || curPwdConfirm !== '';

  state.isSettingsDirty = nameChanged || allUsersChanged || pwdChanged;

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
      const data = JSON.parse(e.data);
      if (data.system) {
        state.system = data.system;
        updateSystemMetrics(data.system);
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
  if (badge) badge.textContent = state.desktopItems.length;
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
      (containerName && containerName.includes(r)) ||
      (imageName && imageName.includes(r)) ||
      (procName && procName.includes(r)) ||
      (exeName && exeName.includes(r))
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
    return item.name.toLowerCase().includes(query) ||
      (item.target_url || '').toLowerCase().includes(query) ||
      String(item.port).includes(query);
  });

  if (filtered.length === 0) {
    tbody.innerHTML = '<tr><td colspan="9" class="empty-state">暂无已创建的桌面图标</td></tr>';
    return;
  }

  let html = '';
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
    if (item._updating) {
      statusColHtml = `
        <div class="status-updating-badge">
          <span class="spinner-small"></span>
          <span>${escapeHtml(item._statusText || '正在更新中...')}</span>
        </div>`;
    } else if (item._error) {
      statusColHtml = `
        <div class="status-error-badge" style="display: inline-flex; align-items: center; gap: 4px; color: #ef4444; font-size: 0.82rem; font-weight: 500;" title="${escapeHtml(item._statusText || '')}">
          <span>⚠️</span>
          <span>${escapeHtml(item._statusText || '操作失败')}</span>
        </div>`;
    }

    const isUpdating = !!item._updating;

    html += `<tr class="${isUpdating ? 'row-updating' : ''}">
      <td>
        <img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/icon.png')}'" alt="图标">
      </td>
      <td><strong>${escapeHtml(item.name)}</strong></td>
      <td><span class="${modeClass}">${modeText}</span></td>
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

function initModals() {
  document.querySelectorAll('.modal-close').forEach(btn => {
    btn.addEventListener('click', () => {
      const target = btn.dataset.close;
      if (target) closeModal(target);
    });
  });

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
    });
  }

  const btnQuickAdd = document.getElementById('btn-quick-add-desktop');
  if (btnQuickAdd) {
    btnQuickAdd.addEventListener('click', () => {
      resetDesktopForm();
      openModal('modal-desktop-item');
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
  if (mode === 'shortcut') {
    const elUiType = document.getElementById('item-ui-type');
    if (elUiType) elUiType.value = 'url';
  }
}

// --- Icon 3-Tab Editor ---
function setIconModalTab(tabName) {
  state.activeIconTab = tabName;
  document.querySelectorAll('.icon-tab').forEach(b => {
    b.classList.toggle('active', b.dataset.iconTab === tabName);
  });
  document.querySelectorAll('.icon-tab-pane').forEach(p => {
    p.classList.toggle('active', p.id === `icon-pane-${tabName}`);
  });

  const previewImg = document.getElementById('icon-preview-img');
  const previewName = document.getElementById('icon-preview-name');

  if (tabName === 'text') {
    const text = document.getElementById('icon-text-input')?.value || '';
    if (text.trim()) {
      renderTextIconCanvas();
    } else {
      if (previewImg) previewImg.src = apiUrl('/icon.png');
      if (previewName) previewName.textContent = '默认图标';
    }
  } else if (tabName === 'url') {
    const url = document.getElementById('icon-url-input')?.value.trim() || '';
    if (url) {
      if (previewImg) {
        previewImg.src = url;
        previewImg.onerror = () => {
          previewImg.src = apiUrl('/icon.png');
          if (previewName) previewName.textContent = '图片载入失败';
        };
      }
      if (previewName) previewName.textContent = '网络图标';
    } else {
      if (previewImg) previewImg.src = apiUrl('/icon.png');
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
    if (previewImg) previewImg.src = apiUrl('/icon.png');
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
  bg: { hue: 215, s: 0.44, v: 0.23, isDragging: false }
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
    } else {
      updateBgColorUI(hex, false);
    }
    renderTextIconCanvas();
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
    } else {
      updateBgColorUI(hex, false);
    }
    renderTextIconCanvas();
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

  // Popover inner tab buttons (任意颜色 / 预设推荐)
  document.querySelectorAll('.color-popover-tab-btn').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const target = btn.dataset.target;
      const tab = btn.dataset.tab;
      const pop = document.getElementById(`popover-${target}-color`);
      if (!pop) return;
      pop.querySelectorAll('.color-popover-tab-btn').forEach(b => b.classList.toggle('active', b === btn));
      const customPane = document.getElementById(`pane-${target}-color-custom`);
      const presetsPane = document.getElementById(`pane-${target}-color-presets`);
      if (customPane) customPane.classList.toggle('active', tab === 'custom');
      if (presetsPane) presetsPane.classList.toggle('active', tab === 'presets');
      if (tab === 'custom') {
        const canvas = document.getElementById(`spectrum-canvas-${target}`);
        if (canvas) drawColorSpectrum(canvas, spectrumPickers[target].hue);
      }
    });
  });

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
        if (previewImg) previewImg.src = apiUrl('/icon.png');
        if (previewName) previewName.textContent = '默认图标';
      } else {
        if (previewImg) {
          previewImg.src = val;
          previewImg.onerror = () => {
            previewImg.src = apiUrl('/icon.png');
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
      if (previewImg) previewImg.src = apiUrl('/icon.png');
      if (previewName) previewName.textContent = '默认图标';
      showToast('已恢复为默认图标', 'info');
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
  if (uploadStatus) uploadStatus.textContent = '支持 PNG、JPG、SVG、ICO 格式';
  state.currentTextIconDataUrl = null;
  setIconModalTab('text');

  document.getElementById('icon-preview-img').src = apiUrl('/icon.png');
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
        imgEl.src = apiUrl('/icon.png');
        if (urlInput) urlInput.value = '';
        if (elIcon && elIcon.value === cdnUrl) elIcon.value = '';
        document.getElementById('icon-preview-name').textContent = '默认容器图标';
      };
      document.getElementById('icon-preview-name').textContent = `${cleanName}.png (官方推荐)`;
    }
  }

  setDesktopModalMode('local');
  openModal('modal-desktop-item');
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
      showToast(`正在从桌面移出「${item.name}」...`, 'info');

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
        previewImg.src = apiUrl('/icon.png');
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
        previewImg.src = apiUrl('/icon.png');
        if (previewName) previewName.textContent = '图片载入失败';
      };
    }
    if (previewName) previewName.textContent = item.icon ? item.icon.split('/').pop() : '默认图标';
  }

  openModal('modal-desktop-item');
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
      <td><img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/icon.png')}'" alt="图标"></td>
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
        showToast('正在生成并上传文字图标...', 'info');
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
      enabled,
    };

    // Close modal immediately
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

async function handleIconUpload(e) {
  const file = e.target.files[0];
  if (!file) return;

  reportClientLog('action', '用户上传本地图标文件', `文件名: ${file.name}, 大小: ${file.size}字节`, { name: file.name, size: file.size, type: file.type });

  const elIcon = document.getElementById('item-icon');
  const imgEl = document.getElementById('icon-preview-img');
  const nameEl = document.getElementById('icon-preview-name');

  if (nameEl) nameEl.textContent = '正在上传图标...';

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
  showToast('已恢复设置', 'info');
}

async function handleSaveSettingsManual() {
  const name = document.getElementById('setting-portal-name').value.trim();
  const rAll = document.querySelector('input[name="setting-portal-all-users"]:checked');
  const allUsers = rAll ? rAll.value === 'true' : false;

  reportClientLog('action', '用户保存系统设置', `名称: ${name}, 用户范围: ${allUsers ? '所有用户' : '仅管理员'}`, { name, allUsers });

  const pwd = document.getElementById('setting-portal-password').value;
  const pwdConfirm = document.getElementById('setting-portal-password-confirm').value;
  const matchTip = document.getElementById('password-match-tip');
  const statusEl = document.getElementById('settings-status');

  const payload = {
    portal_name: name || '把 Docker 放到桌面',
    portal_ui_type: 'iframe',
    portal_all_users: allUsers,
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
      };
      state.isSettingsDirty = false;
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
      <div style="font-size: 0.9rem; color: var(--text-muted); margin-bottom: 0.3rem;">关联进程 / 容器</div>
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
      showToast('进程列表已刷新', 'info');
    });
  }

  const btnRefreshDesktop = document.getElementById('btn-refresh-desktop');
  if (btnRefreshDesktop) {
    btnRefreshDesktop.addEventListener('click', () => {
      fetchDesktopItems();
      showToast('桌面图标列表已刷新', 'info');
    });
  }

  const btnRefreshProcs = document.getElementById('btn-refresh-procs');
  if (btnRefreshProcs) {
    btnRefreshProcs.addEventListener('click', () => {
      fetchProcesses();
      showToast('系统进程列表已刷新', 'info');
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

  const btnScrollBottom = document.getElementById('btn-scroll-bottom');
  if (btnScrollBottom) {
    btnScrollBottom.addEventListener('click', () => {
      scrollLogsToBottom();
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
