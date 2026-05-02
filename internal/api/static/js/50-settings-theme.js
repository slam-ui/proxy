// SETTINGS PAGE
// ═══════════════════════════════════════════════════
async function toggleAutorun() {
  const el = $id('autorunToggle');
  if (!el) return;
  const willEnable = !el.classList.contains('on');
  try {
    const r = await fetch(API + '/settings/autorun', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({enabled: willEnable})
    });
    if (r.ok) {
      const d = await r.json();
      el.classList.toggle('on', !!d.autorun);
      showToast(d.autorun ? 'Автозапуск включён' : 'Автозапуск выключен', 'on');
    } else {
      showToast('Ошибка изменения автозапуска', 'warn');
    }
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'warn');
  }
}

async function toggleStartupProxy() {
  const el = $id('startupProxyToggle');
  if (!el) return;
  const willEnable = !el.classList.contains('on');
  try {
    const r = await fetch(API + '/settings/startup-proxy', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({enabled: willEnable})
    });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json().catch(() => ({}));
    el.classList.toggle('on', !!d.enabled);
    showToast(d.enabled ? 'Прокси будет включаться при запуске' : 'Прокси будет ждать кнопку включения', 'on');
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'warn');
  }
}

let _appSettingsCache = {};
let _speedProgressTimer = null;
let _speedProgressValue = 0;
let _singboxConfigDirty = false;

function setSpeedProgress(pct, text) {
  _speedProgressValue = Math.max(0, Math.min(100, pct));
  const box = $id('speedProgress');
  if (box) box.classList.add('vis');
  if ($id('speedProgressFill')) $id('speedProgressFill').style.width = _speedProgressValue + '%';
  if ($id('speedProgressPct')) $id('speedProgressPct').textContent = Math.round(_speedProgressValue) + '%';
  if (text && $id('speedProgressText')) $id('speedProgressText').textContent = text;
}

function startSpeedProgress() {
  clearInterval(_speedProgressTimer);
  setSpeedProgress(6, 'Подготовка запроса');
  _speedProgressTimer = setInterval(() => {
    const next = _speedProgressValue < 55
      ? _speedProgressValue + 7
      : _speedProgressValue < 86
        ? _speedProgressValue + 3
        : _speedProgressValue + 0.8;
    const label = next < 25 ? 'Проверка прокси' : next < 70 ? 'Загрузка тестового файла' : 'Подсчёт скорости';
    setSpeedProgress(Math.min(next, 94), label);
  }, 650);
}

function finishSpeedProgress(ok) {
  clearInterval(_speedProgressTimer);
  setSpeedProgress(ok ? 100 : Math.max(_speedProgressValue, 100), ok ? 'Готово' : 'Ошибка');
  setTimeout(() => $id('speedProgress')?.classList.remove('vis'), ok ? 1400 : 2200);
}

async function loadClipboardBanner() {
  try {
    const r = await fetch(API + '/clipboard/vless');
    const d = await r.json();
    const el = $id('clipVlessBanner');
    if (el) el.style.display = d.found ? 'flex' : 'none';
  } catch(_) {}
}

function refreshClipboardBannerIfVisible() {
  if ($id('clipVlessBanner')) loadClipboardBanner();
}
window.addEventListener('focus', refreshClipboardBannerIfVisible);
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) refreshClipboardBannerIfVisible();
});

async function addClipboardServerFromBanner() {
  try {
    const r = await fetch(API + '/clipboard/vless');
    const d = await r.json();
    if (!d.found || !d.url) { showToast('Server URI в буфере не найден', 'warn'); return; }
    const add = await fetch(API + '/servers', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({url:d.url})
    });
    if (!add.ok && add.status !== 409) throw new Error(await add.text());
    showToast(add.status === 409 ? 'Сервер уже добавлен' : 'Сервер добавлен', add.status === 409 ? 'warn' : 'on');
    loadServers();
    loadClipboardBanner();
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
}

async function runSpeedTest() {
  const btn = $id('speedTestBtn');
  if (btn) { btn.disabled = true; btn.textContent = 'Идёт...'; }
  startSpeedProgress();
  try {
    const r = await fetch(API + '/speedtest', {method:'POST'});
    const d = await r.json();
    if (!r.ok || d.error) throw new Error(d.error || 'HTTP ' + r.status);
    $id('speedTestResult').textContent = `${Number(d.download_mbps || 0).toFixed(1)} Мбит/с, ${d.latency_ms || 0} мс`;
    finishSpeedProgress(true);
    showToast('Тест скорости завершён', 'on');
  } catch(e) {
    $id('speedTestResult').textContent = 'ошибка';
    finishSpeedProgress(false);
    showToast('Тест скорости: ' + e.message, 'off');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Тест'; }
  }
}

async function runLeakCheck() {
  const btn = $id('leakCheckBtn');
  if (btn) { btn.disabled = true; btn.textContent = '...'; }
  try {
    const r = await fetch(API + '/leaktest/summary', { method: 'POST', timeoutMs: 30000 });
    const d = await r.json();
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const dns = d.dns || {};
    const ipv6 = d.ipv6 || {};
    const dnsLabel = d.dns_error ? 'DNS недоступен' : (dns.leaked ? 'DNS утечка' : `DNS ${dns.status || 'ok'}`);
    const ipv6Label = d.ipv6_error ? 'IPv6 недоступен' : (ipv6.available ? 'IPv6 активен' : 'IPv6 нет');
    $id('leakCheckResult').textContent = `${dnsLabel} · ${ipv6Label}`;
    const leaked = !!(dns.leaked || ipv6.leaked);
    showToast(leaked ? 'Обнаружен риск утечки' : 'Утечек не найдено', leaked ? 'warn' : 'on');
  } catch(e) {
    $id('leakCheckResult').textContent = 'ошибка';
    showToast('Проверка утечек: ' + e.message, 'off');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Проверить'; }
  }
}

