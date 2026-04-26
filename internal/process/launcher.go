package process

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"proxyclient/internal/apprules"
	"proxyclient/internal/logger"
)

// Launcher интерфейс для запуска процессов
type Launcher interface {
	Launch(executable string, rule *apprules.Rule, args ...string) (*LaunchResult, error)
	LaunchWithRule(executable string, ruleID string, args ...string) (*LaunchResult, error)
}

// LaunchResult результат запуска процесса
type LaunchResult struct {
	PID        int
	Executable string
	RuleID     string
	ProxyAddr  string
	Action     apprules.Action
}

// launcher реализация Launcher
type launcher struct {
	logger logger.Logger
	engine apprules.Engine
	// OPT #9: кэш базового environ — os.Environ() копирует весь environ процесса
	// (~100-200 строк) при каждом вызове. Кэшируем один раз при создании launcher.
	// При частых запусках дочерних процессов экономит одну полную копию environ
	// на каждый Launch. Инвалидируется только при явном вызове refreshBaseEnv().
	baseEnvMu sync.RWMutex
	baseEnv   []string
}

// NewLauncher создает новый process launcher
func NewLauncher(log logger.Logger, engine apprules.Engine) Launcher {
	l := &launcher{
		logger: log,
		engine: engine,
	}
	l.baseEnv = os.Environ() // захватываем environ один раз при старте
	return l
}

// Launch запускает процесс с применением правила
func (l *launcher) Launch(executable string, rule *apprules.Rule, args ...string) (*LaunchResult, error) {
	if executable == "" {
		return nil, fmt.Errorf("executable path is required")
	}

	// Проверяем существование файла; пробуем сначала LookPath (учитывает PATH)
	resolved := executable
	if _, err := os.Stat(executable); os.IsNotExist(err) {
		if found, lerr := exec.LookPath(executable); lerr == nil {
			resolved = found
		} else {
			return nil, fmt.Errorf("executable not found: %s", executable)
		}
	}

	// Создаем команду
	cmd := exec.Command(resolved, args...)
	// BUG FIX: CREATE_NO_WINDOW — подавляет мигание консольного окна при запуске.
	// Все остальные exec.Command в codebase (manager.go, wintun.go) устанавливают этот флаг.
	// Источник: Clash Verge Rev, v2rayN.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000,
		HideWindow:    true,
	}

	// Применяем правило если задано
	if rule != nil {
		if err := l.applyRuleToCommand(cmd, rule); err != nil {
			return nil, fmt.Errorf("failed to apply rule: %w", err)
		}
	}

	// Запускаем процесс
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start process: %w", err)
	}

	pid := cmd.Process.Pid

	// Освобождаем дескриптор процесса после завершения.
	// Без Wait() HANDLE процесса утекает вплоть до финализации GC.
	go func() { _ = cmd.Wait() }()

	result := &LaunchResult{
		PID:        pid,
		Executable: executable,
	}

	if rule != nil {
		result.RuleID = rule.ID
		result.Action = rule.Action
		result.ProxyAddr = rule.ProxyAddr

		l.logger.Info("Launched process %s (PID: %d) with rule '%s' (%s)",
			executable, pid, rule.Name, rule.Action)
	} else {
		l.logger.Info("Launched process %s (PID: %d) without proxy rule", executable, pid)
	}

	return result, nil
}

// LaunchWithRule запускает процесс с правилом по ID
func (l *launcher) LaunchWithRule(executable string, ruleID string, args ...string) (*LaunchResult, error) {
	if ruleID == "" {
		return l.Launch(executable, nil, args...)
	}

	rule, err := l.engine.GetRule(ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to get rule: %w", err)
	}

	return l.Launch(executable, rule, args...)
}

// applyRuleToCommand применяет правило к команде
func (l *launcher) applyRuleToCommand(cmd *exec.Cmd, rule *apprules.Rule) error {
	if rule == nil {
		return nil
	}
	switch rule.Action {
	case apprules.ActionProxy:
		return l.applyProxyEnv(cmd, rule.ProxyAddr)

	case apprules.ActionDirect:
		return l.applyDirectEnv(cmd)

	case apprules.ActionBlock:
		return fmt.Errorf("cannot launch blocked application")

	default:
		return fmt.Errorf("unknown action: %s", rule.Action)
	}
}

// applyProxyEnv устанавливает proxy environment variables
func (l *launcher) applyProxyEnv(cmd *exec.Cmd, proxyAddr string) error {
	if proxyAddr == "" {
		return fmt.Errorf("proxy address is required")
	}

	proxyURL := fmt.Sprintf("http://%s", proxyAddr)

	// OPT #9: копируем кэшированный базовый environ вместо os.Environ().
	l.baseEnvMu.RLock()
	env := make([]string, len(l.baseEnv), len(l.baseEnv)+6)
	copy(env, l.baseEnv)
	l.baseEnvMu.RUnlock()

	env = append(env,
		fmt.Sprintf("HTTP_PROXY=%s", proxyURL),
		fmt.Sprintf("HTTPS_PROXY=%s", proxyURL),
		fmt.Sprintf("http_proxy=%s", proxyURL),
		fmt.Sprintf("https_proxy=%s", proxyURL),
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
	)

	cmd.Env = env

	l.logger.Debug("Applied proxy environment: %s", proxyURL)
	return nil
}

// applyDirectEnv убирает proxy environment variables
func (l *launcher) applyDirectEnv(cmd *exec.Cmd) error {
	// Если cmd.Env уже установлен (например, в тестах), используем его как источник.
	// Иначе — кэшированный базовый environ.
	var base []string
	if len(cmd.Env) > 0 {
		base = cmd.Env
	} else {
		l.baseEnvMu.RLock()
		base = l.baseEnv
		l.baseEnvMu.RUnlock()
	}

	filteredEnv := make([]string, 0, len(base)+2)
	for _, e := range base {
		if !isProxyEnvVar(e) {
			filteredEnv = append(filteredEnv, e)
		}
	}

	filteredEnv = append(filteredEnv,
		"NO_PROXY=*",
		"no_proxy=*",
	)

	cmd.Env = filteredEnv

	l.logger.Debug("Applied direct (no proxy) environment")
	return nil
}

// isProxyEnvVar проверяет, является ли переменная proxy-related.
// OPT #7: заменили линейный поиск по слайсу на strings.Cut + switch.
// os.Environ() возвращает ~100-200 строк, для каждой ранее выполнялось
// 8 сравнений с префиксом. Switch компилируется в jump-table — O(1).
func isProxyEnvVar(env string) bool {
	key, _, _ := strings.Cut(env, "=")
	switch strings.ToUpper(key) {
	case "HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY":
		return true
	}
	return false
}
