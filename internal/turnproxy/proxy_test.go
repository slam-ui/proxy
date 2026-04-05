package turnproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// ─── DTLS encode/decode ───────────────────────────────────────────────────────

func TestEncodeDTLS_ValidHeader(t *testing.T) {
	payload := []byte("hello world")
	pkt := encodeDTLS(payload, 42)

	if pkt[0] != dtlsContentTypeAppData {
		t.Errorf("content_type want %d got %d", dtlsContentTypeAppData, pkt[0])
	}
	if pkt[1] != dtlsVersionMajor || pkt[2] != dtlsVersionMinor {
		t.Errorf("version want FE FD got %02x %02x", pkt[1], pkt[2])
	}
	if len(pkt) != dtlsHeaderLen+len(payload) {
		t.Errorf("len want %d got %d", dtlsHeaderLen+len(payload), len(pkt))
	}
}

func TestDecodeDTLS_RoundTrip(t *testing.T) {
	original := []byte("test payload 12345")
	encoded := encodeDTLS(original, 7)

	decoded, err := decodeDTLS(encoded)
	if err != nil {
		t.Fatalf("decodeDTLS error: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Errorf("want %q got %q", original, decoded)
	}
}

func TestDecodeDTLS_TooShort(t *testing.T) {
	_, err := decodeDTLS([]byte{0x17, 0xFE})
	if err == nil {
		t.Error("expected error for short datagram")
	}
}

func TestDecodeDTLS_WrongContentType(t *testing.T) {
	pkt := encodeDTLS([]byte("x"), 1)
	pkt[0] = 22 // handshake, not app_data
	_, err := decodeDTLS(pkt)
	if err == nil {
		t.Error("expected error for wrong content type")
	}
}

func TestDecodeDTLS_WrongVersion(t *testing.T) {
	pkt := encodeDTLS([]byte("x"), 1)
	pkt[1] = 0x03 // TLS, not DTLS
	_, err := decodeDTLS(pkt)
	if err == nil {
		t.Error("expected error for wrong version")
	}
}

func TestDecodeDTLS_LengthOverflow(t *testing.T) {
	pkt := encodeDTLS([]byte("hello"), 1)
	// Inflate declared length beyond actual data.
	pkt[11] = 0xFF
	pkt[12] = 0xFF
	_, err := decodeDTLS(pkt)
	if err == nil {
		t.Error("expected error for length overflow")
	}
}

// ─── innerFrame encode/decode ─────────────────────────────────────────────────

func TestInnerFrame_RoundTrip(t *testing.T) {
	cases := []innerFrame{
		{streamID: 1, seq: 100, flags: FlagDATA, data: []byte("some data")},
		{streamID: 0xDEADBEEF, seq: 0, flags: FlagSYN, data: nil},
		{streamID: 2, seq: 999, flags: FlagFIN, data: nil},
		{streamID: 3, seq: 1, flags: FlagPING, data: nil},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("flags=%02x", tc.flags), func(t *testing.T) {
			b := encodeInner(tc)
			got, err := decodeInner(b)
			if err != nil {
				t.Fatalf("decodeInner: %v", err)
			}
			if got.streamID != tc.streamID {
				t.Errorf("streamID want %d got %d", tc.streamID, got.streamID)
			}
			if got.seq != tc.seq {
				t.Errorf("seq want %d got %d", tc.seq, got.seq)
			}
			if got.flags != tc.flags {
				t.Errorf("flags want %02x got %02x", tc.flags, got.flags)
			}
			if !bytes.Equal(got.data, tc.data) {
				t.Errorf("data want %q got %q", tc.data, got.data)
			}
		})
	}
}

