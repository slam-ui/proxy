// STATUS POLLING  →  GET /api/status
// ═══════════════════════════════════════════════════
let _pollStatusRunning = false;
let _pollStatusFails = 0;
let _backendWasOffline = false;
let _pollStatusPromise = null;

function _xrayReadyAtMs(xray) {
  const readyAtMs = Number(xray?.ready_at_ms || 0);
  if (readyAtMs > 0) return readyAtMs;
  const readyAt = Number(xray?.ready_at || 0);
  return readyAt > 0 ? readyAt * 1000 : 0;
}

function _etaTotalMsFromTimerStart(readyAtMs) {
  if (readyAtMs <= Date.now()) return undefined;
  return Math.max(1000, readyAtMs - OpTimer.getStartMs());
}

async function pollStatus() {
  if (_pollStatusRunning) return _pollStatusPromise;
  _pollStatusRunning = true;
  _pollStatusPromise = (async () => { try {
    const r = await fetch(API + '/status');
    if (!r.ok) throw new Error(r.status);
    const d = await r.json();
    _pollStatusFails = 0;
    $id('offlineBadge').classList.remove('vis');
    if (_backendWasOffline) { showToast('Соединение восстановлено', 'on'); _backendWasOffline = false; }

    const prevWarming = state.warming;
    state.running = d.xray?.running  ?? false;
    state.enabled = d.proxy?.enabled ?? false;
    state.warming = d.xray?.warming  ?? false;
    state.uptimeSec = d.proxy?.proxy_uptime_secs || 0;
    const addr = d.proxy?.address || '';
    state.proxyMode = addr ? (addr.includes('1080') ? 'PROXY' : 'TUN') : 'TUN';
    renderState();
    _updateSrvMeta();
    pollSecurityStatus();

    // OpTimer: warming / restarting — показываем обратный отсчёт
    const healthStatus = d.xray?.health_status || '';
    const readyAtMs = _xrayReadyAtMs(d.xray);
    const curTimer = OpTimer.current();
    const canOwnTimer = !curTimer || curTimer === 'toggle' || curTimer === 'warming';
    if (state.warming && canOwnTimer && curTimer !== 'warming') {
      const label = healthStatus === 'restarting'
        ? 'Перезапуск sing-box (восстановление TUN)...'
        : 'Запуск sing-box...';
      const estMs = readyAtMs > Date.now() ? Math.max(1000, readyAtMs - Date.now()) : 0;
      OpTimer.start('warming', label, estMs);
    } else if (state.warming && OpTimer.current() === 'warming') {
      // Обновляем label с деталями (попытки TUN recovery)
      const tunAttempt = d.xray?.tun_attempt || 0;
      const tunMax = d.xray?.tun_max_attempt || 0;
      const attemptInfo = tunAttempt > 0 ? ` (попытка ${tunAttempt}/${tunMax})` : '';
      const label = healthStatus === 'restarting'
        ? 'Перезапуск sing-box' + attemptInfo
        : 'Запуск sing-box...';
      const updEstMs = _etaTotalMsFromTimerStart(readyAtMs);
      OpTimer.update('warming', label, updEstMs);
    } else if (!state.warming && prevWarming && OpTimer.current() === 'warming') {
      // Warming закончился
      OpTimer.done('warming', 'sing-box запущен');
    }

    if (!state.activeId && state.servers.length > 0) { /* activeId управляется извне */ }
  } catch (_) {
    _pollStatusFails++;
    if (_pollStatusFails >= 3) { $id('offlineBadge').classList.add('vis'); _backendWasOffline = true; }
  } })();
  try {
    return await _pollStatusPromise;
  } finally {
    _pollStatusRunning = false;
    _pollStatusPromise = null;
  }
}

