// RULES PAGE  →  /api/tun/rules
// ═══════════════════════════════════════════════════
let _dragRuleId = null;
let _allRules   = []; // кэш для drag-and-drop reorder
let _routingConfig = { default_action: 'proxy', rules: [] };
let _rulesJsonDirty = false;
let _visualRouting = { default_action: 'proxy', rules: [], presets: [] };
let _visualDragId = null;
let _activeProfileName = localStorage.getItem('safesky-active-profile') || '';

function _ruleTypeTag(t) {
  if (t === 'geosite') return `<span class="rule-type-tag geo">geo</span>`;
  if (t === 'process') return `<span class="rule-type-tag proc">proc</span>`;
  if (t === 'ip')      return `<span class="rule-type-tag ip">ip</span>`;
  return `<span class="rule-type-tag">domain</span>`;
}
function _ruleTypeIco(t) {
  if (t === 'geosite') return iconSvg('geosite', 'rule-inline-icon ssk-icon');
  if (t === 'process') return iconSvg('process', 'rule-inline-icon ssk-icon');
  if (t === 'ip')      return iconSvg('ip', 'rule-inline-icon ssk-icon');
  return iconSvg('link', 'rule-inline-icon ssk-icon');
}

function setRuleAction(targetId, action) {
  const target = $id(targetId);
  action = (action || 'proxy').toLowerCase();
  if (!['proxy', 'direct', 'block'].includes(action)) action = 'proxy';
  if (target) target.value = action;
  document.querySelectorAll(`.rule-action-picker[data-target="${targetId}"] .rule-action-btn`).forEach(btn => {
    btn.classList.toggle('active', btn.dataset.action === action);
  });
}

async function loadRules() {
  const el = $id('rulesList');
  try {
    const r = await fetch(API + '/tun/rules');
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const data = await r.json();
    _allRules = data.rules || [];
    if ($id('rulesHeroCount')) $id('rulesHeroCount').textContent = _allRules.length;
    if ($id('rulesListCount')) $id('rulesListCount').textContent = `(${_allRules.length})`;
    _routingConfig = {
      default_action: data.default_action || 'proxy',
      rules: _allRules,
      bypass_enabled: !!data.bypass_enabled,
      dns: data.dns || undefined,
      block_quic: data.block_quic !== false,
      block_telemetry: !!data.block_telemetry,
      lan_share_enabled: !!data.lan_share_enabled,
      lan_share_port: data.lan_share_port || 10808
    };
    renderDefaultAction(_routingConfig.default_action);
    syncRulesJson(false);
    if (!_allRules.length) {
      el.innerHTML = '<div class="rules-empty">Правил нет.<br>Добавьте первое правило выше.</div>';
      if ($id('rulesCnt')) $id('rulesCnt').textContent = '0';
      updateActiveProfileMeta();
      return;
    }
    el.innerHTML = _allRules.map((rule, idx) => {
      const action = (rule.action || 'proxy').toLowerCase();
      const cls  = action === 'direct' ? 'd' : action === 'block' ? 'b' : 'p';
      const lbl  = action === 'direct' ? 'DIRECT' : action === 'block' ? 'BLOCK' : 'PROXY';
      const delay = idx < 15 ? ` style="animation-delay:${idx * 0.03}s"` : '';
      const val  = rule.value || '';
      const typ  = rule.type || 'domain';
      const valArg = jsArg(val);
      return `<div class="rule-item" draggable="true"
        data-id="${esc(val)}" data-idx="${idx}"
        data-pattern="${esc(val + ' ' + typ + ' ' + action + ' ' + (rule.note || ''))}"
        data-action="${esc(action)}" data-type="${esc(typ)}"${delay}
        ondragstart="ruleDragStart(event,${valArg})"
        ondragover="ruleDragOver(event)"
        ondragleave="ruleDragLeave(event)"
        ondrop="ruleDrop(event,${valArg},${idx})">
        <span class="rule-drag" title="Перетащить">::</span>
        <div class="rule-main">
          <div class="rule-line">
            <span class="rule-badge ${cls}">${lbl}</span>
            ${_ruleTypeTag(typ)}
            <div class="rule-nm" title="${esc(val)}">${_ruleTypeIco(typ)} ${esc(val)}</div>
          </div>
          ${rule.note ? `<div class="rule-proc">${esc(rule.note)}</div>` : ''}
        </div>
        <div class="rule-actions">
          <button class="pg-btn danger" style="padding:3px 8px;font-size:8px"
            onclick="deleteRule(${valArg})">✕</button>
        </div>
      </div>`;
    }).join('');
    if ($id('rulesCnt')) { $id('rulesCnt').textContent = _allRules.length; $id('rulesCnt').style.display = ''; }
    updateActiveProfileMeta();
  } catch(e) {
    if ($id('rulesHeroCount')) $id('rulesHeroCount').textContent = '—';
    el.innerHTML = `<div class="rules-empty">Ошибка загрузки правил<br><span style="font-size:8px;opacity:0.6">${esc(e.message)}</span></div>`;
  }
}

