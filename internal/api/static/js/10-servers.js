// SERVER PANEL
// ═══════════════════════════════════════════════════
function toggleSrv() {
  if (state.srvOpen) closeSrv();
  else openSrv();
}

function openSrv() {
  state.srvOpen = true;
  srvPanel.classList.add('open');
  srvOverlay.classList.add('open');
  loadServers();
}

// FIX #5: closeSrv — отдельная функция, вызывается и из overlay
function closeSrv() {
  state.srvOpen = false;
  srvPanel.classList.remove('open');
  srvOverlay.classList.remove('open');
}

async function loadServers() {
  try {
    const r = await fetch(API + '/servers');
    if (!r.ok) throw new Error(r.status);
    const d = await r.json();
    // API returns { servers: [...], active_id: "..." }
    // Preserve cached country_code so flag doesn't reset to 🌐 on every poll
    const _prevSrv = new Map((state.servers || []).map(s => [s.id, s]));
    state.servers = (d && d.servers) || [];
    // FIX 33: восстанавливаем country_code из кэша если новое значение "??" (не только falsy).
    state.servers.forEach(s => {
      const prev = _prevSrv.get(s.id);
      if ((!s.country_code || s.country_code === '??') && prev?.country_code && prev.country_code !== '??')
        s.country_code = prev.country_code;
    });
    if (d && d.active_id) state.activeId = d.active_id;
    await loadServerHealth(false);
    _restoreCountryCodeCache();
    renderServerList();
    // Запускаем ping-all в фоне
    pingAll();
  } catch (e) {
    $id('splist').innerHTML = '<div class="sp-noservers">Не удалось загрузить серверы.<br>Убедитесь, что приложение запущено.</div>';
  }
}

let _srvTab = 'all';
let _srvSortByPing = false;
let _srvSearch = '';
let _srvViewMode = localStorage.getItem('safesky.serverViewMode') || 'comfort';

async function loadServerHealth(render = true) {
  try {
    const r = await fetch(API + '/servers/health');
    if (!r.ok) throw new Error(r.status);
    const d = await r.json();
    state.health = {};
    (d.servers || []).forEach(h => { state.health[h.id] = h; });
    if (render) renderServerList();
  } catch (_) {
    state.health = state.health || {};
  }
}

function toggleSrvSort() {
  _srvSortByPing = !_srvSortByPing;
  const btn = $id('srvSortBtn');
  if (btn) { btn.textContent = _srvSortByPing ? '↑ Задержка' : '↕ Задержка'; btn.classList.toggle('active', _srvSortByPing); }
  renderServerList();
}
function setSrvTab(tab) {
  _srvTab = tab;
  ['all','fast','slow'].forEach(t => {
    $id('srvTab' + t.charAt(0).toUpperCase() + t.slice(1))?.classList.toggle('active', t === tab);
  });
  renderServerList();
}

function setSrvSearch(value) {
  _srvSearch = String(value || '').trim().toLowerCase();
  renderServerList();
}

function toggleSrvViewMode() {
  _srvViewMode = _srvViewMode === 'compact' ? 'comfort' : 'compact';
  localStorage.setItem('safesky.serverViewMode', _srvViewMode);
  renderServerList();
}

function _srvHostPort(url) {
  if (!url) return '—';
  try { const u = new URL(url); return u.host; } catch(_) { return url.slice(0, 40); }
}

function _serverHost(url) {
  if (!url) return '';
  try { return new URL(url).hostname; } catch(_) { return ''; }
}

