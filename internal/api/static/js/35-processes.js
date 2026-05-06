
// ═══════════════════════════════════════════════════
// PROCESSES PAGE  →  /api/apps/processes
// ═══════════════════════════════════════════════════
let _processRulesLoadPromise = null;
let _processSubtab = 'apps';

function setProcessSubtab(tab) {
  _processSubtab = tab === 'connections' ? 'connections' : tab === 'proxy' ? 'proxy' : 'apps';
  $id('procTabApps')?.classList.toggle('active', _processSubtab === 'apps');
  $id('procTabProxy')?.classList.toggle('active', _processSubtab === 'proxy');
  $id('procTabConnections')?.classList.toggle('active', _processSubtab === 'connections');
  const apps = $id('procAppsPanel');
  const conns = $id('procConnectionsPanel');
  if (apps) apps.style.display = _processSubtab === 'connections' ? 'none' : 'flex';
  if (conns) conns.style.display = _processSubtab === 'connections' ? 'flex' : 'none';
  if (_processSubtab === 'connections') {
    pollConnections?.();
    setTimeout(autoFitExeColumn, 60);
    return;
  }
  loadProcs();
}

async function ensureProcessRulesLoaded() {
  if (Array.isArray(_allRules) && _allRules.length) return;
  if (_processRulesLoadPromise) return _processRulesLoadPromise;
  _processRulesLoadPromise = fetch(API + '/tun/rules')
    .then(r => r.ok ? r.json() : null)
    .then(data => {
      if (data && Array.isArray(data.rules)) {
        _allRules = data.rules;
        _routingConfig.default_action = data.default_action || _routingConfig.default_action || 'proxy';
      }
    })
    .catch(() => {})
    .finally(() => { _processRulesLoadPromise = null; });
  return _processRulesLoadPromise;
}

function _normProcValue(value) {
  return String(value || '')
    .trim()
    .toLowerCase()
    .replace(/^process:/, '')
    .replace(/^file:\/+/, '')
    .replace(/\\/g, '/');
}

function _procRuleCandidates(p) {
  const values = [p.path, p.executable, p.name].filter(Boolean);
  values.forEach(v => values.push(basename(v)));
  const out = new Set();
  values.forEach(v => {
    const n = _normProcValue(v);
    if (!n) return;
    out.add(n);
    const b = basename(n);
    if (b) out.add(b);
    if (b.endsWith('.exe')) out.add(b.slice(0, -4));
  });
  return out;
}

function _ruleLooksProcess(rule) {
  const value = _normProcValue(rule?.value);
  return rule?.type === 'process' || value.endsWith('.exe') || value.includes('/') || value.includes('\\');
}

function matchProcessTunRule(p) {
  const candidates = _procRuleCandidates(p);
  if (!candidates.size) return null;
  for (const rule of (_allRules || [])) {
    if (!_ruleLooksProcess(rule)) continue;
    const raw = _normProcValue(rule.value);
    if (!raw) continue;
    const ruleBase = basename(raw);
    const ruleNoExt = ruleBase.endsWith('.exe') ? ruleBase.slice(0, -4) : ruleBase;
    if (candidates.has(raw) || candidates.has(ruleBase) || candidates.has(ruleNoExt)) return rule;
    for (const c of candidates) {
      if (raw.includes('/') && (c === raw || c.endsWith('/' + ruleBase))) return rule;
      if (ruleBase && c.endsWith('/' + ruleBase)) return rule;
    }
  }
  return null;
}

function processMode(p, matchedRule) {
  const raw = String(p.proxy_status || p.rule?.mode || p.mode || '').toLowerCase();
  if (raw === 'proxied') return 'proxy';
  if (raw === 'blocked') return 'block';
  if (raw === 'proxy' || raw === 'direct' || raw === 'block') return raw;
  return (matchedRule?.action || 'unknown').toLowerCase();
}

function procModeMeta(mode) {
  if (mode === 'proxy') return { cls:'p', label:'VPN', title:'Через прокси' };
  if (mode === 'direct') return { cls:'d', label:'DIRECT', title:'Напрямую' };
  if (mode === 'block') return { cls:'b', label:'BLOCK', title:'Блокируется' };
  return { cls:'u', label:'БЕЗ ПРАВИЛА', title:'Без совпадения' };
}