async function loadTotalStats() {
  try {
    const r = await fetch(API + '/stats/total');
    const d = await r.json();
    const down = (d.total_download_bytes || 0) + (d.session_download_bytes || 0);
    const up = (d.total_upload_bytes || 0) + (d.session_upload_bytes || 0);
    $id('totalStatsResult').textContent = `↓ ${fmtBytes(down)} ↑ ${fmtBytes(up)} · сессий ${d.total_sessions || 0}`;
  } catch(_) {
    if ($id('totalStatsResult')) $id('totalStatsResult').textContent = 'недоступно';
  }
}

async function loadConnectionHistory() {
  try {
    const r = await fetch(API + '/connections/history');
    const d = await r.json();
    const events = d.events || [];
    const last = events[events.length - 1];
    $id('connHistoryResult').textContent = last ? `${last.kind} ${last.server || ''}` : 'событий нет';
  } catch(_) {
    if ($id('connHistoryResult')) $id('connHistoryResult').textContent = 'недоступно';
  }
}

async function loadCrashReports() {
  try {
    const r = await fetch(API + '/diagnostics/crashes');
    const d = await r.json();
    const reports = d.reports || [];
    $id('crashReportsResult').textContent = reports.length ? `${reports.length} последних отчётов` : 'нет отчётов';
  } catch(_) {
    if ($id('crashReportsResult')) $id('crashReportsResult').textContent = 'недоступно';
  }
}

async function runRuleAnalyze() {
  try {
    const r = await fetch(API + '/tun/rules/analyze');
    const d = await r.json();
    const el = $id('ruleAnalysisResult');
    if (el) el.textContent = d.count ? `${d.count} замечаний` : 'проблем нет';
    showToast(d.count ? `Найдено замечаний: ${d.count}` : 'Правила без конфликтов', d.count ? 'warn' : 'on');
  } catch(e) { showToast('Анализ правил: ' + e.message, 'off'); }
}

async function fixRuleAnalyze() {
  try {
    const r = await fetch(API + '/tun/rules/analyze/fix', {method:'POST'});
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    if ($id('ruleAnalysisResult')) $id('ruleAnalysisResult').textContent = `${d.before || 0} → ${d.after || 0}`;
    showToast('Правила очищены и отсортированы', 'on');
    loadRules();
  } catch(e) { showToast('Исправление правил: ' + e.message, 'off'); }
}

async function runDNSGuardCheck() {
  try {
    const r = await fetch(API + '/security/dns-guard/check');
    const d = await r.json();
    const el = $id('dnsGuardResult');
    if (el) el.textContent = `прокси ${d.proxy_ip || '—'} / напрямую ${d.direct_ip || '—'}`;
    showToast(d.leaked ? 'Защита DNS/IP обнаружила утечку' : 'Утечек DNS/IP не найдено', d.leaked ? 'warn' : 'on');
  } catch(e) { showToast('Защита DNS: ' + e.message, 'off'); }
}

async function checkFailoverStatus() {
  try {
    const r = await fetch(API + '/servers/failover');
    const d = await r.json();
    const alive = (d.servers || []).filter(s => s.ok).length;
    const active = (d.servers || []).find(s => s.active);
    if ($id('failoverResult')) $id('failoverResult').textContent = `${alive}/${(d.servers || []).length} доступно${active ? ', активный ' + active.latency_ms + ' мс' : ''}`;
  } catch(e) {
    if ($id('failoverResult')) $id('failoverResult').textContent = 'недоступно: ' + e.message;
  }
}

async function loadTrafficBudget() {
  try {
    const r = await fetch(API + '/security/traffic-budget');
    const d = await r.json();
    const el = $id('trafficBudgetResult');
    if (el) el.textContent = `сессия ${fmtBytes(d.session_bytes || 0)} · всего ${fmtBytes(d.total_bytes || 0)}`;
  } catch(_) {}
}

async function saveTrafficBudget() {
  await saveLifecycleSettings();
  loadTrafficBudget();
}

async function runIntegrityCheck() {
  try {
    const r = await fetch(API + '/diagnostics/integrity');
    const d = await r.json();
    const bad = (d.files || []).filter(f => !f.ok).length;
    if ($id('integrityResult')) $id('integrityResult').textContent = bad ? `${bad} проблем` : `${(d.files || []).length} файлов доступны`;
    showToast(bad ? 'Проверка целостности: есть проблемы' : 'Проверка целостности: файлы доступны', bad ? 'warn' : 'on');
  } catch(e) { showToast('Проверка целостности: ' + e.message, 'off'); }
}

function downloadDiagnosticsPackage() {
  window.location.href = API + '/diagnostics/package';
}

async function runDiagnose() {
  const btn = $id('diagnoseBtn');
  if (btn) { btn.disabled = true; btn.textContent = '...'; }
  try {
    const r = await fetch(API + '/diagnose', { method: 'POST', timeoutMs: 45000 });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    const failed = (d.steps || []).filter(s => !s.ok);
    if ($id('diagnoseResult')) {
      $id('diagnoseResult').textContent = failed.length
        ? `${failed[0].message || failed[0].code}: ${failed[0].hint || ''}`
        : `OK · ${d.duration_ms || 0} мс`;
    }
    showToast(failed.length ? 'Диагностика нашла проблему' : 'Диагностика без ошибок', failed.length ? 'warn' : 'on');
  } catch(e) {
    if ($id('diagnoseResult')) $id('diagnoseResult').textContent = 'ошибка';
    showToast('Диагностика: ' + e.message, 'off');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Diagnose'; }
  }
}