// ═══════════════════════════════════════════════════
// SRV-META (#4)
// ═══════════════════════════════════════════════════
function _updateSrvMeta() {
  const meta = $id('srvMeta');
  if (!meta) return;
  if (!state.running) { meta.classList.remove('vis'); return; }
  meta.classList.add('vis');

  // B4: показываем анимированный статус если прогрев
  if (state.warming) {
    const pingEl = $id('srvMetaPing');
    if (pingEl) { pingEl.textContent = '⏳ прогрев...'; pingEl.className = 'srv-meta-ping warming'; }
    return;
  }
  const pingEl2 = $id('srvMetaPing');
  if (pingEl2) pingEl2.classList.remove('warming');

  // Пинг
  const srv = state.servers.find(s => s.id === state.activeId);
  const pingMs = srv ? state.pings[srv.id] : null;
  const pingEl = $id('srvMetaPing');
  if (pingEl) {
    if (pingMs == null) { pingEl.textContent = '—'; pingEl.className = ''; }
    else if (pingMs < 0) { pingEl.textContent = 'timeout'; pingEl.className = 'srv-meta-ping slow'; }
    else {
      pingEl.textContent = pingMs + ' ms';
      pingEl.className = 'srv-meta-ping ' + (pingMs < 80 ? 'fast' : pingMs < 200 ? 'ok' : 'slow');
    }
  }
  // Режим
  const modeEl = $id('srvMetaMode');
  if (modeEl) {
    modeEl.textContent = state.proxyMode || 'TUN';
  }
  const locEl = $id('srvMetaLocation');
  if (locEl) {
    locEl.textContent = srv ? serverDisplayName(srv) : 'Нет сервера';
  }
}

function _updateSrvMetaFromStats(d) {
  void d;
}

let _securityStatusRunning = false;
let _securityStatusLastFetch = 0;

async function pollSecurityStatus(force = false) {
  const now = Date.now();
  if (_securityStatusRunning || (!force && now - _securityStatusLastFetch < 10000)) return;
  _securityStatusRunning = true;
  try {
    const r = await fetch(API + '/security/status');
    if (!r.ok) throw new Error(r.status);
    const d = await r.json();
    _securityStatusLastFetch = now;
    renderSecurityStatus(d);
  } catch (_) {
    renderSecurityStatus(null);
  } finally {
    _securityStatusRunning = false;
  }
}

function _setSecurityRow(key, level, mark, title, sub) {
  const row = $id('sec' + key + 'Row');
  const markEl = $id('sec' + key + 'Mark');
  const titleEl = $id('sec' + key + 'Title');
  const subEl = $id('sec' + key + 'Sub');
  if (row) row.className = 'security-row ' + level;
  if (markEl) {
    markEl.className = 'security-mark ' + level;
    markEl.textContent = mark;
  }
  if (titleEl) titleEl.textContent = title;
  if (subEl) subEl.textContent = sub || '';
}

function renderSecurityStatus(d) {
  if (!d) {
    _setSecurityRow('Tunnel', 'wait', '⏳', 'Статус защиты недоступен', 'нет ответа API');
    _setSecurityRow('DNS', 'wait', '⏳', 'DNS защита', 'статус будет обновлён позже');
    _setSecurityRow('Kill', 'wait', '⏳', 'Kill switch', 'статус будет обновлён позже');
    _setSecurityRow('Backup', 'wait', '⏳', 'Резервный сервер', 'статус будет обновлён позже');
    return;
  }
  const tunnelOn = !!(d.tunnel && d.tunnel.active);
  _setSecurityRow('Tunnel', tunnelOn ? 'ok' : 'bad', tunnelOn ? '✓' : '✕',
    tunnelOn ? 'Туннель активен' : 'Туннель отключён',
    tunnelOn ? 'маршрут защищён' : 'нажмите Подключить');

  const dnsOn = !!(d.dns_guard && d.dns_guard.enabled);
  const dnsMode = (d.dns_guard && d.dns_guard.mode) || 'warn';
  _setSecurityRow('DNS', dnsOn ? 'ok' : 'warn', dnsOn ? '✓' : '⚠',
    dnsOn ? 'DNS защита включена' : 'DNS защита выключена',
    dnsOn ? (dnsMode === 'strict' ? 'строгий режим' : 'режим предупреждений') : 'включается в настройках');

  const killOn = !!(d.kill_switch && d.kill_switch.enabled);
  _setSecurityRow('Kill', killOn ? 'ok' : 'warn', killOn ? '✓' : '⚠',
    killOn ? 'Kill switch включён' : 'Kill switch выключен',
    killOn ? 'fail-close защита активна' : 'можно включить в настройках');

  const count = d.backup_server && Number.isFinite(Number(d.backup_server.count)) ? Number(d.backup_server.count) : 0;
  const backup = !!(d.backup_server && d.backup_server.available);
  _setSecurityRow('Backup', backup ? 'ok' : 'warn', backup ? '✓' : '⚠',
    backup ? 'Резервный сервер есть' : 'Нет резервного сервера',
    count ? `${count} сервер(а) в списке` : 'добавьте второй сервер');
}