function _procBaseNoExt(value) {
  const base = basename(_normProcValue(value || ''));
  return base.endsWith('.exe') ? base.slice(0, -4) : base;
}

function processRuleText(matchedRule, nm, fullPath) {
  if (!matchedRule) return '';
  const ruleBase = _procBaseNoExt(matchedRule.value);
  const procBase = _procBaseNoExt(nm || fullPath);
  if (ruleBase && procBase && ruleBase === procBase) return '';
  return 'правило: ' + matchedRule.value;
}

function processFallbackIcon(name) {
  return name.match(/chrome|chromium|edge/i) ? '🌐' : name.match(/firefox/i) ? '🦊' :
         name.match(/telegram/i) ? '✈️' : name.match(/discord/i) ? '💬' :
         name.match(/code\.exe|cursor/i) ? '▣' : name.match(/steam/i) ? '▶' :
         name.match(/spotify/i) ? '♪' : '⚙';
}

function groupProcessRows(rows) {
  const groups = new Map();
  rows.forEach(row => {
    const appKey = _normProcValue(row.fullPath || row.p.executable || row.p.path || row.nm || row.p.name || '');
    const ruleKey = _normProcValue(row.matchedRule?.value || '');
    const key = [
      row.system ? 'sys' : 'user',
      row.mode || 'unknown',
      appKey || _normProcValue(row.nm || row.p.name || ''),
      ruleKey
    ].join('|');
    let group = groups.get(key);
    if (!group) {
      group = {
        ...row,
        instances: [],
        pids: [],
        hay: row.hay || ''
      };
      groups.set(key, group);
    }
    group.instances.push(row);
    if (row.p.pid != null && row.p.pid !== '') {
      const pid = String(row.p.pid);
      if (!group.pids.includes(pid)) group.pids.push(pid);
    }
    if (!group.matchedRule && row.matchedRule) group.matchedRule = row.matchedRule;
    if (!group.fullPath && row.fullPath) group.fullPath = row.fullPath;
    if (!group.nm && row.nm) group.nm = row.nm;
    group.hay += ' ' + (row.hay || '');
  });
  return Array.from(groups.values()).map(group => {
    group.instanceCount = group.instances.length;
    group.pidPreview = group.pids.slice(0, 5).join(', ');
    group.extraPidCount = Math.max(0, group.pids.length - 5);
    return group;
  });
}

