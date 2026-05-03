const onboarding = {
  step: 'welcome',
  source: 'subscription',
  busy: false,
  message: ''
};

function onboardingShow(step) {
  onboarding.step = step;
  onboarding.message = '';
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
    await onboardingComplete();
    onboardingShow('done');
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
    await onboardingComplete();
    onboardingShow('done');
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

async function initOnboarding() {
  try {
    const r = await fetch(API + '/onboarding/status');
    if (!r.ok) return;
    const d = await r.json();
    if (!d.onboarded) renderOnboarding();
  } catch(_) {}
}