function openSecurityTarget(target) {
  if (target === 'dns') {
    navTo(4);
    setTimeout(() => {
      const section = $id('leakCheckBtn')?.closest('details');
      if (section) section.open = true;
      $id('leakCheckBtn')?.focus();
    }, 80);
    return;
  }
  if (target === 'kill') {
    navTo(4);
    setTimeout(() => $id('dnsGuardToggle')?.closest('details')?.scrollIntoView({ block: 'center' }), 80);
    return;
  }
  if (target === 'backup') {
    toggleSrv();
  }
}

// ═══════════════════════════════════════════════════
// STATS POLLING  →  GET /api/stats
// ═══════════════════════════════════════════════════
let _pollStatsRunning = false;

async function pollStats() {
  if (_pollStatsRunning) return;
  _pollStatsRunning = true;
  try {
    const r = await fetch(API + '/stats');
    if (!r.ok) return;
    const d = await r.json();

    const upBps = (d.proxy_up || 0) + (d.direct_up || 0);
    const dnBps = (d.proxy_dn || 0) + (d.direct_dn || 0);
    pushChartData(upBps, dnBps);

    $id('stup').textContent   = fmtBytes(d.sess_up_bytes || 0);
    $id('stdn').textContent   = fmtBytes(d.sess_dn_bytes || 0);
    $id('stconn').textContent = d.active_connections || 0;
    $id('stconnSub').textContent = d.api_available ? 'активных' : 'API недост.';
    _updateSrvMetaFromStats(d);
  } catch (_) {
    pushChartData(0, 0);
  } finally {
    _pollStatsRunning = false;
  }
}

// ═══════════════════════════════════════════════════
// CONNECTIONS POLLING  →  GET /api/connections
// ═══════════════════════════════════════════════════
let _pollConnsRunning = false;
let _prevConnBytes = {};
let _prevConnTs = 0;

// ── Фильтр соединений (all / user / sys) ────────────
let _connFilter = 'all';
function setConnFilter(f) {
  _connFilter = f;
  ['All','User','Sys'].forEach(k => {
    const btn = $id('cf' + k);
    btn.classList.remove('active','sys-active','usr-active');
  });
  if (f === 'all')  { $id('cfAll').classList.add('active'); }
  if (f === 'user') { $id('cfUser').classList.add('usr-active'); }
  if (f === 'sys')  { $id('cfSys').classList.add('sys-active'); }
}

async function addRuleFromConnection(value, type, action, evt) {
  if (evt) evt.stopPropagation();
  value = String(value || '').trim();
  if (!value || value === '—') return;
  try {
    const r = await fetch(API + '/connections/rule', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value, type, action })
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    showToast(`Правило ${action}: ${value}`, 'on');
    loadRules();
  } catch (e) {
    showToast('Правило из соединения: ' + e.message, 'off');
  }
}

// Системные процессы Windows / Linux
const SYS_PROCS = new Set([
  'svchost.exe','system','lsass.exe','csrss.exe','winlogon.exe','services.exe',
  'smss.exe','wininit.exe','spoolsv.exe','lsaiso.exe','dwm.exe','fontdrvhost.exe',
  'taskhostw.exe','sihost.exe','ctfmon.exe','audiodg.exe','runtimebroker.exe',
  'searchindexer.exe','wuauclt.exe','msiexec.exe','conhost.exe','dllhost.exe',
  'ntoskrnl.exe','registry','memory compression','systemd','kworker','kthread',
  'init','kernel','ksoftirqd','migration','rcu_','watchdog','netns',
]);
function isSystemProc(proc) {
  if (!proc) return true; // нет процесса — системное
  const p = proc.toLowerCase();
  for (const s of SYS_PROCS) { if (p === s || p.startsWith(s)) return true; }
  return false;
}

