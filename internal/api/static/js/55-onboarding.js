const onboarding = {
  step: 'welcome',
  source: 'subscription',
  busy: false,
  message: '',
  test: null
};

function onboardingShow(step) {
  onboarding.step = step;
  onboarding.message = '';
  if (step !== 'test') onboarding.test = null;
  renderOnboarding();
}

function onboardingOption(id, title, sub) {
  const active = onboarding.source === id ? ' active' : '';
  return `<button class="onboarding-option${active}" onclick="onboarding.source='${esc(id)}';renderOnboarding()">
    <span class="onboarding-dot"></span>
    <span><b>${esc(title)}</b><br><span class="pg-sub">${esc(sub)}</span></span>
  </button>`;
}

function onboardingActions(primary, primaryFn, backStep) {
  const back = backStep ? `<button class="pg-btn" onclick="onboardingShow('${esc(backStep)}')">Назад</button>` : '<span></span>';
  return `<div class="onboarding-actions">
    ${back}
    <div style="display:flex;gap:8px;flex-wrap:wrap;justify-content:flex-end">
      <button class="pg-btn" onclick="onboardingSkip()">Пропустить</button>
      <button class="pg-btn acc" onclick="${esc(primaryFn)}" ${onboarding.busy ? 'disabled' : ''}>${esc(primary)}</button>
    </div>
  </div>`;
}

function renderOnboarding() {
  const root = $id('onboardingOv');
  const body = $id('onboardingBody');
  if (!root || !body) return;
  root.style.display = 'flex';
  document.body.classList.add('onboarding-open');
  const msg = onboarding.message ? `<div class="onboarding-result">${esc(onboarding.message)}</div>` : '<div class="onboarding-result"></div>';
  if (onboarding.step === 'welcome') {
    body.innerHTML = `<div class="onboarding-kicker">Первый запуск</div>
      <div class="onboarding-title">Добро пожаловать</div>
      <div class="onboarding-copy">SafeSky поможет быстро добавить доступ к серверу и проверить подключение. Настройка займёт около минуты.</div>
      ${onboardingActions('Начать', "onboardingShow('source')", '')}`;
    return;
  }
  if (onboarding.step === 'source') {
    body.innerHTML = `<div class="onboarding-kicker">Источник конфигурации</div>
      <div class="onboarding-title">Откуда вы получили доступ?</div>
      <div class="onboarding-options">
        ${onboardingOption('subscription', 'Подписка (URL)', 'Добавит и обновит список серверов')}
        ${onboardingOption('key', 'Один ключ', 'vless://, trojan://, ss://, hysteria2://, tuic://, vmess://')}
        ${onboardingOption('wireguard', 'WireGuard config', '.conf или wireguard://')}
        ${onboardingOption('none', 'У меня пока ничего нет', 'Открыть приложение без настройки')}
      </div>
      ${onboardingActions('Далее', 'onboardingNextSource()', 'welcome')}`;
    return;
  }
  if (onboarding.step === 'subscription') {
    body.innerHTML = `<div class="onboarding-kicker">Подписка</div>
      <div class="onboarding-title">Вставьте URL подписки</div>
      <input class="pg-inp" id="onboardingSubUrl" type="url" placeholder="https://example.com/sub">
      ${msg}
      ${onboardingActions('Загрузить', 'onboardingImportSubscription()', 'source')}`;
    return;
  }
  if (onboarding.step === 'key') {
    body.innerHTML = `<div class="onboarding-kicker">Ключ сервера</div>
      <div class="onboarding-title">Вставьте server URI</div>
      <textarea class="pg-inp onboarding-textarea" id="onboardingKeyText" spellcheck="false" placeholder="vless://..."></textarea>
      ${msg}
      ${onboardingActions('Добавить', 'onboardingImportKey()', 'source')}`;
    return;
  }
  if (onboarding.step === 'wireguard') {
    body.innerHTML = `<div class="onboarding-kicker">WireGuard</div>
      <div class="onboarding-title">Вставьте WireGuard config</div>
      <textarea class="pg-inp onboarding-textarea" id="onboardingWGText" spellcheck="false" placeholder="[Interface]&#10;PrivateKey = ..."></textarea>
      ${msg}
      ${onboardingActions('Продолжить', 'onboardingWGNotReady()', 'source')}`;
    return;
  }
  if (onboarding.step === 'test') {
    const test = onboarding.test || {};
    const rows = [
      ['server', 'Сервер доступен', test.server],
      ['handshake', 'Handshake успешен', test.handshake],
      ['dns', 'DNS защищён', test.dns],
      ['ipv6', 'IPv6 проверка', test.ipv6]
    ].map(([id, label, value]) => {
      const ok = value === true;
      const warn = value === 'warn';
      const mark = ok ? '✓' : warn ? '⚠' : '...';
      return `<div class="onboarding-check ${ok ? 'ok' : warn ? 'warn' : ''}" data-check="${id}">
        <span>${mark}</span><span>${esc(label)}</span>
      </div>`;
    }).join('');
    body.innerHTML = `<div class="onboarding-kicker">Проверка подключения</div>
      <div class="onboarding-title">${onboarding.busy ? 'Проверяю подключение...' : 'Проверка завершена'}</div>
      <div class="onboarding-checks">${rows}</div>
      ${msg}
      <div class="onboarding-actions">
        <button class="pg-btn" onclick="onboardingShow('source')" ${onboarding.busy ? 'disabled' : ''}>Назад</button>
        <button class="pg-btn acc" onclick="onboardingFinishAfterTest()" ${onboarding.busy ? 'disabled' : ''}>Готово</button>
      </div>`;
    return;
  }
  body.innerHTML = `<div class="onboarding-kicker">Готово</div>
    <div class="onboarding-title">Всё готово</div>
    <div class="onboarding-copy">Иконка в трее даёт быстрый доступ. Ctrl+Alt+P включает и отключает подключение. Профили можно настроить в Settings.</div>
    <div class="onboarding-actions">
      <span></span>
      <button class="pg-btn acc" onclick="onboardingFinish()">Открыть приложение</button>
    </div>`;
}

