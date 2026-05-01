//go:build windows

package process

import (
	"os/exec"
	"strings"
	"sync"
	"testing"

	"proxyclient/internal/apprules"
	"proxyclient/internal/logger"
)

// ── isProxyEnvVar Tests ──────────────────────────────────────────────────────────

func TestIsProxyEnvVar_DetectsProxyVariables(t *testing.T) {
	proxyVars := []string{
		"HTTP_PROXY=http://localhost:8080",
		"HTTPS_PROXY=http://localhost:8080",
		"ALL_PROXY=http://localhost:8080",
		"http_proxy=http://localhost:8080",
		"NO_PROXY=localhost",
		"no_proxy=localhost",
	}

	for _, env := range proxyVars {
		t.Run(env, func(t *testing.T) {
			if !isProxyEnvVar(env) {
				t.Errorf("isProxyEnvVar(%q) = false, want true", env)
			}
		})
	}
}

// ── NewLauncher Tests ─────────────────────────────────────────────────────────────

func TestNewLauncher_CreatesLauncher(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()

	l := NewLauncher(log, engine)
	if l == nil {
		t.Fatal("NewLauncher returned nil")
	}
}

func TestNewLauncher_CapturesBaseEnv(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()

	l := NewLauncher(log, engine).(*launcher)
	if len(l.baseEnv) == 0 {
		t.Error("baseEnv should not be empty")
	}
}

// ── Launch Tests ──────────────────────────────────────────────────────────────────

func TestLaunch_LaunchesProcess(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not found")
	}

	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()
	l := NewLauncher(log, engine)

	result, err := l.Launch("cmd.exe", nil, "/c", "exit 0")
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if result.PID == 0 {
		t.Error("PID should not be 0")
	}
}

// ── LaunchWithRule Tests ──────────────────────────────────────────────────────────

func TestLaunchWithRule_ReturnsError_WhenRuleNotFound(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()
	l := NewLauncher(log, engine)

	_, err := l.LaunchWithRule("cmd.exe", "nonexistent-id")
	if err == nil {
		t.Error("Expected error for nonexistent rule ID")
	}
}

// ── applyRuleToCommand Tests ──────────────────────────────────────────────────────

func TestApplyRuleToCommand_ProxyAction(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()
	l := NewLauncher(log, engine).(*launcher)

	cmd := exec.Command("test.exe")
	rule := &apprules.Rule{
		ID:        "test",
		Pattern:   "test.exe",
		Action:    apprules.ActionProxy,
		ProxyAddr: "127.0.0.1:8080",
	}

	err := l.applyRuleToCommand(cmd, rule)
	if err != nil {
		t.Fatalf("applyRuleToCommand failed: %v", err)
	}

	envMap := envToMap(cmd.Env)
	if !strings.Contains(envMap["HTTP_PROXY"], "127.0.0.1:8080") {
		t.Errorf("HTTP_PROXY = %q, want it to contain 127.0.0.1:8080", envMap["HTTP_PROXY"])
	}
}

func TestApplyProxyEnvReplacesExistingProxyVars(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()
	l := NewLauncher(log, engine).(*launcher)
	l.baseEnv = []string{
		"PATH=C:\\Windows",
		"HTTP_PROXY=http://old-proxy",
		"NO_PROXY=old.local",
	}

	cmd := exec.Command("test.exe")
	if err := l.applyProxyEnv(cmd, "127.0.0.1:8080"); err != nil {
		t.Fatalf("applyProxyEnv: %v", err)
	}

	if got := countEnvKey(cmd.Env, "HTTP_PROXY"); got != 1 {
		t.Fatalf("HTTP_PROXY entries=%d, want 1 in %v", got, cmd.Env)
	}
	if got := countEnvKey(cmd.Env, "NO_PROXY"); got != 1 {
		t.Fatalf("NO_PROXY entries=%d, want 1 in %v", got, cmd.Env)
	}
	envMap := envToMap(cmd.Env)
	if envMap["HTTP_PROXY"] != "http://127.0.0.1:8080" {
		t.Fatalf("HTTP_PROXY=%q, want new proxy", envMap["HTTP_PROXY"])
	}
	if envMap["NO_PROXY"] != "localhost,127.0.0.1,::1" {
		t.Fatalf("NO_PROXY=%q, want local bypass list", envMap["NO_PROXY"])
	}
}