async function loadProcs() {
  const el = $id('procList');
  const filterVal = ($id('procFilter')?.value || '').trim().toLowerCase();
  try {
    const [, r] = await Promise.all([
      ensureProcessRulesLoaded(),
      fetch(API + '/apps/processes')
    ]);
    if (!r.ok) throw new Error();
    const data = await r.json();
    const procs = (Array.isArray(data) ? data : data.processes) || [];
    if (!procs.length) {
      if ($id('procHeroTotal')) $id('procHeroTotal').textContent = '0';
      if ($id('procHeroUser')) $id('procHeroUser').textContent = '0';
      el.innerHTML = '<div class="rules-empty">Нет активных процессов</div>';
      return;
    }

    // Дедупликация: один и тот же PID может прийти из API несколько раз
    // (например, при быстром обновлении монитора процессов).
    const seen = new Set();
    const uniqueProcs = procs.slice(0, 300).filter(p => {
      const key = (p.pid != null ? String(p.pid) : '') + '|' + (p.path || p.executable || p.name || '');
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });

    const makeGroups = () => ({ proxy: [], direct: [], block: [], unknown: [] });
    const buckets = { USER: makeGroups(), SYS: makeGroups() };
    let rows = uniqueProcs.map((p, idx) => {
      const nm = basename(p.path || p.executable || p.name || '');
      const fullPath = p.executable || p.path || '';
      const matchedRule = matchProcessTunRule(p);
      const mode = processMode(p, matchedRule);
      const hay = [nm, fullPath, p.name || '', matchedRule?.value || '', mode].join(' ').toLowerCase();
      return { p, idx, nm, fullPath, matchedRule, mode, hay, system: isSystemProc(nm || p.name || p.executable) };
    }).filter(row => !filterVal || row.hay.includes(filterVal));
    if (_processSubtab === 'proxy') {
      rows = rows.filter(row => row.mode === 'proxy');
    }

    const groupedRows = groupProcessRows(rows);

    groupedRows.forEach(row => {
      const bucket = row.system ? buckets.SYS : buckets.USER;
      (bucket[row.mode] || bucket.unknown).push(row);
    });

    const renderProc = (row, pidx) => {
      const { p, nm, fullPath, matchedRule, mode } = row;
      const count = row.instanceCount || 1;
      const pidLabel = count > 1
        ? `${count} процессов`
        : `PID ${p.pid || '—'}`;
      const pidTitle = row.pidPreview
        ? `PID: ${row.pidPreview}${row.extraPidCount ? ' +' + row.extraPidCount : ''}`
        : 'PID не определён';
      const meta = procModeMeta(mode);
      const ruleText = processRuleText(matchedRule, nm, fullPath);
      const sourceText = matchedRule ? 'TUN-правило' : 'монитор';
      const sourceTitle = matchedRule
        ? `Правило маршрутизации: ${matchedRule.value}`
        : 'Статус получен от монитора процессов';
      const fallbackIco = processFallbackIcon(nm || '');
      const ico = fullPath
        ? `<img src="${API}/procicon?path=${encodeURIComponent(fullPath)}" width="20" height="20" style="border-radius:4px;object-fit:contain" alt="${esc(nm)}" onerror="this.outerHTML='${fallbackIco}'">`
        : fallbackIco;
      const delay = pidx < 15 ? `style="animation-delay:${pidx * 0.03}s"` : '';
      const titleAttr = fullPath ? `title="${esc(fullPath)}"` : '';
      const procNameArg = jsArg(nm);
      return `<div class="proc-item proc-card" ${delay} ${titleAttr}>
        <div class="proc-ico">${ico}</div>
        <div class="proc-main">
          <div class="proc-line">
            <div class="proc-nm">${esc(nm || '—')}</div>
            <span class="proc-rule ${meta.cls}" title="${esc(meta.title)}">${meta.label}</span>
          </div>
          <div class="proc-path">${esc(fullPath || p.name || 'путь процесса не определён')}</div>
          <div class="proc-detail">
            <span title="${esc(pidTitle)}">${esc(pidLabel)}</span>
            <span title="${esc(sourceTitle)}">${esc(sourceText)}</span>
            ${ruleText ? `<span title="${esc(ruleText)}">${esc(ruleText)}</span>` : ''}
          </div>
        </div>
        <button class="proc-add-btn" onclick="openAddRuleModal(${procNameArg})">${matchedRule ? 'Правило' : '+ Правило'}</button>
      </div>`;
    };

    const renderGroup = (key, items, open) => {
      if (!items.length) return '';
      const meta = procModeMeta(key);
      return `<details class="proc-status-group ${meta.cls}" ${open ? 'open' : ''}>
        <summary>
          <span>${meta.title}</span>
          <span class="proc-group-badge ${meta.cls}">${items.length}</span>
        </summary>
        <div class="proc-rows">${items.map((row, i) => renderProc(row, i)).join('')}</div>
      </details>`;
    };

    const countGroups = g => g.proxy.length + g.direct.length + g.block.length + g.unknown.length;
    const userTotal = countGroups(buckets.USER);
    const sysTotal = countGroups(buckets.SYS);
    const total = userTotal + sysTotal;
    if ($id('procHeroTotal')) $id('procHeroTotal').textContent = total;
    if ($id('procHeroUser')) $id('procHeroUser').textContent = userTotal;
    const allRows = groupedRows;
    const stat = key => allRows.filter(row => row.mode === key).length;
    const statsBar = `<div class="proc-stats-bar">
      <div class="proc-stat-card p"><b>${stat('proxy')}</b><span>через прокси</span></div>
      <div class="proc-stat-card d"><b>${stat('direct')}</b><span>напрямую</span></div>
      <div class="proc-stat-card u"><b>${stat('unknown')}</b><span>без правила</span></div>
    </div>`;

    const renderBucket = (bucketKey, label, groups, collapsed) => {
      const total = countGroups(groups);
      if (!total) return '';
      const id = 'procBucket_' + bucketKey;
      const content = renderGroup('proxy', groups.proxy, true) +
        renderGroup('direct', groups.direct, true) +
        renderGroup('block', groups.block, true) +
        renderGroup('unknown', groups.unknown, !groups.proxy.length && !groups.direct.length && !groups.block.length);
      const idArg = jsArg(id);
      return `<div class="proc-bucket">
        <div class="proc-group-hd" onclick="toggleProcGroup(${idArg})">
        ${label} <span class="proc-group-badge ${bucketKey === 'SYS' ? 'u' : 'd'}">${total}</span>
        <span style="margin-left:auto;font-size:9px" id="${id}_arrow">${collapsed ? '▸' : '▾'}</span>
        </div>
        <div id="${id}" style="${collapsed ? 'display:none' : ''}">${content}</div>
      </div>`;
    };

    const html =
      statsBar +
      renderBucket('USER', 'ПОЛЬЗОВАТЕЛЬСКИЕ ПРОЦЕССЫ', buckets.USER, false) +
      renderBucket('SYS', 'СИСТЕМНЫЕ ПРОЦЕССЫ', buckets.SYS, true);

    if (!rows.length && filterVal) {
      el.innerHTML = `<div class="rules-empty">Нет процессов по запросу «${esc(filterVal)}»</div>`;
    } else {
      el.innerHTML = html;
    }
    if (el.parentElement) el.parentElement.scrollTop = 0;
  } catch(_) {
    if ($id('procHeroTotal')) $id('procHeroTotal').textContent = '—';
    if ($id('procHeroUser')) $id('procHeroUser').textContent = '—';
    el.innerHTML = '<div class="rules-empty">Ошибка загрузки процессов</div>';
  }
}

