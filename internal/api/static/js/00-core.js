// ═══════════════════════════════════════════════════
// CONFIG
// ═══════════════════════════════════════════════════
const API = 'http://127.0.0.1:8080/api';
const POLL_STATUS = 3000;   // ms
const POLL_STATS  = 1000;   // ms  — реальная скорость
const POLL_CONNS  = 3000;   // ms
const FETCH_TIMEOUT_MS = 10000;
const ICON_SPRITE = 'assets/icons/safesky-icons.svg';

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
const TOAST_ICON_NAMES = { on: 'safe', off: 'bad', warn: 'warn', info: 'diagnostics' };

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
const qaTimer  = $id('qaTimer');
const qaTimerLabel = $id('qaTimerLabel');
const qaTimerTime  = $id('qaTimerTime');
const qaTimerBar   = $id('qaTimerBar');

function isSupportedServerURI(url) {
  return /^\s*(vless|trojan|ss|hysteria2|hy2|tuic|wireguard|vmess):\/\//i.test(url || '');
}

function iconId(name) {
  return String(name || 'fallback-image').replace(/[^a-z0-9-]/gi, '') || 'fallback-image';
}

function iconSvg(name, className) {
  const cls = String(className || 'ssk-icon').replace(/[^a-z0-9_ -]/gi, '').trim() || 'ssk-icon';
  return `<svg class="${cls}" aria-hidden="true" focusable="false"><use href="${ICON_SPRITE}#${iconId(name)}"></use></svg>`;
}