func TestApplyDirectEnvRemovesExistingNoProxyVars(t *testing.T) {
	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()
	l := NewLauncher(log, engine).(*launcher)

	cmd := exec.Command("test.exe")
	cmd.Env = []string{
		"PATH=C:\\Windows",
		"HTTP_PROXY=http://old-proxy",
		"NO_PROXY=old.local",
		"no_proxy=old.local",
	}
	if err := l.applyDirectEnv(cmd); err != nil {
		t.Fatalf("applyDirectEnv: %v", err)
	}

	if got := countEnvKey(cmd.Env, "HTTP_PROXY"); got != 0 {
		t.Fatalf("HTTP_PROXY entries=%d, want 0 in %v", got, cmd.Env)
	}
	if got := countEnvKey(cmd.Env, "NO_PROXY"); got != 1 {
		t.Fatalf("NO_PROXY entries=%d, want 1 in %v", got, cmd.Env)
	}
	if got := countEnvKey(cmd.Env, "no_proxy"); got != 1 {
		t.Fatalf("no_proxy entries=%d, want 1 in %v", got, cmd.Env)
	}
	envMap := envToMap(cmd.Env)
	if envMap["NO_PROXY"] != "*" || envMap["no_proxy"] != "*" {
		t.Fatalf("direct NO_PROXY values = %q/%q, want *", envMap["NO_PROXY"], envMap["no_proxy"])
	}
}

// ── LaunchResult Tests ────────────────────────────────────────────────────────────

func TestLaunchResult_FilledCorrectly(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not found")
	}

	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()

	// Добавляем обязательный Pattern и используем возвращаемый ID
	addedRule, err := engine.AddRule(apprules.Rule{
		Name:      "Test Rule",
		Pattern:   "cmd.exe",
		Action:    apprules.ActionProxy,
		ProxyAddr: "127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	l := NewLauncher(log, engine)

	result, err := l.LaunchWithRule("cmd.exe", addedRule.ID, "/c", "exit 0")
	if err != nil {
		t.Fatalf("LaunchWithRule failed: %v", err)
	}

	if result.PID == 0 {
		t.Error("PID should not be 0")
	}
	if result.RuleID != addedRule.ID {
		t.Errorf("RuleID = %q, want %q", result.RuleID, addedRule.ID)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────────

func envToMap(env []string) map[string]string {
	result := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

func countEnvKey(env []string, key string) int {
	count := 0
	for _, e := range env {
		got, _, _ := strings.Cut(e, "=")
		if got == key {
			count++
		}
	}
	return count
}

func TestLauncher_ConcurrentLaunch(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not found")
	}

	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()
	l := NewLauncher(log, engine)

	var pids = make(map[int]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := l.Launch("cmd.exe", nil, "/c", "exit 0")
			if err != nil {
				return
			}
			mu.Lock()
			pids[result.PID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
}

func TestLauncher_FullWorkflow(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not found")
	}

	log := logger.New(logger.LevelInfo)
	engine := apprules.NewEngine()

	pRule, err := engine.AddRule(apprules.Rule{
		Name:      "Proxy",
		Pattern:   "cmd.exe",
		Action:    apprules.ActionProxy,
		ProxyAddr: "127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	l := NewLauncher(log, engine)

	result, err := l.LaunchWithRule("cmd.exe", pRule.ID, "/c", "exit 0")
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	if result.Action != apprules.ActionProxy {
		t.Errorf("Action = %q, want proxy", result.Action)
	}
}
