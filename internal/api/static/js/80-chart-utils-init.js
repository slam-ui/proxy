// CANVAS SPEED CHART
// ═══════════════════════════════════════════════════
const CHART_WINDOW_MS = 60_000;
const CHART_SAMPLE_TTL_MS = CHART_WINDOW_MS + 10_000;
let trafficSamples = [];
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

function pruneChartSamples(now) {
  const cutoff = now - CHART_SAMPLE_TTL_MS;
  while (trafficSamples.length && trafficSamples[0].t < cutoff) {
    trafficSamples.shift();
  }
}

function visibleChartSamples(now) {
  pruneChartSamples(now);
  const start = now - CHART_WINDOW_MS;
  const firstVisible = trafficSamples.findIndex(s => s.t >= start);
  if (firstVisible < 0) return [];
  const samples = firstVisible > 0
    ? trafficSamples.slice(firstVisible - 1)
    : trafficSamples.slice(firstVisible);
  const last = samples[samples.length - 1];
  if (last && last.t < now) {
    samples.push({ t: now, up: last.up, dn: last.dn });
  }
  return samples;
}

function pushChartData(upBps, dnBps) {
  // FIX #2: данные из API, а не рандом
  // FIX #2: если proxy выключен — показываем 0
  const isOn = state.running && state.enabled;
  const up = isOn ? cleanBps(upBps) : 0;
  const dn = isOn ? cleanBps(dnBps) : 0;
  const now = Date.now();

  trafficSamples.push({ t: now, up, dn });
  const samples = visibleChartSamples(now);

  // Обновляем динамический максимум с небольшим запасом, чтобы пики не упирались в край.
  chartPeakBps = samples.reduce((peak, sample) => Math.max(peak, cleanBps(sample.up), cleanBps(sample.dn)), 0);
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

  function normValue(value) {
    const max = Math.max(chartMax || 1, 1024);
    return Math.max(0, Math.min(1, cleanBps(value) / max));
  }

  function smoothPath(points) {
    ctx.beginPath();
    if (!points.length) return;
    ctx.moveTo(points[0].x, points[0].y);
    if (points.length === 1) return;
    for (let i = 0; i < points.length - 1; i++) {
      const p0 = points[Math.max(0, i - 1)];
      const p1 = points[i];
      const p2 = points[i + 1];
      const p3 = points[Math.min(points.length - 1, i + 2)];
      const cp1x = p1.x + (p2.x - p0.x) / 6;
      const cp1y = p1.y + (p2.y - p0.y) / 6;
      const cp2x = p2.x - (p3.x - p1.x) / 6;
      const cp2y = p2.y - (p3.y - p1.y) / 6;
      ctx.bezierCurveTo(cp1x, cp1y, cp2x, cp2y, p2.x, p2.y);
    }
  }

  function samplePoints(samples, key, plot, now) {
    const start = now - CHART_WINDOW_MS;
    const points = samples.map(sample => {
      const t = Math.max(start, Math.min(now, sample.t));
      return {
        x: plot.left + ((t - start) / CHART_WINDOW_MS) * plot.w,
        y: plot.bottom - normValue(sample[key]) * plot.h
      };
    });
    const compact = [];
    for (const point of points) {
      const prev = compact[compact.length - 1];
      if (!prev || Math.abs(prev.x - point.x) > 0.15 || Math.abs(prev.y - point.y) > 0.15) {
        compact.push(point);
      }
    }
    if (compact.length === 1) {
      const y = compact[0].y;
      return [{ x: plot.left, y }, { x: plot.right, y }];
    }
    return compact;
  }

  function drawSeries(points, cfg, plot) {
    if (points.length < 2) return;

    const gradient = ctx.createLinearGradient(0, plot.top, 0, plot.bottom);
    gradient.addColorStop(0, cfg.fillTop);
    gradient.addColorStop(1, cfg.fillBottom);

    smoothPath(points);
    ctx.lineTo(points[points.length - 1].x, plot.bottom);
    ctx.lineTo(points[0].x, plot.bottom);
    ctx.closePath();
    ctx.fillStyle = gradient;
    ctx.fill();

    ctx.save();
    ctx.shadowColor = cfg.glow;
    ctx.shadowBlur = cfg.glowBlur;
    smoothPath(points);
    ctx.strokeStyle = cfg.strokeSoft;
    ctx.lineWidth = 3.2;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.stroke();
    ctx.restore();

    smoothPath(points);
    ctx.strokeStyle = cfg.stroke;
    ctx.lineWidth = 2.35;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.stroke();

    const last = points[points.length - 1];
    if (last && last.y < plot.bottom - 1) {
      ctx.beginPath();
      ctx.arc(last.x, last.y, 3.2, 0, Math.PI * 2);
      ctx.fillStyle = cfg.dot;
      ctx.fill();
      ctx.lineWidth = 1.5;
      ctx.strokeStyle = cfg.dotRing;
      ctx.stroke();
    }
  }

  function sampleAtChartTime(samples, targetT) {
    if (!samples.length) return { up: 0, dn: 0 };
    if (targetT <= samples[0].t) return samples[0];
    const last = samples[samples.length - 1];
    if (targetT >= last.t) return last;

    for (let i = 1; i < samples.length; i++) {
      const prev = samples[i - 1];
      const next = samples[i];
      if (targetT <= next.t) {
        const span = Math.max(1, next.t - prev.t);
        const k = Math.max(0, Math.min(1, (targetT - prev.t) / span));
        return {
          up: prev.up + (next.up - prev.up) * k,
          dn: prev.dn + (next.dn - prev.dn) * k
        };
      }
    }
    return last;
  }

  function draw() {
    const now = Date.now();
    const W = canvasW;
    const H = canvasH;
    if (W <= 1 || H <= 1) return;
    ctx.clearRect(0, 0, W, H);

    const isLight = themeName() === 'light';
    const plot = {
      left: 0,
      right: W,
      top: 0,
      bottom: Math.max(1, H - 1)
    };
    plot.w = Math.max(1, plot.right - plot.left);
    plot.h = Math.max(1, plot.bottom - plot.top);

    const samples = visibleChartSamples(now);
    const upPoints = samplePoints(samples, 'up', plot, now);
    const dnPoints = samplePoints(samples, 'dn', plot, now);
    const upColor = isLight ? '#2f6f9f' : '#a8c8dc';
    const upSoft = isLight ? 'rgba(47,111,159,0.30)' : 'rgba(168,200,220,0.30)';
    const dnColor = isLight ? '#8a6a2f' : '#e1bd7d';
    const dnSoft = isLight ? 'rgba(138,106,47,0.28)' : 'rgba(225,189,125,0.32)';

    drawSeries(dnPoints, {
      stroke: dnColor,
      strokeSoft: dnSoft,
      fillTop: isLight ? 'rgba(138,106,47,0.12)' : 'rgba(225,189,125,0.16)',
      fillBottom: 'rgba(225,189,125,0)',
      glow: isLight ? 'rgba(138,106,47,0.18)' : 'rgba(225,189,125,0.22)',
      glowBlur: isLight ? 1.5 : 2.5,
      dot: dnColor,
      dotRing: 'rgba(255,255,255,0.88)'
    }, plot);

    drawSeries(upPoints, {
      stroke: upColor,
      strokeSoft: upSoft,
      fillTop: isLight ? 'rgba(47,111,159,0.12)' : 'rgba(168,200,220,0.14)',
      fillBottom: 'rgba(168,200,220,0)',
      glow: isLight ? 'rgba(47,111,159,0.18)' : 'rgba(168,200,220,0.22)',
      glowBlur: isLight ? 1.5 : 2.5,
      dot: upColor,
      dotRing: 'rgba(255,255,255,0.88)'
    }, plot);

    // Hover crosshair + tooltip
    if (mouseX >= 0) {
      const x = Math.max(plot.left, Math.min(plot.right, mouseX));
      const ratio = plot.w > 0 ? (x - plot.left) / plot.w : 1;
      const targetT = now - CHART_WINDOW_MS + ratio * CHART_WINDOW_MS;
      // Vertical crosshair line
      ctx.strokeStyle = isLight ? 'rgba(255,255,255,0.34)' : 'rgba(255,255,255,0.22)';
      ctx.lineWidth = 1;
      ctx.setLineDash([3, 3]);
      ctx.beginPath(); ctx.moveTo(x, plot.top); ctx.lineTo(x, plot.bottom); ctx.stroke();
      ctx.setLineDash([]);
      // Tooltip content
      const sample = sampleAtChartTime(samples, targetT);
      const up = cleanBps(sample.up), dn = cleanBps(sample.dn);
      const secsAgo = Math.max(0, Math.round((now - targetT) / 1000));
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
const FLAG_GLOBE = '<span class="flag-placeholder" title="Страна определяется">' + iconSvg('globe', 'flag-icon ssk-icon') + '</span>';

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
  initOnboarding?.();

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