// ── GeoIP кэш + автоопределение страны ──────────────
const _geoCache = {}; // hostname → 'RU'
const _geoPending = {}; // hostname → Promise<'RU'|null>

// ISO 3166-1 alpha-2 валидные коды
const _validCC = new Set(['AD','AE','AF','AG','AL','AM','AO','AR','AT','AU','AZ',
  'BA','BB','BD','BE','BF','BG','BH','BI','BJ','BN','BO','BR','BS','BT','BW','BY','BZ',
  'CA','CD','CF','CG','CH','CI','CL','CM','CN','CO','CR','CU','CV','CY','CZ',
  'DE','DJ','DK','DM','DO','DZ','EC','EE','EG','ER','ES','ET','FI','FJ','FR',
  'GA','GB','GD','GE','GH','GI','GL','GM','GN','GQ','GR','GT','GW','GY',
  'HK','HN','HR','HT','HU','ID','IE','IL','IN','IQ','IR','IS','IT',
  'JM','JO','JP','KE','KG','KH','KI','KM','KN','KP','KR','KW','KZ',
  'LA','LB','LC','LI','LK','LR','LS','LT','LU','LV','LY',
  'MA','MC','MD','ME','MG','MK','ML','MM','MN','MO','MR','MT','MU','MV','MW','MX','MY','MZ',
  'NA','NC','NE','NG','NI','NL','NO','NP','NZ',
  'OM','PA','PE','PG','PH','PK','PL','PS','PT','PY',
  'QA','RE','RO','RS','RU','RW',
  'SA','SB','SC','SD','SE','SG','SI','SK','SL','SM','SN','SO','SR','SS','ST','SV','SY','SZ',
  'TD','TG','TH','TJ','TL','TM','TN','TO','TR','TT','TW','TZ',
  'UA','UG','US','UY','UZ','VA','VC','VE','VN','VU','WS','YE','ZA','ZM','ZW']);

// Словарь городов/стран → код (нижний регистр, без пробелов)
const _cityMap = {
  'germany':'DE','frankfurt':'DE','berlin':'DE','munich':'DE',
  'netherlands':'NL','holland':'NL','amsterdam':'NL',
  'france':'FR','paris':'FR','marseille':'FR',
  'finland':'FI','helsinki':'FI',
  'sweden':'SE','stockholm':'SE','gothenburg':'SE',
  'norway':'NO','oslo':'NO',
  'poland':'PL','warsaw':'PL','krakow':'PL',
  'czech':'CZ','prague':'CZ',
  'austria':'AT','vienna':'AT',
  'switzerland':'CH','zurich':'CH','geneva':'CH','bern':'CH',
  'uk':'GB','england':'GB','london':'GB','britain':'GB','manchester':'GB','edinburgh':'GB',
  'usa':'US','america':'US','newyork':'US','losangeles':'US','chicago':'US','dallas':'US','seattle':'US','miami':'US',
  'canada':'CA','toronto':'CA','montreal':'CA','vancouver':'CA',
  'japan':'JP','tokyo':'JP','osaka':'JP','kyoto':'JP',
  'singapore':'SG',
  'australia':'AU','sydney':'AU','melbourne':'AU','brisbane':'AU',
  'newzealand':'NZ','auckland':'NZ',
  'russia':'RU','moscow':'RU','spb':'RU',
  'ukraine':'UA','kyiv':'UA','kharkiv':'UA',
  'turkey':'TR','istanbul':'TR','ankara':'TR',
  'latvia':'LV','riga':'LV',
  'lithuania':'LT','vilnius':'LT',
  'estonia':'EE','tallinn':'EE',
  'moldova':'MD','chisinau':'MD',
  'georgia':'GE','tbilisi':'GE',
  'armenia':'AM','yerevan':'AM',
  'kazakhstan':'KZ','almaty':'KZ',
  'hongkong':'HK','hong':'HK',
  'taiwan':'TW','taipei':'TW',
  'southkorea':'KR','korea':'KR','seoul':'KR',
  'india':'IN','mumbai':'IN','delhi':'IN','bangalore':'IN','chennai':'IN',
  'brazil':'BR','saopaulo':'BR','rio':'BR',
  'romania':'RO','bucharest':'RO',
  'hungary':'HU','budapest':'HU',
  'bulgaria':'BG','sofia':'BG',
  'serbia':'RS','belgrade':'RS',
  'spain':'ES','madrid':'ES','barcelona':'ES',
  'italy':'IT','rome':'IT','milan':'IT',
  'portugal':'PT','lisbon':'PT',
  'belgium':'BE','brussels':'BE',
  'denmark':'DK','copenhagen':'DK',
  'ireland':'IE','dublin':'IE',
  'luxembourg':'LU',
  'iceland':'IS','reykjavik':'IS',
  'southafrica':'ZA','capetown':'ZA','johannesburg':'ZA',
  'uae':'AE','dubai':'AE','abudhabi':'AE',
  'israel':'IL','telaviv':'IL',
  'mexico':'MX','mexicocity':'MX',
  'argentina':'AR','buenosaires':'AR',
  'colombia':'CO','bogota':'CO',
  'chile':'CL','santiago':'CL',
  'egypt':'EG','cairo':'EG',
  'nigeria':'NG','lagos':'NG',
  'kenya':'KE','nairobi':'KE',
  'vietnam':'VN','hanoi':'VN','hochiminh':'VN',
  'thailand':'TH','bangkok':'TH',
  'indonesia':'ID','jakarta':'ID',
  'malaysia':'MY','kualalumpur':'MY',
  'philippines':'PH','manila':'PH',
  'pakistan':'PK','karachi':'PK','lahore':'PK',
};

