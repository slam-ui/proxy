const i18n = {
  locale: 'ru',
  language: 'system',
  messages: {}
};

function tr(key, vars) {
  let text = i18n.messages[key] || key;
  if (vars) {
    Object.keys(vars).forEach(name => {
      text = text.replaceAll('{' + name + '}', String(vars[name]));
    });
  }
  return text;
}

function applyI18n(root) {
  const scope = root || document;
  scope.querySelectorAll('[data-i18n]').forEach(el => {
    el.textContent = tr(el.dataset.i18n);
  });
  scope.querySelectorAll('[data-i18n-title]').forEach(el => {
    el.title = tr(el.dataset.i18nTitle);
  });
  scope.querySelectorAll('[data-i18n-aria-label]').forEach(el => {
    el.setAttribute('aria-label', tr(el.dataset.i18nAriaLabel));
  });
  scope.querySelectorAll('[data-i18n-placeholder]').forEach(el => {
    el.placeholder = tr(el.dataset.i18nPlaceholder);
  });
}

function i18nLocaleTag() {
  return i18n.locale === 'ru' ? 'ru-RU' : 'en-US';
}

function formatDate(value, options) {
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleDateString(i18nLocaleTag(), options);
}

function formatDateTime(value, options) {
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString(i18nLocaleTag(), options);
}

function formatTime(value, options) {
  const d = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString(i18nLocaleTag(), options);
}

function formatRelativeTime(value) {
  const ts = new Date(value).getTime();
  if (!ts || Number.isNaN(ts)) return tr('time.never');
  const sec = Math.max(1, Math.floor((Date.now() - ts) / 1000));
  const rtf = new Intl.RelativeTimeFormat(i18nLocaleTag(), {numeric:'auto'});
  if (sec < 90) return rtf.format(-sec, 'second');
  const min = Math.floor(sec / 60);
  if (min < 90) return rtf.format(-min, 'minute');
  const hours = Math.floor(min / 60);
  if (hours < 48) return rtf.format(-hours, 'hour');
  return rtf.format(-Math.floor(hours / 24), 'day');
}

async function loadI18n(locale) {
  const query = locale ? '?locale=' + encodeURIComponent(locale) : '';
  const r = await fetch(API + '/i18n/messages' + query);
  if (!r.ok) throw new Error(await r.text());
  const d = await r.json();
  i18n.locale = d.locale || locale || i18n.locale;
  i18n.messages = d.messages || {};
  document.documentElement.lang = i18n.locale;
  applyI18n();
}

async function initI18n() {
  try {
    const r = await fetch(API + '/settings');
    const d = await r.json();
    i18n.language = d.language || 'system';
    i18n.locale = d.effective_language || 'ru';
    if ($id('languageInp')) $id('languageInp').value = i18n.language;
  } catch (_) {
    i18n.language = 'system';
  }
  try {
    await loadI18n(i18n.locale);
  } catch (_) {
    applyI18n();
  }
}

async function changeLanguage(value) {
  const language = value === 'ru' || value === 'en' || value === 'system' ? value : 'system';
  try {
    const r = await fetch(API + '/settings', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({language})
    });
    if (!r.ok) throw new Error(await r.text());
    const d = await r.json();
    i18n.language = d.language || language;
    await loadI18n(d.effective_language || language);
    if ($id('languageInp')) $id('languageInp').value = i18n.language;
    showToast(tr('settings.language.saved'), 'on');
  } catch (e) {
    showToast(tr('settings.language.error', {error: e.message}), 'off');
  }
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initI18n, {once:true});
} else {
  initI18n();
}
