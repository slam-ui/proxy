// LOGS PAGE  →  GET /api/events  (SSE или polling)
// ═══════════════════════════════════════════════════
let logLines = [];
let logStreaming = false;
let logAutoScroll = true;
let logSSE = null;
let logPollInterval = null;
let logAnomalies = [];
const LOG_ANOMALY_STORAGE_KEY = 'safesky.log.anomalySnapshots.v1';
const LOG_MAX_LINES = 800;
let logCounts = { total: 0, I: 0, W: 0, E: 0, D: 0 };
let logStreamMode = 'IDLE';

function _setText(id, value) {
  const el = $id(id);
  if (el) el.textContent = value;
}

function _formatLogTime(ts) {
  const raw = String(ts || '');
  if (!raw) {
    return formatTime(new Date(), { hour12: false });
  }
  if (raw.includes('T') && raw.length >= 19) return raw.slice(11, 19);
  const d = new Date(raw);
  if (!isNaN(d.getTime())) return formatTime(d, { hour12: false });
  return raw.slice(0, 19);
}

function setLogStreamState(mode) {
  logStreamMode = mode || 'IDLE';
  const el = $id('logStreamState');
  if (!el) return;
  el.textContent = logStreamMode;
  el.className = 'log-stream-pill ' + logStreamMode.toLowerCase();
}

function _currentLogLevelFilter() {
  return $id('logLevelFilter')?.value || 'warn';
}

function _logMatchesFilters(line) {
  const f = $id('logFilter')?.value.toLowerCase().trim() || '';
  const level = _currentLogLevelFilter();
  const msgOk = !f || (line.dataset.search || line.dataset.msg || '').includes(f);
  const lvlOk = level === 'all'
    || (level === 'warn' && ['W', 'E'].includes(line.dataset.level))
    || line.dataset.level === level;
  return msgOk && lvlOk;
}

function _applyLogVisibility(line) {
  const show = _logMatchesFilters(line);
  line.style.display = show ? '' : 'none';
  return show;
}

function _visibleLogCount() {
  return logLines.reduce((n, line) => n + (line.style.display === 'none' ? 0 : 1), 0);
}

function renderLogMetrics() {
  _setText('logHeroTotal', String(logCounts.total));
  _setText('logHeroErrors', String(logCounts.E || 0));
  _setText('logWarnCount', String(logCounts.W || 0));
  _setText('logErrorCount', String(logCounts.E || 0));
  _setText('logDebugCount', String(logCounts.D || 0));
  _setText('logVisibleCount', String(_visibleLogCount()));
  _setText('logHeroMode', logAutoScroll ? 'AUTO' : 'PAUSE');
  _setText('logHeroAnomalies', String(logAnomalies.length));
  _setText('logHeroSaved', String(_savedAnomalySnapshots().length));
}

function stopLogStream() {
  logSSE?.close();
  logSSE = null;
  logStreaming = false;
  if (logPollInterval !== null) {
    clearInterval(logPollInterval);
    logPollInterval = null;
  }
  setLogStreamState('IDLE');
}

function startLogStream() {
  if (logStreaming) return;
  logSSE?.close();
  logSSE = null;
  logStreaming = true;
  setLogStreamState('SSE');
  const startPolling = () => {
    if (!logStreaming) return;
    setLogStreamState('POLL');
    if (logPollInterval === null) {
      pollLogs();
      logPollInterval = setInterval(pollLogs, 2000);
    }
  };
  try {
    logSSE = new EventSource(API + '/events');
    logSSE.onopen = () => setLogStreamState('SSE');
    logSSE.onmessage = e => {
      if (!logStreaming) return;
      try {
        const ev = JSON.parse(e.data);
        pushLog(ev.time || new Date().toISOString(), ev.level || 'I', ev.message || e.data);
      } catch(_) {
        pushLog(new Date().toISOString(), 'I', e.data);
      }
    };
    logSSE.onerror = () => {
      if (!logStreaming) return;
      logSSE?.close();
      logSSE = null;
      startPolling();
    };
  } catch(_) {
    startPolling();
  }
}