// ── Persistent country code cache (localStorage) ──────────────────────────
function _saveCountryCodeCache() {
  try {
    const cache = {};
    (state.servers || []).forEach(s => {
      if (s.id && s.country_code && s.country_code !== '??') cache[s.id] = s.country_code;
    });
    localStorage.setItem('safesky-cc-cache', JSON.stringify(cache));
  } catch(_) {}
}
function _restoreCountryCodeCache() {
  try {
    const cache = JSON.parse(localStorage.getItem('safesky-cc-cache') || '{}');
    (state.servers || []).forEach(s => {
      if (s.id && (!s.country_code || s.country_code === '??') && cache[s.id]) {
        s.country_code = cache[s.id];
      }
    });
  } catch(_) {}
}

// Извлекает код страны из имени сервера:
// "DE_1", "US-NY", "🇩🇪 Server", "Frankfurt", "Germany" и т.д.
function extractCountryFromName(name) {
  if (!name) return null;
  // 1. Флаг-эмодзи: 🇩🇪 → DE
  const pts = [...name];
  if (pts.length >= 2) {
    const c0 = pts[0].codePointAt(0), c1 = pts[1].codePointAt(0);
    if (c0 >= 0x1F1E6 && c0 <= 0x1F1FF && c1 >= 0x1F1E6 && c1 <= 0x1F1FF) {
      const code = String.fromCharCode(c0 - 0x1F1E6 + 65, c1 - 0x1F1E6 + 65);
      if (_validCC.has(code)) return code;
    }
  }
  // 2. Код в начале строки: "DE_1", "DE-Server", "DE1 ", "DE " (не "DELETE")
  const pfx = name.match(/^([A-Za-z]{2})(?=[\s_\-\.0-9]|$)/);
  if (pfx) { const c = pfx[1].toUpperCase(); if (_validCC.has(c)) return c; }
  // 3. В скобках: "(DE)", "[US]"
  const brk = name.match(/[\(\[]([A-Za-z]{2})[\)\]]/);
  if (brk) { const c = brk[1].toUpperCase(); if (_validCC.has(c)) return c; }
  // 4. Города/страны
  const norm = name.toLowerCase().replace(/[\s\-_\.]/g, '');
  for (const [k, v] of Object.entries(_cityMap)) { if (norm.includes(k)) return v; }
  return null;
}