function renderDefaultAction(action) {
  action = (action || 'proxy').toLowerCase();
  $id('defaultProxyBtn')?.classList.toggle('active', action === 'proxy');
  $id('defaultDirectBtn')?.classList.toggle('active', action === 'direct');
  $id('defaultBlockBtn')?.classList.toggle('active', action === 'block');
  if ($id('rulesHeroDefault')) {
    $id('rulesHeroDefault').textContent = action === 'direct' ? 'DIRECT' : action === 'block' ? 'BLOCK' : 'PROXY';
  }
  const hint = $id('defaultActionHint');
  if (hint) {
    hint.textContent = action === 'direct'
      ? 'Все соединения идут напрямую, кроме правил «Прокси» и «Блок».'
      : action === 'block'
        ? 'Все соединения блокируются, кроме правил «Прокси» и «Напрямую».'
        : 'Все соединения идут через прокси, кроме правил «Напрямую» и «Блок».';
  }
}

async function quickAddRuleValue(value, action) {
  value = String(value || '').trim();
  action = (action || 'proxy').toLowerCase();
  if (!value) return;
  try {
    const r = await fetch(API + '/tun/rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value, action })
    });
    if (!r.ok) throw new Error(await r.text());
    showToast('Правило добавлено', 'on');
    OpTimer.start('apply', 'Применение правила <b>' + value + '</b>', _applyHistory.estimate(5000));
    _watchApply('Применение правила <b>' + value + '</b>');
    loadRules();
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'off');
  }
}

function quickAddSiteRule(action) {
  const value = prompt(action === 'direct' ? 'Какой сайт открыть напрямую?' : 'Какой сайт открыть через VPN?', 'youtube.com');
  if (!value) return;
  quickAddRuleValue(value.replace(/^https?:\/\//i, '').replace(/\/.*$/, ''), action);
}

function quickAddProcessRule(action) {
  const value = prompt('Имя программы', 'chrome.exe');
  if (!value) return;
  quickAddRuleValue(value, action || 'proxy');
}

async function setDefaultAction(action) {
  action = (action || '').toLowerCase();
  if (action !== 'proxy' && action !== 'direct' && action !== 'block') return;
  try {
    const r = await fetch(API + '/tun/default', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({action})
    });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json().catch(() => ({}));
    _routingConfig.default_action = action;
    renderDefaultAction(action);
    syncRulesJson(false);
    if (d.apply_error) {
      showToast('Базовое правило сохранено, но не применено: ' + d.apply_error, 'warn');
    } else {
      const msg = action === 'proxy' ? 'База: проксировать всё' : action === 'direct' ? 'База: всё напрямую' : 'База: блокировать всё';
      showToast(msg, 'on');
      OpTimer.start('apply', 'Применение базового правила', _applyHistory.estimate(5000));
      _watchApply('Применение базового правила');
    }
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'off');
  }
}

function routingJsonPayload() {
  const payload = {
    default_action: _routingConfig.default_action || 'proxy',
    rules: _allRules || []
  };
  if (_routingConfig.bypass_enabled) payload.bypass_enabled = true;
  if (_routingConfig.dns) payload.dns = _routingConfig.dns;
  payload.block_quic = _routingConfig.block_quic !== false;
  payload.block_telemetry = !!_routingConfig.block_telemetry;
  payload.lan_share_enabled = !!_routingConfig.lan_share_enabled;
  payload.lan_share_port = Number(_routingConfig.lan_share_port || 10808);
  return payload;
}

function syncRulesJson(force) {
  const el = $id('rulesJson');
  if (!el || (_rulesJsonDirty && !force)) return;
  el.value = JSON.stringify(routingJsonPayload(), null, 2);
  _rulesJsonDirty = false;
}

function openRulesJsonEditor() {
  const modal = $id('rulesJsonModal');
  if (!modal) return;
  modal.style.display = 'flex';
  syncRulesJson(false);
  loadRulesJson();
  setTimeout(() => $id('rulesJson')?.focus(), 50);
}

function closeRulesJsonEditor() {
  const modal = $id('rulesJsonModal');
  if (modal) modal.style.display = 'none';
}

async function loadRulesJson() {
  try {
    const r = await fetch(API + '/tun/rules');
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const data = await r.json();
    _allRules = data.rules || [];
    _routingConfig = {
      default_action: data.default_action || 'proxy',
      rules: _allRules,
      bypass_enabled: !!data.bypass_enabled,
      dns: data.dns || undefined,
      block_quic: data.block_quic !== false,
      block_telemetry: !!data.block_telemetry,
      lan_share_enabled: !!data.lan_share_enabled,
      lan_share_port: data.lan_share_port || 10808
    };
    renderDefaultAction(_routingConfig.default_action);
    syncRulesJson(true);
    showToast('JSON загружен', 'info');
  } catch(e) {
    showToast('Ошибка загрузки JSON: ' + e.message, 'off');
  }
}

function formatRulesJson() {
  const el = $id('rulesJson');
  if (!el) return;
  try {
    el.value = JSON.stringify(JSON.parse(el.value || '{}'), null, 2);
    _rulesJsonDirty = true;
  } catch(e) {
    showToast('JSON невалиден: ' + e.message, 'warn');
  }
}

