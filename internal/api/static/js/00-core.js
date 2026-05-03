// ═══════════════════════════════════════════════════
// CONFIG
// ═══════════════════════════════════════════════════
const API = 'http://127.0.0.1:8080/api';
const POLL_STATUS = 3000;   // ms
const POLL_STATS  = 1000;   // ms  — реальная скорость
const POLL_CONNS  = 3000;   // ms
const FETCH_TIMEOUT_MS = 10000;

const _nativeFetch = window.fetch.bind(window);
window.fetch = (resource, options = {}) => {
  const opts = { ...options };
  if (opts.signal) return _nativeFetch(resource, opts);
  const timeoutMs = Number(opts.timeoutMs || FETCH_TIMEOUT_MS);
  delete opts.timeoutMs;
  const ctrl = new AbortController();
  const timer = window.setTimeout(() => ctrl.abort(), timeoutMs);
  return _nativeFetch(resource, { ...opts, signal: ctrl.signal })
    .finally(() => window.clearTimeout(timer));
};

// ═══════════════════════════════════════════════════
// STATE
// ═══════════════════════════════════════════════════
let state = {
  running: false,    // xray.running
  enabled: false,    // proxy.enabled
  warming: false,    // xray.warming
  pending: false,    // запрос к API в процессе
  srvOpen: false,
  servers: [],       // ServerEntry[]
  activeId: null,    // id активного сервера
  pings: {},         // id → ms
  health: {},        // id → health snapshot
};
let toastTimer = null;
const toastQueue = [];
let toastShowing = false;
const TOAST_ICONS = { on: '✓ ', off: '✗ ', warn: '⚠ ', info: 'ℹ ' };

// ═══════════════════════════════════════════════════
// DOM refs
// ═══════════════════════════════════════════════════
const $id = id => document.getElementById(id);
const orbStage = $id('orbStage');
const slbl     = $id('slbl');
const aura     = $id('aura');
const toast    = $id('toast');
const srvPanel = $id('srvPanel');
const srvOverlay = $id('srvOverlay');
const warmDot  = $id('warmDot');

function isSupportedServerURI(url) {
  return /^\s*(vless|trojan|ss|hysteria2|hy2|tuic|wireguard|vmess):\/\//i.test(url || '');
}