async function resolveCountryCode(hostname) {
  if (!hostname || hostname === '—') return null;
  if (_geoCache[hostname]) return _geoCache[hostname];
  if (_geoPending[hostname]) return _geoPending[hostname];

  _geoPending[hostname] = (async () => { try {
    // Используем локальный /api/geoip — определение страны без внешних запросов
    // (PTR-lookup + паттерны hostname, никакого ip-api.com)
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 8000);
    let r;
    try {
      r = await fetch(`${API}/geoip?host=${encodeURIComponent(hostname)}`, { signal: ctrl.signal });
    } finally {
      clearTimeout(timer);
    }
    if (!r.ok) return null;
    const d = await r.json();
    const cc = String(d.country_code || '').toUpperCase();
    if (/^[A-Z]{2}$/.test(cc)) {
      _geoCache[hostname] = cc;
      setTimeout(_saveCountryCodeCache, 200);
      return cc;
    }
  } catch(_) {}
  finally { delete _geoPending[hostname]; }
  return null;
  })();
  return _geoPending[hostname];
}

// ── Имя сервера из VLESS-фрагмента (#Название) ──────
function serverDisplayName(srv) {
  if (!srv) return 'Нет сервера';
  try {
    const hash = decodeURIComponent((srv.url || '').split('#')[1] || '');
    if (hash.trim()) return hash.trim();
  } catch(_) {}
  return srv.name || 'Сервер';
}

async function pollConnections() {
  if (_pollConnsRunning) return;
  _pollConnsRunning = true;
  try {
    const r = await fetch(API + '/connections');
    if (!r.ok) return;
    const d = await r.json();
    renderConnections(d.connections || []);
  } catch (_) {}
  finally { _pollConnsRunning = false; }
}

