package leaktest

const WebRTCTestHTML = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>SafeSky WebRTC Leak Test</title>
  <style>
    body{font-family:Segoe UI,Arial,sans-serif;background:#101418;color:#f5f7fa;margin:0;padding:24px}
    main{max-width:760px;margin:0 auto}
    code{display:block;white-space:pre-wrap;background:#1b222b;padding:12px;border-radius:8px}
    .warn{color:#ffd166}.ok{color:#57d68d}
  </style>
</head>
<body>
<main>
  <h1>WebRTC leak test</h1>
  <p id="status">Проверка ICE-кандидатов...</p>
  <code id="out"></code>
</main>
<script>
(async () => {
  const out = document.getElementById('out');
  const status = document.getElementById('status');
  const found = new Set();
  const pc = new RTCPeerConnection({iceServers:[{urls:'stun:stun.l.google.com:19302'}]});
  pc.createDataChannel('safesky');
  pc.onicecandidate = e => {
    if (!e.candidate) return;
    const text = e.candidate.candidate || '';
    out.textContent += text + '\n';
    for (const m of text.matchAll(/([0-9]{1,3}(?:\.[0-9]{1,3}){3}|[a-f0-9:]{8,})/ig)) found.add(m[1]);
  };
  await pc.setLocalDescription(await pc.createOffer());
  setTimeout(() => {
    pc.close();
    const ips = Array.from(found);
    if (!ips.length) {
      status.textContent = 'IP-кандидаты не найдены.';
      status.className = 'ok';
    } else {
      status.textContent = 'WebRTC может раскрывать IP-кандидаты: ' + ips.join(', ');
      status.className = 'warn';
    }
  }, 3500);
})();
</script>
</body>
</html>`
