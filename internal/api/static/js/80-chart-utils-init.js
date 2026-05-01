// CANVAS SPEED CHART
// ═══════════════════════════════════════════════════
const N = 90;
let upBuf = new Array(N).fill(0);
let dnBuf = new Array(N).fill(0);
let chartMax = 1; // динамический максимум для нормализации
let chartPeakBps = 0;

function cleanBps(value) {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

function updateChartPeak(bps) {
  const el = $id('chartPeak');
  if (!el) return;
  const peak = fmtSpeed(cleanBps(bps));
  el.textContent = 'пик ' + peak.val + ' ' + peak.unit;
}

function pushChartData(upBps, dnBps) {
  // FIX #2: данные из API, а не рандом
  // FIX #2: если proxy выключен — показываем 0
  const isOn = state.running && state.enabled;
  const up = isOn ? cleanBps(upBps) : 0;
  const dn = isOn ? cleanBps(dnBps) : 0;

  upBuf.push(up); upBuf.shift();
  dnBuf.push(dn); dnBuf.shift();

  // Обновляем динамический максимум с небольшим запасом, чтобы пики не упирались в край.
  const cleanUp = upBuf.map(cleanBps);
  const cleanDn = dnBuf.map(cleanBps);
  chartPeakBps = Math.max(...cleanUp, ...cleanDn, 0);
  const localMax = Math.max(chartPeakBps, 1024);
  const targetMax = localMax * 1.12;
  if (!Number.isFinite(chartMax) || chartMax < 1024) chartMax = 1024;
  chartMax = targetMax > chartMax
    ? targetMax
    : chartMax * 0.97 + targetMax * 0.03;
  updateChartPeak(chartPeakBps);

  // Обновляем числовые лейблы
  const { val: upVal, unit: upUnit } = fmtSpeed(up);
  const { val: dnVal, unit: dnUnit } = fmtSpeed(dn);
  $id('upv').textContent = upVal;
  $id('dnv').textContent = dnVal;
  $id('upUnit').textContent = upUnit;
  $id('dnUnit').textContent = dnUnit;
}

(function initChart() {
  const c = $id('sc');
  const ctx = c.getContext('2d');
  let canvasW = 1;
  let canvasH = 1;
  let canvasDpr = 1;

  function resize() {
    const r = c.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) return false; // защита от нулевого размера

    const dpr = Math.max(1, Math.min(3, window.devicePixelRatio || 1));
    const cssW = Math.max(1, r.width);
    const cssH = Math.max(1, r.height);
    const pxW = Math.max(1, Math.ceil(cssW * dpr));
    const pxH = Math.max(1, Math.ceil(cssH * dpr));

    if (
      c.width === pxW &&
      c.height === pxH &&
      Math.abs(canvasW - cssW) < 0.05 &&
      Math.abs(canvasH - cssH) < 0.05 &&
      Math.abs(canvasDpr - dpr) < 0.001
    ) {
      return false;
    }

    c.width = pxW;
    c.height = pxH;
    canvasW = cssW;
    canvasH = cssH;
    canvasDpr = dpr;
    ctx.setTransform(pxW / cssW, 0, 0, pxH / cssH, 0, 0);
    ctx.imageSmoothingEnabled = true;
    ctx.imageSmoothingQuality = 'high';
    return true;
  }

  let mouseX = -1;
  let mouseY = -1;
  const tooltip = $id('chartTooltip');

  function themeName() {
    return document.documentElement.getAttribute('data-theme')
      || (typeof currentTheme === 'string' ? currentTheme : 'dark');
  }

  function normArr(buf) {
    const max = Math.max(chartMax || 1, 1024);
    return buf.map(v => Math.max(0, Math.min(1, cleanBps(v) / max)));
  }

  function drawPath(points) {
    ctx.beginPath();
    if (!points.length) return;
    ctx.moveTo(points[0].x, points[0].y);
    for (let i = 1; i < points.length; i++) {
      ctx.lineTo(points[i].x, points[i].y);
    }
  }

  function drawSeries(data, cfg, plot) {
    const step = plot.w / (N - 1);
    const points = data.map((v, i) => ({
      x: plot.left + i * step,
      y: plot.bottom - v * plot.h
    }));

    ctx.save();
    ctx.shadowColor = cfg.glow;
    ctx.shadowBlur = cfg.glowBlur;
    drawPath(points);
    ctx.strokeStyle = cfg.strokeSoft;
    ctx.lineWidth = 3.2;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.stroke();
    ctx.restore();

    drawPath(points);
    ctx.strokeStyle = cfg.stroke;
    ctx.lineWidth = 2.35;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.stroke();

    const last = points[points.length - 1];
    if (last && data[data.length - 1] > 0.01) {
      ctx.beginPath();
      ctx.arc(last.x, last.y, 3.2, 0, Math.PI * 2);
      ctx.fillStyle = cfg.dot;
      ctx.fill();
      ctx.lineWidth = 1.5;
      ctx.strokeStyle = cfg.dotRing;
      ctx.stroke();
    }
  }

  function draw() {
    const W = canvasW;
    const H = canvasH;
    if (W <= 1 || H <= 1) return;
    ctx.clearRect(0, 0, W, H);

    const isLight = themeName() === 'light';
    const plot = {
      left: 0,
      right: W,
      top: 14,
      bottom: Math.max(24, H - 5)
    };
    plot.w = Math.max(1, plot.right - plot.left);
    plot.h = Math.max(1, plot.bottom - plot.top);

    const normUp = normArr(upBuf);
    const normDn = normArr(dnBuf);
    const upColor = isLight ? '#2f6f9f' : '#a8c8dc';
    const upSoft = isLight ? 'rgba(47,111,159,0.30)' : 'rgba(168,200,220,0.30)';
    const dnColor = isLight ? '#8a6a2f' : '#e1bd7d';
    const dnSoft = isLight ? 'rgba(138,106,47,0.28)' : 'rgba(225,189,125,0.32)';

    drawSeries(normDn, {
      stroke: dnColor,
      strokeSoft: dnSoft,
      glow: isLight ? 'rgba(138,106,47,0.18)' : 'rgba(225,189,125,0.22)',
      glowBlur: isLight ? 1.5 : 2.5,
      dot: dnColor,
      dotRing: 'rgba(255,255,255,0.88)'
    }, plot);

    drawSeries(normUp, {
      stroke: upColor,
      strokeSoft: upSoft,
      glow: isLight ? 'rgba(47,111,159,0.18)' : 'rgba(168,200,220,0.22)',
      glowBlur: isLight ? 1.5 : 2.5,
      dot: upColor,
      dotRing: 'rgba(255,255,255,0.88)'
    }, plot);

    // Hover crosshair + tooltip
    if (mouseX >= 0) {
      const step = plot.w / (N - 1);
      const idx = Math.min(N - 1, Math.max(0, Math.round((mouseX - plot.left) / step)));
      // Vertical crosshair line
      ctx.strokeStyle = isLight ? 'rgba(255,255,255,0.34)' : 'rgba(255,255,255,0.22)';
      ctx.lineWidth = 1;
      ctx.setLineDash([3, 3]);
      const x = plot.left + idx * step;
      ctx.beginPath(); ctx.moveTo(x, plot.top); ctx.lineTo(x, plot.bottom); ctx.stroke();
      ctx.setLineDash([]);
      // Tooltip content
      const up = cleanBps(upBuf[idx]), dn = cleanBps(dnBuf[idx]);
      const secsAgo = (N - 1 - idx);
      const timeLabel = secsAgo === 0 ? '0с назад' : secsAgo + 'с назад';
      const { val: upV, unit: upU } = fmtSpeed(up);
      const { val: dnV, unit: dnU } = fmtSpeed(dn);
      const rect = c.getBoundingClientRect();
      tooltip.innerHTML = `
        <div class="ctt-row"><span class="ctt-dot" style="background:${upColor};box-shadow:0 0 3px ${upSoft}"></span><span style="color:${upColor}">исходящий ${upV} ${upU}</span></div>
        <div class="ctt-row"><span class="ctt-dot" style="background:${dnColor};box-shadow:0 0 3px ${dnSoft}"></span><span style="color:${dnColor}">входящий ${dnV} ${dnU}</span></div>
        <div style="margin-top:3px;opacity:0.55;font-size:8px">${timeLabel}</div>`;
      let tx = mouseX + 12;
      const tw = 110;
      if (tx + tw > rect.width) tx = mouseX - tw - 12;
      tooltip.style.left = tx + 'px';
      tooltip.style.top = Math.max(0, mouseY - 30) + 'px';
    }
  }

  // FIX #3: resize только когда размер реально известен
  const chartResizeObserver = new ResizeObserver(() => {
    requestAnimationFrame(() => { resize(); draw(); });
  });
  chartResizeObserver.observe(c);
  if (c.parentElement) chartResizeObserver.observe(c.parentElement);
  window.addEventListener('resize', () => {
    requestAnimationFrame(() => { resize(); draw(); });
  }, { passive: true });

  c.addEventListener('mousemove', e => {
    const rect = c.getBoundingClientRect();
    mouseX = e.clientX - rect.left;
    mouseY = e.clientY - rect.top;
    tooltip.style.display = 'block';
  });
  c.addEventListener('mouseleave', () => {
    mouseX = -1; mouseY = -1;
    tooltip.style.display = 'none';
  });

  // FIX #3: первый resize тоже через rAF
  requestAnimationFrame(() => {
    resize();
    draw();
    // Цикл рисования — только draw(), данные приходят из pollStats()
    function frame() {
      if (Math.abs((window.devicePixelRatio || 1) - canvasDpr) > 0.001) resize();
      draw();
      requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
  });
})();

// ═══════════════════════════════════════════════════
// UTILS
// ═══════════════════════════════════════════════════
function fmtBytes(b) {
  if (b < 1024)          return b + ' B';
  if (b < 1024 * 1024)   return (b / 1024).toFixed(1) + ' KB';
  if (b < 1024**3)       return (b / 1024 / 1024).toFixed(1) + ' MB';
  return (b / 1024**3).toFixed(2) + ' GB';
}

function fmtSpeed(bps) {
  if (bps < 1024)        return { val: bps.toFixed(0), unit: 'B/s' };
  if (bps < 1024 * 1024) return { val: (bps / 1024).toFixed(1), unit: 'KB/s' };
  return { val: (bps / 1024 / 1024).toFixed(1), unit: 'MB/s' };
}

function basename(p) {
  return p ? p.replace(/.*[/\\]/, '') : '';
}

function esc(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function jsArg(s) {
  return esc(JSON.stringify(String(s)));
}

function protocolFromURL(url) {
  if (!url) return '—';
  try {
    const u = new URL(url);
    const params = new URLSearchParams(u.search);
    const type   = params.get('type') || 'tcp';
    const sec    = params.get('security') || '';
    return `${u.protocol.replace(':','')} · ${type}${sec ? ' · ' + sec : ''}`;
  } catch (_) {
    return url.split('://')[0] || '—';
  }
}

// countryFlag возвращает <img> с флагом страны через flagcdn.com.
// При ошибке загрузки (нет сети / неизвестный код) — заменяем на CC-бейдж.
function countryFlag(code) {
  if (!code || !/^[a-z]{2}$/i.test(code)) return FLAG_GLOBE;
  const lc = code.toLowerCase();
  const uc = code.toUpperCase();
  return `<img src="https://flagcdn.com/w20/${lc}.png" class="flag-img" alt="${uc}" title="${uc}" `
    + `onerror="this.outerHTML='<span class=\\'cc-tag\\'>${uc}</span>'">`;
}
// Глобус-фолбэк как HTML
const FLAG_GLOBE = '<span class="flag-placeholder" title="Страна определяется"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8"/><path d="M4 12h16"/><path d="M12 4c2 2.2 3 4.8 3 8s-1 5.8-3 8"/><path d="M12 4c-2 2.2-3 4.8-3 8s1 5.8 3 8"/></svg></span>';

// ═══════════════════════════════════════════════════
// INIT + POLLING LOOPS
// ═══════════════════════════════════════════════════
async function init() {
  try { history.scrollRestoration = 'manual'; } catch(_) {}
  $id('page0')?.scrollTo?.(0, 0);
  function updateNavH() {
    const nav = document.querySelector('.nav');
    if (nav) document.documentElement.style.setProperty('--nav-h', nav.offsetHeight + 'px');
  }
  updateNavH();
  new ResizeObserver(updateNavH).observe(document.querySelector('.nav'));
  for (let i = 0; i < PAGE_COUNT; i++) {
    const page = $id('page' + i);
    if (page) page.addEventListener('scroll', updateChromeScrollState, { passive: true });
  }
  updateChromeScrollState();

  // Проверяем нужен ли онбординг (нет sing-box или нет серверов)
  const needsSetup = await checkSetupRequired();
  if (needsSetup) {
    // Поллинг запускаем в фоне — при закрытии онбординга UI уже готов
    setInterval(pollStatus,      POLL_STATUS);
    setInterval(pollStats,       POLL_STATS);
    setInterval(pollConnections, POLL_CONNS);
    return;
  }

  // Первый опрос сразу
  await pollStatus();

  // Запускаем серверы чтобы знать activeId при старте
  try {
    const r = await fetch(API + '/servers');
    if (r.ok) {
      const d = await r.json();
      // API returns { servers: [...], active_id: "..." }
      const _prevSrv2 = new Map((state.servers || []).map(s => [s.id, s]));
      state.servers = (d && d.servers) || [];
      // Restore persisted country codes before rendering
      _restoreCountryCodeCache();
      // FIX 33: восстанавливаем country_code если "??" или пустой.
      state.servers.forEach(s => {
        const prev2 = _prevSrv2.get(s.id);
        if ((!s.country_code || s.country_code === '??') && prev2?.country_code && prev2.country_code !== '??')
          s.country_code = prev2.country_code;
      });
      if (d && d.active_id) state.activeId = d.active_id;
      updateServerPill();
    }
  } catch (_) {}

  // Polling loops
  setInterval(pollStatus,      POLL_STATUS);
  setInterval(pollStats,       POLL_STATS);
  setInterval(pollConnections, POLL_CONNS);

  // Paste fills the field only. Adding/applying a new VLESS key stays explicit.
  const srvInp = $id('srvUrlInp');
  if (srvInp) {
    srvInp.addEventListener('paste', e => {
      const text = (e.clipboardData || window.clipboardData).getData('text') || '';
      const protos = ['vless://'];
      const lines = text.split(/\r?\n/).map(l => l.trim()).filter(Boolean);
      const validLines = lines.filter(l => protos.some(p => l.startsWith(p)));
      if (!validLines.length) return;
      e.preventDefault();
      if (validLines.length > 1) {
        srvInp.value = validLines[0];
        showToast(`Найдено ${validLines.length} VLESS-ссылок. Нажмите «Из буфера» для массового импорта.`, 'info');
      } else {
        srvInp.value = validLines[0];
        showToast('VLESS-ссылка вставлена. Нажмите «Добавить» для применения.', 'info');
      }
    });
  }
  // Первые вызовы
  pollStats();
  pollConnections();
}

init();