async function saveRulesJson() {
  const el = $id('rulesJson');
  if (!el) return;
  let payload;
  try {
    payload = JSON.parse(el.value || '{}');
  } catch(e) {
    showToast('JSON невалиден: ' + e.message, 'warn');
    return;
  }
  if (!payload.default_action) payload.default_action = 'proxy';
  if (!Array.isArray(payload.rules)) {
    showToast('Поле rules должно быть массивом', 'warn');
    return;
  }
  try {
    const r = await fetch(API + '/tun/import', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    });
    if (!r.ok) throw new Error(await r.text());
    showToast('JSON правил сохранён', 'on');
    _rulesJsonDirty = false;
    OpTimer.start('apply', 'Применение JSON правил', _applyHistory.estimate(5000));
    _watchApply('Применение JSON правил');
    loadRules();
  } catch(e) {
    showToast('Ошибка сохранения JSON: ' + e.message, 'off');
  }
}

async function importRulesFile(input) {
  const file = input.files && input.files[0];
  if (!file) return;
  try {
    const content = await file.text();
    const name = (file.name || '').toLowerCase();
    const format = name.endsWith('.base64') ? 'gfwlist' : (name.endsWith('.yaml') || name.endsWith('.yml') ? 'clash' : 'text');
    const r = await fetch(API + '/tun/rules/import', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({format, content, action: 'proxy'})
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || await r.text());
    showToast(`Импортировано правил: ${d.imported || 0}`, 'on');
    OpTimer.start('apply', 'Применение импортированных правил', _applyHistory.estimate(5000));
    _watchApply('Импорт правил');
    loadRules();
  } catch(e) {
    showToast('Ошибка импорта правил: ' + e.message, 'off');
  } finally {
    input.value = '';
  }
}

function ruleDragStart(e, id) {
  _dragRuleId = id;
  e.currentTarget.style.opacity = '0.5';
  e.dataTransfer.effectAllowed = 'move';
}
function ruleDragOver(e) {
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  e.currentTarget.classList.add('drag-over');
}
function ruleDragLeave(e) {
  e.currentTarget.classList.remove('drag-over');
}
async function ruleDrop(e, targetVal, targetIdx) {
  e.preventDefault();
  e.currentTarget.classList.remove('drag-over');
  document.querySelectorAll('.rule-item').forEach(el => { el.style.opacity = ''; });
  if (!_dragRuleId || _dragRuleId === targetVal) { _dragRuleId = null; return; }
  // Reorder: move dragged rule to targetIdx position, then PUT the whole list
  const from = _allRules.findIndex(r => r.value === _dragRuleId);
  const to   = targetIdx;
  if (from < 0) { _dragRuleId = null; return; }
  const reordered = [..._allRules];
  const [moved] = reordered.splice(from, 1);
  reordered.splice(to, 0, moved);
  try {
    // FIX: берём текущий default_action из загруженных правил, не хардкодим 'proxy'
    const currentDefaultAction = (await fetch(API + '/tun/rules').then(r => r.json()).catch(() => ({}))).default_action || 'proxy';
    const r = await fetch(API + '/tun/rules', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ default_action: currentDefaultAction, rules: reordered })
    });
    if (!r.ok) { showToast('Ошибка изменения порядка', 'off'); }
    else {
      // FIX: проверяем apply_error
      const d = await r.json().catch(() => ({}));
      if (d.apply_error) {
        showToast('Порядок обновлён, но не применён: ' + d.apply_error, 'warn');
      } else {
        showToast('Порядок обновлён', 'on');
        OpTimer.start('apply', 'Применение нового порядка правил', _applyHistory.estimate(5000));
        _watchApply('Применение нового порядка правил');
      }
      loadRules();
    }
  } catch(_) { showToast('Ошибка изменения порядка', 'off'); }
  _dragRuleId = null;
}

const _knownGeosites = ['youtube','instagram','google','github','twitter','facebook','netflix','twitch','amazon','microsoft','apple','cn','geolocation-!cn','category-ads-all','telegram','pinterest'];
const _geositeAliases = [
  ['youtube', ['youtube.com','youtu.be','ytimg.com','googlevideo.com','youtube']],
  ['instagram', ['instagram.com','cdninstagram.com','threads.net','instagram']],
  ['google', ['google.com','googleapis.com','gstatic.com','google']],
  ['github', ['github.com','githubusercontent.com','githubassets.com','github']],
  ['twitter', ['twitter.com','x.com','twimg.com','twitter']],
  ['facebook', ['facebook.com','fbcdn.net','messenger.com','facebook']],
  ['netflix', ['netflix.com','nflxvideo.net','netflix']],
  ['twitch', ['twitch.tv','ttvnw.net','twitch']],
  ['amazon', ['amazon.com','amazonaws.com','amazon']],
  ['microsoft', ['microsoft.com','live.com','office.com','windows.net','microsoft']],
  ['apple', ['apple.com','icloud.com','mzstatic.com','apple']],
  ['telegram', ['telegram.org','t.me','telegram.me','telegram']],
  ['pinterest', ['pinterest.com','pinimg.com','pinterest']]
];