let lastLogId = 0;
async function pollLogs() {
  try {
    const r = await fetch(API + '/events?since=' + lastLogId);
    if (!r.ok) return;
    setLogStreamState('POLL');
    const data = await r.json();
    const events = Array.isArray(data) ? data : (data.events || []);
    if (data.latest_id != null) lastLogId = data.latest_id;
    events.slice().sort((a, b) => {
      const ta = new Date(a.time || a.timestamp || 0).getTime();
      const tb = new Date(b.time || b.timestamp || 0).getTime();
      if (Number.isFinite(ta) && Number.isFinite(tb) && ta !== tb) return ta - tb;
      return Number(a.id || 0) - Number(b.id || 0);
    }).forEach(ev => {
      pushLog(ev.time || '', ev.level || 'I', ev.message || JSON.stringify(ev));
    });
  } catch(_) {
    if (logStreaming) setLogStreamState('ERR');
  }
}

function _highlightLogMsg(msg) {
  const safe = esc(msg);
  // Сначала IP — точная проверка всех 4 октетов
  const withIPs = safe.replace(/\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b/g,
    (m) => {
      const parts = m.split('.').map(Number);
      return parts.every(p => p <= 255)
        ? '<span class="log-ip">' + m + '</span>'
        : m;
    });
  // Домены — только если не внутри уже расставленных тегов
  const withDomains = withIPs.replace(
    /(?:^|(?<=>|[\s,;:(]))([a-z0-9][a-z0-9-]{0,61}\.[a-z]{2,})(?=[)\s,;:\n<]|$)/gi,
    (full, d) => '<span class="log-domain">' + d + '</span>'
  );
  return withDomains;
}

// ── Дедупликация повторяющихся лог-строк ──────────────────────────
let _lastLogKey = '';
let _lastLogEl  = null;
let _lastLogCnt = 0;

// Нормализует сообщение для сравнения: убирает переменные части (порты, goroutine-ID, таймеры,
// прогресс скачивания — проценты, скорость, размеры файлов)
function _normalizeLogKey(msg) {
  return msg
    .replace(/\b\d{1,3}%/g, 'PCT')                          // 34% 100% → PCT
    .replace(/[\d.]+\s*(MB|KB|GB|B)\/[\d.]+\s*(MB|KB|GB|B)/gi, 'SZ/SZ') // 1.2 MB/5.3 MB
    .replace(/[\d.]+\s*(MB|KB|GB|B)\/s/gi, 'SPD')           // 1.5 MB/s
    .replace(/\b\d{4,5}\b/g, 'PORT')                        // порты: 59398 → PORT
    .replace(/\[\d+\]/g, '')                                 // [3306] goroutine ID
    .replace(/\[\d+\s+[\d.]+\w+\]/g, '')                    // [3376472556 19.26s]
    .replace(/\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/g, 'IP') // IP-адреса
    .replace(/\s+/g, ' ').trim();
}

// Очищает sing-box сообщения от шума
function _cleanLogMsg(msg) {
  return msg
    // убираем "[3306]" goroutine ID в начале
    .replace(/^(ERROR|WARN|INFO|DEBUG)\[\d+\]\s*/i, '')
    // убираем "[3376472556 19.26s]" timing
    .replace(/\[\d+\s+[\d.]+\w+\]\s*/g, '')
    // сокращаем Windows wsarecv timeout
    .replace(/wsarecv: A connection attempt failed because the connected party did not properly respond after a period of time, or established connection failed because connected host has failed to respond\./gi,
             'connection timed out')
    // context canceled/deadline
    .replace(/context canceled/gi, 'cancelled')
    .replace(/context deadline exceeded/gi, 'deadline exceeded')
    // EOF
    .replace(/\bEOF\b/g, 'connection closed')
    .trim();
}

