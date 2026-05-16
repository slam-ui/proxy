// NAV TABS
// ═══════════════════════════════════════════════════
const PAGE_COUNT = 5;
let currentPage = 0;

function activePageEl() {
  return $id('page' + currentPage);
}

function updateChromeScrollState() {
  const page = activePageEl();
  const scrolled = !!page && page.scrollTop > 18;
  document.documentElement.classList.toggle('ui-scrolled', scrolled);
}

function navTo(i) {
  // Close all modals to prevent breaking other tabs
  closeSrv();
  closeAddRuleModal();
  closeRulesJsonEditor();
  closeVisualRuleEditor();
  closeSingboxConfigEditor();
  closeServerInfoModal();

  const prev = currentPage;
  // Переключаем DOM только если меняем страницу
  if (i !== currentPage) {
    currentPage = i;
    for (let j = 0; j < PAGE_COUNT; j++) {
      $id('ni' + j).classList.toggle('active', j === i);
    }
    const p0 = $id('page0');
    if (i === 0) { p0.style.display = 'flex'; } else { p0.style.display = 'none'; }
    for (let j = 1; j < PAGE_COUNT; j++) {
      const p = $id('page' + j);
      if (p) p.classList.toggle('active', j === i);
    }
    const activePage = $id('page' + i);
    if (activePage) activePage.scrollTop = 0;
    updateChromeScrollState();
    // Закрываем лог-стрим при уходе со страницы логов (#3)
    if (prev === 3 && i !== 3) stopLogStream();
  }
  // Lazy-load вызывается всегда (повторный клик = обновление данных) (#2)
  if (i === 1) { loadRules(); loadProfiles(); loadVisualRouting(); }
  if (i === 2) loadProcs();
  if (i === 3 && !logStreaming) startLogStream();
  if (i === 4) loadSettingsPage();
  if (typeof OpTimer !== 'undefined' && OpTimer.refreshPlacement) OpTimer.refreshPlacement();
  updateChromeScrollState();
}

// ═══════════════════════════════════════════════════