function iconElement(name, className) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', String(className || 'ssk-icon'));
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', `${ICON_SPRITE}#${iconId(name)}`);
  svg.appendChild(use);
  return svg;
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
  let _curLabel  = '';
  let _curKind   = '';

  function _isHomeActionVisible() {
    if (!qaTimer) return false;
    const page = $id('page0');
    const btn = qaTimer.closest('.hero-toggle-action');
    if (!page || !btn) return false;
    if (typeof currentPage === 'number' && currentPage !== 0) return false;
    return page.style.display !== 'none';
  }

  function _canDockHeroTimer(op) {
    return op === 'toggle' || op === 'warming' || op === 'apply' || op === 'connect';
  }

  function _shouldUseHeroTimer(op) {
    return _canDockHeroTimer(op) && _isHomeActionVisible();
  }

  function _fmtTime(ms) {
    if (ms < 0) ms = 0;
    const s = Math.ceil(ms / 1000);   // ceil: "5с" пока не пройдёт полная секунда
    const m = Math.floor(s / 60);
    const ss = s % 60;
    return m > 0 ? m + ':' + String(ss).padStart(2, '0') : s + 'с';
  }

  function _plainLabel(html) {
    const tpl = document.createElement('template');
    tpl.innerHTML = String(html || '');
    return (tpl.content.textContent || '').replace(/\s+/g, ' ').trim();
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

  function _setHeroTimerVisible(visible, kind) {
    if (!qaTimer) return;
    const btn = qaTimer.closest('.hero-toggle-action');
    btn?.classList.toggle('timer-active', visible);
    qaTimer.classList.toggle('vis', visible);
    qaTimer.classList.remove('running', 'success', 'error', 'indeterminate');
    if (visible) qaTimer.classList.add(kind || 'running');
  }

  function _setHeroTimerLabel(label) {
    if (!qaTimerLabel) return;
    qaTimerLabel.textContent = _plainLabel(label) || 'Запуск';
  }

  function _renderHeroTimer(timeText, pct, indeterminate) {
    if (!_shouldUseHeroTimer(_curOp) || !qaTimer) return;
    if (qaTimerTime) qaTimerTime.textContent = timeText;
    if (qaTimerBar) qaTimerBar.style.width = Math.max(0, Math.min(100, pct || 0)) + '%';
    qaTimer.classList.toggle('indeterminate', !!indeterminate);
  }

  function _syncPlacement(kind) {
    if (!_curOp) {
      el.className = 'op-timer';
      _setHeroTimerVisible(false);
      _clearVisible();
      return false;
    }
    const heroTimer = _shouldUseHeroTimer(_curOp);
    if (heroTimer) {
      el.className = 'op-timer';
      _clearVisible();
      _setHeroTimerVisible(true, kind || 'running');
      _setHeroTimerLabel(_curLabel);
    } else {
      _setHeroTimerVisible(false);
      const cls = kind === 'success' ? 'op-timer vis success'
        : kind === 'error' ? 'op-timer vis error'
        : 'op-timer vis';
      el.className = cls;
      _setVisible(kind || 'running');
    }
    return heroTimer;
  }

  function _tick() {
    if (!_curOp) return;
    _syncPlacement(_curKind || 'running');
    const elapsed = Date.now() - _startMs;
    if (_estMs > 0) {
      const remain = Math.max(0, _estMs - elapsed);
      if (remain > 0) {
        // Обратный отсчёт: показываем сколько осталось
        elTime.textContent = _fmtTime(remain);
        const pct = Math.min(100, (elapsed / _estMs) * 100);
        elBar.style.width = pct + '%';
        elBar.classList.remove('indeterminate');
        _renderHeroTimer(_fmtTime(remain), pct, false);
      } else {
        // Оценка истекла но операция ещё идёт — показываем '...' вместо count-up.
        elTime.textContent = '...';
        elBar.classList.add('indeterminate');
        _renderHeroTimer('...', 100, true);
      }
    } else {
      // Неизвестная длительность — показываем '...' вместо count-up
      elTime.textContent = '...';
      elBar.classList.add('indeterminate');
      _renderHeroTimer('...', 0, true);
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
    _curLabel = label || '';
    _curKind = 'running';
    _startMs = Date.now();
    _estMs = estMs || 0;
    _setLabel(label);
    _syncPlacement('running');
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
    if (label) {
      _curLabel = label;
      _setLabel(label);
      if (_shouldUseHeroTimer(op)) _setHeroTimerLabel(label);
    }
    // FIX: только положительное значение обновляет оценку — 0 не затирает рабочий обратный отсчёт
    if (estMs > 0) _estMs = estMs;
    _curKind = 'running';
    _tick();
  }

  /** Завершить таймер успешно. Баннер скрывается через hideDelay мс. */
  function done(op, msg, hideDelay) {
    if (_curOp !== op) return;
    clearInterval(_interval);
    _interval = null;
    _curLabel = msg || 'Готово';
    _curKind = 'success';
    const elapsed = Date.now() - _startMs;
    const heroTimer = _syncPlacement('success');
    if (heroTimer) {
      _setHeroTimerLabel(msg || 'Готово');
      _renderHeroTimer(_fmtTime(elapsed), 100, false);
    }
    _setLabel(msg || 'Готово');
    elTime.textContent = _fmtTime(elapsed);
    elBar.style.width = '100%';
    elBar.classList.remove('indeterminate');
    _hideTimer = setTimeout(() => {
      el.className = 'op-timer';
      _curOp = null;
      _curLabel = '';
      _curKind = '';
      _setHeroTimerVisible(false);
      _clearVisible();
    }, hideDelay || 2500);
  }

  /** Завершить таймер с ошибкой. */
  function fail(op, msg, hideDelay) {
    if (_curOp !== op) return;
    clearInterval(_interval);
    _interval = null;
    _curLabel = msg || 'Ошибка';
    _curKind = 'error';
    const elapsed = Date.now() - _startMs;
    const heroTimer = _syncPlacement('error');
    if (heroTimer) {
      _setHeroTimerLabel(msg || 'Ошибка');
      _renderHeroTimer(_fmtTime(elapsed), 100, false);
    }
    _setLabel(msg || 'Ошибка');
    elTime.textContent = _fmtTime(elapsed);
    elBar.style.width = '100%';
    elBar.classList.remove('indeterminate');
    _hideTimer = setTimeout(() => {
      el.className = 'op-timer';
      _curOp = null;
      _curLabel = '';
      _curKind = '';
      _setHeroTimerVisible(false);
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
    _curLabel = '';
    _curKind = '';
    _startMs = 0;
    _estMs = 0;
    _setHeroTimerVisible(false);
    _clearVisible();
  }

  function refreshPlacement() {
    if (!_curOp) return;
    const heroTimer = _syncPlacement(_curKind || 'running');
    if (_curKind === 'running') {
      _tick();
      return;
    }
    if (heroTimer) _renderHeroTimer(elTime.textContent || '', 100, false);
  }

  function current() { return _curOp; }
  /** Время старта текущего таймера (Date.now() ms). Для расчёта нового estMs при update. */
  function getStartMs() { return _startMs; }

  return { start, update, done, fail, hide, refreshPlacement, current, getStartMs };
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
  toast.replaceChildren();
  const iconName = TOAST_ICON_NAMES[type];
  if (iconName) toast.appendChild(iconElement(iconName, 'toast-icon ssk-icon'));
  toast.appendChild(document.createTextNode(msg));
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
  OpTimer.start('toggle', turnOn ? 'Подключение...' : 'Отключение...', 0);

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
