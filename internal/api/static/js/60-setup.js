// ONBOARDING / SETUP
// ═══════════════════════════════════════════════════
let _setupEngineTimer = null;

async function checkSetupRequired() {
  // 1. Проверяем наличие sing-box
  try {
    const r = await fetch(API + '/engine/version');
    if (r.ok) {
      const d = await r.json();
      if (!d.installed) {
        $id('setupOv').style.display = 'flex';
        await startEngineDownload();
        return true;
      }
    }
  } catch(_) {}

  // 2. Движок есть — проверяем наличие серверов
  try {
    const r = await fetch(API + '/servers');
    if (r.ok) {
      const d = await r.json();
      const srvs = (d && d.servers) || (Array.isArray(d) ? d : []);
      if (!srvs.length) {
        $id('setupOv').style.display = 'flex';
        _setupGoStep2();
        return true;
      }
    }
  } catch(_) {}

  return false;
}

async function startEngineDownload() {
  $id('setupErr').style.display = 'none';
  $id('setupRetryBtn').style.display = 'none';
  $id('setupBarFill').className = 'setup-bar-fill indet';
  $id('setupStage').textContent = 'Запуск загрузки...';
  $id('setupStage').classList.remove('ok');
  try {
    const r = await fetch(API + '/engine/download', { method: 'POST' });
    if (!r.ok) throw new Error(await r.text());
    _pollEngineProgress();
  } catch(e) {
    _setupShowErr(e.message);
  }
}

function _pollEngineProgress() {
  clearInterval(_setupEngineTimer);
  OpTimer.start('engine', 'Загрузка sing-box...');
  _setupEngineTimer = setInterval(async () => {
    try {
      const r = await fetch(API + '/engine/status');
      if (!r.ok) return;
      const d = await r.json();
      const stage = (d.stage || '').toLowerCase();
      const msg   = d.message || stage || '...';
      const pct   = d.percent || 0;

      $id('setupStage').textContent = msg;
      if (pct > 0) OpTimer.update('engine', 'Загрузка sing-box: <b>' + pct + '%</b>');

      if (stage === 'done' || stage === 'complete' || (!d.running && d.installed)) {
        clearInterval(_setupEngineTimer);
        $id('setupBarFill').className = 'setup-bar-fill';
        $id('setupBarFill').style.width = '100%';
        $id('setupStage').textContent = '✓ Установлено';
        $id('setupStage').classList.add('ok');
        OpTimer.done('engine', 'sing-box установлен');
        setTimeout(_setupGoStep2, 700);
        return;
      }
      if (stage === 'error') {
        clearInterval(_setupEngineTimer);
        _setupShowErr(d.error || 'Ошибка загрузки');
        OpTimer.fail('engine', 'Ошибка загрузки sing-box');
        return;
      }
      // Если загрузка завершилась до начала опроса
      if (!d.running && !stage) {
        clearInterval(_setupEngineTimer);
        OpTimer.done('engine', 'sing-box установлен');
        _setupGoStep2();
        return;
      }
      if (pct > 0) {
        $id('setupBarFill').className = 'setup-bar-fill';
        $id('setupBarFill').style.width = pct + '%';
      }
    } catch(_) {}
  }, 400);
}

function _setupShowErr(msg) {
  const el = $id('setupErr');
  el.textContent = msg;
  el.style.display = 'block';
  $id('setupRetryBtn').style.display = 'block';
  $id('setupBarFill').className = 'setup-bar-fill';
  $id('setupBarFill').style.width = '0%';
  $id('setupStage').textContent = 'Загрузка не удалась';
}

function _setupGoStep2() {
  $id('setupStep1').style.display = 'none';
  $id('setupStep2').style.display = 'flex';
  $id('sdot0').className = 'setup-dot done';
  $id('sdot1').className = 'setup-dot cur';
  $id('setupVlessInp').focus();
}

async function setupPasteVless() {
  let text = '';
  try {
    text = await navigator.clipboard.readText();
  } catch (_) {
    try {
      const r = await fetch(API + '/clipboard/vless');
      const d = await r.json();
      if (d.found && d.url) text = d.url;
    } catch(_) {}
  }
  const match = String(text || '').split(/\s+/).find(v => v.startsWith('vless://'));
  if (!match) {
    $id('setupSrvErr').textContent = 'VLESS-ссылка в буфере не найдена';
    $id('setupSrvErr').style.display = 'block';
    return;
  }
  $id('setupSrvErr').style.display = 'none';
  $id('setupVlessInp').value = match;
  $id('setupVlessInp').focus();
}

async function setupSubmitServer() {
  const url = $id('setupVlessInp').value.trim();
  $id('setupSrvErr').style.display = 'none';
  if (!url) {
    $id('setupSrvErr').textContent = 'Введите VLESS-ссылку';
    $id('setupSrvErr').style.display = 'block';
    return;
  }
  const btn = $id('setupConnectBtn');
  btn.disabled = true;
  $id('setupSrvStage').textContent = 'Добавляем сервер...';
  try {
    const r = await fetch(API + '/servers', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ url })
    });
    if (!r.ok) throw new Error(await r.text());
    const srv = await r.json();

    $id('setupSrvStage').textContent = 'Подключаемся...';

    const srvId = srv.id || (srv.server && srv.server.id);
    if (srvId) {
      await fetch(`${API}/servers/${srvId}/connect`, { method: 'POST' });
    } else {
      await fetch(API + '/servers/auto-connect', { method: 'POST' });
    }

    $id('setupSrvStage').textContent = '✓ Готово!';
    $id('setupSrvStage').classList.add('ok');

    setTimeout(async () => {
      // Обновляем состояние и скрываем онбординг
      const ov = $id('setupOv');
      ov.classList.add('fade');
      await pollStatus();
      try {
        const d2 = await (await fetch(API + '/servers')).json();
        state.servers = (d2 && d2.servers) || d2 || [];
        if (d2 && d2.active_id) state.activeId = d2.active_id;
        updateServerPill();
      } catch(_) {}
      setTimeout(() => { ov.style.display = 'none'; ov.classList.remove('fade'); }, 400);
    }, 800);
  } catch(e) {
    $id('setupSrvErr').textContent = e.message;
    $id('setupSrvErr').style.display = 'block';
    $id('setupSrvStage').textContent = '';
    btn.disabled = false;
  }
}

// ═══════════════════════════════════════════════════