async function openLogFolder() {
  try {
    const r = await fetch(API + '/diagnostics/log-folder', { method: 'POST' });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    showToast(d.opened ? 'Папка логов открыта' : 'Папка логов: ' + (d.path || 'logs'), 'info');
  } catch(e) {
    showToast('Логи: ' + e.message, 'off');
  }
}

function applyRoutingControls() {
  $id('blockQuicToggle')?.classList.toggle('on', _routingConfig.block_quic !== false);
  $id('blockTelemetryToggle')?.classList.toggle('on', !!_routingConfig.block_telemetry);
  $id('lanShareToggle')?.classList.toggle('on', !!_routingConfig.lan_share_enabled);
  if ($id('lanSharePortInp')) $id('lanSharePortInp').value = _routingConfig.lan_share_port || 10808;
}

async function loadRoutingOptions() {
  try {
    const r = await fetch(API + '/tun/rules');
    if (!r.ok) return;
    const d = await r.json();
    _routingConfig = {
      ..._routingConfig,
      default_action: d.default_action || _routingConfig.default_action || 'proxy',
      rules: d.rules || _allRules || [],
      bypass_enabled: !!d.bypass_enabled,
      dns: d.dns || _routingConfig.dns,
      block_quic: d.block_quic !== false,
      block_telemetry: !!d.block_telemetry,
      lan_share_enabled: !!d.lan_share_enabled,
      lan_share_port: d.lan_share_port || 10808
    };
    applyRoutingControls();
  } catch(_) {}
}