function normalizeRuleCandidate(raw) {
  let v = (raw || '').trim().toLowerCase();
  if (!v) return '';
  if (v.startsWith('geosite:')) return v;
  v = v.replace(/^https?:\/\//, '').replace(/^\/\//, '');
  v = v.split(/[/?#]/)[0];
  v = v.replace(/:\d+$/, '');
  return v;
}

function geositeCandidateForRule(raw) {
  const v = normalizeRuleCandidate(raw);
  if (!v || v.startsWith('geosite:') || v.endsWith('.exe')) return '';
  for (const [name, aliases] of _geositeAliases) {
    if (aliases.some(a => v === a || v.endsWith('.' + a) || v.includes(a))) return name;
  }
  return '';
}

async function ensureGeositeInstalled(name) {
  try {
    const r = await fetch(API + '/geosite');
    if (r.ok) {
      const data = await r.json();
      const item = (data?.items || []).find(g => g.name === name);
      if (item && item.available) return true;
    }
  } catch(_) {}
  if (!confirm(`Для правила доступна база geosite:${name}. Скачать и активировать её?`)) return false;
  OpTimer.start('geosite', `Загрузка geosite:${name}`, 15000);
  const r = await fetch(API + '/geosite/download', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name, apply: false})
  });
  if (!r.ok) {
    OpTimer.fail('geosite', `geosite:${name} не загружен`);
    showToast(`Не удалось скачать geosite:${name}`, 'warn');
    return false;
  }
  OpTimer.done('geosite', `geosite:${name} загружен`);
  showToast(`geosite:${name} загружен`, 'on');
  return true;
}

function onRuleInputChange(val) {
  const box = $id('geoSuggestBox');
  if (!box) return;
  const v = val.trim().toLowerCase();
  if (!v) { box.style.display = 'none'; return; }
  const normalized = normalizeRuleCandidate(v);
  const lookup = normalized.replace(/^geosite:/, '');
  const directCandidate = geositeCandidateForRule(v);
  const matches = _knownGeosites
    .filter(g => (g.includes(lookup) && lookup.length >= 2) || g === directCandidate);
  if (!matches.length) { box.style.display = 'none'; return; }
  box.style.display = 'block';
  box.innerHTML = [...new Set(matches)].slice(0, 6).map(g => {
    const reason = g === directCandidate && !normalized.startsWith('geosite:')
      ? `подходит для ${esc(normalized)}`
      : 'готовая geosite база';
    const geositeArg = jsArg(g);
    return `<div class="geo-suggest-item"
      onmousedown="event.preventDefault()"
      onclick="applyGeositeSuggestion(${geositeArg})">
      <span class="geo-suggest-main">
        <span class="geo-suggest-title">geosite:<b>${esc(g)}</b></span>
        <span class="geo-suggest-sub">${reason}</span>
      </span>
      <span class="geo-suggest-kind">geo</span>
    </div>`;
  }).join('');
}

async function applyGeositeSuggestion(name) {
  const inp = $id('ruleApp');
  if (inp) inp.value = 'geosite:' + name;
  const box = $id('geoSuggestBox');
  if (box) box.style.display = 'none';
}

// _watchApply — ждёт завершения apply-горутины и показывает результат.
// Вызывается после addRule/deleteRule/ruleDrop/saveAddRuleModal/applyProfile
// когда apply_error пустой (apply запустился).
// Опрашивает /api/tun/apply/status: сначала через 4с, потом раз в 5с до 60с или завершения.
function _watchApply(opLabel) {
  const label = opLabel || 'Применение правил';
  // OpTimer уже запущен вызывающей функцией — здесь только polling
  const _watchStartMs = OpTimer.getStartMs() || Date.now(); // для записи в историю
  let attempts = 0;
  let wasRunning = false;
  const maxAttempts = 60; // ~3 минуты (wintun cleanup может занять до 120с)
  const check = async () => {
    try {
      const r = await fetch(API + '/tun/apply/status');
      if (!r.ok) return;
      const d = await r.json();
      // FIX: считаем "в процессе" и running, и pending_apply (ожидает повторного apply)
      if (d.running || d.pending_apply) {
        wasRunning = true;
        // Обновляем OpTimer с данными от сервера
        const mode = d.reload_mode === 'restart' ? 'перезапуск' : '';
        const detail = mode ? ` <b>(${mode})</b>` : '';
        const pending = d.pending_apply ? ' + в очереди' : '';
        // FIX: передаём estMs только если сервер вернул положительную оценку — иначе не затираем текущий countdown
        const serverEst = d.estimated_remain_ms > 0 ? d.elapsed_ms + d.estimated_remain_ms : undefined;
        OpTimer.update('apply', label + detail + pending, serverEst);
        if (++attempts < maxAttempts) setTimeout(check, 3000);
        else OpTimer.fail('apply', 'Таймаут применения правил');
        return;
      }
      if (d.last_err) {
        showToast('Ошибка применения: ' + d.last_err, 'off');
        OpTimer.fail('apply', 'Ошибка: ' + d.last_err);
      } else if (wasRunning) {
        const mode = 'Перезапуск';
        // Записываем реальное время в историю для следующих предсказаний
        _applyHistory.push(Date.now() - _watchStartMs);
        showToast('Правила применены ✓', 'on');
        OpTimer.done('apply', mode + ' завершён — правила применены');
      } else {
        OpTimer.done('apply', 'Правила применены');
      }
      // Обновляем статус после завершения apply
      pollStatus();
    } catch(_) {}
  };
  setTimeout(check, 2000);
}