function onboardingNextSource() {
  if (onboarding.source === 'none') {
    onboardingSkip();
    return;
  }
  onboardingShow(onboarding.source);
}

async function onboardingComplete() {
  await fetch(API + '/onboarding/complete', { method: 'POST' });
}

async function onboardingSkip() {
  await fetch(API + '/onboarding/skip', { method: 'POST' }).catch(() => {});
  onboardingFinish();
}

function onboardingFinish() {
  const root = $id('onboardingOv');
  if (root) root.style.display = 'none';
  document.body.classList.remove('onboarding-open');
}

async function onboardingImportSubscription() {
  const url = ($id('onboardingSubUrl')?.value || '').trim();
  if (!/^https:\/\//i.test(url)) {
    onboarding.message = 'Subscription URL должен начинаться с https://';
    renderOnboarding();
    return;
  }
  onboarding.busy = true;
  onboarding.message = 'Загружаю подписку...';
  renderOnboarding();
  try {
    const r = await fetch(API + '/subscriptions', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({name: 'Default subscription', url, update_every: '24h'}),
      timeoutMs: 45000
    });
    const d = await r.json().catch(() => ({}));
    if (!r.ok && r.status !== 202) throw new Error(d.error || await r.text());
    const added = d.result && Number.isFinite(Number(d.result.added)) ? Number(d.result.added) : 0;
    onboarding.message = added ? `Найдено серверов: ${added}` : (d.error || 'Подписка сохранена, серверы не найдены');
    await onboardingRunTest();
  } catch(e) {
    onboarding.message = 'Не удалось загрузить подписку: ' + e.message;
  } finally {
    onboarding.busy = false;
    renderOnboarding();
  }
}

async function onboardingImportKey() {
  const url = ($id('onboardingKeyText')?.value || '').trim();
  if (!isSupportedServerURI(url)) {
    onboarding.message = 'Поддерживаются vless, trojan, ss, hysteria2, tuic, wireguard, vmess';
    renderOnboarding();
    return;
  }
  onboarding.busy = true;
  onboarding.message = 'Добавляю сервер...';
  renderOnboarding();
  try {
    const r = await fetch(API + '/servers', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({name: 'Default', url})
    });
    if (!r.ok && r.status !== 409) throw new Error(await r.text());
    await onboardingRunTest();
    loadServers?.();
  } catch(e) {
    onboarding.message = 'Не удалось добавить ключ: ' + e.message;
  } finally {
    onboarding.busy = false;
    renderOnboarding();
  }
}

function onboardingWGNotReady() {
  onboarding.message = 'Импорт WireGuard .conf будет добавлен отдельным шагом. Можно пропустить и импортировать позже.';
  renderOnboarding();
}

async function onboardingRunTest() {
  onboarding.step = 'test';
  onboarding.busy = true;
  onboarding.test = { server: false, handshake: false, dns: false, ipv6: 'warn' };
  onboarding.message = 'Подключаюсь к лучшему серверу...';
  renderOnboarding();
  try {
    const connect = await fetch(API + '/servers/auto-connect', { method: 'POST', timeoutMs: 45000 });
    if (!connect.ok) throw new Error(await connect.text());
    onboarding.test.server = true;
    onboarding.test.handshake = true;
    onboarding.message = 'Проверяю внешний IP и DNS...';
    renderOnboarding();
    await new Promise(resolve => setTimeout(resolve, 1500));
    const diag = await fetch(API + '/diagnostics/test', { timeoutMs: 30000 });
    const d = await diag.json().catch(() => ({}));
    if (!diag.ok || d.ok === false) throw new Error(d.error || 'diagnostics failed');
    onboarding.test.dns = !d.dns_leak;
    onboarding.test.ipv6 = d.vpn_works ? true : 'warn';
    onboarding.message = `Подключено${d.latency_ms ? `, ${d.latency_ms}ms` : ''}`;
  } catch(e) {
    onboarding.message = 'Импорт выполнен, но проверка подключения не прошла: ' + e.message;
    onboarding.test.dns = 'warn';
    onboarding.test.ipv6 = 'warn';
  } finally {
    await onboardingComplete().catch(() => {});
    onboarding.busy = false;
    renderOnboarding();
    pollStatus?.();
    loadServers?.();
  }
}

function onboardingFinishAfterTest() {
  onboardingShow('done');
}

function restartOnboarding() {
  onboarding.step = 'welcome';
  onboarding.source = 'subscription';
  onboarding.busy = false;
  onboarding.message = '';
  onboarding.test = null;
  renderOnboarding();
}

async function initOnboarding() {
  try {
    const r = await fetch(API + '/onboarding/status');
    if (!r.ok) return;
    const d = await r.json();
    if (!d.onboarded) renderOnboarding();
  } catch(_) {}
}