async function saveRoutingOptions() {
  const port = Number($id('lanSharePortInp')?.value || _routingConfig.lan_share_port || 10808);
  _routingConfig.lan_share_port = Math.max(1024, Math.min(65535, port));
  try {
    const r = await fetch(API + '/tun/rules', {
      method: 'PUT',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify(routingJsonPayload())
    });
    if (!r.ok) throw new Error(await r.text());
    showToast('Настройки маршрутизации сохранены', 'on');
    loadLANInfo();
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
}

async function toggleRoutingOption(key) {
  if (key === 'block_quic') _routingConfig.block_quic = !($id('blockQuicToggle')?.classList.contains('on'));
  if (key === 'block_telemetry') _routingConfig.block_telemetry = !($id('blockTelemetryToggle')?.classList.contains('on'));
  if (key === 'lan_share_enabled') _routingConfig.lan_share_enabled = !($id('lanShareToggle')?.classList.contains('on'));
  applyRoutingControls();
  await saveRoutingOptions();
}

async function loadLANInfo() {
  try {
    const r = await fetch(API + '/settings/lan-info');
    const d = await r.json();
    const ips = d.ips || [];
    $id('lanInfoResult').textContent = ips.length ? ips.map(ip => `${ip}:${d.port || 10808}`).join(', ') : 'LAN IP не найден';
  } catch(_) {
    if ($id('lanInfoResult')) $id('lanInfoResult').textContent = 'недоступно';
  }
}

function applyLifecycleControls(d) {
  _appSettingsCache = d || _appSettingsCache || {};
  $id('keepaliveToggle')?.classList.toggle('on', !!_appSettingsCache.keepalive_enabled);
  $id('scheduleToggle')?.classList.toggle('on', !!(_appSettingsCache.schedule && _appSettingsCache.schedule.enabled));
  $id('manualConfigToggle')?.classList.toggle('on', !!_appSettingsCache.manual_singbox_config);
  $id('smartFailoverToggle')?.classList.toggle('on', !!(_appSettingsCache.smart_failover && _appSettingsCache.smart_failover.enabled));
  $id('dnsGuardToggle')?.classList.toggle('on', !!(_appSettingsCache.dns_guard && _appSettingsCache.dns_guard.enabled));
  $id('networkStrictToggle')?.classList.toggle('on', !!(_appSettingsCache.network_protection && _appSettingsCache.network_protection.strict_on_change));
  $id('trafficBudgetToggle')?.classList.toggle('on', !!(_appSettingsCache.traffic_budget && _appSettingsCache.traffic_budget.enabled));
  if ($id('keepaliveIntervalInp')) $id('keepaliveIntervalInp').value = _appSettingsCache.keepalive_interval_sec || 120;
  if ($id('reconnectIntervalInp')) $id('reconnectIntervalInp').value = _appSettingsCache.reconnect_interval_min || 0;
  if ($id('memoryLimitInp')) $id('memoryLimitInp').value = _appSettingsCache.memory_limit_mb || 0;
  if ($id('scheduleOnInp')) $id('scheduleOnInp').value = (_appSettingsCache.schedule && _appSettingsCache.schedule.proxy_on) || '09:00';
  if ($id('scheduleOffInp')) $id('scheduleOffInp').value = (_appSettingsCache.schedule && _appSettingsCache.schedule.proxy_off) || '18:00';
  const sf = _appSettingsCache.smart_failover || {};
  if ($id('failoverMaxLatencyInp')) $id('failoverMaxLatencyInp').value = sf.max_latency_ms || 800;
  if ($id('failoverIntervalInp')) $id('failoverIntervalInp').value = sf.check_interval_sec || 60;
  const dg = _appSettingsCache.dns_guard || {};
  setDNSGuardMode(dg.mode || 'warn', false);
  const tb = _appSettingsCache.traffic_budget || {};
  if ($id('trafficSessionMBInp')) $id('trafficSessionMBInp').value = tb.session_limit_mb || 0;
  if ($id('trafficTotalMBInp')) $id('trafficTotalMBInp').value = tb.total_limit_mb || 0;
  applyUpdateControls(_appSettingsCache.updates || {});
  const lt = _appSettingsCache.leak_test || {};
  if ($id('leakDomainInp')) $id('leakDomainInp').value = lt.domain || 'dnsleak.example.com';
  if ($id('leakReportURLInp')) $id('leakReportURLInp').value = lt.report_url || 'https://example.com/api/dnsleak/check';
  const ks = _appSettingsCache.kill_switch_state || {};
  const recovery = !!(ks.active && ks.expected_clean_shutdown === false);
  if ($id('killSwitchRecoveryRow')) $id('killSwitchRecoveryRow').style.display = recovery ? 'flex' : 'none';
}

function applyUpdateControls(upd) {
  const settings = upd || {};
  $id('updateEnabledToggle')?.classList.toggle('on', settings.enabled !== false);
  $id('updateAutoInstallToggle')?.classList.toggle('on', !!settings.auto_install);
  if ($id('updateChannelInp')) $id('updateChannelInp').value = settings.channel === 'beta' ? 'beta' : 'stable';
  if ($id('updateBaseURLInp')) $id('updateBaseURLInp').value = settings.base_url || 'https://example.com/safesky';
}

function setDNSGuardMode(mode, persist = true) {
  const normalized = mode === 'strict' ? 'strict' : 'warn';
  if ($id('dnsGuardModeInp')) $id('dnsGuardModeInp').value = normalized;
  $id('dnsModeWarnBtn')?.classList.toggle('active', normalized === 'warn');
  $id('dnsModeStrictBtn')?.classList.toggle('active', normalized === 'strict');
  if (persist) saveLifecycleSettings();
}

async function saveLifecycleSettings() {
  const schedule = _appSettingsCache.schedule || {};
  schedule.enabled = !!$id('scheduleToggle')?.classList.contains('on');
  schedule.proxy_on = $id('scheduleOnInp')?.value || '09:00';
  schedule.proxy_off = $id('scheduleOffInp')?.value || '18:00';
  if (!Array.isArray(schedule.weekdays) || !schedule.weekdays.length) schedule.weekdays = [1,2,3,4,5,6,7];
  const payload = {
    keepalive_enabled: !!$id('keepaliveToggle')?.classList.contains('on'),
    keepalive_interval_sec: Number($id('keepaliveIntervalInp')?.value || 120),
    reconnect_interval_min: Number($id('reconnectIntervalInp')?.value || 0),
    memory_limit_mb: Number($id('memoryLimitInp')?.value || 0),
    manual_singbox_config: !!$id('manualConfigToggle')?.classList.contains('on'),
    smart_failover: {
      enabled: !!$id('smartFailoverToggle')?.classList.contains('on'),
      max_latency_ms: Number($id('failoverMaxLatencyInp')?.value || 800),
      check_interval_sec: Number($id('failoverIntervalInp')?.value || 60),
      min_improvement_ms: 50
    },
    dns_guard: {
      enabled: !!$id('dnsGuardToggle')?.classList.contains('on'),
      mode: $id('dnsGuardModeInp')?.value || 'warn',
      check_interval_sec: 60
    },
    network_protection: {
      enabled: true,
      strict_on_change: !!$id('networkStrictToggle')?.classList.contains('on'),
      check_interval_sec: 10
    },
    traffic_budget: {
      enabled: !!$id('trafficBudgetToggle')?.classList.contains('on'),
      session_limit_mb: Number($id('trafficSessionMBInp')?.value || 0),
      total_limit_mb: Number($id('trafficTotalMBInp')?.value || 0),
      warn_percent: 80
    },
    updates: currentUpdateSettings(),
    leak_test: {
      enabled: true,
      domain: ($id('leakDomainInp')?.value || 'dnsleak.example.com').trim(),
      report_url: ($id('leakReportURLInp')?.value || 'https://example.com/api/dnsleak/check').trim(),
      expected_resolvers: [],
      check_interval_min: 30
    },
    schedule
  };
  try {
    const r = await fetch(API + '/settings', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify(payload)
    });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json().catch(() => payload);
    applyLifecycleControls(d);
    showToast('Настройки сохранены', 'on');
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
}

function currentUpdateSettings() {
  return {
    enabled: !!$id('updateEnabledToggle')?.classList.contains('on'),
    channel: $id('updateChannelInp')?.value === 'beta' ? 'beta' : 'stable',
    base_url: ($id('updateBaseURLInp')?.value || 'https://example.com/safesky').trim(),
    auto_install: !!$id('updateAutoInstallToggle')?.classList.contains('on')
  };
}

async function loadClientUpdateStatus() {
  try {
    const r = await fetch(API + '/update/status');
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    const build = d.build || {};
    if ($id('appVersion')) $id('appVersion').textContent = build.version || '—';
    if ($id('appUpdateStatus')) $id('appUpdateStatus').textContent = `канал ${(d.settings && d.settings.channel) || 'stable'} · ${build.commit || 'unknown'}`;
    applyUpdateControls(d.settings || {});
  } catch(e) {
    if ($id('appUpdateStatus')) $id('appUpdateStatus').textContent = 'статус обновлений недоступен';
  }
}

function openWebRTCTest() {
  window.open(API + '/leaktest/webrtc', '_blank');
}

async function saveUpdateSettings() {
  await saveLifecycleSettings();
}

async function toggleUpdateSetting(key) {
  if (key === 'enabled') $id('updateEnabledToggle')?.classList.toggle('on');
  if (key === 'auto_install') $id('updateAutoInstallToggle')?.classList.toggle('on');
  await saveUpdateSettings();
}

async function checkClientUpdate() {
  const btn = $id('updateCheckBtn');
  if (btn) { btn.disabled = true; btn.textContent = 'Проверка...'; }
  try {
    const r = await fetch(API + '/update/check', { method: 'POST', timeoutMs: 45000 });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    const latest = d.latest || {};
    if ($id('appUpdateStatus')) {
      if (d.manual_update_required) {
        $id('appUpdateStatus').textContent = `нужно ручное обновление до ${latest.version || 'новой версии'}`;
      } else if (d.update_available) {
        $id('appUpdateStatus').textContent = `доступна версия ${latest.version}`;
      } else {
        $id('appUpdateStatus').textContent = 'установлена актуальная версия';
      }
    }
    showToast(d.update_available ? `Доступна версия ${latest.version}` : 'Обновлений нет', d.update_available ? 'warn' : 'on');
  } catch(e) {
    showToast('Проверка обновлений: ' + e.message, 'off');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Проверить'; }
  }
}

async function installClientUpdate() {
  const btn = $id('updateInstallBtn');
  if (btn) { btn.disabled = true; btn.textContent = 'Установка...'; }
  try {
    const r = await fetch(API + '/update/install', { method: 'POST', timeoutMs: 90000 });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    if (!d.started) {
      showToast(d.reason || 'Обновление не требуется', 'info');
      return;
    }
    showToast(`Установка версии ${d.version || ''} запущена`, 'on');
    if ($id('appUpdateStatus')) $id('appUpdateStatus').textContent = 'клиент перезапустится для обновления';
  } catch(e) {
    showToast('Установка обновления: ' + e.message, 'off');
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = 'Обновить'; }
  }
}