async function addRule() {
  let val  = $id('ruleApp').value.trim();
  const mode = $id('ruleMode').value;
  if (!val) { showToast('Введите значение правила', 'warn'); return; }
  const normalized = normalizeRuleCandidate(val);
  if (normalized.startsWith('geosite:')) {
    const name = normalized.replace(/^geosite:/, '');
    if (name) {
      const ok = await ensureGeositeInstalled(name);
      val = 'geosite:' + name;
      if (!ok) showToast(`Добавляю правило без скачанной базы geosite:${name}`, 'warn');
    }
  } else {
    const suggested = geositeCandidateForRule(val);
    if (suggested && confirm(`Для ${normalized} есть geosite:${suggested}. Скачать базу и добавить geosite-правило вместо домена?`)) {
      if (await ensureGeositeInstalled(suggested)) {
        val = 'geosite:' + suggested;
      } else {
        showToast('Оставляю доменное правило без geosite', 'info');
      }
    }
  }
  try {
    const r = await fetch(API + '/tun/rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value: val, action: mode })
    });
    if (!r.ok) {
      const errText = await r.text().catch(() => '');
      throw new Error(errText || 'HTTP ' + r.status);
    }
    $id('ruleApp').value = '';
    const d = await r.json().catch(() => ({}));
    if (d.apply_error) {
      showToast('Правило сохранено, но не применено: ' + d.apply_error, 'warn');
    } else {
      showToast('Правило добавлено', 'on');
      OpTimer.start('apply', 'Применение правила <b>' + val + '</b>', _applyHistory.estimate(5000));
      _watchApply('Применение правила <b>' + val + '</b>');
    }
    loadRules();
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
}

async function deleteRule(val) {
  try {
    const r = await fetch(API + '/tun/rules/' + encodeURIComponent(val), { method: 'DELETE' });
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const d = await r.json().catch(() => ({}));
    if (d.apply_error) {
      showToast('Правило удалено, но не применено: ' + d.apply_error, 'warn');
    } else {
      showToast('Правило удалено', 'info');
      OpTimer.start('apply', 'Удаление правила <b>' + val + '</b>', _applyHistory.estimate(5000));
      _watchApply('Удаление правила <b>' + val + '</b>');
    }
    loadRules();
  } catch(e) { showToast('Ошибка удаления: ' + e.message, 'off'); }
}

function filterRules() {
  const f = ($id('rulesFilter')?.value || '').toLowerCase();
  document.querySelectorAll('.rule-item').forEach(el => {
    const pat = (el.dataset.pattern || '').toLowerCase();
    el.style.display = (!f || pat.includes(f)) ? '' : 'none';
  });
}

// ═══════════════════════════════════════════════════
// VISUAL ROUTING EDITOR  →  /api/routing/visual
// ═══════════════════════════════════════════════════
function _visualActionClass(action) {
  action = (action || 'proxy').toLowerCase();
  return action === 'direct' ? 'd' : action === 'block' ? 'b' : 'p';
}

function _visualActionLabel(action) {
  action = (action || 'proxy').toLowerCase();
  return action === 'direct' ? 'DIRECT' : action === 'block' ? 'BLOCK' : 'PROXY';
}

function _visualValuesText(rule) {
  const vals = rule?.match?.values || [];
  if (!vals.length) return '—';
  const first = vals.slice(0, 3).join(', ');
  return vals.length > 3 ? `${first}, +${vals.length - 3}` : first;
}

async function loadVisualRouting() {
  const list = $id('visualRuleList');
  if (!list) return;
  try {
    const [rulesResp, presetsResp] = await Promise.all([
      fetch(API + '/routing/visual'),
      fetch(API + '/routing/visual/presets')
    ]);
    if (!rulesResp.ok) throw new Error('HTTP ' + rulesResp.status);
    const data = await rulesResp.json();
    let presets = [];
    if (presetsResp.ok) {
      presets = (await presetsResp.json()).presets || [];
    }
    _visualRouting = {
      default_action: data.default_action || 'proxy',
      rules: data.rules || [],
      presets
    };
    renderVisualRouting(data.conflicts || []);
  } catch(e) {
    list.innerHTML = `<div class="rules-empty">Ошибка загрузки визуальных правил<br><span style="font-size:8px;opacity:0.6">${esc(e.message)}</span></div>`;
  }
}

function renderVisualRouting(conflicts) {
  renderVisualPresets();
  renderVisualConflicts(conflicts || []);
  const list = $id('visualRuleList');
  if (!list) return;
  const rules = [...(_visualRouting.rules || [])].sort((a, b) => (a.priority || 0) - (b.priority || 0));
  if (!rules.length) {
    list.innerHTML = '<div class="rules-empty">Визуальных правил нет</div>';
    return;
  }
  list.innerHTML = rules.map((rule, idx) => {
    const cls = _visualActionClass(rule.action);
    const idArg = jsArg(rule.id || '');
    const typ = rule.match?.type || 'domain_suffix';
    const enabled = rule.enabled !== false;
    const disabled = enabled ? '' : ' off';
    return `<div class="visual-rule-item${disabled}" draggable="true"
      data-id="${esc(rule.id || '')}"
      ondragstart="visualRuleDragStart(event,${idArg})"
      ondragover="visualRuleDragOver(event)"
      ondragleave="visualRuleDragLeave(event)"
      ondrop="visualRuleDrop(event,${idx})">
      <button class="visual-handle" title="Перетащить">::</button>
      <button class="visual-enabled ${enabled ? 'on' : ''}" onclick="toggleVisualRule(${idArg})" title="Включить"></button>
      <div class="visual-rule-main" onclick="editVisualRule(${idArg})">
        <div class="rule-line">
          <span class="rule-badge ${cls}">${_visualActionLabel(rule.action)}</span>
          <span class="rule-type-tag">${esc(typ)}</span>
          <div class="rule-nm">${esc(rule.name || rule.id || 'Rule')}</div>
        </div>
        <div class="rule-proc">${esc(typ)}: ${esc(_visualValuesText(rule))}${rule.match?.inverse ? ' · NOT' : ''}${rule.server ? ' · ' + esc(rule.server) : ''}</div>
      </div>
      <div class="rule-actions">
        <button class="pg-btn" style="padding:3px 8px;font-size:8px" onclick="moveVisualRule(${idx},-1)">↑</button>
        <button class="pg-btn" style="padding:3px 8px;font-size:8px" onclick="moveVisualRule(${idx},1)">↓</button>
        <button class="pg-btn danger" style="padding:3px 8px;font-size:8px" onclick="deleteVisualRule(${idArg})">✕</button>
      </div>
    </div>`;
  }).join('');
}