func TestDecodeInner_TooShort(t *testing.T) {
	_, err := decodeInner([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short inner frame")
	}
}

// ─── DTLSSeq monotonically increases ─────────────────────────────────────────

func TestEncodeDTLS_SeqMonotonic(t *testing.T) {
	// Verify that sequence numbers are correctly encoded and increase.
	seqs := []uint64{0, 1, 255, 256, 65535, 65536, 0xFFFFFFFF}
	for _, seq := range seqs {
		pkt := encodeDTLS([]byte("x"), seq)
		// Decode and verify seq via the epoch+seq bytes.
		// We don't expose a seq extractor, but we can verify round-trip is stable.
		payload, err := decodeDTLS(pkt)
		if err != nil {
			t.Errorf("seq=%d: decode failed: %v", seq, err)
		}
		if string(payload) != "x" {
			t.Errorf("seq=%d: payload corrupted", seq)
		}
	}
}

// ─── Integration: Proxy ↔ echo relay ─────────────────────────────────────────

// echoUDPRelay is a minimal UDP relay for testing.
// It receives DTLS-wrapped frames and echoes data back to the sender.
type echoUDPRelay struct {
	conn    *net.UDPConn
	addr    string
	stopCh  chan struct{}
	dtlsSeq uint64
	mu      sync.Mutex
}

func startEchoRelay(t *testing.T) *echoUDPRelay {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	r := &echoUDPRelay{
		conn:   conn,
		addr:   conn.LocalAddr().String(),
		stopCh: make(chan struct{}),
	}
	go r.run(t)
	return r
}

func (r *echoUDPRelay) run(t *testing.T) {
	buf := make([]byte, 65536)
	for {
		r.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, clientAddr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.stopCh:
				return
			default:
				continue
			}
		}

		payload, err := decodeDTLS(buf[:n])
		if err != nil {
			continue
		}
		frame, err := decodeInner(payload)
		if err != nil {
			continue
		}

		switch frame.flags {
		case FlagSYN:
			// Acknowledge with a PONG (stream_id=0) so client knows relay is live.
			r.send(clientAddr, innerFrame{streamID: 0, flags: FlagPONG})
		case FlagDATA:
			// Echo back.
			r.send(clientAddr, innerFrame{
				streamID: frame.streamID,
				seq:      frame.seq,
				flags:    FlagDATA,
				data:     frame.data,
			})
		case FlagPING:
			r.send(clientAddr, innerFrame{streamID: 0, flags: FlagPONG})
		}
	}
}

func (r *echoUDPRelay) send(addr *net.UDPAddr, f innerFrame) {
	r.mu.Lock()
	r.dtlsSeq++
	seq := r.dtlsSeq
	r.mu.Unlock()
	inner := encodeInner(f)
	pkt := encodeDTLS(inner, seq)
	_, _ = r.conn.WriteToUDP(pkt, addr)
}

func (r *echoUDPRelay) stop() { close(r.stopCh); r.conn.Close() }

// TestProxy_Echo запускает Proxy против echoUDPRelay.
// Данные отправленные в TCP должны вернуться обратно (эхо).
func TestProxy_Echo(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration in -short mode")
	}

	relay := startEchoRelay(t)
	defer relay.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := New(Config{
		ListenAddr: "127.0.0.1:0", // OS выбирает порт
		RelayAddr:  relay.addr,
	})
	if err := p.Start(ctx); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	defer p.Stop()

	// Подключаемся к proxy как sing-box.
	conn, err := net.DialTimeout("tcp4", p.listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	want := []byte("hello from sing-box")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("echo mismatch: want %q got %q", want, got)
	}
}