function renderConnections(conns) {
  const el = $id('connList');
  const cnt = conns.length;
  $id('ccnt').textContent = cnt;

  if (!cnt) {
    el.innerHTML = '<div class="conn-empty">нет соединений</div>';
    el.classList.remove('stable');
    _prevConnBytes = {};
    return;
  }

  const now = Date.now();

  // ── 1. Группируем одинаковые соединения ────────────────────────────────
  // Ключ группы: процесс + хост + сеть + outbound
  const groupMap = new Map();
  conns.forEach(c => {
    const host      = c.metadata?.host || c.metadata?.destinationIP || '—';
    const fullPath  = c.metadata?.processPath || c.process || '';
    const proc      = basename(fullPath);
    const net       = c.metadata?.network || '';
    const ob        = c.outbound || (c.chains && c.chains[0]) || 'direct';
    const rule      = c.rulePayload || c.rule || '';
    const key       = `${proc}|${host}|${net}|${ob}|${rule}`;

    const bytes  = (c.upload || 0) + (c.download || 0);
    if (groupMap.has(key)) {
      const g = groupMap.get(key);
      g.totalBytes += bytes;
      g.count++;
      g.ids.push(c.id);
    } else {
      groupMap.set(key, { key, host, proc, fullPath, net, ob, rule, totalBytes: bytes, count: 1, ids: [c.id] });
    }
  });

  const groups = [...groupMap.values()];

  // Обновляем счётчик: показываем уникальные группы / всего
  $id('ccnt').textContent = groups.length < cnt ? `${groups.length} (${cnt})` : cnt;

  // ── 2. Считаем дельту скорости по ключу группы ────────────────────────
  const newBytes = {};
  groups.forEach(g => { newBytes[g.key] = g.totalBytes; });

  // ── 3. Делим на системные и пользовательские ──────────────────────────
  const userGroups = groups.filter(g => !isSystemProc(g.proc));
  const sysGroups  = groups.filter(g =>  isSystemProc(g.proc));

  let filtered;
  if (_connFilter === 'user') filtered = userGroups;
  else if (_connFilter === 'sys') filtered = sysGroups;
  else filtered = groups;

  // ── 4. Рендер одной группы ────────────────────────────────────────────
  function renderItem(g) {
    const { host, proc, fullPath, net, ob, rule, count, key } = g;
    const hostShort = host.length > 28 ? host.slice(0, 27) + '…' : host;
    const type  = outboundType(ob);
    const isSys = isSystemProc(proc);
    // Иконка: реальная из .exe если есть путь, иначе emoji-фолбэк
    const fallIco = proc.match(/chrome|chromium/i) ? '🌐' : proc.match(/firefox/i) ? '🦊' :
                    proc.match(/telegram/i) ? '✈️' : proc.match(/discord/i) ? '💬' :
                    isSys ? '⚙️' : '📦';
    const exePath = (fullPath && fullPath.endsWith('.exe')) ? fullPath : '';
    const ico = exePath
      ? `<img src="${API}/procicon?path=${encodeURIComponent(exePath)}" width="16" height="16" style="border-radius:3px;object-fit:contain;vertical-align:middle" alt="${esc(proc)}" onerror="this.outerHTML='${fallIco}'">`
      : fallIco;

    let speedStr = '';
    if (_prevConnBytes[key] != null && now - _prevConnTs > 0) {
      const dt = (now - _prevConnTs) / 1000 || 1;
      const delta = Math.max(0, (newBytes[key] || 0) - (_prevConnBytes[key] || 0));
      const { val, unit } = fmtSpeed(delta / dt);
      speedStr = `↕ ${val} ${unit}`;
    }

    // Флаг страны: берём из кэша или запускаем async-резолвинг
    const safeKey = key.replace(/[^\w]/g, '_');
    const flagId  = `cflag_${safeKey.slice(0, 60)}`;
    let   flagHtml = FLAG_GLOBE;
    const cachedCode = _geoCache[host];
    if (cachedCode) {
      flagHtml = countryFlag(cachedCode);
    } else if (host && host !== '—') {
      // Async резолв — обновим DOM когда придёт ответ
      resolveCountryCode(host).then(code => {
        if (code) {
          document.querySelectorAll(`[data-cflagkey="${CSS.escape(flagId)}"]`)
            .forEach(el => { el.innerHTML = countryFlag(code); });
        }
      });
    }

    const countBadge = count > 1
      ? `<span style="font-size:8px;background:var(--acc);color:#fff;border-radius:6px;padding:1px 5px;font-family:var(--mono);flex-shrink:0;line-height:1">${count}×</span>`
      : '';
    const hostArg = jsArg(host);
    const ruleButtons = host && host !== '—'
      ? `<span class="ci-actions">
          <button class="ci-act" title="Всегда через прокси" onclick="addRuleFromConnection(${hostArg},'domain','proxy',event)">↗</button>
          <button class="ci-act" title="Всегда напрямую" onclick="addRuleFromConnection(${hostArg},'domain','direct',event)">→</button>
          <button class="ci-act" title="Блокировать" onclick="addRuleFromConnection(${hostArg},'domain','block',event)">×</button>
        </span>`
      : '';

    return `
    <div class="ci ${type}">
      <div class="cdot ${type}"></div>
      <span style="font-size:14px;line-height:1;flex-shrink:0;display:flex;align-items:center">${ico}</span>
      <div class="ci-exe" title="${esc(proc || '—')}"><span class="ci-exe-name">${esc(proc || '—')}</span>${countBadge}</div>
      <div class="ci-host-col" title="${esc(host)}">
        <span data-cflagkey="${esc(flagId)}" style="font-size:13px;line-height:1;flex-shrink:0">${flagHtml}</span>
        <span>${esc(host)}${net ? ' <span style="color:var(--muted);font-size:8.5px">· ' + net.toUpperCase() + '</span>' : ''}</span>
      </div>
      <div class="ci-right">
        ${ruleButtons}
        ${isSys ? '<span class="ci-sys-badge" style="margin-right:2px">SYS</span>' : ''}
        ${speedStr ? `<span style="font-size:8px;font-family:var(--mono);color:var(--muted)">${esc(speedStr)}</span>` : ''}
        <span class="cbadge ${type}">${badgeText(ob)}</span>
      </div>
    </div>`;
  }

  if (_connFilter !== 'all') {
    el.innerHTML = filtered.slice(0, 30).map(renderItem).join('');
  } else {
    let html = '';
    if (userGroups.length) {
      html += `<div class="conn-section-hd">Пользовательские · ${userGroups.length}</div>`;
      html += userGroups.slice(0, 20).map(renderItem).join('');
    }
    if (sysGroups.length) {
      html += `<div class="conn-section-hd">Системные · ${sysGroups.length}</div>`;
      html += sysGroups.slice(0, 20).map(renderItem).join('');
    }
    if (!html) html = '<div class="conn-empty">нет соединений</div>';
    el.innerHTML = html;
  }

  _prevConnBytes = { ...newBytes };
  _prevConnTs = now;
  // After first successful render, mark list as stable to disable entrance animation
  setTimeout(() => { el.classList.add('stable'); }, 50);
  // Auto-fit exe column to widest content after DOM update
  requestAnimationFrame(() => autoFitExeColumn());
}