function toggleLifecycleOption(key) {
  if (key === 'keepalive_enabled') $id('keepaliveToggle')?.classList.toggle('on');
  if (key === 'schedule_enabled') $id('scheduleToggle')?.classList.toggle('on');
  if (key === 'smart_failover') $id('smartFailoverToggle')?.classList.toggle('on');
  if (key === 'dns_guard') $id('dnsGuardToggle')?.classList.toggle('on');
  if (key === 'network_strict') $id('networkStrictToggle')?.classList.toggle('on');
  if (key === 'traffic_budget') $id('trafficBudgetToggle')?.classList.toggle('on');
  saveLifecycleSettings();
}

async function unlockKillSwitch() {
  if (!confirm('Вы выходите в незащищённую сеть. Снять блокировку Kill switch?')) return;
  try {
    const r = await fetch(API + '/settings/killswitch', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({enabled:false})
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    showToast('Kill switch снят', 'warn');
    loadSettingsPage();
  } catch(e) {
    showToast('Kill switch: ' + e.message, 'off');
  }
}

function renderSingboxConfigStatus(d) {
  if (!d) return;
  _appSettingsCache.manual_singbox_config = !!d.manual_enabled;
  $id('manualConfigToggle')?.classList.toggle('on', !!d.manual_enabled);
  const mode = d.manual_enabled ? 'ручной режим включён' : 'автогенерация включена';
  const exists = d.exists === false ? 'файл не найден' : (d.path || 'config.singbox.json');
  if ($id('singboxConfigStatus')) $id('singboxConfigStatus').textContent = `${mode} · ${exists}`;
  if ($id('singboxConfigPath')) $id('singboxConfigPath').textContent = d.path || 'config.singbox.json';
  if ($id('singboxConfigMode')) $id('singboxConfigMode').textContent = 'режим: ' + mode;
  if ($id('singboxConfigUpdated')) {
    const ts = d.updated_at ? new Date(d.updated_at * 1000).toLocaleString() : '—';
    $id('singboxConfigUpdated').textContent = 'изменён: ' + ts;
  }
}

async function loadSingboxConfigStatus() {
  try {
    const r = await fetch(API + '/singbox-config');
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const d = await r.json();
    renderSingboxConfigStatus(d);
  } catch(_) {
    if ($id('singboxConfigStatus')) $id('singboxConfigStatus').textContent = 'недоступно';
  }
}

async function loadSingboxConfig() {
  try {
    const r = await fetch(API + '/singbox-config');
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    renderSingboxConfigStatus(d);
    if ($id('singboxConfigText')) $id('singboxConfigText').value = d.content || '';
    _singboxConfigDirty = false;
    showToast('config.singbox.json загружен', 'info');
  } catch(e) {
    showToast('Ошибка загрузки config.singbox.json: ' + e.message, 'off');
  }
}

function openSingboxConfigEditor() {
  const modal = $id('singboxConfigModal');
  if (!modal) return;
  modal.style.display = 'flex';
  loadSingboxConfig();
  setTimeout(() => $id('singboxConfigText')?.focus(), 50);
}

function closeSingboxConfigEditor() {
  const modal = $id('singboxConfigModal');
  if (modal) modal.style.display = 'none';
}

function formatSingboxConfig() {
  const el = $id('singboxConfigText');
  if (!el) return;
  try {
    el.value = JSON.stringify(JSON.parse(el.value || '{}'), null, 2);
    _singboxConfigDirty = true;
  } catch(e) {
    showToast('JSON невалиден: ' + e.message, 'warn');
  }
}

async function saveSingboxConfig(apply) {
  const el = $id('singboxConfigText');
  if (!el) return;
  let formatted = '';
  try {
    formatted = JSON.stringify(JSON.parse(el.value || '{}'), null, 2);
  } catch(e) {
    showToast('JSON невалиден: ' + e.message, 'warn');
    return;
  }
  try {
    const r = await fetch(API + '/singbox-config', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({content: formatted, manual_enabled: true, apply: !!apply})
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || await r.text());
    el.value = formatted;
    _singboxConfigDirty = false;
    renderSingboxConfigStatus({path: d.path || $id('singboxConfigPath')?.textContent, manual_enabled: d.manual_enabled, exists: true, updated_at: d.updated_at});
    showToast(apply ? 'config.singbox.json сохранён и применяется' : 'config.singbox.json сохранён', 'on');
    if (apply) _watchApply('Применение ручного config.singbox.json');
  } catch(e) {
    showToast('Ошибка сохранения config.singbox.json: ' + e.message, 'off');
  }
}

async function toggleManualSingboxConfig() {
  const el = $id('manualConfigToggle');
  const enabled = !el?.classList.contains('on');
  try {
    const r = await fetch(API + '/singbox-config', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({manual_enabled: enabled})
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || await r.text());
    _appSettingsCache.manual_singbox_config = !!d.manual_enabled;
    el?.classList.toggle('on', !!d.manual_enabled);
    loadSingboxConfigStatus();
    showToast(d.manual_enabled ? 'Ручной config.singbox.json включён' : 'Автогенерация config.singbox.json включена', 'on');
  } catch(e) {
    showToast('Ошибка: ' + e.message, 'off');
  }
}

async function loadSettingsPage() {
  // Engine status — берём из state (обновляется pollStatus), не из /engine/status
  if ($id('engState')) $id('engState').textContent = state.running ? 'Запущен' : 'Остановлен';
  if ($id('engDot')) $id('engDot').className = 'eng-dot ' + (state.running ? 'ok' : 'err');
  // Autorun state
  try {
    const r = await fetch(API + '/settings');
    if (r.ok) {
      const d = await r.json();
      const el = $id('autorunToggle');
      if (el) el.classList.toggle('on', !!d.autorun);
      const startupEl = $id('startupProxyToggle');
      if (startupEl) startupEl.classList.toggle('on', !!d.start_proxy_on_launch);
      applyLifecycleControls(d);
    }
  } catch(_) {}
  try {
    const r = await fetch(API + '/engine/version');
    if (r.ok) {
      const d = await r.json();
      if ($id('engVer')) $id('engVer').textContent = d.installed || '—';
      if ($id('engVerFull')) {
        $id('engVerFull').textContent = d.installed || '—';
        if (d.update_available) {
          $id('engVerFull').insertAdjacentHTML('afterend',
            '<span style="color:var(--warn);font-size:8px;margin-left:6px">⬆ ' + esc(d.latest || '') + ' доступно</span>');
        }
      }
    }
  } catch(_) {}
  // Geosite list
  try {
    const r = await fetch(API + '/geosite');
    if (r.ok) {
      const data = await r.json();
      const list = (data && data.items) || [];
      const el = $id('geositeList');
      const available = list.filter(g => g.available);
      const missing = list.filter(g => !g.available);
      if (!available.length) {
        el.innerHTML = `<div class="geo-empty-state">
          <div class="geo-empty-icon">📦</div>
          <div class="geo-empty-title">Базы не загружены</div>
          <div class="geo-empty-sub">Нажмите «Обновить базы из правил» — будут скачаны только geosite, которые используются в маршрутизации</div>
          ${missing.length ? `<div class="geo-missing-list">${missing.map(g => `<span class="geo-chip missing">${esc(g.name)}</span>`).join('')}</div>` : ''}
        </div>`;
      } else {
        el.innerHTML = available.map(g => {
          const sizeMb = g.file_size ? (g.file_size / 1024).toFixed(1) + ' KB' : '';
          return `<div class="pg-row geo-item-row">
            <div style="display:flex;align-items:center;gap:8px;flex:1;min-width:0">
              <span class="geo-dot available"></span>
              <div>
                <div class="pg-lbl">${esc(g.name)}</div>
                ${sizeMb ? `<div class="pg-sub">${sizeMb}</div>` : ''}
              </div>
            </div>
            <span class="geo-badge ok">✓ загружена</span>
          </div>`;
        }).join('') + (missing.length ? `<div class="geo-missing-block">
          <div class="pg-sub" style="margin-bottom:6px">Не загружены (${missing.length}):</div>
          <div style="display:flex;flex-wrap:wrap;gap:4px">${missing.map(g => `<span class="geo-chip missing">${esc(g.name)}</span>`).join('')}</div>
        </div>` : '');
      }
    }
  } catch(_) {}
  loadClipboardBanner();
  loadRoutingOptions();
  loadLANInfo();
  loadSingboxConfigStatus();
  loadTotalStats();
  loadConnectionHistory();
  loadCrashReports();
  loadTrafficBudget();
  checkFailoverStatus();
  loadClientUpdateStatus();
}

let _importSrvRunning = false;

async function importSrvUrl() {
  if (_importSrvRunning) return;
  _importSrvRunning = true;
  const addBtn = document.querySelector('.pg-btn.acc[onclick="importSrvUrl()"]');
  if (addBtn) addBtn.disabled = true;
  const url = $id('srvUrlInp').value.trim();
  if (!url) { showToast('Введите ссылку', 'warn'); _importSrvRunning = false; if (addBtn) addBtn.disabled = false; return; }
  if (!isSupportedServerURI(url)) {
    showToast('Поддерживаются vless, trojan, ss, hysteria2, tuic, wireguard', 'warn');
    _importSrvRunning = false; if (addBtn) addBtn.disabled = false; return;
  }
  try {
    const r = await fetch(API + '/servers', {
      method:'POST',
      headers:{'Content-Type':'application/json'},
      body: JSON.stringify({url})
    });
    if (r.status === 409) {
      showToast('Сервер уже добавлен', 'warn');
      $id('srvUrlInp').value = '';
      return;
    }
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json();
    if (d.server) {
      state.servers = [...state.servers, d.server];
      renderServerList();
    }
    $id('srvUrlInp').value = '';
    showToast('Сервер добавлен', 'on');
    loadServers();
  } catch(e) { showToast('Ошибка: ' + e.message, 'off'); }
  finally { _importSrvRunning = false; if (addBtn) addBtn.disabled = false; }
}

async function importClipboard() {
  let text = '';
  try {
    text = await navigator.clipboard.readText();
  } catch (_) {
    try {
      const r = await fetch(API + '/clipboard/vless');
      const d = await r.json();
      if (d.found && d.url) text = d.url;
    } catch(_) {}
    if (!text) {
      showToast('Нет доступа к буферу обмена', 'off');
      return;
    }
  }
  const protos = ['vless://', 'trojan://', 'ss://', 'hysteria2://', 'hy2://', 'tuic://', 'wireguard://'];
  const lines = text.split(/\r?\n/).map(l => l.trim())
    .filter(l => protos.some(p => l.startsWith(p)));
  if (!lines.length) { showToast('Server URI не найдены', 'warn'); return; }
  let added = 0, skipped = 0;
  for (const url of lines) {
    try {
      const r = await fetch(API + '/servers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url })
      });
      if (r.ok) added++;
      else if (r.status === 409) skipped++;
    } catch (_) {}
  }
  showToast(`Добавлено: ${added}, пропущено: ${skipped}`, added ? 'on' : 'warn');
  if (added > 0) loadServers();
}

