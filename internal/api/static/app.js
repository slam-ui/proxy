// SafeSky UI script entrypoint. Edit files in js/ by domain.
const SAFE_SCRIPT_SOURCES = Object.freeze([
  "js/00-core.js",
  "js/10-servers.js",
  "js/20-navigation.js",
  "js/30-rules-processes.js",
  "js/40-logs.js",
  "js/50-settings-theme.js",
  "js/55-onboarding.js",
  "js/60-setup.js",
  "js/70-runtime-polling.js",
  "js/80-chart-utils-init.js"
]);

(function loadSafeScript(index) {
  if (index >= SAFE_SCRIPT_SOURCES.length) return;
  const src = SAFE_SCRIPT_SOURCES[index];
  if (!/^js\/[0-9]{2}-[a-z0-9-]+\.js$/i.test(src)) return;

  const script = document.createElement("script");
  script.src = src;
  script.onload = () => loadSafeScript(index + 1);
  document.head.appendChild(script);
})(0);
