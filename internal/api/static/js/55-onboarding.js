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
  const back = backStep ? `<button class="pg-btn" onclick="onboardingShow('${esc(backStep)}')">${esc(tr('onboarding.action.back'))}</button>` : '<span></span>';
  return `<div class="onboarding-actions">
    ${back}
    <div style="display:flex;gap:8px;flex-wrap:wrap;justify-content:flex-end">
      <button class="pg-btn" onclick="onboardingSkip()">${esc(tr('onboarding.action.skip'))}</button>
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
    body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.welcome.kicker'))}</div>
      <div class="onboarding-title">${esc(tr('onboarding.welcome.title'))}</div>
      <div class="onboarding-copy">${esc(tr('onboarding.welcome.copy'))}</div>
      ${onboardingActions(tr('onboarding.action.start'), "onboardingShow('source')", '')}`;
    return;
  }
  if (onboarding.step === 'source') {
    body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.source.kicker'))}</div>
      <div class="onboarding-title">${esc(tr('onboarding.source.title'))}</div>
      <div class="onboarding-options">
        ${onboardingOption('subscription', tr('onboarding.source.subscription'), tr('onboarding.source.subscription.sub'))}
        ${onboardingOption('key', tr('onboarding.source.key'), tr('onboarding.source.key.sub'))}
        ${onboardingOption('wireguard', tr('onboarding.source.wireguard'), tr('onboarding.source.wireguard.sub'))}
        ${onboardingOption('none', tr('onboarding.source.none'), tr('onboarding.source.none.sub'))}
      </div>
      ${onboardingActions(tr('onboarding.action.next'), 'onboardingNextSource()', 'welcome')}`;
    return;
  }
  if (onboarding.step === 'subscription') {
    body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.subscription.kicker'))}</div>
      <div class="onboarding-title">${esc(tr('onboarding.subscription.title'))}</div>
      <input class="pg-inp" id="onboardingSubUrl" type="url" placeholder="https://example.com/sub">
      ${msg}
      ${onboardingActions(tr('onboarding.action.load'), 'onboardingImportSubscription()', 'source')}`;
    return;
  }
  if (onboarding.step === 'key') {
    body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.key.kicker'))}</div>
      <div class="onboarding-title">${esc(tr('onboarding.key.title'))}</div>
      <textarea class="pg-inp onboarding-textarea" id="onboardingKeyText" spellcheck="false" placeholder="vless://..."></textarea>
      ${msg}
      ${onboardingActions(tr('onboarding.action.add'), 'onboardingImportKey()', 'source')}`;
    return;
  }
  if (onboarding.step === 'wireguard') {
    body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.wireguard.kicker'))}</div>
      <div class="onboarding-title">${esc(tr('onboarding.wireguard.title'))}</div>
      <textarea class="pg-inp onboarding-textarea" id="onboardingWGText" spellcheck="false" placeholder="[Interface]&#10;PrivateKey = ..."></textarea>
      ${msg}
      ${onboardingActions(tr('onboarding.action.continue'), 'onboardingWGNotReady()', 'source')}`;
    return;
  }
  if (onboarding.step === 'test') {
    const test = onboarding.test || {};
    const rows = [
      ['server', tr('onboarding.test.server'), test.server],
      ['handshake', tr('onboarding.test.handshake'), test.handshake],
      ['dns', tr('onboarding.test.dns'), test.dns],
      ['ipv6', tr('onboarding.test.ipv6'), test.ipv6]
    ].map(([id, label, value]) => {
      const ok = value === true;
      const warn = value === 'warn';
      const mark = ok ? '✓' : warn ? '⚠' : '...';
      return `<div class="onboarding-check ${ok ? 'ok' : warn ? 'warn' : ''}" data-check="${id}">
        <span>${mark}</span><span>${esc(label)}</span>
      </div>`;
    }).join('');
    body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.test.kicker'))}</div>
      <div class="onboarding-title">${esc(onboarding.busy ? tr('onboarding.test.running') : tr('onboarding.test.done'))}</div>
      <div class="onboarding-checks">${rows}</div>
      ${msg}
      <div class="onboarding-actions">
        <button class="pg-btn" onclick="onboardingShow('source')" ${onboarding.busy ? 'disabled' : ''}>${esc(tr('onboarding.action.back'))}</button>
        <button class="pg-btn acc" onclick="onboardingFinishAfterTest()" ${onboarding.busy ? 'disabled' : ''}>${esc(tr('onboarding.action.done'))}</button>
      </div>`;
    return;
  }
  body.innerHTML = `<div class="onboarding-kicker">${esc(tr('onboarding.done.kicker'))}</div>
    <div class="onboarding-title">${esc(tr('onboarding.done.title'))}</div>
    <div class="onboarding-copy">${esc(tr('onboarding.done.copy'))}</div>
    <div class="onboarding-actions">
      <span></span>
      <button class="pg-btn acc" onclick="onboardingFinish()">${esc(tr('onboarding.action.open'))}</button>
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
    onboarding.message = tr('onboarding.subscription.https_only');
    renderOnboarding();
    return;
  }
  onboarding.busy = true;
  onboarding.message = tr('onboarding.subscription.loading');
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
    onboarding.message = added ? tr('onboarding.subscription.added', {count: added}) : (d.error || tr('onboarding.subscription.none_found'));
    await onboardingRunTest();
  } catch(e) {
    onboarding.message = tr('onboarding.subscription.error', {error: e.message});
  } finally {
    onboarding.busy = false;
    renderOnboarding();
  }
}

async function onboardingImportKey() {
  const url = ($id('onboardingKeyText')?.value || '').trim();
  if (!isSupportedServerURI(url)) {
    onboarding.message = tr('onboarding.key.supported');
    renderOnboarding();
    return;
  }
  onboarding.busy = true;
  onboarding.message = tr('onboarding.key.adding');
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
    onboarding.message = tr('onboarding.key.error', {error: e.message});
  } finally {
    onboarding.busy = false;
    renderOnboarding();
  }
}

function onboardingWGNotReady() {
  onboarding.message = tr('onboarding.wireguard.not_ready');
  renderOnboarding();
}

async function onboardingRunTest() {
  onboarding.step = 'test';
  onboarding.busy = true;
  onboarding.test = { server: false, handshake: false, dns: false, ipv6: 'warn' };
  onboarding.message = tr('onboarding.test.connecting_best');
  renderOnboarding();
  try {
    const connect = await fetch(API + '/servers/auto-connect', { method: 'POST', timeoutMs: 45000 });
    if (!connect.ok) throw new Error(await connect.text());
    onboarding.test.server = true;
    onboarding.test.handshake = true;
    onboarding.message = tr('onboarding.test.checking_dns');
    renderOnboarding();
    await new Promise(resolve => setTimeout(resolve, 1500));
    const diag = await fetch(API + '/diagnostics/test', { timeoutMs: 30000 });
    const d = await diag.json().catch(() => ({}));
    if (!diag.ok || d.ok === false) throw new Error(d.error || 'diagnostics failed');
    onboarding.test.dns = !d.dns_leak;
    onboarding.test.ipv6 = d.vpn_works ? true : 'warn';
    onboarding.message = d.latency_ms ? tr('onboarding.test.connected_latency', {latency: d.latency_ms}) : tr('onboarding.test.connected');
  } catch(e) {
    onboarding.message = tr('onboarding.test.failed', {error: e.message});
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