function _cssEsc(value) {
  return (window.CSS && CSS.escape) ? CSS.escape(String(value || '')) : String(value || '').replace(/"/g, '\\"');
}

function serverHealth(srv) {
  return (srv && state.health && state.health[srv.id]) || null;
}

function serverHealthLatency(srv) {
  const h = serverHealth(srv);
  if (h && h.average_latency_ms > 0) return h.average_latency_ms;
  const ms = state.pings[srv.id];
  return ms != null && ms >= 0 ? ms : null;
}

function healthStatusInfo(srv) {
  const h = serverHealth(srv);
  if (!h) return { cls: 'unknown', text: 'health: unknown', meta: 'health: —' };
  const avg = h.average_latency_ms > 0 ? `${h.average_latency_ms}ms` : 'timeout';
  const loss = `${Math.round((h.packet_loss || 0) * 100)}% loss`;
  const uptime = `${Math.round((h.uptime || 0) * 100)}% uptime`;
  const rec = h.recommended ? ' ★' : '';
  return {
    cls: h.status || 'unknown',
    text: `${avg} · ${loss} · ${uptime}${rec}`,
    meta: `${avg} · ${loss} · ${uptime}${rec}`
  };
}

function _applyServerCountryCode(srv, code) {
  const cc = String(code || '').trim().toUpperCase();
  if (!srv || !_validCC.has(cc)) return;
  srv.country_code = cc;
  const curSrv = state.servers.find(s => s.id === srv.id);
  if (curSrv) curSrv.country_code = cc;
  document.querySelectorAll(`.sp-flag[data-srvid="${_cssEsc(srv.id)}"]`)
    .forEach(el => { el.innerHTML = countryFlag(cc); });
  const activeFlag = $id('sflag');
  if (activeFlag && srv.id === state.activeId) activeFlag.innerHTML = countryFlag(cc);
  setTimeout(_saveCountryCodeCache, 200);
}

function serverCountryCode(srv, displayName) {
  if (!srv) return null;
  const stored = String(srv.country_code || '').trim().toUpperCase();
  if (_validCC.has(stored)) return stored;

  const nameCode = extractCountryFromName(displayName || serverDisplayName(srv));
  if (nameCode) {
    _applyServerCountryCode(srv, nameCode);
    return nameCode;
  }

  const host = _serverHost(srv.url);
  const hostCode = host ? extractCountryFromName(host.split('.')[0]) : null;
  if (hostCode) {
    _applyServerCountryCode(srv, hostCode);
    return hostCode;
  }

  if (host) {
    resolveCountryCode(host).then(code => _applyServerCountryCode(srv, code)).catch(() => {});
  }
  return null;
}

function renderServerList() {
  const list = state.servers;
  const el = $id('splist');
  const viewBtn = $id('srvViewBtn');
  if (viewBtn) {
    viewBtn.textContent = _srvViewMode === 'compact' ? 'Подробно' : 'Компактно';
    viewBtn.classList.toggle('active', _srvViewMode === 'compact');
    viewBtn.style.display = list.length >= 4 ? '' : 'none';
  }
  if (!list.length) {
    el.classList.remove('compact');
    el.innerHTML = `<div class="sp-noservers">
      <div class="sp-empty-title">У вас пока нет серверов</div>
      <div class="sp-empty-sub">Добавьте подписку или один server URI, чтобы подключиться.</div>
      <div class="sp-empty-actions">
        <button class="pg-btn acc" onclick="openImportSettings('subscription')">Добавить через подписку</button>
        <button class="pg-btn" onclick="openImportSettings('key')">Добавить ключом</button>
        <button class="pg-btn" onclick="showToast('WireGuard .conf импорт будет доступен в onboarding flow', 'info')">Импортировать WireGuard</button>
      </div>
      <button class="pg-btn ghost" onclick="showToast('Источник обычно выдаёт ваш VPN-провайдер или администратор сервера', 'info')">Где найти серверы</button>
    </div>`;
    updateSrvPanelSummary(list, []);
    return;
  }

  let filtered = list.filter(srv => {
    if (_srvTab === 'all') return true;
    const ms = serverHealthLatency(srv);
    if (_srvTab === 'fast') return ms != null && ms >= 0 && ms < 100;
    if (_srvTab === 'slow') return ms == null || ms < 0 || ms >= 100;
    return true;
  });

  if (_srvSearch) {
    filtered = filtered.filter(srv => {
      const hay = [
        serverDisplayName(srv),
        _srvHostPort(srv.url),
        protocolFromURL(srv.url),
        srv.country_code || '',
        srv.id || ''
      ].join(' ').toLowerCase();
      return hay.includes(_srvSearch);
    });
  }

  if (_srvSortByPing) {
    filtered.sort((a, b) => {
      const ma = serverHealthLatency(a), mb = serverHealthLatency(b);
      if (ma == null || ma < 0) return 1;
      if (mb == null || mb < 0) return -1;
      return ma - mb;
    });
  }

  updateSrvPanelSummary(list, filtered);
  el.classList.toggle('compact', _srvViewMode === 'compact' && filtered.length >= 4);

  if (!filtered.length) {
    el.innerHTML = '<div class="sp-noservers">Ничего не найдено.<br>Проверьте фильтр или поиск.</div>';
    return;
  }

  el.innerHTML = filtered.map((srv, idx) => {
    const isCur = srv.id === state.activeId;
    const pingMs = state.pings[srv.id];
    const { pingText, pingCls, barCls, barPct } = pingInfo(pingMs);
    const health = healthStatusInfo(srv);
    const protocol = protocolFromURL(srv.url);
    const hostPort = _srvHostPort(srv.url);
    const displayName = serverDisplayName(srv);
    const delay = idx < 15 ? ` style="animation-delay:${idx * 0.03}s"` : '';
    const knownCode = serverCountryCode(srv, displayName);
    const flag = knownCode ? countryFlag(knownCode) : FLAG_GLOBE;
    const srvIdArg = jsArg(srv.id);
    const srvUrlArg = jsArg(srv.url || '');
    const activeBadge = isCur ? '<span class="sp-badge active">Активен</span>' : '<span class="sp-badge">Готов</span>';
    return `
    <div class="spitem${isCur ? ' cur' : ''}" onclick="connectServer(${srvIdArg},event)" title="${esc(displayName + ' · ' + hostPort)}"${delay}>
      <div class="sp-main">
        <span class="sp-health-dot ${esc(health.cls)}" title="${esc(health.text)}"></span>
        <span class="sp-flag" data-srvid="${esc(srv.id)}">${flag}</span>
        <div class="sp-inf">
          <div class="sp-line"><span class="sp-nm" title="${esc(displayName)}">${esc(displayName)}</span>${activeBadge}</div>
          <div class="sp-dt">${esc(hostPort)}</div>
          <div class="sp-proto">${esc(protocol)} · ${esc(health.meta)}</div>
        </div>
      </div>
      <div class="sp-metrics">
        <div class="sp-ping ${pingCls}">${pingText}</div>
        <div class="sp-bar-track"><div class="sp-bar-fill ${barCls}" style="width:${barPct}%"></div></div>
      </div>
      <div class="sp-actions">
        <button class="srv-copy-btn" title="Копировать ссылку" onclick="copySrvUrl(event,${srvUrlArg})">Копия</button>
        <button class="srv-copy-btn" title="QR-код" onclick="showSrvQR(event,${srvIdArg})">QR</button>
        <button class="srv-copy-btn" title="История задержки" onclick="showLatencyHistory(event,${srvIdArg})">График</button>
        <button class="srv-del-btn" title="Удалить" onclick="deleteSrv(event,${srvIdArg})">Удалить</button>
      </div>
    </div>`;
  }).join('');
}

function openImportSettings(target) {
  navTo(4);
  const id = target === 'subscription' ? 'subUrlInp' : 'srvUrlInp';
  setTimeout(() => $id(id)?.focus(), 80);
}

function updateSrvPanelSummary(all, visible) {
  const total = all.length;
  const fast = all.filter(s => {
    const ms = serverHealthLatency(s);
    return ms != null && ms >= 0 && ms < 100;
  }).length;
  const active = all.find(s => s.id === state.activeId);
  $id('srvSummary')?.classList.toggle('minimal', total < 4);
  if ($id('srvSummaryTotal')) $id('srvSummaryTotal').textContent = String(visible.length || total || 0);
  if ($id('srvSummaryFast')) $id('srvSummaryFast').textContent = String(fast);
  if ($id('srvSummaryActive')) $id('srvSummaryActive').textContent = active ? serverDisplayName(active) : 'нет';
}

async function importSrvUrlFromPanel() {
  const inp = $id('srvPanelUrlInp');
  const url = inp ? inp.value.trim() : '';
  if (!url) { showToast('Вставьте server URI', 'warn'); return; }
  if (!isSupportedServerURI(url)) { showToast('Поддерживаются vless, trojan, ss, hysteria2, tuic, wireguard, vmess', 'warn'); return; }
  try {
    const r = await fetch(API + '/servers', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({ url })
    });
    if (r.status === 409) {
      showToast('Сервер уже добавлен', 'warn');
      if (inp) inp.value = '';
      return;
    }
    if (!r.ok) throw new Error(await r.text());
    if (inp) inp.value = '';
    showToast('Сервер добавлен', 'on');
    loadServers();
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'off');
  }
}