async function autoConnect() {
  try {
    const r = await fetch(API + '/servers/auto-connect', {method:'POST'});
    if (!r.ok) throw new Error();
    const d = await r.json();
    if (d.connected_id) {
      state.activeId = d.connected_id;
      updateServerPill();
    }
    const msg = d.changed
      ? `⚡ Переключено: ${d.latency_ms}ms`
      : `⚡ Оптимальный сервер уже активен`;
    showToast(msg, 'on');
    setTimeout(pollStatus, 1500);
  } catch(_) { showToast('Ошибка автоподключения', 'off'); }
}

async function downloadEngine() {
  const btn = $id('engDlBtn');
  btn.disabled = true; btn.textContent = '...';
  try {
    const r = await fetch(API + '/engine/download', {method:'POST'});
    if (!r.ok) throw new Error();
    showToast('Загрузка движка запущена', 'info');
  } catch(_) { showToast('Ошибка загрузки', 'off'); }
  finally { btn.disabled = false; btn.textContent = '↓ Скачать'; }
}

async function downloadGeosite() {
  const btn = $id('geoUpdateBtn') || document.querySelector('[onclick="downloadGeosite()"]');
  if (btn) { btn.disabled = true; btn.classList.add('geo-downloading'); btn.textContent = '↓ Загрузка...'; }
  OpTimer.start('geosite', 'Обновление баз geosite...');
  try {
    OpTimer.update('geosite', 'Загрузка geosite из правил...');
    const r = await fetch(API + '/geosite/download', { method: 'POST' });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);

    const requested = (d.requested || []).length;
    const updated = (d.updated || []).length;
    const errors = d.errors || [];
    if (!requested) {
      showToast('В правилах нет geosite-баз', 'info');
      OpTimer.done('geosite', 'Нет geosite-правил');
      return;
    }
    if (errors.length) {
      const msg = `Обновлено ${updated}, ошибок: ${errors.length}`;
      showToast(msg, 'warn');
      OpTimer.fail('geosite', msg);
    } else {
      showToast(`Обновлено ${updated} баз ✓`, 'on');
      OpTimer.done('geosite', `Обновлено ${updated} баз`);
    }
    if (d.apply_error) showToast('Базы обновлены, применение поставлено в очередь', 'info');
    setTimeout(loadSettingsPage, 1000);
  } catch(e) {
    showToast('✗ Ошибка: ' + e.message, 'off');
    OpTimer.fail('geosite', 'Ошибка загрузки баз');
  } finally {
    if (btn) { btn.disabled = false; btn.classList.remove('geo-downloading'); btn.textContent = 'Обновить базы Geosite'; }
  }
}

