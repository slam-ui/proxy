// SafeSky UI script entrypoint. Edit files in js/ by domain.
[
  "js/00-core.js",
  "js/10-servers.js",
  "js/20-navigation.js",
  "js/30-rules-processes.js",
  "js/40-logs.js",
  "js/50-settings-theme.js",
  "js/60-setup.js",
  "js/70-runtime-polling.js",
  "js/80-chart-utils-init.js"
].forEach((src) => {
  document.write('<script src="' + src + '"><\/script>');
});