// ═══════════════════════════════════════════════════
// OpTimer — универсальный баннер-таймер для долгих операций
// ═══════════════════════════════════════════════════
const OpTimer = (() => {
  const root     = document.documentElement;
  const el       = $id('opTimer');
  const elText   = $id('opTimerText');
  const elTime   = $id('opTimerTime');
  const elBar    = $id('opTimerBar');
  let _interval  = null;
  let _startMs   = 0;
  let _estMs     = 0;      // estimated total duration ms (0 = unknown)
  let _hideTimer = null;
  let _curOp     = null;    // текущая операция (string id)

  function _fmtTime(ms) {
    if (ms < 0) ms = 0;
    const s = Math.ceil(ms / 1000);   // ceil: "5с" пока не пройдёт полная секунда
    const m = Math.floor(s / 60);
    const ss = s % 60;
    return m > 0 ? m + ':' + String(ss).padStart(2, '0') : s + 'с';
  }

  function _setVisible(kind) {
    root.classList.add('op-timer-active');
    root.dataset.opTimer = kind || 'running';
  }

  function _clearVisible() {
    root.classList.remove('op-timer-active');
    delete root.dataset.opTimer;
  }

  function _setLabel(html) {
    const tpl = document.createElement('template');
    tpl.innerHTML = String(html || '');
    const frag = document.createDocumentFragment();
    const appendSafe = (node, parent) => {
      if (node.nodeType === Node.TEXT_NODE) {
        parent.appendChild(document.createTextNode(node.textContent || ''));
        return;
      }
      if (node.nodeType !== Node.ELEMENT_NODE) return;
      if (node.tagName === 'B') {
        const b = document.createElement('b');
        node.childNodes.forEach(child => appendSafe(child, b));
        parent.appendChild(b);
        return;
      }
      parent.appendChild(document.createTextNode(node.textContent || ''));
    };
    tpl.content.childNodes.forEach(node => appendSafe(node, frag));
    elText.replaceChildren(frag);
  }

  function _tick() {
    const elapsed = Date.now() - _startMs;
    if (_estMs > 0) {
      const remain = Math.max(0, _estMs - elapsed);
      if (remain > 0) {
        // Обратный отсчёт: показываем сколько осталось
        elTime.textContent = _fmtTime(remain);
        const pct = Math.min(100, (elapsed / _estMs) * 100);
        elBar.style.width = pct + '%';
        elBar.classList.remove('indeterminate');
      } else {
        // Оценка истекла но операция ещё идёт — показываем '...' вместо count-up.
        elTime.textContent = '...';
        elBar.classList.add('indeterminate');
      }
    } else {
      // Неизвестная длительность — показываем '...' вместо count-up
      elTime.textContent = '...';
      elBar.classList.add('indeterminate');
    }
  }

  /** Запустить таймер.
   * @param {string} op     — id операции (apply, connect, geosite, engine, toggle, warming)
   * @param {string} label  — текст для пользователя, напр. "Применение правил"
   * @param {number} [estMs=0] — ожидаемое время в мс (0 = неизвестно)
   */
  function start(op, label, estMs) {
    if (_hideTimer) { clearTimeout(_hideTimer); _hideTimer = null; }
    _curOp = op;
    _startMs = Date.now();
    _estMs = estMs || 0;
    el.className = 'op-timer vis';
    _setVisible('running');
    _setLabel(label);
    elTime.textContent = _estMs > 0 ? _fmtTime(_estMs) : '...';
    elBar.style.width = '0%';
    elBar.classList.toggle('indeterminate', !_estMs);
    clearInterval(_interval);
    _interval = setInterval(_tick, 250);
    _tick();
  }

  /** Обновить label и/или оценку времени (не сбрасывая таймер).
   *  estMs > 0 обновляет оценку; estMs=0/undefined оставляет текущую оценку.
   */
  function update(op, label, estMs) {
    if (_curOp !== op) return; // другая операция уже показана
    if (label) _setLabel(label);
    // FIX: только положительное значение обновляет оценку — 0 не затирает рабочий обратный отсчёт
    if (estMs > 0) _estMs = estMs;
    _tick();
  }

  /** Завершить таймер успешно. Баннер скрывается через hideDelay мс. */
  function done(op, msg, hideDelay) {
    if (_curOp !== op) return;
    clearInterval(_interval);
    _interval = null;
    const elapsed = Date.now() - _startMs;
    el.className = 'op-timer vis success';
    _setVisible('success');
    _setLabel(msg || 'Готово');
    elTime.textContent = _fmtTime(elapsed);
    elBar.style.width = '100%';
    elBar.classList.remove('indeterminate');
    _hideTimer = setTimeout(() => {
      el.className = 'op-timer';
      _curOp = null;
      _clearVisible();
    }, hideDelay || 2500);
  }

  /** Завершить таймер с ошибкой. */
  function fail(op, msg, hideDelay) {
    if (_curOp !== op) return;
    clearInterval(_interval);
    _interval = null;
    const elapsed = Date.now() - _startMs;
    el.className = 'op-timer vis error';
    _setVisible('error');
    _setLabel(msg || 'Ошибка');
    elTime.textContent = _fmtTime(elapsed);
    elBar.style.width = '100%';
    elBar.classList.remove('indeterminate');
    _hideTimer = setTimeout(() => {
      el.className = 'op-timer';
      _curOp = null;
      _clearVisible();
    }, hideDelay || 4000);
  }

  /** Скрыть таймер немедленно. */
  function hide(op) {
    if (op && _curOp !== op) return;
    clearInterval(_interval); _interval = null;
    clearTimeout(_hideTimer); _hideTimer = null;
    el.className = 'op-timer';
    elBar.classList.remove('indeterminate');
    elBar.style.width = '0%';
    elText.textContent = '';
    elTime.textContent = '';
    _curOp = null;
    _startMs = 0;
    _estMs = 0;
    _clearVisible();
  }

  function current() { return _curOp; }
  /** Время старта текущего таймера (Date.now() ms). Для расчёта нового estMs при update. */
  function getStartMs() { return _startMs; }

  return { start, update, done, fail, hide, current, getStartMs };
})();