function toggleProcGroup(id) {
  const el = $id(id);
  if (!el) return;
  const hidden = el.style.display === 'none';
  el.style.display = hidden ? '' : 'none';
  const arrow = $id(id + '_arrow');
  if (arrow) arrow.textContent = hidden ? '▾' : '▸';
}

// ── Add-rule modal (#7) ──
function openAddRuleModal(appName) {
  $id('modalRuleApp').value = appName || '';
  setRuleAction('modalRuleMode', 'proxy');
  $id('addRuleModal').style.display = 'flex';
  setTimeout(() => $id('modalRuleApp')?.focus(), 50);
}
function closeAddRuleModal() {
  $id('addRuleModal').style.display = 'none';
}
async function saveAddRuleModal() {
  const app  = $id('modalRuleApp').value.trim();
  const mode = $id('modalRuleMode').value;
  if (!app) { showToast('Введите значение правила', 'warn'); return; }
  try {
    const r = await fetch(API + '/tun/rules', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value: app, action: mode.toLowerCase() })
    });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json().catch(() => ({}));
    closeAddRuleModal();
    if (d.apply_error) {
      showToast('Правило сохранено, но не применено: ' + d.apply_error, 'warn');
    } else {
      showToast('Правило добавлено', 'on');
      OpTimer.start('apply', 'Применение правила <b>' + app + '</b>', _applyHistory.estimate(5000));
      _watchApply('Применение правила <b>' + app + '</b>');
    }
    loadRules();
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
}

// Escape закрывает модал и geosite suggestion
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') { closeAddRuleModal(); closeRulesJsonEditor(); closeSingboxConfigEditor(); const b = $id('geoSuggestBox'); if(b) b.style.display='none'; }
});
document.addEventListener('click', e => {
  const b = $id('geoSuggestBox');
  if (b && !b.contains(e.target) && e.target.id !== 'ruleApp') b.style.display = 'none';
});

async function refreshProcs() {
  const btn = $id('procRefreshBtn');
  btn.disabled = true; btn.textContent = '...';
  try {
    await fetch(API + '/apps/processes/refresh', {method:'POST'});
    await loadProcs();
  } finally { btn.disabled = false; btn.textContent = '↻'; }
}

// ═══════════════════════════════════════════════════