function renderVisualPresets() {
  const row = $id('visualPresetRow');
  if (!row) return;
  const presets = _visualRouting.presets || [];
  row.innerHTML = presets.map(p => {
    const idArg = jsArg(p.id);
    return `<button class="pg-btn" title="${esc(p.description || '')}" onclick="importVisualPreset(${idArg})">${esc(p.name || p.id)}</button>`;
  }).join('');
}

function openPresetLoader() {
  const row = $id('visualPresetRow');
  if (!row) return;
  row.classList.toggle('open');
  if (row.classList.contains('open')) row.scrollIntoView({ block: 'nearest' });
}

function renderVisualConflicts(conflicts) {
  const el = $id('visualConflicts');
  if (!el) return;
  if (!conflicts.length) {
    el.style.display = 'none';
    el.innerHTML = '';
    return;
  }
  el.style.display = '';
  el.innerHTML = conflicts.slice(0, 3).map(c =>
    `Конфликт: ${esc(c.rule_a)} выше ${esc(c.rule_b)} для ${esc(c.value)} (${esc(c.action_a)} / ${esc(c.action_b)})`
  ).join('<br>');
}

function openVisualRuleEditor(seed) {
  const editor = $id('visualRuleEditor');
  if (!editor) return;
  const rule = seed || {
    id: '',
    name: '',
    enabled: true,
    match: { type: 'domain_suffix', values: [] },
    action: 'proxy'
  };
  $id('vrEditId').value = rule.id || '';
  $id('vrName').value = rule.name || '';
  $id('vrType').value = rule.match?.type || 'domain_suffix';
  $id('vrValues').value = (rule.match?.values || []).join('\n');
  $id('vrEnabled').checked = rule.enabled !== false;
  $id('vrInverse').checked = !!rule.match?.inverse;
  $id('vrServer').value = rule.server || '';
  $id('vrTestValue').value = '';
  $id('vrTestResult').textContent = '';
  setRuleAction('vrAction', rule.action || 'proxy');
  editor.style.display = 'block';
  setTimeout(() => $id('vrName')?.focus(), 40);
}

function closeVisualRuleEditor() {
  const editor = $id('visualRuleEditor');
  if (editor) editor.style.display = 'none';
}

function editVisualRule(id) {
  const rule = (_visualRouting.rules || []).find(r => r.id === id);
  if (rule) openVisualRuleEditor(JSON.parse(JSON.stringify(rule)));
}

function buildVisualRuleFromForm() {
  const currentId = $id('vrEditId')?.value || '';
  const name = ($id('vrName')?.value || '').trim();
  const values = ($id('vrValues')?.value || '').split(/\r?\n|,/).map(v => v.trim()).filter(Boolean);
  const id = currentId || 'rule-' + Date.now().toString(36);
  const existing = (_visualRouting.rules || []).find(r => r.id === currentId);
  return {
    id,
    name: name || values[0] || 'Rule',
    enabled: $id('vrEnabled')?.checked !== false,
    priority: existing?.priority || ((_visualRouting.rules || []).length + 1) * 10,
    match: {
      type: $id('vrType')?.value || 'domain_suffix',
      values,
      inverse: !!$id('vrInverse')?.checked
    },
    action: $id('vrAction')?.value || 'proxy',
    server: ($id('vrServer')?.value || '').trim()
  };
}

async function saveVisualRule() {
  const rule = buildVisualRuleFromForm();
  if (!rule.match.values.length) {
    showToast('Добавьте хотя бы одно значение', 'warn');
    return;
  }
  const rules = [...(_visualRouting.rules || [])];
  const idx = rules.findIndex(r => r.id === rule.id);
  if (idx >= 0) rules[idx] = rule; else rules.push(rule);
  _visualRouting.rules = normalizeVisualPriorities(rules);
  await persistVisualRouting('Правило сохранено');
  closeVisualRuleEditor();
}

function normalizeVisualPriorities(rules) {
  return [...rules].sort((a, b) => (a.priority || 0) - (b.priority || 0)).map((r, idx) => ({ ...r, priority: (idx + 1) * 10 }));
}