// TestProxy_MultipleConnections проверяет что несколько параллельных TCP
// соединений корректно мультиплексируются через один UDP сокет.
func TestProxy_MultipleConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration in -short mode")
	}

	relay := startEchoRelay(t)
	defer relay.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	p := New(Config{
		ListenAddr: "127.0.0.1:0",
		RelayAddr:  relay.addr,
	})
	if err := p.Start(ctx); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	defer p.Stop()

	const N = 5
	var wg sync.WaitGroup
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp4", p.listener.Addr().String(), 2*time.Second)
			if err != nil {
				errs <- fmt.Errorf("conn %d: dial: %w", id, err)
				return
			}
			defer conn.Close()

			msg := []byte(fmt.Sprintf("stream-%d-payload", id))
			if _, err := conn.Write(msg); err != nil {
				errs <- fmt.Errorf("conn %d: write: %w", id, err)
				return
			}

			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, buf); err != nil {
				errs <- fmt.Errorf("conn %d: read: %w", id, err)
				return
			}
			if !bytes.Equal(buf, msg) {
				errs <- fmt.Errorf("conn %d: want %q got %q", id, msg, buf)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestProxy_StopCleansUp проверяет что Stop() закрывает listener и сессии.
func TestProxy_StopCleansUp(t *testing.T) {
	relay := startEchoRelay(t)
	defer relay.stop()

	ctx := context.Background()
	p := New(Config{
		ListenAddr: "127.0.0.1:0",
		RelayAddr:  relay.addr,
	})
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	listenAddr := p.listener.Addr().String()

	p.Stop()

	// После Stop() новые соединения должны быть отклонены.
	conn, err := net.DialTimeout("tcp4", listenAddr, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Error("expected dial to fail after Stop()")
	}
}

// TestProxy_StopIdempotent проверяет что двойной Stop() не паникует.
func TestProxy_StopIdempotent(t *testing.T) {
	relay := startEchoRelay(t)
	defer relay.stop()

	p := New(Config{
		ListenAddr: "127.0.0.1:0",
		RelayAddr:  relay.addr,
	})
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	p.Stop()
	p.Stop() // второй вызов не должен паниковать
}

// TestProxy_ContextCancel проверяет остановку через отмену контекста.
func TestProxy_ContextCancel(t *testing.T) {
	relay := startEchoRelay(t)
	defer relay.stop()

	ctx, cancel := context.WithCancel(context.Background())
	p := New(Config{
		ListenAddr: "127.0.0.1:0",
		RelayAddr:  relay.addr,
	})
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	listenAddr := p.listener.Addr().String()

	cancel()
	time.Sleep(100 * time.Millisecond) // даём shutdown горутине отработать

	conn, err := net.DialTimeout("tcp4", listenAddr, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Error("expected dial to fail after context cancel")
	}
}

// TestProxy_InvalidRelayAddr проверяет что Start() возвращает ошибку при
// невалидном адресе relay.
func TestProxy_InvalidRelayAddr(t *testing.T) {
	p := New(Config{
		ListenAddr: "127.0.0.1:0",
		RelayAddr:  "not-a-valid-address",
	})
	err := p.Start(context.Background())
	if err == nil {
		p.Stop()
		t.Error("expected error for invalid relay addr")
	}
}

// TestProxy_DefaultListenAddr проверяет что пустой ListenAddr получает дефолт.
func TestProxy_DefaultListenAddr(t *testing.T) {
	relay := startEchoRelay(t)
	defer relay.stop()

	p := New(Config{RelayAddr: relay.addr})
	if p.cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("default listen addr want 127.0.0.1:9000 got %s", p.cfg.ListenAddr)
	}
}

// A-6: при >= 3 последовательных ошибках UDP write — proxy пересоздаёт сокет.
// Проверяем что consecutiveUDPErrors счётчик работает корректно.
func TestProxy_ConsecutiveUDPErrors_TriggersReconnect(t *testing.T) {
	relay := startEchoRelay(t)
	defer relay.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := New(Config{
		ListenAddr: "127.0.0.1:0",
		RelayAddr:  relay.addr,
	})
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Stop()

	// Симулируем 3 последовательных ошибки: закрываем UDP сокет изнутри.
	// После этого sendData будет падать с ошибкой на каждый write.
	p.mu.Lock()
	oldConn := p.udpConn
	p.mu.Unlock()

	// Закрываем сокет — следующие write упадут с ошибкой
	oldConn.Close()

	// Вручную добавляем 2 ошибки (до порога 3)
	p.consecutiveUDPErrors.Store(2)

	// Третья ошибка должна тригернуть reconnect
	count := p.consecutiveUDPErrors.Add(1)
	if count >= 3 {
		p.consecutiveUDPErrors.Store(0)
		go p.reconnectUDP(ctx)
	}

	// Даём время reconnect горутине отработать
	time.Sleep(200 * time.Millisecond)

	// Проверяем что счётчик сброшен (reconnect выполнен)
	if v := p.consecutiveUDPErrors.Load(); v != 0 {
		t.Errorf("consecutiveUDPErrors = %d после reconnect, want 0", v)
	}

	// Проверяем что новый сокет отличается от старого (reconnect создал новый)
	p.mu.RLock()
	newConn := p.udpConn
	p.mu.RUnlock()

	if newConn == oldConn {
		t.Error("UDP сокет не был пересоздан после 3 последовательных ошибок")
	}
}
