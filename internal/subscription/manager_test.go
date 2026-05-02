package subscription

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type atomicClock struct {
	unix atomic.Int64
}

func newAtomicClock(t time.Time) *atomicClock {
	c := &atomicClock{}
	c.Store(t)
	return c
}

func (c *atomicClock) Store(t time.Time) {
	c.unix.Store(t.UnixNano())
}

func (c *atomicClock) Add(d time.Duration) {
	c.unix.Add(int64(d))
}

func (c *atomicClock) Now() time.Time {
	return time.Unix(0, c.unix.Load()).UTC()
}

func testManager(t *testing.T, url string, handler http.HandlerFunc, clock *atomicClock) (*Manager, *httptest.Server) {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)
	client := ts.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // nosec G402: local httptest TLS certificate.
	if url == "" {
		url = ts.URL
	}
	m, err := NewManager(Options{
		Dir:          t.TempDir(),
		Client:       client,
		IsSupported:  supported,
		Now:          clock.Now,
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Add(&Subscription{ID: "sub1", Name: "Test", URL: url, UpdateEvery: time.Hour, UserAgent: "SafeSkyTest/1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return m, ts
}

func TestManagerAddRejectsHTTP(t *testing.T) {
	now := time.Now()
	m, err := NewManager(Options{Dir: t.TempDir(), IsSupported: supported, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Add(&Subscription{Name: "bad", URL: "http://example.com/sub"}); err == nil {
		t.Fatal("Add accepted http URL")
	}
}

func TestManagerUpdateNowFetchesAndDiffs(t *testing.T) {
	clock := newAtomicClock(time.Unix(1000, 0))
	var sawUA atomic.Bool
	m, _ := testManager(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "SafeSkyTest/1" {
			sawUA.Store(true)
		}
		w.Header().Set("subscription-userinfo", "upload=10; download=20; total=100; expire=1746000000")
		_, _ = w.Write([]byte("vless://id@example.com:443?encryption=none#one\nss://YWVzOnBhc3M@example.net:8388#two"))
	}, clock)

	got, err := m.UpdateNow(context.Background(), "sub1")
	if err != nil {
		t.Fatalf("UpdateNow: %v", err)
	}
	if !sawUA.Load() {
		t.Fatal("User-Agent was not sent")
	}
	if got.Added != 2 || got.Removed != 0 || got.Quota.Used() != 30 {
		t.Fatalf("unexpected result: %+v", got)
	}
	list := m.List()
	if len(list) != 1 || len(list[0].Servers) != 2 || list[0].LastError != "" {
		t.Fatalf("unexpected subscription state: %+v", list)
	}
}

func TestManagerUpdateNowBase64AndBackoff(t *testing.T) {
	clock := newAtomicClock(time.Unix(1000, 0))
	var fail atomic.Bool
	fail.Store(true)
	m, _ := testManager(t, "", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		body := base64.StdEncoding.EncodeToString([]byte("trojan://pass@example.com:443#one\n"))
		_, _ = w.Write([]byte(body))
	}, clock)

	if _, err := m.UpdateNow(context.Background(), "sub1"); err == nil {
		t.Fatal("UpdateNow succeeded on HTTP 502")
	}
	afterFail := m.List()[0]
	if afterFail.Backoff != initialBackoff || !afterFail.NextAttempt.Equal(clock.Now().Add(initialBackoff)) {
		t.Fatalf("unexpected backoff state: %+v", afterFail)
	}

	fail.Store(false)
	clock.Add(initialBackoff)
	got, err := m.UpdateNow(context.Background(), "sub1")
	if err != nil {
		t.Fatalf("UpdateNow after recovery: %v", err)
	}
	if got.Added != 1 || m.List()[0].Backoff != 0 {
		t.Fatalf("unexpected recovery result: %+v state=%+v", got, m.List()[0])
	}
}

func TestManagerStartUpdatesOnlyDueSubscriptions(t *testing.T) {
	clock := newAtomicClock(time.Unix(1000, 0))
	var hits atomic.Int32
	m, _ := testManager(t, "", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("vless://id@example.com:443?encryption=none#one"))
	}, clock)

	if _, err := m.UpdateNow(context.Background(), "sub1"); err != nil {
		t.Fatalf("initial UpdateNow: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Start(ctx)
	}()
	time.Sleep(30 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits before interval=%d, want 1", got)
	}
	clock.Add(time.Hour + time.Second)
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if got := hits.Load(); got < 2 {
		t.Fatalf("hits after interval=%d, want >=2", got)
	}
}

func TestManagerPersistsEncryptedURLAndCachedServers(t *testing.T) {
	clock := newAtomicClock(time.Unix(1000, 0))
	dir := t.TempDir()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("vless://id@example.com:443?encryption=none#one"))
	}))
	defer ts.Close()
	client := ts.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // nosec G402: local httptest TLS certificate.
	m, err := NewManager(Options{Dir: dir, Client: client, IsSupported: supported, Now: clock.Now})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Add(&Subscription{ID: "sub1", Name: "Persist", URL: ts.URL}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := m.UpdateNow(context.Background(), "sub1"); err != nil {
		t.Fatalf("UpdateNow: %v", err)
	}
	meta, err := os.ReadFile(m.metaPath("sub1"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(meta), ts.URL) {
		t.Fatal("metadata contains plaintext subscription URL")
	}
	reloaded, err := NewManager(Options{Dir: dir, Client: client, IsSupported: supported, Now: clock.Now})
	if err != nil {
		t.Fatalf("reload NewManager: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].URL != ts.URL || len(list[0].Servers) != 1 {
		t.Fatalf("unexpected reloaded state: %+v", list)
	}
}