async function persistVisualRouting(okMsg) {
  try {
    const r = await fetch(API + '/routing/visual', {
      method: 'PUT',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        default_action: _visualRouting.default_action || _routingConfig.default_action || 'proxy',
        rules: _visualRouting.rules || []
      })
    });
    const text = await r.text();
    const data = text ? JSON.parse(text) : {};
    if (!r.ok) throw new Error(data.error || text || 'HTTP ' + r.status);
    _visualRouting.rules = data.rules || _visualRouting.rules || [];
    _visualRouting.default_action = data.default_action || _visualRouting.default_action || 'proxy';
    showToast(okMsg || 'Визуальные правила сохранены', 'on');
    OpTimer.start('apply', 'Применение визуальных правил', _applyHistory.estimate(5000));
    _watchApply('Применение визуальных правил');
    await loadRules();
    await loadVisualRouting();
  } catch(e) {
    showToast('Визуальные правила не сохранены: ' + e.message, 'off');
  }
}

function deleteVisualRule(id) {
  _visualRouting.rules = normalizeVisualPriorities((_visualRouting.rules || []).filter(r => r.id !== id));
  persistVisualRouting('Правило удалено');
}

function toggleVisualRule(id) {
  _visualRouting.rules = (_visualRouting.rules || []).map(r => r.id === id ? { ...r, enabled: r.enabled === false } : r);
  persistVisualRouting('Правило обновлено');
}

function moveVisualRule(idx, delta) {
  const rules = normalizeVisualPriorities(_visualRouting.rules || []);
  const next = idx + delta;
  if (next < 0 || next >= rules.length) return;
  [rules[idx], rules[next]] = [rules[next], rules[idx]];
  _visualRouting.rules = normalizeVisualPriorities(rules);
  persistVisualRouting('Порядок обновлён');
}

function visualRuleDragStart(e, id) {
  _visualDragId = id;
  e.currentTarget.style.opacity = '0.5';
  e.dataTransfer.effectAllowed = 'move';
}
function visualRuleDragOver(e) {
  e.preventDefault();
  e.currentTarget.classList.add('drag-over');
}
function visualRuleDragLeave(e) {
  e.currentTarget.classList.remove('drag-over');
}
function visualRuleDrop(e, targetIdx) {
  e.preventDefault();
  document.querySelectorAll('.visual-rule-item').forEach(el => { el.style.opacity = ''; el.classList.remove('drag-over'); });
  if (!_visualDragId) return;
  const rules = normalizeVisualPriorities(_visualRouting.rules || []);
  const from = rules.findIndex(r => r.id === _visualDragId);
  if (from < 0 || from === targetIdx) { _visualDragId = null; return; }
  const [moved] = rules.splice(from, 1);
  rules.splice(targetIdx, 0, moved);
  _visualRouting.rules = normalizeVisualPriorities(rules);
  _visualDragId = null;
  persistVisualRouting('Порядок обновлён');
}

async function testVisualRule() {
  const result = $id('vrTestResult');
  const value = ($id('vrTestValue')?.value || '').trim();
  if (!value) {
    showToast('Введите test value', 'warn');
    return;
  }
  try {
    const r = await fetch(API + '/routing/visual/test', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ rule: buildVisualRuleFromForm(), value })
    });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    result.textContent = d.matches ? `matches → ${d.action} (${d.outbound})` : 'does not match';
    result.className = 'visual-test-result ' + (d.matches ? 'ok' : 'miss');
  } catch(e) {
    result.textContent = e.message;
    result.className = 'visual-test-result err';
  }
}

function quickVisualRule(kind) {
  const value = prompt(kind === 'direct-app' ? 'process.exe' : 'domain.com');
  if (!value) return;
  const rule = {
    id: 'rule-' + Date.now().toString(36),
    name: kind === 'direct-app' ? value + ' direct' : value,
    enabled: true,
    priority: ((_visualRouting.rules || []).length + 1) * 10,
    match: { type: kind === 'direct-app' ? 'process' : 'domain_suffix', values: [value.trim()] },
    action: kind === 'block-domain' ? 'block' : kind === 'direct-app' ? 'direct' : 'proxy'
  };
  _visualRouting.rules = normalizeVisualPriorities([ ...(_visualRouting.rules || []), rule ]);
  persistVisualRouting('Правило добавлено');
}

async function importVisualPreset(id) {
  if (!id || !confirm('Импорт preset заменит текущие визуальные правила. Продолжить?')) return;
  try {
    const r = await fetch(API + '/routing/visual/presets/' + encodeURIComponent(id) + '/import', {method:'POST'});
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    showToast('Preset импортирован', 'on');
    OpTimer.start('apply', 'Применение routing preset', _applyHistory.estimate(5000));
    _watchApply('Применение routing preset');
    await loadRules();
    await loadVisualRouting();
  } catch(e) {
    showToast('Preset не импортирован: ' + e.message, 'off');
  }
}

