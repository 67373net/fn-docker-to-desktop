// --- 把 Docker 放到桌面 - 前端应用程序 ---

const BASE_PATH = window.location.pathname.startsWith('/app/fn-docker-to-desktop')
  ? '/app/fn-docker-to-desktop'
  : '';

function apiUrl(path) {
  return BASE_PATH + path;
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
      navigator.sendBeacon(apiUrl('/api/logs/client'), blob);
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
  portFilters: new Set(['docker']), // 默认筛选 Docker 容器
  portSearch: '',
  desktopSearch: '',
  procSearch: '',
  procSort: 'cpu',
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
  } else if (tab === 'system') {
    fetchSystem();
    fetchHost();
  } else if (tab === 'logs') {
    fetchLogs();
  } else if (tab === 'settings') {
    fetchSettings();
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
      const elName = document.getElementById('setting-portal-name');
      if (elName) elName.value = settings.portal_name || '把 Docker 放到桌面';

      const titleEl = document.getElementById('settings-card-title');
      if (titleEl && settings.version) {
        titleEl.textContent = `把 Docker 放到桌面 v${settings.version} - 自身桌面图标设置`;
      }

      const allUsers = settings.portal_all_users ? 'true' : 'false';
      const rAll = document.querySelector(`input[name="setting-portal-all-users"][value="${allUsers}"]`);
      if (rAll) rAll.checked = true;

      const pwdEl = document.getElementById('setting-portal-password');
      const pwdConfirmEl = document.getElementById('setting-portal-password-confirm');
      const tipEl = document.getElementById('password-match-tip');
      if (pwdEl) pwdEl.value = '';
      if (pwdConfirmEl) pwdConfirmEl.value = '';
      if (tipEl) tipEl.textContent = '';
    }
  } catch (err) {
    console.error('Fetch settings error:', err);
  }
}