function _isRoutineHttpLog(lvlChar, msg) {
  if (!['I', 'D'].includes(lvlChar)) return false;
  const m = String(msg || '').match(/^(?:→\s*)?(GET|HEAD)\s+(\/api\/[^\s]+)\s+(200|304)(?:\s|$)/i);
  if (!m) return false;
  return /^\/api\/(?:settings|engine\/version|geoip|clipboard\/vless|tun\/rules|singbox-config|diagnostics\/crashes|servers\/ping-all|apps\/processes|profiles|stats|connections|servers|lan-info)(?:\?|$|\/)/i.test(m[2]);
}

function _formatLogMsg(msg) {
  const clean = String(msg || '');
  const http = clean.match(/^(?:→\s*)?(GET|POST|PUT|PATCH|DELETE|HEAD)\s+(\S+)\s+(\d{3})(?:\s+([0-9.]+\s*(?:ms|s|µs|us)))?(.*)$/i);
  if (http) {
    const method = http[1].toUpperCase();
    const path = http[2];
    const status = http[3];
    const dur = (http[4] || '').replace(/\s+/g, '');
    const tail = (http[5] || '').trim();
    const statusCls = status[0] === '2' ? 'ok' : status[0] === '3' ? 'mid' : 'bad';
    return `<span class="log-http">
      <span class="log-method">${esc(method)}</span>
      <span class="log-path">${_highlightLogMsg(path)}</span>
      <span class="log-status ${statusCls}">${esc(status)}</span>
      ${dur ? `<span class="log-duration">${esc(dur)}</span>` : ''}
      ${tail ? `<span class="log-tail">${_highlightLogMsg(tail)}</span>` : ''}
    </span>`;
  }
  return _highlightLogMsg(clean);
}

function _savedAnomalySnapshots() {
  try {
    const raw = localStorage.getItem(LOG_ANOMALY_STORAGE_KEY);
    const list = raw ? JSON.parse(raw) : [];
    return Array.isArray(list) ? list : [];
  } catch(_) {
    return [];
  }
}

function _storeAnomalySnapshots(list) {
  try {
    localStorage.setItem(LOG_ANOMALY_STORAGE_KEY, JSON.stringify(list.slice(0, 12)));
  } catch(_) {}
}

function _isLogAnomaly(lvlChar, cleanMsg) {
  if (_isRoutineHttpLog(lvlChar, cleanMsg)) return false;
  if (lvlChar === 'W' && _isBenignLogWarning(cleanMsg)) return false;
  if (lvlChar === 'E' || lvlChar === 'W') return true;
  return /\b(error|failed|fail|panic|timeout|denied|refused|unreachable|deadline|ошибка|сбой|таймаут|отказ|недоступ)\b/i.test(cleanMsg);
}

function _isBenignLogWarning(msg) {
  const m = String(msg || '').toLowerCase();
  if (!m.includes('dns')) return false;
  return m.includes('context canceled')
    || m.includes('read/write on closed pipe')
    || m.includes('clientconn.close');
}

function _rememberAnomaly(ts, lvlChar, rawMsg, cleanMsg) {
  if (!_isLogAnomaly(lvlChar, cleanMsg)) return;
  const key = lvlChar + ':' + _normalizeLogKey(cleanMsg);
  const now = ts || new Date().toISOString();
  const existing = logAnomalies.find(item => item.key === key);
  if (existing) {
    existing.count++;
    existing.last = now;
  } else {
    logAnomalies.push({
      key,
      time: now,
      last: now,
      level: lvlChar,
      message: cleanMsg || rawMsg || '',
      count: 1
    });
    if (logAnomalies.length > 80) logAnomalies.shift();
  }
  renderAnomalyList();
}

