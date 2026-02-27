package process

import (
	"fmt"
	"os"
	"os/exec"

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
}

// NewLauncher создает новый process launcher
func NewLauncher(log logger.Logger, engine apprules.Engine) Launcher {
	return &launcher{
		logger: log,
		engine: engine,
	}
}

// Launch запускает процесс с применением правила
func (l *launcher) Launch(executable string, rule *apprules.Rule, args ...string) (*LaunchResult, error) {
	if executable == "" {
		return nil, fmt.Errorf("executable path is required")
	}

	// Проверяем существование файла
	if _, err := os.Stat(executable); os.IsNotExist(err) {
		return nil, fmt.Errorf("executable not found: %s", executable)
	}

	// Создаем команду
	cmd := exec.Command(executable, args...)

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

	// Формируем proxy URL
	proxyURL := fmt.Sprintf("http://%s", proxyAddr)

	// Получаем текущие переменные окружения
	env := os.Environ()

	// Добавляем proxy переменные
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
	// Получаем текущие переменные окружения
	env := os.Environ()

	// Фильтруем proxy переменные
	filteredEnv := make([]string, 0, len(env))
	for _, e := range env {
		// Пропускаем proxy переменные
		if !isProxyEnvVar(e) {
			filteredEnv = append(filteredEnv, e)
		}
	}

	// Явно отключаем proxy
	filteredEnv = append(filteredEnv,
		"NO_PROXY=*",
		"no_proxy=*",
	)

	cmd.Env = filteredEnv

	l.logger.Debug("Applied direct (no proxy) environment")
	return nil
}

// isProxyEnvVar проверяет, является ли переменная proxy-related
func isProxyEnvVar(env string) bool {
	proxyVars := []string{
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"FTP_PROXY=",
		"ALL_PROXY=",
		"http_proxy=",
		"https_proxy=",
		"ftp_proxy=",
		"all_proxy=",
	}

	for _, prefix := range proxyVars {
		if len(env) >= len(prefix) && env[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}