// ── Column resize ──────────────────────────────────────────────────────────────
// Вычисляет позицию ручки ресайза: paddingLeft + (6px dot + 22px icon + 2×gap) + exeW
function _getResizeHandleLeft() {
  const hdr = document.getElementById('connColHd');
  if (!hdr) return 110 + 14 + 6 + 22 + 12; // fallback
  const cs  = getComputedStyle(hdr);
  const pl  = parseFloat(cs.paddingLeft) || 14;
  const gap = parseFloat(cs.columnGap || cs.gap) || 6;
  const exeW = parseInt(getComputedStyle(document.documentElement)
                 .getPropertyValue('--ci-exe-w')) || 110;
  // grid: [dot 6px] [gap] [icon 22px] [gap] [exeW] → граница столбца
  return pl + 6 + gap + 22 + gap + exeW;
}

function _positionResizeHandle() {
  const handle = document.getElementById('exeColHandle');
  if (!handle) return;
  handle.style.left = _getResizeHandleLeft() + 'px';
}

function autoFitExeColumn() {
  const items = document.querySelectorAll('.ci-exe');
  if (!items.length) return;
  const cv = document.createElement('canvas');
  const cx = cv.getContext('2d');
  cx.font = '8.5px monospace';
  let maxW = 60;
  items.forEach(el => {
    // textContent может содержать счётчик "6×" — берём только первый текстовый узел
    const txt = el.childNodes[0]?.textContent || el.textContent;
    const w = cx.measureText(txt).width + 20;
    if (w > maxW) maxW = w;
  });
  const newW = Math.max(60, Math.min(220, Math.ceil(maxW)));
  document.documentElement.style.setProperty('--ci-exe-w', newW + 'px');
  _positionResizeHandle();
}

(function initColResize() {
  let dragging = false, startX = 0, startW = 0;
  const handle = () => document.getElementById('exeColHandle');

  // Начальное позиционирование после загрузки DOM
  document.addEventListener('DOMContentLoaded', _positionResizeHandle);
  // Повторно после первого рендера соединений
  setTimeout(_positionResizeHandle, 500);

  document.addEventListener('mousedown', e => {
    if (!e.target.closest('#exeColHandle')) return;
    dragging = true;
    startX = e.clientX;
    startW = parseInt(getComputedStyle(document.documentElement)
               .getPropertyValue('--ci-exe-w')) || 110;
    handle()?.classList.add('dragging');
    e.preventDefault();
  });
  document.addEventListener('mousemove', e => {
    if (!dragging) return;
    const newW = Math.max(60, Math.min(300, startW + e.clientX - startX));
    document.documentElement.style.setProperty('--ci-exe-w', newW + 'px');
    _positionResizeHandle();
  });
  document.addEventListener('mouseup', () => {
    if (!dragging) return;
    dragging = false;
    handle()?.classList.remove('dragging');
  });
  // Двойной клик — авто-подбор ширины
  document.addEventListener('dblclick', e => {
    if (!e.target.closest('#exeColHandle')) return;
    autoFitExeColumn();
  });
})();

function outboundType(ob) {
  const l = (ob || '').toLowerCase();
  if (l.includes('block') || l.includes('reject')) return 'b';
  if (l === 'direct') return 'd';
  return 'p';
}
function badgeText(ob) {
  const l = (ob || '').toLowerCase();
  if (l.includes('block') || l.includes('reject')) return 'BLOCK';
  if (l === 'direct') return 'DIRECT';
  return 'PROXY';
}

// ═══════════════════════════════════════════════════