function renderAnomalyList() {
  const saved = _savedAnomalySnapshots();
  renderLogMetrics();
  const el = $id('logAnomalyList');
  if (!el) return;

  const current = logAnomalies.slice(-8).reverse();
  const currentHtml = current.map(item => {
    const time = (item.last || item.time || '').slice(11, 19);
    const repeat = item.count > 1 ? `<span class="log-anomaly-repeat">×${item.count}</span>` : '';
    return `<div class="log-anomaly-item ${item.level}">
      <div class="log-anomaly-top"><span>${esc(time || 'сейчас')}</span><b>${esc(item.level)}</b>${repeat}</div>
      <div class="log-anomaly-msg">${_highlightLogMsg(item.message)}</div>
    </div>`;
  }).join('');

  const savedHtml = saved.slice(0, 4).map(snap => {
    const d = new Date(snap.time || snap.id || Date.now());
    const label = isNaN(d.getTime()) ? tr('logs.snapshot') : formatDateTime(d, { day:'2-digit', month:'2-digit', hour:'2-digit', minute:'2-digit' });
    return `<div class="log-snapshot-item">
      <span>${esc(label)}</span>
      <b>${Number(snap.count || snap.items?.length || 0)}</b>
    </div>`;
  }).join('');

  if (!currentHtml && !savedHtml) {
    el.innerHTML = '<div class="log-anomaly-empty">Предупреждений пока нет</div>';
    return;
  }
  el.innerHTML =
    (currentHtml ? `<div class="log-anomaly-section">Текущие</div>${currentHtml}` : '') +
    (savedHtml ? `<div class="log-anomaly-section saved">Сохранённые разборы</div>${savedHtml}` : '');
}

function pushLog(ts, level, msg) {
  const wrap = $id('logWrap');
  if (!wrap) return;
  if (wrap.querySelector('.log-empty')) wrap.innerHTML = '';

  const rawLvlChar = (String(level || 'I')[0] || 'I').toUpperCase();
  const lvlChar = ['E', 'W', 'I', 'D'].includes(rawLvlChar) ? rawLvlChar : 'I';
  const timeStr = _formatLogTime(ts);
  const raw = String(msg || '');
  const clean = _cleanLogMsg(raw);

  if (_isRoutineHttpLog(lvlChar, clean)) return;
  logCounts.total++;
  logCounts[lvlChar] = (logCounts[lvlChar] || 0) + 1;
  _setText('logLastEvent', timeStr || 'сейчас');

  // Дедупликация: если ключ совпадает — только обновляем счётчик
  const key = lvlChar + ':' + _normalizeLogKey(clean);
  if (key === _lastLogKey && _lastLogEl) {
    _rememberAnomaly(ts, lvlChar, raw, clean);
    _lastLogCnt++;
    const body = _lastLogEl.querySelector('.log-body') || _lastLogEl;
    let badge = body.querySelector('.log-dup');
    if (!badge) {
      badge = document.createElement('span');
      badge.className = 'log-dup' + (lvlChar === 'W' ? ' warn' : '');
      body.appendChild(badge);
    }
    badge.textContent = '×' + (_lastLogCnt + 1);
    const tsEl = _lastLogEl.querySelector('.log-ts');
    if (tsEl) tsEl.textContent = timeStr;
    _applyLogVisibility(_lastLogEl);
    renderLogMetrics();
    if (logAutoScroll) wrap.scrollTop = 0;
    return;
  }

  _lastLogKey = key;
  _lastLogCnt = 0;

  _rememberAnomaly(ts, lvlChar, raw, clean);
  const line  = document.createElement('div');
  line.className = 'log-line level-' + lvlChar;
  line.dataset.msg = raw;
  line.dataset.search = (raw + ' ' + clean).toLowerCase();
  line.dataset.level = lvlChar;

  // Длинные сообщения (>120 символов): показываем свёрнутыми, разворачиваем по клику
  const msgCls = clean.length > 120 ? 'log-msg clamp' : 'log-msg';
  const clickable = clean.length > 120 ? ' onclick="this.classList.toggle(\'open\')" title="Нажмите чтобы развернуть"' : '';
  line.innerHTML = `<span class="log-ts">${esc(timeStr)}</span><span class="log-lvl ${lvlChar}">${esc(lvlChar)}</span><span class="log-body"><span class="${msgCls}"${clickable}>${_formatLogMsg(clean)}</span></span>`;

  _applyLogVisibility(line);
  logLines.unshift(line);
  if (logLines.length > LOG_MAX_LINES) {
    const removed = logLines.pop();
    removed?.remove();
  }
  wrap.prepend(line);
  _lastLogEl = line;
  renderLogMetrics();
  if (logAutoScroll) wrap.scrollTop = 0;
}