// ═══════════════════════════════════════════════════
// TOAST — очередь с иконками (#11)
// ═══════════════════════════════════════════════════
function showToast(msg, type) {
  if (toastQueue.length >= 3) toastQueue.shift();
  toastQueue.push({ msg, type });
  if (!toastShowing) _nextToast();
}
function _nextToast() {
  if (!toastQueue.length) { toastShowing = false; return; }
  toastShowing = true;
  const { msg, type } = toastQueue.shift();
  clearTimeout(toastTimer);
  const icon = TOAST_ICONS[type] || '';
  toast.textContent = icon + msg;
  toast.className = 'toast ' + type + ' show';
  toastTimer = setTimeout(() => {
    toast.className = 'toast';
    setTimeout(_nextToast, 320);
  }, 2400);
}

// ═══════════════════════════════════════════════════
// RENDER — орб, статус, аура
// ═══════════════════════════════════════════════════
function renderState() {
  const { running, enabled, warming, pending } = state;
  const isOn = running && enabled;

  // Орб
  orbStage.classList.remove('off', 'warm', 'loading', 'connecting');
  if (pending) {
    orbStage.classList.add('loading');
  } else if (warming) {
    orbStage.classList.add('warm');
  } else if (!isOn) {
    orbStage.classList.add('off');
  }

  // Статус-label
  slbl.className = 'status-lbl';
  if (pending || warming) {
    slbl.classList.add('warm');
    slbl.textContent = warming ? 'ЗАПУСК...' : 'ОЖИДАНИЕ...';
  } else if (isOn) {
    slbl.textContent = 'ВКЛЮЧЁН';
  } else {
    slbl.classList.add('off');
    slbl.textContent = 'ОТКЛЮЧЁН';
  }

  // Аура фона
  aura.className = 'bg-aura ' + (warming ? 'warm' : isOn ? 'on' : 'off');

  // Warm-dot в хедере
  warmDot.classList.toggle('vis', warming);
  const qaToggleLabel = $id('qaToggleLabel');
  const qaState = $id('qaState');
  if (qaToggleLabel) {
    qaToggleLabel.textContent = pending || warming
      ? 'Подключение...'
      : isOn ? 'Отключить' : 'Подключить';
  }
  if (qaState) {
    qaState.textContent = pending || warming
      ? 'ожидание сети'
      : isOn ? 'маршрут активен' : 'защита выключена';
  }
  // B3: кнопка переподключения
  const rb = $id('reconnectBtn');
  if (rb) rb.style.display = !isOn && !pending ? '' : 'none';
}

// ═══════════════════════════════════════════════════
// TOGGLE (кнопка-орб)
// ═══════════════════════════════════════════════════
async function toggle() {
  if (state.pending || state.warming) return;

  const turnOn = !state.enabled;
  state.pending = true;
  renderState();
  OpTimer.start('toggle', turnOn ? 'Подключение...' : 'Отключение...', 3000);

  try {
    const ep = turnOn ? '/proxy/enable' : '/proxy/disable';
    const r = await fetch(API + ep, { method: 'POST' });
    if (!r.ok) {
      const errText = await r.text();
      const alreadyDesired =
        (turnOn && /уже\s+включ/i.test(errText)) ||
        (!turnOn && /уже\s+отключ/i.test(errText));
      if (!alreadyDesired) throw new Error(errText);
    }
    state.enabled = turnOn;
    showToast(turnOn ? '— ПОДКЛЮЧЕНО' : '— ОТКЛЮЧЕНО', turnOn ? 'on' : 'off');
    OpTimer.done('toggle', turnOn ? 'Подключено' : 'Отключено');
    renderState();
    await pollStatus();
  } catch (e) {
    showToast('Ошибка: ' + e.message, 'off');
    OpTimer.fail('toggle', 'Ошибка: ' + e.message);
  } finally {
    state.pending = false;
    renderState();
  }
}

// ═══════════════════════════════════════════════════