async function deleteSrv(e, id) {
  e.stopPropagation();
  if (!confirm('Удалить сервер?')) return;
  try {
    await fetch(API + '/servers/' + encodeURIComponent(id), { method: 'DELETE' });
    showToast('Сервер удалён', 'info');
    loadServers();
  } catch(_) { showToast('Ошибка удаления', 'off'); }
}

function copySrvUrl(e, url) {
  e.stopPropagation();
  navigator.clipboard.writeText(url)
    .then(() => showToast('Ссылка скопирована', 'on'))
    .catch(() => showToast('Ошибка копирования', 'off'));
}

function openServerInfo(title, html) {
  const modal = $id('serverInfoModal');
  const body = $id('serverInfoBody');
  if (!modal || !body) return;
  $id('serverInfoTitle').textContent = title || 'Сервер';
  body.innerHTML = html;
  modal.style.display = 'flex';
}
function closeServerInfoModal() {
  const modal = $id('serverInfoModal');
  if (modal) modal.style.display = 'none';
}
function _serverNameById(id) {
  const srv = state.servers.find(s => s.id === id);
  return srv ? serverDisplayName(srv) : id;
}
function showSrvQR(e, id) {
  e.stopPropagation();
  const name = _serverNameById(id);
  const src = API + '/servers/' + encodeURIComponent(id) + '/qr';
  closeSrv();
  openServerInfo(name || 'QR', `<img src="${src}" alt="QR" style="width:256px;height:256px;border-radius:8px;background:#fff;padding:8px">`);
}
async function showLatencyHistory(e, id) {
  e.stopPropagation();
  const name = _serverNameById(id);
  closeSrv();
  openServerInfo(name || 'Latency', '<div class="pg-sub">загрузка...</div>');
  try {
    const r = await fetch(API + '/servers/' + encodeURIComponent(id) + '/latency-history');
    if (!r.ok) throw new Error(r.status);
    const d = await r.json();
    const points = d.history || [];
    const rows = points.slice(-12).reverse().map(p => {
      const ms = p.latency_ms ?? p.ms ?? p.latency ?? '—';
      const at = p.at || p.time || p.timestamp || '';
      return `<div class="pg-row" style="width:100%"><span class="pg-sub">${esc(String(at)).slice(0,19)}</span><span class="pg-val">${esc(String(ms))} ms</span></div>`;
    }).join('');
    openServerInfo(name || 'Latency', rows || '<div class="pg-sub">история пока пуста</div>');
  } catch(e2) {
    openServerInfo(name || 'Latency', `<div class="pg-sub">ошибка: ${esc(e2.message)}</div>`);
  }
}