function downloadBackup() {
  window.location.href = API + '/backup';
}

async function restoreBackup(input) {
  const file = input.files[0];
  if (!file) return;
  const fd = new FormData();
  fd.append('file', file);
  fd.append('overwrite', 'true');
  OpTimer.start('restore', 'Восстановление конфигурации...', 10000);
  try {
    const r = await fetch(API + '/backup/restore', {method:'POST', body: fd});
    if (!r.ok) throw new Error(await r.text());
    showToast('Конфигурация восстановлена', 'on');
    OpTimer.done('restore', 'Конфигурация восстановлена');
  } catch(e) {
    showToast('Ошибка импорта: ' + e.message, 'off');
    OpTimer.fail('restore', 'Ошибка восстановления');
  }
  input.value = '';
}

async function quitApp() {
  if (!confirm('Завершить работу SafeSky?')) return;
  try { await fetch(API + '/quit', {method:'POST'}); } catch(_) {}
  if (window.windowClose) windowClose();
}

const THEMES = {
  dark: {
    '--bg':'#0d0d12','--s1':'#111218','--s2':'#15161e','--s3':'#1a1b26',
    '--g0':'rgba(255,255,255,0.055)','--g1':'rgba(255,255,255,0.09)','--g2':'rgba(255,255,255,0.14)',
    '--on':'#2de89a','--on2':'rgba(45,232,154,0.18)','--on3':'rgba(45,232,154,0.08)',
    '--acc':'#38c8ff','--acc2':'rgba(56,200,255,0.16)','--acc3':'rgba(56,200,255,0.08)',
    '--off':'#ff5074','--off2':'rgba(255,80,116,0.16)',
    '--warn':'#ffbb2e','--text':'#eaf2ff',
    '--dim':'rgba(221,232,255,0.72)','--muted':'rgba(221,232,255,0.52)',
    '--hairline':'rgba(255,255,255,0.10)','--hairline2':'rgba(255,255,255,0.18)',
    '--grid':'rgba(255,255,255,0.02)',
    '--body-g1':'rgba(56,200,255,0.04)','--body-g2':'rgba(45,232,154,0.03)',
  },
  light: {
    '--bg':'#f5f7fb','--s1':'#edf0f7','--s2':'#e4e9f4','--s3':'#d7deee',
    '--g0':'rgba(0,20,60,0.04)','--g1':'rgba(0,20,60,0.07)','--g2':'rgba(0,20,60,0.13)',
    '--on':'#16a34a','--on2':'rgba(22,163,74,0.13)','--on3':'rgba(22,163,74,0.07)',
    '--acc':'#2563eb','--acc2':'rgba(37,99,235,0.13)','--acc3':'rgba(37,99,235,0.07)',
    '--off':'#dc2626','--off2':'rgba(220,38,38,0.11)',
    '--warn':'#b45309','--text':'#0f172a',
    '--dim':'rgba(15,23,42,0.82)','--muted':'rgba(15,23,42,0.52)',
    '--hairline':'rgba(0,0,0,0.09)','--hairline2':'rgba(0,0,0,0.16)',
    '--grid':'rgba(0,0,0,0.018)',
    '--body-g1':'rgba(37,99,235,0.04)','--body-g2':'rgba(22,163,74,0.03)',
  },
};