// --- SSE Real-time Updates ---
function initEventSource() {
  if (state.eventSource) {
    state.eventSource.close();
  }
  state.eventSource = new EventSource(apiUrl('/api/events'));
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

    // Multi-select Filter chip matching
    const filters = state.portFilters;
    if (filters.has('all')) {
      return true;
    }

    const hasDocker = filters.has('docker');
    const hasHost = filters.has('host');
    const isDocker = !!(p.docker && p.docker.is_docker);

    if (hasDocker && !hasHost) {
      if (!isDocker) return false;
    } else if (hasHost && !hasDocker) {
      if (isDocker) return false;
    }

    const hasTcp = filters.has('tcp');
    const hasUdp = filters.has('udp');
    const protoStr = (p.protocol || '').toLowerCase();
    const isTcp = (p.protocols || []).some(x => x.toLowerCase().includes('tcp')) || protoStr.includes('tcp');
    const isUdp = (p.protocols || []).some(x => x.toLowerCase().includes('udp')) || protoStr.includes('udp');

    if (hasTcp && !hasUdp) {
      if (!isTcp) return false;
    } else if (hasUdp && !hasTcp) {
      if (!isUdp) return false;
    }

    return true;
  });

  if (filtered.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty-state">没有符合条件的端口</td></tr>';
    return;
  }

  let html = '';
  for (const p of filtered) {
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
            <span>已在桌面(${count})</span>
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

    html += `<tr>
      <td>
        <a href="${portUrl}" target="_blank" rel="noopener noreferrer" class="port-link" title="在浏览器新窗口打开 ${portUrl}">
          <span>${p.local_port}</span>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"></path><polyline points="15 3 21 3 21 9"></polyline><line x1="10" y1="14" x2="21" y2="3"></line></svg>
        </a>
      </td>
      <td class="col-hide-minimal">
        <span class="protocol-tag">${escapeHtml(p.protocol)}</span>
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
    let targetText = `:${item.port}`;
    if (item.mode === 'proxy') {
      modeText = '代理服务';
      targetText = `${item.target_url} ➔ :${item.port}`;
    } else if (item.mode === 'shortcut') {
      modeText = '网页快捷';
      targetText = item.target_url;
    }

    const openModeText = item.ui_type === 'iframe' ? '飞牛内部弹窗' : '浏览器新标签';
    const permText = item.all_users ? '所有用户' : '仅管理员';
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

    const iconSrc = item.icon ? (item.icon.startsWith('http') || item.icon.startsWith('data:') ? item.icon : apiUrl(`/icons/${item.icon.replace(/^icons\//, '')}`)) : apiUrl('/icon.png');

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
      <td><span class="protocol-tag">${modeText}</span></td>
      <td><code>${escapeHtml(targetText)}</code></td>
      <td>${openModeText}</td>
      <td>${permText}</td>
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

  if (state.procSort === 'cpu') {
    list.sort((a, b) => b.cpu_percent - a.cpu_percent);
  } else if (state.procSort === 'mem') {
    list.sort((a, b) => b.mem_rss_bytes - a.mem_rss_bytes);
  } else if (state.procSort === 'pid') {
    list.sort((a, b) => a.pid - b.pid);
  }

  if (list.length === 0) {
    tbody.innerHTML = '<tr><td colspan="9" class="empty-state">没有符合条件的进程</td></tr>';
    return;
  }

  let html = '';
  for (const p of list.slice(0, 150)) { // Limit to top 150 for peak DOM performance
    const dockerTag = (p.docker && p.docker.is_docker) ? `<span class="protocol-tag tag-docker">${escapeHtml(p.docker.container_name)}</span>` : '-';
    html += `<tr>
      <td><code>${p.pid}</code></td>
      <td><strong>${escapeHtml(p.name)}</strong></td>
      <td>${escapeHtml(p.user || 'root')}</td>
      <td>${escapeHtml(p.state)}</td>
      <td style="font-variant-numeric: tabular-nums;">${p.cpu_percent.toFixed(1)}%</td>
      <td style="font-variant-numeric: tabular-nums;">${formatBytes(p.mem_rss_bytes)}</td>
      <td style="font-variant-numeric: tabular-nums;">${formatRate(p.io_read_rate + p.io_write_rate)}</td>
      <td>${dockerTag}</td>
      <td class="filler-col"></td>
    </tr>`;
  }
  tbody.innerHTML = html;
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

  // Icon preset chips click listener
  document.querySelectorAll('.icon-chip').forEach(chip => {
    chip.addEventListener('click', async () => {
      const iconName = chip.dataset.icon;
      if (!iconName) return;
      reportClientLog('action', '用户选择预置官方图标', `图标: ${iconName}`, { iconName });
      const cdnUrl = `https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/${iconName}.png`;
      const elIcon = document.getElementById('item-icon');
      const imgEl = document.getElementById('icon-preview-img');
      const nameEl = document.getElementById('icon-preview-name');
      if (elIcon) elIcon.value = cdnUrl;
      if (imgEl) imgEl.src = cdnUrl;
      if (nameEl) nameEl.textContent = `${iconName}.png (官方图标)`;

      // Try converting to Data URL so the payload is 100% offline-ready
      try {
        const dataUrl = await loadAndConvertUrlToDataUrl(cdnUrl);
        if (dataUrl && dataUrl.startsWith('data:') && elIcon && elIcon.value === cdnUrl) {
          elIcon.value = dataUrl;
        }
      } catch (err) {
        // Keep cdnUrl as fallback
      }
    });
  });

  // Icon input URL preview
  const elIcon = document.getElementById('item-icon');
  if (elIcon) {
    elIcon.addEventListener('input', () => {
      const val = elIcon.value.trim();
      const imgEl = document.getElementById('icon-preview-img');
      const nameEl = document.getElementById('icon-preview-name');
      if (!val) {
        imgEl.src = apiUrl('/icon.png');
        nameEl.textContent = '默认容器图标';
      } else if (val.startsWith('http://') || val.startsWith('https://') || val.startsWith('data:')) {
        imgEl.src = val;
        nameEl.textContent = '自定义链接图标';
      } else {
        imgEl.src = apiUrl(`/icons/${val.replace(/^icons\//, '')}`);
        nameEl.textContent = val;
      }
    });
  }

  // Settings auto-save listeners
  const elPortalName = document.getElementById('setting-portal-name');
  if (elPortalName) {
    elPortalName.addEventListener('input', () => triggerSettingsAutoSave(600));
  }
  document.querySelectorAll('input[name="setting-portal-all-users"]').forEach(r => {
    r.addEventListener('change', () => triggerSettingsAutoSave(0));
  });
  const elPwd = document.getElementById('setting-portal-password');
  const elPwdConfirm = document.getElementById('setting-portal-password-confirm');
  if (elPwd) {
    elPwd.addEventListener('input', () => triggerSettingsAutoSave(600));
  }
  if (elPwdConfirm) {
    elPwdConfirm.addEventListener('input', () => triggerSettingsAutoSave(600));
  }

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
      document.getElementById('item-icon').value = cdnUrl;
      const imgEl = document.getElementById('icon-preview-img');
      imgEl.src = cdnUrl;
      imgEl.onerror = () => {
        imgEl.src = apiUrl('/icon.png');
        document.getElementById('item-icon').value = '';
        document.getElementById('icon-preview-name').textContent = '默认容器图标';
      };
      document.getElementById('icon-preview-name').textContent = `${cleanName}.png (官方推荐)`;
      loadAndConvertUrlToDataUrl(cdnUrl).then(dataUrl => {
        const elIcon = document.getElementById('item-icon');
        if (dataUrl && dataUrl.startsWith('data:') && elIcon && elIcon.value === cdnUrl) {
          elIcon.value = dataUrl;
        }
      }).catch(() => {});
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
        // Attempt POST /delete first for reverse-proxy compatibility, fallback to DELETE
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

  if (item.icon) {
    const src = item.icon.startsWith('http') || item.icon.startsWith('data:') ? item.icon : apiUrl(`/icons/${item.icon.replace(/^icons\//, '')}`);
    document.getElementById('icon-preview-img').src = src;
    document.getElementById('icon-preview-name').textContent = item.icon.startsWith('data:') ? '已保存图标 (Base64)' : item.icon;
  } else {
    document.getElementById('icon-preview-img').src = apiUrl('/icon.png');
    document.getElementById('icon-preview-name').textContent = '默认图标';
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
    const iconSrc = item.icon
      ? (item.icon.startsWith('http') ? item.icon : apiUrl(`/icons/${item.icon.replace(/^icons\//, '')}`))
      : apiUrl('/icon.png');
    const openModeText = item.ui_type === 'iframe' ? '飞牛内部弹窗' : '浏览器新标签';
    const statusText = item.enabled ? '<span class="status-badge active">就绪</span>' : '<span class="status-badge paused">已停用</span>';

    html += `<tr>
      <td><img class="icon-cell-img" src="${iconSrc}" onerror="this.src='${apiUrl('/icon.png')}'" alt="图标"></td>
      <td><strong>${escapeHtml(item.name)}</strong></td>
      <td><code>${escapeHtml(item.path || '/')}</code></td>
      <td>${openModeText}</td>
      <td>${statusText}</td>
      <td>
        <div class="table-actions">
          <button class="btn btn-sm btn-secondary btn-edit-from-list" data-id="${item.id}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20h9"></path><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"></path></svg>
            <span>编辑</span>
          </button>
          <button class="btn btn-sm btn-danger btn-delete-from-list" data-id="${item.id}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
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
    const icon = document.getElementById('item-icon').value.trim();
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

function convertFileToPngDataUrl(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = (e) => {
      const img = new Image();
      img.onload = () => {
        try {
          const canvas = document.createElement('canvas');
          canvas.width = 256;
          canvas.height = 256;
          const ctx = canvas.getContext('2d');
          ctx.clearRect(0, 0, 256, 256);
          const scale = Math.min(256 / img.width, 256 / img.height);
          const w = img.width * scale;
          const h = img.height * scale;
          const x = (256 - w) / 2;
          const y = (256 - h) / 2;
          ctx.drawImage(img, x, y, w, h);
          resolve(canvas.toDataURL('image/png'));
        } catch (err) {
          resolve(e.target.result);
        }
      };
      img.onerror = () => resolve(e.target.result);
      img.src = e.target.result;
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
}

function loadAndConvertUrlToDataUrl(url) {
  return new Promise((resolve) => {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => {
      try {
        const canvas = document.createElement('canvas');
        canvas.width = 256;
        canvas.height = 256;
        const ctx = canvas.getContext('2d');
        ctx.clearRect(0, 0, 256, 256);
        ctx.drawImage(img, 0, 0, 256, 256);
        resolve(canvas.toDataURL('image/png'));
      } catch (e) {
        resolve(url);
      }
    };
    img.onerror = () => resolve(url);
    img.src = url;
  });
}

async function handleIconUpload(e) {
  const file = e.target.files[0];
  if (!file) return;

  reportClientLog('action', '用户上传本地图标文件', `文件名: ${file.name}, 大小: ${file.size}字节`, { name: file.name, size: file.size, type: file.type });

  try {
    const pngDataUrl = await convertFileToPngDataUrl(file);
    document.getElementById('item-icon').value = pngDataUrl;
    document.getElementById('icon-preview-img').src = pngDataUrl;
    document.getElementById('icon-preview-name').textContent = file.name;
    showToast(`本地图标「${file.name}」已加载`, 'success');
  } catch (err) {
    console.warn('Canvas conversion failed, fallback to direct upload', err);
  }

  // Also upload file to server cache in background
  const formData = new FormData();
  formData.append('icon', file);
  fetch(apiUrl('/api/icons/upload'), {
    method: 'POST',
    body: formData,
  }).catch(() => {});
}

let settingsAutoSaveTimer = null;

function triggerSettingsAutoSave(debounceMs = 500) {
  if (settingsAutoSaveTimer) {
    clearTimeout(settingsAutoSaveTimer);
  }
  const statusEl = document.getElementById('settings-save-status');
  if (statusEl) {
    statusEl.textContent = '正在保存...';
    statusEl.style.color = 'var(--text-muted)';
  }
  settingsAutoSaveTimer = setTimeout(() => {
    executeAutoSaveSettings();
  }, debounceMs);
}

async function executeAutoSaveSettings() {
  const name = document.getElementById('setting-portal-name').value.trim();
  const rAll = document.querySelector('input[name="setting-portal-all-users"]:checked');
  const allUsers = rAll ? rAll.value === 'true' : false;

  reportClientLog('action', '用户保存系统设置', `名称: ${name}, 用户范围: ${allUsers ? '所有用户' : '仅管理员'}`, { name, allUsers });

  const pwd = document.getElementById('setting-portal-password').value;
  const pwdConfirm = document.getElementById('setting-portal-password-confirm').value;
  const matchTip = document.getElementById('password-match-tip');
  const statusEl = document.getElementById('settings-save-status');

  const payload = {
    portal_name: name || '把 Docker 放到桌面',
    portal_ui_type: 'iframe',
    portal_all_users: allUsers,
  };

  // Password confirmation check
  if (pwd !== '' || pwdConfirm !== '') {
    if (pwd !== pwdConfirm) {
      if (matchTip) {
        matchTip.textContent = '两次输入的密码不一致，密码未更新';
        matchTip.style.color = 'var(--color-danger, #ef4444)';
      }
      if (statusEl) {
        statusEl.textContent = '设置已保存（密码未更新，请确保两次密码一致）';
        statusEl.style.color = 'var(--color-warning, #f59e0b)';
      }
      // Still save other non-password settings
      try {
        await fetch(apiUrl('/api/settings'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        });
      } catch (e) {}
      return;
    } else {
      payload.auth_password = pwd;
      if (matchTip) {
        matchTip.textContent = '两次密码一致，已更新密码';
        matchTip.style.color = 'var(--color-success, #10b981)';
      }
    }
  } else {
    if (matchTip) {
      matchTip.textContent = '';
    }
  }

  try {
    const res = await fetch(apiUrl('/api/settings'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    if (res.ok) {
      if (statusEl) {
        statusEl.textContent = '设置已自动保存并即时生效';
        statusEl.style.color = 'var(--color-success, #10b981)';
      }
    } else {
      const data = await res.json().catch(() => ({}));
      if (statusEl) {
        statusEl.textContent = data.error || '保存失败';
        statusEl.style.color = 'var(--color-danger, #ef4444)';
      }
    }
  } catch (err) {
    if (statusEl) {
      statusEl.textContent = '保存失败: ' + err.message;
      statusEl.style.color = 'var(--color-danger, #ef4444)';
    }
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

  // Filter chips in Ports tab (multi-select with special 'all' handling)
  const portFilterChips = document.querySelectorAll('#port-filter-chips .chip[data-filter]');
  portFilterChips.forEach(chip => {
    chip.addEventListener('click', () => {
      const filter = chip.dataset.filter;
      if (filter === 'all') {
        state.portFilters.clear();
        state.portFilters.add('all');
        portFilterChips.forEach(c => c.classList.toggle('active', c.dataset.filter === 'all'));
      } else {
        state.portFilters.delete('all');
        const allChip = document.querySelector('#port-filter-chips .chip[data-filter="all"]');
        if (allChip) allChip.classList.remove('active');

        if (state.portFilters.has(filter)) {
          state.portFilters.delete(filter);
          chip.classList.remove('active');
        } else {
          state.portFilters.add(filter);
          chip.classList.add('active');
        }

        // If no filter selected, revert back to 'all'
        if (state.portFilters.size === 0) {
          state.portFilters.add('all');
          if (allChip) allChip.classList.add('active');
        }
      }
      renderPortsTable();
    });
  });

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

  // Sort chips in Process tab
  document.querySelectorAll('.chip[data-proc-sort]').forEach(chip => {
    chip.addEventListener('click', () => {
      document.querySelectorAll('.chip[data-proc-sort]').forEach(c => c.classList.remove('active'));
      chip.classList.add('active');
      state.procSort = chip.dataset.procSort;
      renderProcessesTable();
    });
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
