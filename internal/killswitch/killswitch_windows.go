//go:build windows

// Package killswitch блокирует весь интернет-трафик когда туннель упал.
// Реализован через netsh advfirewall — создаёт именованные правила которые
// разрешают только:
//   - Трафик через loopback (127.0.0.1) — системные нужды и HTTP-прокси порт
//   - Трафик к IP прокси-сервера — чтобы sing-box мог переподключиться
//   - Уже установленные соединения — не рвём текущие VLESS-соединения
//
// Правила создаются при падении туннеля (Enable) и удаляются при старте (Disable).
package killswitch

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"proxyclient/internal/logger"
)

const (
	ruleNameBlock  = "ProxyClient-KillSwitch-Block"
	ruleNameAllow  = "ProxyClient-KillSwitch-Allow"
)

var (
	mu      sync.Mutex
	enabled bool
)

// IsEnabled возвращает текущее состояние Kill Switch.
func IsEnabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// Enable активирует Kill Switch: блокирует весь исходящий трафик кроме
// loopback и serverIP. Вызывается при падении туннеля.
func Enable(serverIP string, log logger.Logger) {
	mu.Lock()
	defer mu.Unlock()
	if enabled {
		return
	}

	// Сначала удаляем старые правила если остались
	deleteRules()

	// Правило 1: Разрешить loopback и адрес прокси-сервера
	allowIPs := "127.0.0.1"
	if serverIP != "" && serverIP != "127.0.0.1" {
		allowIPs += "," + serverIP
	}
	runNetsh(
		"advfirewall", "firewall", "add", "rule",
		"name="+ruleNameAllow,
		"dir=out",
		"action=allow",
		"protocol=any",
		"remoteip="+allowIPs,
		"enable=yes",
		"profile=any",
	)

	// Правило 2: Блокировать всё остальное исходящее
	runNetsh(
		"advfirewall", "firewall", "add", "rule",
		"name="+ruleNameBlock,
		"dir=out",
		"action=block",
		"protocol=any",
		"remoteip=any",
		"enable=yes",
		"profile=any",
	)

	enabled = true
	if log != nil {
		log.Warn("Kill Switch АКТИВЕН — трафик заблокирован до восстановления туннеля (разрешён: %s)", allowIPs)
	}
}

// Disable снимает блокировку Kill Switch. Вызывается при успешном старте sing-box.
func Disable(log logger.Logger) {
	mu.Lock()
	defer mu.Unlock()
	if !enabled {
		return
	}
	deleteRules()
	enabled = false
	if log != nil {
		log.Info("Kill Switch снят — трафик разблокирован")
	}
}

// CleanupOnStart удаляет правила Kill Switch при старте приложения.
// Защита от ситуации когда приложение аварийно завершилось с активным KS.
func CleanupOnStart(log logger.Logger) {
	mu.Lock()
	defer mu.Unlock()
	deleted := deleteRules()
	if deleted && log != nil {
		log.Info("Kill Switch: удалены правила от предыдущего сеанса")
	}
	enabled = false
}

// deleteRules удаляет оба firewall-правила. Возвращает true если хоть одно было удалено.
func deleteRules() bool {
	r1 := runNetsh("advfirewall", "firewall", "delete", "rule", "name="+ruleNameAllow)
	r2 := runNetsh("advfirewall", "firewall", "delete", "rule", "name="+ruleNameBlock)
	return r1 || r2
}

// runNetsh запускает netsh скрытно. Возвращает true при успехе.
func runNetsh(args ...string) bool {
	cmd := exec.Command("netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		HideWindow:    true,
	}
	out, err := cmd.CombinedOutput()
	// "No rules match" при delete — это нормально
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "no rules match") {
		return false
	}
	return true
}