let _pingAllRunning = false;
async function pingAll() {
  if (_pingAllRunning) return;
  _pingAllRunning = true;
  const spin = $id('pingSpin');
  spin?.classList.add('vis');
  try {
    const r = await fetch(API + '/servers/ping-all');
    if (!r.ok) throw new Error(r.status);
    const data = await r.json();
    (data.results || []).forEach(p => { state.pings[p.id] = p.ok ? p.latency_ms : -1; });
    await loadServerHealth(false);
    renderServerList();
    updateServerPill();
  } catch (_) { /* ignore */ } finally {
    spin?.classList.remove('vis');
    _pingAllRunning = false;
  }
}

async function connectServer(id, evt) {
  const item = evt.currentTarget;
  item.classList.add('connecting');
  // Орб — состояние connecting (#13)
  orbStage.classList.remove('off', 'warm', 'loading');
  orbStage.classList.add('connecting');
  const srvName = state.servers.find(s=>s.id===id)?.name || id;
  OpTimer.start('connect', 'Переключение на <b>' + srvName + '</b>', 10000);
  try {
    const r = await fetch(`${API}/servers/${id}/connect`, { method: 'POST' });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json();
    state.activeId = id;
    renderServerList();
    updateServerPill();
    if (d.restart_required) {
      showToast(`⏳ ${srvName} — применяется...`, 'warn');
      // Сервер запустил TriggerApplyFull — следим через _watchApply
      OpTimer.update('connect', 'Перезапуск sing-box для <b>' + srvName + '</b>', 30000);
      // Переключаем op на apply чтобы _watchApply мог управлять баннером
      OpTimer.hide('connect');
      OpTimer.start('apply', 'Перезапуск sing-box — смена сервера на <b>' + srvName + '</b>', _applyHistory.estimate(30000));
      _watchApply('Смена сервера на <b>' + srvName + '</b>');
    } else {
      showToast(`Переключено: ${srvName}`, 'on');
      OpTimer.done('connect', 'Переключено на <b>' + srvName + '</b>');
    }
    setTimeout(closeSrv, 300);
    await pollStatus();
  } catch (e) {
    showToast('Ошибка: ' + e.message, 'off');
    OpTimer.fail('connect', 'Ошибка подключения: ' + e.message);
    item.classList.remove('connecting');
  } finally {
    orbStage.classList.remove('connecting');
  }
}

