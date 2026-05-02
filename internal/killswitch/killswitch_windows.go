//go:build windows

// Package killswitch блокирует весь интернет-трафик когда туннель упал.
// Реализован через netsh advfirewall — создаёт именованные правила которые
// разрешают только:
//   - Трафик через loopback (127.0.0.1) — системные нужды и HTTP-прокси порт
//   - Трафик к IP прокси-сервера — чтобы sing-box мог переподключиться
//   - Уже установленные соединения — не рвём текущие VLESS-соединения
//
// Правила создаются при падении туннеля (Enable) и остаются после краша.
// Автоматическая очистка на старте разрешена только после clean shutdown state.
package killswitch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
	"proxyclient/internal/logger"
)

const (
	ruleNameBlock   = "ProxyClient-KillSwitch-Block"
	ruleNameAllow   = "ProxyClient-KillSwitch-Allow"
	ruleNameBlockV6 = "ProxyClient-KillSwitch-Block-IPv6"
	ksLegacyFile    = "killswitch_active"
)

var (
	mu      sync.Mutex
	enabled bool
)

type RuleState struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
}

type State struct {
	Active                bool        `json:"active"`
	CreatedAt             time.Time   `json:"created_at,omitempty"`
	CreatedByPID          int         `json:"created_by_pid,omitempty"`
	Rules                 []RuleState `json:"rules"`
	ExpectedCleanShutdown bool        `json:"expected_clean_shutdown"`
}

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

	// BUG FIX: отдельное правило для IPv6 — remoteip= охватывает только IPv4.
	// На Windows с IPv6-enabled сетями трафик утекал через IPv6 stack.
	// Источник: WireGuard-windows, Tailscale wgengine/filter.
	runNetsh(
		"advfirewall", "firewall", "add", "rule",
		"name="+ruleNameBlockV6,
		"dir=out",
		"action=block",
		"protocol=any",
		"remoteip=2000::/3",
		"enable=yes",
		"profile=any",
	)

	_ = saveState(State{
		Active:       true,
		CreatedAt:    time.Now().UTC(),
		CreatedByPID: os.Getpid(),
		Rules: []RuleState{
			{Name: ruleNameAllow},
			{Name: ruleNameBlock},
			{Name: ruleNameBlockV6},
		},
		ExpectedCleanShutdown: false,
	})
	_ = os.WriteFile(ksLegacyFile, []byte("1"), 0644)
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
	_ = saveState(State{Active: false, ExpectedCleanShutdown: true})
	_ = os.Remove(ksLegacyFile)
	if log != nil {
		log.Info("Kill Switch снят — трафик разблокирован")
	}
}

// CleanupOnStart применяет fail-close policy при старте приложения.
// После краша правила остаются активными до явного действия пользователя.
func CleanupOnStart(log logger.Logger) {
	mu.Lock()
	defer mu.Unlock()

	st, err := LoadState()
	if err != nil && log != nil {
		log.Warn("Kill Switch: не удалось прочитать state: %v", err)
	}
	if !st.Active {
		if _, err := os.Stat(ksLegacyFile); os.IsNotExist(err) {
			enabled = false
			return
		}
		// Legacy marker existed without JSON state. Treat it as crash-active.
		enabled = true
		if log != nil {
			log.Warn("Kill Switch активен из прошлой сессии — правила оставлены до явной разблокировки")
		}
		return
	}
	if !st.ExpectedCleanShutdown {
		enabled = true
		if log != nil {
			log.Warn("Kill Switch активен из прошлой сессии — трафик остаётся заблокированным до явной разблокировки")
		}
		return
	}

	cleanupAfterCleanShutdown(log)
}

func cleanupAfterCleanShutdown(log logger.Logger) {
	if _, err := os.Stat(ksLegacyFile); os.IsNotExist(err) {
		enabled = false
		return
	}
	_ = os.Remove(ksLegacyFile)

	deleted := deleteRules()
	if deleted && log != nil {
		log.Info("Kill Switch: удалены правила после чистого завершения")
	}
	enabled = false
}

func LoadState() (State, error) {
	var st State
	data, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	return st, nil
}

func saveState(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath()), 0755); err != nil {
		return err
	}
	return fileutil.WriteAtomic(statePath(), data, 0644)
}

func statePath() string {
	return filepath.Join(config.DataDir, "killswitch_state.json")
}

// deleteRules удаляет все firewall-правила. Возвращает true если хоть одно было удалено.
func deleteRules() bool {
	r1 := runNetsh("advfirewall", "firewall", "delete", "rule", "name="+ruleNameAllow)
	r2 := runNetsh("advfirewall", "firewall", "delete", "rule", "name="+ruleNameBlock)
	r3 := runNetsh("advfirewall", "firewall", "delete", "rule", "name="+ruleNameBlockV6)
	return r1 || r2 || r3
}

// runNetsh запускает netsh скрытно. Возвращает true при успехе.
// BUG FIX: используем CommandContext с таймаутом 10 секунд и WaitDelay 1 секунду.
// Без таймаута netsh может заблокировать на WaitForSingleObject(INFINITE) если
// процесс не реагирует (нет прав администратора или системная проблема).
func runNetsh(args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		HideWindow:    true,
	}
	cmd.WaitDelay = 1 * time.Second
	out, err := cmd.CombinedOutput()
	// "No rules match" при delete — это нормально
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "no rules match") {
		return false
	}
	return true
}