function filterLogs() {
  logLines.forEach(_applyLogVisibility);
  const clearBtn = document.querySelector('.log-filter-clear');
  if (clearBtn) clearBtn.classList.toggle('active', !!($id('logFilter')?.value || '').trim());
  renderLogMetrics();
}

function clearLogFilter() {
  const input = $id('logFilter');
  if (input) input.value = '';
  filterLogs();
}

function setLogLevelFilter(level) {
  const normalized = level || 'warn';
  const input = $id('logLevelFilter');
  if (input) input.value = normalized;
  document.querySelectorAll('.log-level-btn').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.level === normalized);
  });
  filterLogs();
}

async function clearLogs() {
  try { await fetch(API + '/events/clear', {method:'POST'}); } catch(_) {}
  logLines = [];
  logAnomalies = [];
  logCounts = { total: 0, I: 0, W: 0, E: 0, D: 0 };
  lastLogId = 0;
  _lastLogKey = ''; _lastLogEl = null; _lastLogCnt = 0;
  const wrap = $id('logWrap');
  if (wrap) wrap.innerHTML = '<div class="log-empty">лог очищен</div>';
  _setText('logLastEvent', '—');
  renderAnomalyList();
}

function toggleLogAuto() {
  logAutoScroll = !logAutoScroll;
  const btn = $id('logAutoBtn');
  const label = $id('logAutoText');
  if (label) label.textContent = logAutoScroll ? 'Авто' : 'Пауза';
  btn?.classList.toggle('paused', !logAutoScroll);
  renderLogMetrics();
}

function exportLogs() {
  const lines = logLines.map(l => {
    const ts  = l.querySelector('.log-ts')?.textContent || '';
    const lvl = l.querySelector('.log-lvl')?.textContent || '';
    const dup = l.querySelector('.log-dup')?.textContent || '';
    const msg = l.dataset.msg || '';
    return `[${ts}] ${lvl}${dup ? ' ' + dup : ''} ${msg}`;
  }).join('\n');
  const blob = new Blob([lines], { type: 'text/plain' });
  const url  = URL.createObjectURL(blob);
  const a    = document.createElement('a');
  a.href = url; a.download = 'proxy-logs.txt'; a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function saveAnomalySnapshot() {
  if (!logAnomalies.length) {
    showToast('Нет предупреждений для сохранения', 'info');
    renderAnomalyList();
    return;
  }
  const snapshot = {
    id: Date.now(),
    time: new Date().toISOString(),
    count: logAnomalies.length,
    items: logAnomalies.map(item => ({
      time: item.time,
      last: item.last,
      level: item.level,
      message: item.message,
      count: item.count
    }))
  };
  const saved = [snapshot, ..._savedAnomalySnapshots()].slice(0, 12);
  _storeAnomalySnapshots(saved);
  showToast('Снимок предупреждений сохранён', 'on');
  renderAnomalyList();
}

function exportAnomalies() {
  const payload = {
    exported_at: new Date().toISOString(),
    current: logAnomalies.map(item => ({
      time: item.time,
      last: item.last,
      level: item.level,
      message: item.message,
      count: item.count
    })),
    snapshots: _savedAnomalySnapshots()
  };
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' });
  const url  = URL.createObjectURL(blob);
  const a    = document.createElement('a');
  a.href = url; a.download = 'safesky-warnings.json'; a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

setTimeout(renderAnomalyList, 0);

// ═══════════════════════════════════════════════════
