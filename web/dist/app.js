// μVNC Relay Modern Dashboard Controller (< 12 KB Pure Vanilla ES6)
(() => {
  'use strict';

  let ws = null;
  let wsReconnectTimer = null;
  let cachedDevices = [];
  let cachedStatuses = {};
  let cachedBindErrors = {};

  // DOM Elements
  const elUptime = document.getElementById('uptime-val');
  const elMemory = document.getElementById('memory-val');
  const elSessionsCount = document.getElementById('sessions-count-val');
  const elRateSummary = document.getElementById('rate-summary-val');
  const elRxRate = document.getElementById('stat-rx-rate');
  const elTxRate = document.getElementById('stat-tx-rate');
  const elRxTotal = document.getElementById('stat-rx-total');
  const elTxTotal = document.getElementById('stat-tx-total');
  const elActiveStreams = document.getElementById('stat-active-streams');
  const elWsBadge = document.getElementById('ws-status-badge');
  const elDevicesBadge = document.getElementById('devices-badge-count');
  const elSessionsBadge = document.getElementById('sessions-badge-count');
  const elDevicesTbody = document.getElementById('devices-tbody');
  const elSessionsTbody = document.getElementById('sessions-tbody');
  const elLogContent = document.getElementById('log-content');
  const elLogViewer = document.getElementById('log-viewer');
  const elDeviceModal = document.getElementById('device-modal');
  const elDeviceForm = document.getElementById('device-form');
  const elLoginModal = document.getElementById('login-modal');
  const elLoginForm = document.getElementById('login-form');
  const elLoginError = document.getElementById('login-error');
  const elSetupModal = document.getElementById('setup-modal');
  const elSetupForm = document.getElementById('setup-form');
  const elSetupError = document.getElementById('setup-error');
  const elBtnLogout = document.getElementById('btn-logout');

  // Formatting Utilities
  function formatBytes(bytes) {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  }

  function formatDuration(sec) {
    if (!sec || sec < 0) return '0s';
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    if (h > 0) return `${h}h ${m}m ${s}s`;
    if (m > 0) return `${m}m ${s}s`;
    return `${s}s`;
  }

  function renderRTTBadge(ms) {
    if (ms === undefined || ms === null || ms <= 0) return '<span class="text-muted">-</span>';
    let cls = 'fast';
    if (ms > 50) cls = 'med';
    if (ms > 150) cls = 'slow';
    return `<span class="rtt-val ${cls}">${ms.toFixed(1)} ms</span>`;
  }

  // Floating Toast Notifications
  function showToast(message, type = 'success') {
    let container = document.getElementById('toast-container');
    if (!container) {
      container = document.createElement('div');
      container.id = 'toast-container';
      container.className = 'toast-container';
      document.body.appendChild(container);
    }

    const toast = document.createElement('div');
    toast.className = `toast ${type}`;
    toast.innerHTML = `<span>${escapeHTML(message)}</span>`;
    container.appendChild(toast);

    setTimeout(() => {
      toast.classList.add('toast-fadeout');
      setTimeout(() => {
        if (toast.parentNode) toast.parentNode.removeChild(toast);
      }, 300);
    }, 4000);
  }

  // Auth & Initial Setup Handling
  async function checkAuth() {
    try {
      const res = await fetch('/api/v1/auth/status');
      if (res.ok) {
        const data = await res.json();
        if (data.auth_broken) {
          if (elSetupModal) elSetupModal.style.display = 'none';
          if (elLoginModal) elLoginModal.style.display = 'flex';
          if (elBtnLogout) elBtnLogout.style.display = 'none';
          if (elLoginError) {
            elLoginError.textContent = 'auth.json exists but is invalid. Delete it and restart the relay to reset credentials.';
            elLoginError.style.display = 'block';
          }
          return false;
        }

        if (data.setup_required) {
          if (elSetupModal) elSetupModal.style.display = 'flex';
          if (elLoginModal) elLoginModal.style.display = 'none';
          if (elBtnLogout) elBtnLogout.style.display = 'none';
          return false;
        }

        if (elSetupModal) elSetupModal.style.display = 'none';

        if (data.auth_enabled) {
          if (elBtnLogout) elBtnLogout.style.display = data.authenticated ? 'inline-flex' : 'none';
          if (!data.authenticated) {
            if (elLoginModal) elLoginModal.style.display = 'flex';
            return false;
          }
        }
        if (elLoginModal) elLoginModal.style.display = 'none';
        return true;
      }
    } catch (e) {
      console.warn('Auth check failed:', e);
    }
    return true;
  }

  // First-Run Initial Setup Form
  if (elSetupForm) {
    elSetupForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (elSetupError) elSetupError.style.display = 'none';

      const setupToken = document.getElementById('setup-token').value.trim();
      const user = document.getElementById('setup-username').value.trim();
      const pass = document.getElementById('setup-password').value;
      const confirmPass = document.getElementById('setup-password-confirm').value;

      if (!setupToken) {
        showSetupError('The one-time setup token is required. Read it from journalctl or docker logs.');
        return;
      }
      if (!user) {
        showSetupError('Username is required');
        return;
      }
      if (pass.length < 6) {
        showSetupError('Password must be at least 6 characters');
        return;
      }
      if (pass !== confirmPass) {
        showSetupError('Passwords do not match');
        return;
      }

      try {
        const res = await fetch('/api/v1/auth/setup', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: user, password: pass, setup_token: setupToken })
        });
        if (res.ok) {
          if (elSetupModal) elSetupModal.style.display = 'none';
          if (elBtnLogout) elBtnLogout.style.display = 'inline-flex';
          initWebSocket();
          fetchDevices();
          fetchLogs();
        } else {
          const errData = await res.json().catch(() => ({}));
          showSetupError(errData.error || 'Setup failed');
        }
      } catch (err) {
        showSetupError(`Network error: ${err.message}`);
      }
    });
  }

  function showSetupError(msg) {
    if (elSetupError) {
      elSetupError.textContent = msg;
      elSetupError.style.display = 'block';
    }
  }

  if (elLoginForm) {
    elLoginForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (elLoginError) elLoginError.style.display = 'none';
      const user = document.getElementById('login-username').value;
      const pass = document.getElementById('login-password').value;

      try {
        const res = await fetch('/api/v1/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: user, password: pass })
        });
        if (res.ok) {
          if (elLoginModal) elLoginModal.style.display = 'none';
          if (elBtnLogout) elBtnLogout.style.display = 'inline-flex';
          initWebSocket();
          fetchDevices();
          fetchLogs();
        } else {
          const errData = await res.json().catch(() => ({}));
          if (elLoginError) {
            elLoginError.textContent = errData.error || (res.status === 429 ? 'Too many failed attempts. Account temporarily locked.' : 'Invalid username or password');
            elLoginError.style.display = 'block';
          }
        }
      } catch (err) {
        if (elLoginError) {
          elLoginError.textContent = 'Network error during sign in';
          elLoginError.style.display = 'block';
        }
      }
    });
  }

  if (elBtnLogout) {
    elBtnLogout.addEventListener('click', async () => {
      await fetch('/api/v1/logout', { method: 'POST' });
      window.location.reload();
    });
  }

  // WebSocket Telemetry Connection
  function initWebSocket() {
    if (ws) {
      try { ws.close(); } catch (_) {}
    }
    clearTimeout(wsReconnectTimer);

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/api/v1/ws/telemetry`;

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
      if (elWsBadge) {
        elWsBadge.textContent = '● WebSocket Live';
        elWsBadge.className = 'status-indicator live';
      }
    };

    ws.onmessage = (event) => {
      try {
        const payload = JSON.parse(event.data);
        handleTelemetryUpdate(payload);
      } catch (err) {
        console.error('Failed to parse telemetry frame:', err);
      }
    };

    ws.onclose = () => {
      if (elWsBadge) {
        elWsBadge.textContent = '○ Reconnecting...';
        elWsBadge.className = 'status-indicator text-muted';
      }
      wsReconnectTimer = setTimeout(initWebSocket, 3000);
    };

    ws.onerror = () => {
      try { ws.close(); } catch (_) {}
    };
  }

  function handleTelemetryUpdate(data) {
    if (data.metrics) {
      const m = data.metrics;
      if (elUptime) elUptime.textContent = formatDuration(m.uptime_seconds);
      if (elMemory) elMemory.textContent = `${(m.memory_rss_mb || 0).toFixed(1)} MB`;
      if (elRxRate) elRxRate.textContent = `${formatBytes(m.total_rx_bytes_sec)}/s`;
      if (elTxRate) elTxRate.textContent = `${formatBytes(m.total_tx_bytes_sec)}/s`;
      if (elRxTotal) elRxTotal.textContent = `Total: ${formatBytes(m.total_cumulative_rx)}`;
      if (elTxTotal) elTxTotal.textContent = `Total: ${formatBytes(m.total_cumulative_tx)}`;
      if (elRateSummary) {
        elRateSummary.textContent = `RX: ${formatBytes(m.total_rx_bytes_sec)}/s | TX: ${formatBytes(m.total_tx_bytes_sec)}/s`;
      }
    }

    if (data.sessions !== undefined) {
      renderSessionsTable(data.sessions);
    }

    if (data.bind_errors !== undefined) {
      cachedBindErrors = data.bind_errors || {};
    }

    if (data.device_statuses || data.bind_errors !== undefined) {
      if (data.device_statuses) {
        cachedStatuses = data.device_statuses;
      }
      renderDevicesTable();
    }
  }

  // Devices CRUD & Rendering
  async function fetchDevices() {
    try {
      const res = await fetch('/api/v1/devices');
      if (res.ok) {
        const data = await res.json();
        cachedDevices = data.devices || [];
        if (data.statuses) {
          cachedStatuses = data.statuses;
        }
        cachedBindErrors = data.bind_errors || {};
        renderDevicesTable();
      }
    } catch (e) {
      console.warn('Failed to fetch devices:', e);
    }
  }

  function renderDevicesTable() {
    if (elDevicesBadge) elDevicesBadge.textContent = cachedDevices.length;
    if (!elDevicesTbody) return;

    if (cachedDevices.length === 0) {
      elDevicesTbody.innerHTML = `<tr><td colspan="7" class="text-center py-4 text-muted">No VNC target devices configured. Click "+ Add Target" to register one.</td></tr>`;
      return;
    }

    let html = '';
    cachedDevices.forEach((dev) => {
      const id = dev.id || dev.ID || '';
      const name = dev.name || dev.Name || id;
      const host = dev.host || dev.Host || '127.0.0.1';
      const port = dev.port !== undefined ? dev.port : (dev.Port !== undefined ? dev.Port : 5900);
      const vport = dev.virtual_port !== undefined ? dev.virtual_port : (dev.VirtualPort !== undefined ? dev.VirtualPort : 0);
      const mode2id = dev.mode2_id !== undefined ? dev.mode2_id : (dev.Mode2ID !== undefined ? dev.Mode2ID : 0);
      const enabled = dev.enabled !== undefined ? dev.enabled : (dev.Enabled !== undefined ? dev.Enabled : true);

      const st = cachedStatuses[id] || {};
      const bindErr = cachedBindErrors[id] || '';
      let statusBadge = '<span class="status-badge offline">Offline</span>';
      if (!enabled) {
        statusBadge = '<span class="status-badge pending">Disabled</span>';
      } else if (bindErr) {
        statusBadge = `<span class="status-badge conflict" title="${escapeHTML(bindErr)}">⚠️ Port Conflict</span>`;
      } else if (st.online) {
        statusBadge = '<span class="status-badge online">● Online</span>';
      }

      let modeStr = 'Direct';
      let portDetails = '';
      if (vport > 0) {
        modeStr = 'Mode 1';
        portDetails += `<code>:${vport}</code>`;
      }
      if (mode2id > 0) {
        if (portDetails) portDetails += ' / ';
        portDetails += `<code>ID:${mode2id}</code>`;
        modeStr = vport > 0 ? 'Mode 1 + 2' : 'Mode 2 (Reverse)';
      }
      if (!portDetails) portDetails = '<span class="text-muted">-</span>';
      if (enabled && bindErr) {
        portDetails += `<div style="color:var(--accent-yellow);font-size:11px;margin-top:2px;">⚠️ Port in use</div>`;
      }

      const targetRTT = st.target_rtt_ms ? renderRTTBadge(st.target_rtt_ms) : '<span class="text-muted">-</span>';

      html += `
        <tr>
          <td>${statusBadge}</td>
          <td>
            <strong>${escapeHTML(name)}</strong>
            <div class="text-muted" style="font-size:11px;">${escapeHTML(id)}</div>
          </td>
          <td><span class="badge secondary">${modeStr}</span></td>
          <td><code>${escapeHTML(host)}:${port}</code></td>
          <td>${portDetails}</td>
          <td>${targetRTT}</td>
          <td class="text-right">
            <div class="btn-group">
              <button class="btn btn-sm btn-outline btn-test-dev" data-id="${escapeHTML(id)}">Test</button>
              <button class="btn btn-sm btn-outline btn-edit-dev" data-id="${escapeHTML(id)}">Edit</button>
              <button class="btn btn-sm btn-outline danger btn-del-dev" data-id="${escapeHTML(id)}">Delete</button>
            </div>
          </td>
        </tr>
      `;
    });

    elDevicesTbody.innerHTML = html;
    attachDeviceRowEvents();
  }

  function attachDeviceRowEvents() {
    document.querySelectorAll('.btn-test-dev').forEach((btn) => {
      btn.onclick = async () => {
        const id = btn.getAttribute('data-id');
        if (!id) return;
        btn.textContent = 'Testing...';
        btn.disabled = true;
        try {
          const res = await fetch(`/api/v1/devices/${encodeURIComponent(id)}/test`, { method: 'POST' });
          if (res.ok) {
            const st = await res.json();
            cachedStatuses[id] = st;
            renderDevicesTable();
          }
        } finally {
          btn.textContent = 'Test';
          btn.disabled = false;
        }
      };
    });

    document.querySelectorAll('.btn-edit-dev').forEach((btn) => {
      btn.onclick = () => {
        const id = btn.getAttribute('data-id');
        const dev = cachedDevices.find((d) => (d.id || d.ID) === id);
        if (dev) openDeviceModal(dev);
      };
    });

    document.querySelectorAll('.btn-del-dev').forEach((btn) => {
      btn.onclick = async () => {
        const id = btn.getAttribute('data-id');
        if (!id) return;
        if (confirm(`Are you sure you want to remove target machine "${id}"?`)) {
          const res = await fetch(`/api/v1/devices/${encodeURIComponent(id)}`, { method: 'DELETE' });
          if (res.ok) {
            showToast(`Target "${id}" removed`, 'warning');
            await fetchDevices();
          }
        }
      };
    });
  }

  // Device Modal Dialog
  const btnAddDevice = document.getElementById('btn-add-device');
  if (btnAddDevice) btnAddDevice.onclick = () => openDeviceModal(null);

  const btnModalClose = document.getElementById('modal-close-btn');
  if (btnModalClose) btnModalClose.onclick = () => (elDeviceModal.style.display = 'none');

  const btnModalCancel = document.getElementById('modal-cancel-btn');
  if (btnModalCancel) btnModalCancel.onclick = () => (elDeviceModal.style.display = 'none');

  function openDeviceModal(dev) {
    const isEdit = !!dev;
    document.getElementById('form-is-edit').value = isEdit ? 'true' : 'false';
    document.getElementById('device-modal-title').textContent = isEdit ? 'Edit VNC Target' : 'Add VNC Target';

    const id = dev ? (dev.id || dev.ID || '') : '';
    const name = dev ? (dev.name || dev.Name || '') : '';
    const host = dev ? (dev.host || dev.Host || '127.0.0.1') : '127.0.0.1';
    const port = dev ? (dev.port !== undefined ? dev.port : dev.Port) : 5900;
    const vport = dev ? (dev.virtual_port !== undefined ? dev.virtual_port : dev.VirtualPort) : 0;
    const mode2id = dev ? (dev.mode2_id !== undefined ? dev.mode2_id : dev.Mode2ID) : 0;
    const enabled = dev ? (dev.enabled !== undefined ? dev.enabled : (dev.Enabled !== undefined ? dev.Enabled : true)) : true;

    const idInput = document.getElementById('form-dev-id');
    idInput.value = id;
    idInput.readOnly = isEdit;

    document.getElementById('form-dev-name').value = name;
    document.getElementById('form-dev-host').value = host;
    document.getElementById('form-dev-port').value = port;
    document.getElementById('form-dev-vport').value = vport;
    document.getElementById('form-dev-mode2id').value = mode2id;
    document.getElementById('form-dev-enabled').checked = enabled;

    if (elDeviceModal) elDeviceModal.style.display = 'flex';
  }

  if (elDeviceForm) {
    elDeviceForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const isEdit = document.getElementById('form-is-edit').value === 'true';
      const id = document.getElementById('form-dev-id').value.trim();
      const payload = {
        id: id,
        name: document.getElementById('form-dev-name').value.trim(),
        host: document.getElementById('form-dev-host').value.trim(),
        port: parseInt(document.getElementById('form-dev-port').value, 10),
        virtual_port: parseInt(document.getElementById('form-dev-vport').value, 10) || 0,
        mode2_id: parseInt(document.getElementById('form-dev-mode2id').value, 10) || 0,
        enabled: document.getElementById('form-dev-enabled').checked
      };

      const url = isEdit ? `/api/v1/devices/${encodeURIComponent(id)}` : '/api/v1/devices';
      const method = isEdit ? 'PUT' : 'POST';

      try {
        const res = await fetch(url, {
          method: method,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        if (res.ok) {
          const resData = await res.json().catch(() => ({}));
          if (elDeviceModal) elDeviceModal.style.display = 'none';
          await fetchDevices();
          const targetName = payload.name || payload.id;
          if (resData.bind_error) {
            showToast(`⚠️ Target "${targetName}" saved, but ${resData.bind_error}`, 'warning');
          } else {
            let detail = '';
            if (payload.virtual_port > 0) {
              detail = `listening on port :${payload.virtual_port}`;
            } else if (payload.mode2_id > 0) {
              detail = `ready for Mode 2 reverse ID:${payload.mode2_id}`;
            } else {
              detail = 'configured';
            }
            showToast(isEdit ? `✓ Target "${targetName}" updated!` : `✓ Target "${targetName}" is live! (${detail})`, 'success');
          }
        } else {
          const err = await res.json().catch(() => ({}));
          showToast(`⚠️ ${err.error || res.statusText}`, 'error');
        }
      } catch (err) {
        showToast(`Network error saving device: ${err.message}`, 'error');
      }
    });
  }

  // Sessions Table Rendering
  function renderSessionsTable(sessions) {
    if (elSessionsBadge) elSessionsBadge.textContent = sessions.length;
    if (elSessionsCount) elSessionsCount.textContent = sessions.length;
    if (elActiveStreams) elActiveStreams.textContent = sessions.length;
    if (!elSessionsTbody) return;

    if (sessions.length === 0) {
      elSessionsTbody.innerHTML = `<tr><td colspan="8" class="text-center py-4 text-muted">No active VNC client streams connected.</td></tr>`;
      return;
    }

    let html = '';
    sessions.forEach((s) => {
      const clientRTT = renderRTTBadge(s.client_rtt_ms);
      const targetRTT = renderRTTBadge(s.target_rtt_ms);

      html += `
        <tr>
          <td><code>${escapeHTML(s.session_id)}</code></td>
          <td>
            <strong>${escapeHTML(s.device_name || s.device_id)}</strong>
            <div class="text-muted" style="font-size:11px;">${escapeHTML(s.device_id)}</div>
          </td>
          <td><span class="badge secondary">${escapeHTML(s.mode)}</span></td>
          <td>
            <div>${escapeHTML(s.client_ip)}</div>
            <small class="text-muted">RTT: ${clientRTT}</small>
          </td>
          <td>
            <div>${escapeHTML(s.target_ip)}</div>
            <small class="text-muted">RTT: ${targetRTT}</small>
          </td>
          <td>
            <div>↓ ${formatBytes(s.rx_bytes_sec)}/s | ↑ ${formatBytes(s.tx_bytes_sec)}/s</div>
            <small class="text-muted">${formatBytes(s.rx_bytes_total + s.tx_bytes_total)} total</small>
          </td>
          <td>${formatDuration(s.duration_seconds)}</td>
          <td class="text-right">
            <button class="btn btn-sm btn-outline danger btn-disconnect-sess" data-id="${escapeHTML(s.session_id)}">Disconnect</button>
          </td>
        </tr>
      `;
    });

    elSessionsTbody.innerHTML = html;
    document.querySelectorAll('.btn-disconnect-sess').forEach((btn) => {
      btn.onclick = async () => {
        const id = btn.getAttribute('data-id');
        btn.disabled = true;
        await fetch(`/api/v1/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
      };
    });
  }

  // Logs Viewer
  async function fetchLogs() {
    try {
      const res = await fetch('/api/v1/logs');
      if (res.ok) {
        const lines = await res.json();
        if (lines && lines.length > 0 && elLogContent) {
          elLogContent.textContent = lines.map((l) => l.formatted || `[${l.level}] ${l.message}`).join('\n');
          if (elLogViewer) elLogViewer.scrollTop = elLogViewer.scrollHeight;
        } else if (elLogContent) {
          elLogContent.textContent = 'No event logs recorded yet.';
        }
      }
    } catch (e) {
      console.warn('Failed to fetch logs:', e);
    }
  }

  const btnRefreshLogs = document.getElementById('btn-refresh-logs');
  if (btnRefreshLogs) btnRefreshLogs.onclick = fetchLogs;

  const btnClearLogs = document.getElementById('btn-clear-logs');
  if (btnClearLogs) {
    btnClearLogs.onclick = async () => {
      await fetch('/api/v1/system/reset-counters', { method: 'POST' });
      if (elLogContent) elLogContent.textContent = 'Logs cleared.';
    };
  }

  const btnResetCounters = document.getElementById('btn-reset-counters');
  if (btnResetCounters) {
    btnResetCounters.onclick = async () => {
      if (confirm('Reset cumulative byte meters, 24h bandwidth graph, and in-memory logs?')) {
        await fetch('/api/v1/system/reset-counters', { method: 'POST' });
        fetchLogs();
      }
    };
  }

  function escapeHTML(str) {
    if (!str) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  // Periodic fallback refresh
  setInterval(() => {
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      fetchDevices();
    }
  }, 5000);

  // Initialize
  (async () => {
    const authed = await checkAuth();
    if (authed) {
      initWebSocket();
      fetchDevices();
      fetchLogs();
    }
  })();
})();
