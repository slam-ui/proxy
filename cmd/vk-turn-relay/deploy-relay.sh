#!/usr/bin/env bash
# deploy-relay.sh — сборка и установка vk-turn-relay на VPS
#
# Использование (запускать на VPS):
#   git pull && bash deploy-relay.sh
#
# Или кросс-компиляция локально и scp:
#   GOOS=linux GOARCH=amd64 go build -o vk-turn-relay ./cmd/vk-turn-relay/
#   scp vk-turn-relay root@<VPS>:/usr/local/bin/
#   scp cmd/vk-turn-relay/vk-turn-relay.service root@<VPS>:/etc/systemd/system/
#   ssh root@<VPS> "systemctl daemon-reload && systemctl enable --now vk-turn-relay"

set -euo pipefail

BINARY=/usr/local/bin/vk-turn-relay
SERVICE=/etc/systemd/system/vk-turn-relay.service
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "==> Сборка vk-turn-relay..."
cd "$REPO_ROOT"
go build -ldflags="-s -w" -o "$BINARY" ./cmd/vk-turn-relay/
echo "    Бинарь: $BINARY ($(du -sh "$BINARY" | cut -f1))"

echo "==> Установка systemd сервиса..."
cp "$SCRIPT_DIR/vk-turn-relay.service" "$SERVICE"
chmod 644 "$SERVICE"

echo "==> UFW: открываем порт 3478/udp..."
if command -v ufw &>/dev/null; then
    ufw allow 3478/udp
    echo "    ufw allow 3478/udp — OK"
else
    echo "    ufw не найден, добавь правило вручную: iptables -A INPUT -p udp --dport 3478 -j ACCEPT"
fi

echo "==> Включаем и перезапускаем сервис..."
systemctl daemon-reload
systemctl enable vk-turn-relay
systemctl restart vk-turn-relay

sleep 1
systemctl is-active --quiet vk-turn-relay && echo "    ✓ vk-turn-relay запущен" || {
    echo "    ✗ Сервис не запустился. Лог:"
    journalctl -u vk-turn-relay --no-pager -n 20
    exit 1
}

echo ""
echo "==> Готово. Статус:"
systemctl status vk-turn-relay --no-pager -l | head -20
echo ""
echo "    Логи:  journalctl -u vk-turn-relay -f"
echo "    Порт:  UDP 3478 (DTLS 1.2 masquerade)"
echo "    Backend: 127.0.0.1:1080 (sing-box VLESS inbound)"