function _applyThemeVars(theme) {
  const vars = THEMES[theme] || THEMES.dark;
  const root = document.documentElement;
  Object.entries(vars).forEach(([k, v]) => root.style.setProperty(k, v));
  root.setAttribute('data-theme', theme);
}

let currentTheme = localStorage.getItem('safesky-theme') || 'dark';
// Migrate old theme names to supported themes
if (!THEMES[currentTheme]) { currentTheme = 'dark'; localStorage.setItem('safesky-theme', 'dark'); }

// ── Apply timing history — предсказание времени по прошлым запускам ────────
// Хранит последние MAX реальных длительностей apply (мс) в localStorage.
// estimate() возвращает взвешенное среднее (новые → бо́льший вес) + 15% буфер.
const _applyHistory = (function() {
  const KEY = 'safesky-apply-timings', MAX = 8;
  function get() {
    try { return JSON.parse(localStorage.getItem(KEY) || '[]'); } catch(_) { return []; }
  }
  function push(ms) {
    // Игнорируем выбросы: hot-reload < 2с, полный рестарт < 5 мин
    if (ms < 800 || ms > 360000) return;
    const arr = get();
    arr.push(Math.round(ms));
    if (arr.length > MAX) arr.splice(0, arr.length - MAX);
    try { localStorage.setItem(KEY, JSON.stringify(arr)); } catch(_) {}
  }
  function estimate(fallbackMs) {
    const arr = get().filter(v => v > 800);
    if (arr.length < 2) return fallbackMs || 0;
    // Взвешенное среднее: arr[0] — самый старый (вес 1), arr[last] — самый новый (вес MAX)
    let sum = 0, w = 0;
    arr.forEach((v, i) => { const wi = i + 1; sum += v * wi; w += wi; });
    // +15% буфер чтобы таймер не истекал раньше реального завершения
    return Math.round((sum / w) * 1.15);
  }
  return { push, estimate };
})();

function applyTheme(chip) {
  const theme = chip.dataset.theme;
  currentTheme = theme;
  localStorage.setItem('safesky-theme', theme);
  document.querySelectorAll('.theme-chip').forEach(c => c.classList.toggle('active', c.dataset.theme === theme));
  _applyThemeVars(theme);
  if (window.setTitleTheme) setTitleTheme(theme);
}
// Применяем сохранённую тему при старте
(function() {
  let saved = localStorage.getItem('safesky-theme') || 'dark';
  if (!THEMES[saved]) { saved = 'dark'; localStorage.setItem('safesky-theme', 'dark'); }
  _applyThemeVars(saved);
  if (saved && window.setTitleTheme) { setTitleTheme(saved); }
  document.querySelectorAll('.theme-chip').forEach(c => c.classList.toggle('active', c.dataset.theme === saved));
})();


// ═══════════════════════════════════════════════════
