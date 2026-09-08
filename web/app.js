// --- Put Port On Desktop - Frontend Application ---

let state = {
  currentTab: 'ports',
  ports: [],
  desktopItems: [],
  processes: [],
  system: null,
  host: null,
  portFilter: 'all',
  portSearch: '',
  desktopSearch: '',
  procSearch: '',
  procSort: 'cpu',
  eventSource: null,
  activeMode: 'local',
  logDate: '',
  logLevel: 'ALL',
  logSearch: '',
  logAutoRefresh: true,
  logTimer: null,
  logs: [],
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
    startLogTimer();
  } else if (tab === 'settings') {
    fetchSettings();
  }

  if (tab !== 'logs') {
    stopLogTimer();
  }
}

// --- Data Fetching ---
async function fetchPorts() {
  try {
    const res = await fetch('/api/ports');
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
    const res = await fetch('/api/desktop/items');
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      state.desktopItems = await res.json();
      renderDesktopTable();
      updateDesktopCountBadge();
    }
  } catch (err) {
    console.error('Fetch desktop items error:', err);
  }
}

async function fetchProcesses() {
  try {
    const res = await fetch('/api/processes');
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
    const res = await fetch('/api/system');
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
    const res = await fetch('/api/host');
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
    const res = await fetch('/api/settings');
    if (res.status === 401) return showAuthModal();
    if (res.ok) {
      const settings = await res.json();
      document.getElementById('setting-portal-name').value = settings.portal_name || '把Docker放到桌面';
      document.getElementById('setting-portal-port').value = settings.portal_port || 5900;
      
      const uiType = settings.portal_ui_type || 'iframe';
      const rUi = document.querySelector(`input[name="setting-portal-ui-type"][value="${uiType}"]`);
      if (rUi) rUi.checked = true;

      const allUsers = settings.portal_all_users ? 'true' : 'false';
      const rAll = document.querySelector(`input[name="setting-portal-all-users"][value="${allUsers}"]`);
      if (rAll) rAll.checked = true;
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
  state.eventSource = new EventSource('/api/events');
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

function updateSystemMetrics(sys) {
  if (!sys) return;
  const cpuPct = Math.round(sys.cpu_percent || 0);
  const memPct = Math.round(sys.mem_percent || 0);

  // Header quick stats
  const topCpu = document.getElementById('top-cpu');
  const topMem = document.getElementById('top-mem');
  if (topCpu) topCpu.textContent = `${cpuPct}%`;
  if (topMem) topMem.textContent = `${memPct}%`;

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
  const filter = state.portFilter;

  const filtered = state.ports.filter(p => {
    // Search matching
    if (query) {
      const matchPort = String(p.local_port).includes(query);
      const matchProc = (p.process_name || '').toLowerCase().includes(query);
      const matchDocker = (p.docker && p.docker.container_name || '').toLowerCase().includes(query);
      const matchExe = (p.exe || '').toLowerCase().includes(query);
      if (!matchPort && !matchProc && !matchDocker && !matchExe) return false;
    }

    // Filter chip matching
    if (filter === 'docker') {
      return p.docker && p.docker.is_docker;
    } else if (filter === 'host') {
      return !(p.docker && p.docker.is_docker);
    } else if (filter === 'listen') {
      return p.state === 'LISTEN';
    } else if (filter === 'tcp') {
      return (p.protocols || []).includes('tcp') || p.protocol.includes('tcp');
    } else if (filter === 'udp') {
      return (p.protocols || []).includes('udp') || p.protocol.includes('udp');
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
    const typeTag = isDocker
      ? `<span class="protocol-tag tag-docker" title="Docker 容器: ${escapeHtml(p.docker.image)}">Docker: ${escapeHtml(p.docker.container_name)}</span>`
      : `<span class="protocol-tag tag-host">宿主原生</span>`;

    const desktopCell = p.has_desktop
      ? `<span class="status-badge on-desktop" title="桌面图标：${escapeHtml(p.desktop_name)}">已在桌面</span>`
      : `<button class="btn btn-sm btn-secondary btn-add-port-to-desktop" data-port="${p.local_port}" data-name="${escapeHtml(procDisplayName)}">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="7" height="7"></rect><rect x="14" y="3" width="7" height="7"></rect><rect x="14" y="14" width="7" height="7"></rect><rect x="3" y="14" width="7" height="7"></rect></svg>
          <span>放到桌面</span>
        </button>`;

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
      <td>
        <span class="protocol-tag">${escapeHtml(p.protocol)}</span>
      </td>
      <td><code>${escapeHtml(p.local_ip || '0.0.0.0')}</code></td>
      <td>
        <div><strong>${escapeHtml(procDisplayName)}</strong></div>
        <div style="margin-top: 3px;">${typeTag}</div>
      </td>
      <td style="font-variant-numeric: tabular-nums;">${resText}</td>
      <td>${desktopCell}</td>
      <td>
        <div class="table-actions">
          <button class="btn btn-sm btn-secondary btn-view-port-detail" data-port="${p.local_port}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"></circle><line x1="12" y1="16" x2="12" y2="12"></line><line x1="12" y1="8" x2="12.01" y2="8"></line></svg>
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
      openCreateDesktopModalWithPort(port, name);
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
    const statusText = item.enabled ? '<span class="status-badge active">就绪</span>' : '<span class="status-badge paused">已停用</span>';

    const iconSrc = item.icon ? (item.icon.startsWith('http') ? item.icon : `/icons/${item.icon.replace('icons/', '')}`) : 'icon.png';

    html += `<tr>
      <td>
        <img class="icon-cell-img" src="${iconSrc}" onerror="this.src='icon.png'" alt="图标">
      </td>
      <td><strong>${escapeHtml(item.name)}</strong></td>
      <td><span class="protocol-tag">${modeText}</span></td>
      <td><code>${escapeHtml(targetText)}</code></td>
      <td>${openModeText}</td>
      <td>${permText}</td>
      <td>${statusText}</td>
      <td>
        <div class="table-actions">
          <button class="btn btn-sm btn-secondary btn-edit-desktop" data-id="${item.id}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20h9"></path><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"></path></svg>
            <span>编辑</span>
          </button>
          <button class="btn btn-sm btn-danger btn-delete-desktop" data-id="${item.id}">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
            <span>移出桌面</span>
          </button>
        </div>
      </td>
      <td class="filler-col"></td>
    </tr>`;
  }

  tbody.innerHTML = html;

  tbody.querySelectorAll('.btn-edit-desktop').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = btn.dataset.id;
      openEditDesktopModal(id);
    });
  });

  tbody.querySelectorAll('.btn-delete-desktop').forEach(btn => {
    btn.addEventListener('click', async () => {
      const id = btn.dataset.id;
      if (confirm('确定从飞牛桌面移出此图标吗？')) {
        await fetch(`/api/desktop/items/${id}`, { method: 'DELETE' });
        await fetchDesktopItems();
        await fetchPorts();
      }
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

  // Settings save button
  const btnSaveSettings = document.getElementById('btn-save-settings');
  if (btnSaveSettings) {
    btnSaveSettings.addEventListener('click', handleSaveSettings);
  }

  // Auth form
  const formAuth = document.getElementById('form-auth');
  if (formAuth) {
    formAuth.addEventListener('submit', handleAuthLogin);
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
}

function resetDesktopForm() {
  document.getElementById('item-id').value = '';
  document.getElementById('item-name').value = '';
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
  document.getElementById('icon-preview-img').src = 'icon.png';
  document.getElementById('icon-preview-name').textContent = '默认图标';
  document.getElementById('desktop-modal-title').textContent = '添加桌面图标';
  setDesktopModalMode('local');
}

function openCreateDesktopModalWithPort(port, name) {
  resetDesktopForm();
  document.getElementById('item-local-port').value = port;
  document.getElementById('item-name').value = name;
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
  document.getElementById('item-protocol').value = item.protocol || 'http';
  document.getElementById('item-path').value = item.path || '/';
  document.getElementById('item-ui-type').value = item.ui_type || 'url';
  document.getElementById('item-all-users').value = item.all_users ? 'true' : 'false';
  document.getElementById('item-icon').value = item.icon || '';

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
    const src = item.icon.startsWith('http') ? item.icon : `/icons/${item.icon.replace('icons/', '')}`;
    document.getElementById('icon-preview-img').src = src;
    document.getElementById('icon-preview-name').textContent = item.icon;
  }

  openModal('modal-desktop-item');
}

async function handleSaveDesktopItem(e) {
  e.preventDefault();
  const id = document.getElementById('item-id').value.trim();
  const mode = document.getElementById('item-mode').value;
  const name = document.getElementById('item-name').value.trim();
  const protocol = document.getElementById('item-protocol').value;
  const path = document.getElementById('item-path').value.trim() || '/';
  const uiType = document.getElementById('item-ui-type').value;
  const allUsers = document.getElementById('item-all-users').value === 'true';
  const icon = document.getElementById('item-icon').value.trim();

  let port = 0;
  let targetUrl = '';
  let skipTls = false;

  if (mode === 'local') {
    port = parseInt(document.getElementById('item-local-port').value, 10);
    if (!port || port <= 0) return alert('请输入有效的本机端口');
  } else if (mode === 'proxy') {
    targetUrl = document.getElementById('item-target-url').value.trim();
    port = parseInt(document.getElementById('item-proxy-port').value, 10);
    skipTls = document.getElementById('item-skip-tls').checked;
    if (!targetUrl) return alert('请输入目标地址');
    if (!port || port <= 0) return alert('请输入本机代理监听端口');
  } else if (mode === 'shortcut') {
    targetUrl = document.getElementById('item-shortcut-url').value.trim();
    if (!targetUrl) return alert('请输入目标网址');
  }

  const payload = {
    id: id || `item-${Date.now() % 1000000}`,
    name,
    mode,
    port,
    target_url: targetUrl,
    protocol,
    path,
    ui_type: uiType,
    all_users: allUsers,
    icon,
    skip_tls_verify: skipTls,
  };

  try {
    const method = id ? 'PUT' : 'POST';
    const url = id ? `/api/desktop/items/${id}` : '/api/desktop/items';
    const res = await fetch(url, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    if (!res.ok) {
      const errData = await res.json();
      return alert(errData.error || '保存失败');
    }

    closeModal('modal-desktop-item');
    await fetchDesktopItems();
    await fetchPorts();
  } catch (err) {
    alert('请求失败: ' + err.message);
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
    const res = await fetch('/api/proxy/test', {
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
    const res = await fetch('/api/ports/available?start=18000');
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

  const formData = new FormData();
  formData.append('icon', file);

  try {
    const res = await fetch('/api/icons/upload', {
      method: 'POST',
      body: formData,
    });
    const data = await res.json();
    if (res.ok && data.filename) {
      document.getElementById('item-icon').value = data.filename;
      document.getElementById('icon-preview-img').src = data.url;
      document.getElementById('icon-preview-name').textContent = data.filename;
    } else {
      alert(data.error || '图标上传失败');
    }
  } catch (err) {
    alert('上传异常: ' + err.message);
  }
}

async function handleSaveSettings() {
  const name = document.getElementById('setting-portal-name').value.trim();
  const port = parseInt(document.getElementById('setting-portal-port').value, 10);
  const password = document.getElementById('setting-portal-password').value.trim();

  const rUi = document.querySelector('input[name="setting-portal-ui-type"]:checked');
  const uiType = rUi ? rUi.value : 'iframe';

  const rAll = document.querySelector('input[name="setting-portal-all-users"]:checked');
  const allUsers = rAll ? rAll.value === 'true' : false;

  const statusEl = document.getElementById('settings-save-status');
  statusEl.textContent = '正在保存并应用到飞牛桌面...';

  try {
    const res = await fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        portal_name: name,
        portal_port: port,
        portal_ui_type: uiType,
        portal_all_users: allUsers,
        auth_password: password,
      }),
    });

    if (res.ok) {
      statusEl.textContent = '设置已保存并同步至飞牛桌面！';
      setTimeout(() => { statusEl.textContent = ''; }, 3000);
    } else {
      const data = await res.json();
      statusEl.textContent = data.error || '保存失败';
    }
  } catch (err) {
    statusEl.textContent = '请求失败: ' + err.message;
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
    const res = await fetch('/api/auth/login', {
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

  // Filter chips in Ports tab
  document.querySelectorAll('.filter-chips .chip[data-filter]').forEach(chip => {
    chip.addEventListener('click', () => {
      document.querySelectorAll('.filter-chips .chip[data-filter]').forEach(c => c.classList.remove('active'));
      chip.classList.add('active');
      state.portFilter = chip.dataset.filter;
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

  // Manual refresh button
  const btnRefresh = document.getElementById('btn-refresh-manual');
  if (btnRefresh) {
    btnRefresh.addEventListener('click', () => {
      fetchPorts();
      fetchDesktopItems();
      fetchProcesses();
      fetchSystem();
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

  const autoRefreshCb = document.getElementById('log-auto-refresh');
  if (autoRefreshCb) {
    autoRefreshCb.addEventListener('change', (e) => {
      state.logAutoRefresh = e.target.checked;
      if (state.logAutoRefresh && state.currentTab === 'logs') {
        startLogTimer();
      } else {
        stopLogTimer();
      }
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

function startLogTimer() {
  stopLogTimer();
  if (state.logAutoRefresh) {
    state.logTimer = setInterval(() => {
      if (state.currentTab === 'logs') {
        fetchLogs(true);
      }
    }, 3000);
  }
}

function stopLogTimer() {
  if (state.logTimer) {
    clearInterval(state.logTimer);
    state.logTimer = null;
  }
}

async function fetchLogs(isAutoPoll = false) {
  try {
    let url = `/api/logs?level=${encodeURIComponent(state.logLevel || 'ALL')}`;
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
  window.open(`/api/logs/download?date=${encodeURIComponent(date)}`, '_blank');
}

window.addEventListener('DOMContentLoaded', initApp);