function updateServerPill() {
  const srv = state.servers.find(s => s.id === state.activeId);
  if (!srv) {
    $id('sflag').textContent = '—';
    $id('snm').textContent = 'Нет сервера';
    $id('sdt').textContent = '—';
    setPingEl($id('sping'), null);
    return;
  }
  const displayNamePill = serverDisplayName(srv);
  const knownCode = serverCountryCode(srv, displayNamePill);
  $id('sflag').innerHTML = knownCode ? countryFlag(knownCode) : FLAG_GLOBE;

  $id('snm').textContent = serverDisplayName(srv);
  $id('sdt').textContent = protocolFromURL(srv.url);

  // FIX #6: обновляем пинг через единую функцию
  setPingEl($id('sping'), state.pings[srv.id]);
  // A7: обновляем srv-meta сразу при смене сервера
  _updateSrvMeta();
}

// FIX #1 & #6: единственное место, где задаётся цвет пинга — через className, без style.color
function setPingEl(el, ms) {
  const { pingText, pingCls } = pingInfo(ms);
  el.textContent = pingText;
  // Сбрасываем все цветовые классы, потом ставим нужный
  el.className = el.className.replace(/\bcol-\w+/g, '').trim() + ' ' + pingCls;
}

function pingInfo(ms) {
  if (ms == null) return { pingText: '—', pingCls: 'col-dim', barCls: 'dim', barPct: 0 };
  if (ms < 0)     return { pingText: 'timeout', pingCls: 'col-r',  barCls: 'r',   barPct: 5 };
  const pct = Math.max(5, Math.min(100, 100 - (ms / 3)));
  const cls = ms < 80 ? 'g' : ms < 150 ? 'y' : 'r';
  return { pingText: ms + ' ms', pingCls: 'col-' + cls, barCls: cls, barPct: Math.round(pct) };
}

// ═══════════════════════════════════════════════════
// DIAG — FIX #4: кнопка ··· теперь работает
// ═══════════════════════════════════════════════════
async function runDiag() {
  const btn = $id('diagBtn');
  if (btn) {
    btn.disabled = true;
    btn.classList.add('busy');
  }
  showToast('Тест соединения...', 'info');
  try {
    const r = await fetch(API + '/diagnostics/test');
    const d = await r.json();
    if (d.ok) {
      showToast(`IP: ${d.external_ip}  ${d.latency_ms}ms${d.dns_leak?' ⚠ DNS leak':''}`, d.dns_leak ? 'warn' : 'on');
    } else {
      showToast('Нет соединения через прокси', 'off');
    }
  } catch (e) {
    showToast('Диагностика недоступна', 'off');
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.classList.remove('busy');
    }
  }
}

// ═══════════════════════════════════════════════════
