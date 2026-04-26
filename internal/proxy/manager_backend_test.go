package proxy

import (
	"os"
	"sync"
	"testing"
)

type fakeProxyBackend struct {
	mu          sync.Mutex
	enabled     bool
	config      Config
	setErr      error
	disableErr  error
	setCalls    int
	disableCall int
	stateCalls  int
}

func TestMain(m *testing.M) {
	oldFactory := newProxyBackend
	newProxyBackend = func() proxyBackend {
		return &fakeProxyBackend{}
	}
	code := m.Run()
	newProxyBackend = oldFactory
	os.Exit(code)
}

func newFakeManager(enabled bool, config Config) (*manager, *fakeProxyBackend) {
	backend := &fakeProxyBackend{enabled: enabled, config: config}
	return newManagerWithBackend(nil, backend), backend
}

func (b *fakeProxyBackend) set(config Config) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.setCalls++
	if b.setErr != nil {
		return b.setErr
	}
	b.enabled = true
	b.config = config
	return nil
}

func (b *fakeProxyBackend) disable() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.disableCall++
	if b.disableErr != nil {
		return b.disableErr
	}
	b.enabled = false
	return nil
}

func (b *fakeProxyBackend) state() (bool, Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stateCalls++
	return b.enabled, b.config
}

func (b *fakeProxyBackend) setDisabledExternally() {
	b.mu.Lock()
	b.enabled = false
	b.mu.Unlock()
}