// ═══════════════════════════════════════════════════
// PROFILES  →  /api/profiles
// ═══════════════════════════════════════════════════
async function loadProfiles() {
  const el = $id('profilesList');
  try {
    const r = await fetch(API + '/profiles');
    if (!r.ok) throw new Error();
    const d = await r.json();
    const profiles = (d && d.profiles) || [];
    if (!profiles.length) {
      el.innerHTML = '<div class="prof-empty">Профилей нет.<br>Нажмите «+ Сохранить текущие» чтобы создать снимок правил.</div>';
      renderActiveProfile(null);
      return;
    }
    const active = profiles.find(p => p.name === _activeProfileName) || profiles[0];
    renderActiveProfile(active);
    el.innerHTML = profiles.map(p => {
      const date = p.updated_at ? formatDate(p.updated_at, {day:'2-digit',month:'2-digit',year:'2-digit'}) : '—';
      const cnt  = p.rule_count != null ? p.rule_count : '?';
      const profileNameArg = jsArg(p.name);
      return `<div class="profile-item">
        <div class="prof-ico">🗂</div>
        <div class="prof-inf">
          <div class="prof-nm">${esc(p.name)}</div>
          <div class="prof-meta">${cnt} правил · ${date}</div>
        </div>
        <div class="prof-actions">
          <button class="pg-btn acc" style="padding:3px 9px;font-size:8px"
                  onclick="applyProfile(${profileNameArg},this)">▶ Применить</button>
          <button class="pg-btn danger" style="padding:3px 8px;font-size:8px"
                  onclick="deleteProfile(${profileNameArg})">✕</button>
        </div>
      </div>`;
    }).join('');
  } catch(_) {
    el.innerHTML = '<div class="prof-empty">Ошибка загрузки профилей</div>';
  }
}

function renderActiveProfile(profile) {
  const nameEl = $id('profileActiveName');
  const metaEl = $id('profileActiveMeta');
  if (!nameEl || !metaEl) return;
  if (!profile) {
    nameEl.textContent = 'Текущие правила ▾';
    metaEl.textContent = `${(_allRules || []).length} правил активно`;
    return;
  }
  _activeProfileName = profile.name || '';
  localStorage.setItem('safesky-active-profile', _activeProfileName);
  nameEl.textContent = (_activeProfileName || 'Текущие правила') + ' ▾';
  const cnt = profile.rule_count != null ? profile.rule_count : (_allRules || []).length;
  metaEl.textContent = `${cnt} правил в профиле`;
}

function updateActiveProfileMeta() {
  const metaEl = $id('profileActiveMeta');
  if (metaEl && !_activeProfileName) metaEl.textContent = `${(_allRules || []).length} правил активно`;
}

function toggleProfileChooser() {
  const chooser = $id('profileChooser');
  if (!chooser) return;
  chooser.style.display = chooser.style.display === 'none' ? 'block' : 'none';
}

function showSaveProfile() {
  $id('saveProfileRow').style.display = 'flex';
  $id('profileNameInp').focus();
}

function hideSaveProfile() {
  $id('saveProfileRow').style.display = 'none';
  $id('profileNameInp').value = '';
}

async function saveProfile() {
  const name = $id('profileNameInp').value.trim();
  if (!name) { showToast('Введите название профиля', 'warn'); return; }
  try {
    // Берём текущие tun-правила
    const rr = await fetch(API + '/tun/rules');
    if (!rr.ok) throw new Error('Не удалось получить правила');
    const routing = await rr.json(); // { default_action, rules }
    // Сохраняем профиль
    const r = await fetch(API + '/profiles', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ name, routing })
    });
    if (!r.ok) throw new Error(await r.text());
    showToast('Профиль «' + name + '» сохранён', 'on');
    hideSaveProfile();
    loadProfiles();
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
}

async function applyProfile(name, btn) {
  if (btn) { btn.disabled = true; btn.textContent = '...'; }
  try {
    const ra = await fetch(API + '/profiles/' + encodeURIComponent(name) + '/apply', { method: 'POST' });
    if (!ra.ok) throw new Error(await ra.text());
    const rd = await ra.json().catch(() => ({}));

    const opLabel = 'Применение профиля <b>' + name + '</b>';
    if (rd.apply_error) {
      // Правила сохранены в routing.json, но TriggerApply не запустился
      // (например, sing-box восстанавливается после сбоя TUN).
      // Явно вызываем POST /api/tun/apply — если sing-box ещё перезапускается,
      // тот поставит apply в очередь (pendingApply) и применит после восстановления.
      showToast('Профиль «' + name + '» сохранён — запускаем apply...', 'warn');
      OpTimer.start('apply', opLabel + ' (повтор apply)', _applyHistory.estimate(30000));
      try {
        const applyR = await fetch(API + '/tun/apply', { method: 'POST' });
        if (applyR.ok) {
          _watchApply(opLabel);
        } else {
          const errTxt = await applyR.text().catch(() => '');
          OpTimer.fail('apply', 'Ошибка apply: ' + errTxt);
          showToast('Не удалось применить: ' + errTxt, 'off');
        }
      } catch(applyErr) {
        OpTimer.fail('apply', 'Ошибка: ' + applyErr.message);
      }
    } else {
      showToast('Профиль «' + name + '» применён', 'on');
      OpTimer.start('apply', opLabel, _applyHistory.estimate(8000));
      _watchApply(opLabel);
    }
    // Обновляем список правил на вкладке
    _activeProfileName = name;
    localStorage.setItem('safesky-active-profile', name);
    const chooser = $id('profileChooser');
    if (chooser) chooser.style.display = 'none';
    loadRules();
    loadProfiles();
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'off');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '▶ Применить'; }
  }
}

async function deleteProfile(name) {
  if (!confirm('Удалить профиль «' + name + '»?')) return;
  try {
    const r = await fetch(API + '/profiles/' + encodeURIComponent(name), { method: 'DELETE' });
    if (!r.ok) throw new Error();
    showToast('Профиль удалён', 'info');
    loadProfiles();
  } catch(_) { showToast('Ошибка удаления', 'off'); }
}
