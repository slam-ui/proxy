package turnproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"proxyclient/internal/logger"
)

const (
	// keepaliveInterval — как часто слать PING для удержания NAT-состояния.
	keepaliveInterval = 20 * time.Second
	// udpReadBuf — размер буфера чтения UDP.
	udpReadBuf = 65536
	// reorderBufSize — глубина буфера переупорядочивания (пакеты могут прийти не по порядку).
	reorderBufSize = 64
)

// Config — настройки TURNProxy.
type Config struct {
	// ListenAddr — TCP адрес для sing-box (default: 127.0.0.1:9000).
	ListenAddr string
	// RelayAddr — адрес server-side relay (host:port UDP).
	RelayAddr string
	// Logger опциональный.
	Logger logger.Logger
}

// Proxy — локальный TURN прокси.
// Принимает TCP соединения от sing-box, туннелирует через UDP/DTLS к серверному relay.
// Потокобезопасен.
type Proxy struct {
	cfg      Config
	log      logger.Logger
	listener net.Listener
	udpConn  *net.UDPConn

	mu       sync.RWMutex
	sessions map[uint32]*tcpSession

	nextStreamID atomic.Uint32
	dtlsSeq      atomic.Uint64

	// consecutiveUDPErrors счётчик последовательных ошибок записи в UDP сокет.
	// При достижении 3 — сокет пересоздаётся через reconnectUDP.
	consecutiveUDPErrors atomic.Int32

	stopOnce sync.Once
	stopCh   chan struct{}
}

// New создаёт Proxy. Start() запускает его.
func New(cfg Config) *Proxy {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:9000"
	}
	log := cfg.Logger
	if log == nil {
		log = logger.NewNop()
	}
	return &Proxy{
		cfg:      cfg,
		log:      log,
		sessions: make(map[uint32]*tcpSession),
		stopCh:   make(chan struct{}),
	}
}

// Start открывает TCP listener и UDP сокет, запускает горутины.
// Возвращает сразу — не блокирует. Остановка через Stop() или отмену ctx.
func (p *Proxy) Start(ctx context.Context) error {
	// Резолвим relay адрес.
	relayUDP, err := net.ResolveUDPAddr("udp4", p.cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("turnproxy: resolve relay %s: %w", p.cfg.RelayAddr, err)
	}

	// UDP сокет к relay (connected — отфильтровывает чужие пакеты).
	udpConn, err := net.DialUDP("udp4", nil, relayUDP)
	if err != nil {
		return fmt.Errorf("turnproxy: dial UDP %s: %w", p.cfg.RelayAddr, err)
	}
	p.udpConn = udpConn

	// TCP listener для sing-box.
	ln, err := net.Listen("tcp4", p.cfg.ListenAddr)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("turnproxy: listen TCP %s: %w", p.cfg.ListenAddr, err)
	}
	p.listener = ln

	p.log.Info("turnproxy: старт | TCP %s → UDP relay %s (DTLS 1.2 маскировка)",
		p.cfg.ListenAddr, p.cfg.RelayAddr)

	// Принимаем UDP от relay.
	go p.readUDPLoop(ctx)
	// Принимаем TCP от sing-box.
	go p.acceptTCPLoop(ctx)
	// Keepalive PING.
	go p.keepaliveLoop(ctx)

	// Стоп по контексту.
	go func() {
		select {
		case <-ctx.Done():
		case <-p.stopCh:
		}
		p.shutdown()
	}()

	return nil
}

// Stop немедленно останавливает прокси.
func (p *Proxy) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		p.shutdown()
	})
}

func (p *Proxy) shutdown() {
	if p.listener != nil {
		_ = p.listener.Close()
	}
	if p.udpConn != nil {
		// Шлём FIN-broadcast: уведомляем relay что уходим.
		_ = p.sendControl(0, FlagFIN)
		_ = p.udpConn.Close()
	}
	// Закрываем все активные TCP сессии.
	p.mu.Lock()
	for id, s := range p.sessions {
		s.close()
		delete(p.sessions, id)
	}
	p.mu.Unlock()
	p.log.Info("turnproxy: остановлен")
}

// acceptTCPLoop принимает входящие TCP соединения от sing-box.
func (p *Proxy) acceptTCPLoop(ctx context.Context) {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			case <-p.stopCh:
			default:
				p.log.Error("turnproxy: accept error: %v", err)
			}
			return
		}
		go p.handleTCP(ctx, conn)
	}
}

// handleTCP обрабатывает одно TCP соединение от sing-box.
func (p *Proxy) handleTCP(ctx context.Context, conn net.Conn) {
	streamID := p.nextStreamID.Add(1)
	sess := newTCPSession(streamID, conn)

	p.mu.Lock()
	p.sessions[streamID] = sess
	p.mu.Unlock()

	p.log.Info("turnproxy: новое соединение stream=%d src=%s", streamID, conn.RemoteAddr())

	// Отправляем SYN relay серверу.
	if err := p.sendControl(streamID, FlagSYN); err != nil {
		p.log.Error("turnproxy: SYN stream=%d: %v", streamID, err)
		p.removeSession(streamID)
		conn.Close()
		return
	}

	// Горутина: TCP → UDP.
	go p.tcpToUDP(ctx, sess)

	// Горутина: UDP → TCP (данные приходят через readUDPLoop → sess.recvCh).
	p.udpToTCP(sess)

	// Соединение закрыто.
	if err := p.sendControl(streamID, FlagFIN); err != nil {
		p.log.Warn("turnproxy: FIN stream=%d: %v", streamID, err)
	}
	p.removeSession(streamID)
	p.log.Info("turnproxy: соединение закрыто stream=%d", streamID)
}

// tcpToUDP читает из TCP и отправляет датаграммы relay серверу.
// Отслеживает последовательные ошибки UDP: при >= 3 — пересоздаёт сокет.
func (p *Proxy) tcpToUDP(ctx context.Context, sess *tcpSession) {
	buf := make([]byte, MaxChunkSize)
	for {
		n, err := sess.conn.Read(buf)
		if n > 0 {
			if sendErr := p.sendData(sess.streamID, sess.nextSeq(), buf[:n]); sendErr != nil {
				p.log.Warn("turnproxy: UDP send stream=%d: %v", sess.streamID, sendErr)
				// A-6: считаем последовательные ошибки UDP
				if count := p.consecutiveUDPErrors.Add(1); count >= 3 {
					p.log.Warn("turnproxy: %d последовательных ошибок UDP — пересоздаём сокет", count)
					p.consecutiveUDPErrors.Store(0)
					go p.reconnectUDP(ctx)
				}
				break
			} else {
				// Успешная отправка — сбрасываем счётчик ошибок
				p.consecutiveUDPErrors.Store(0)
			}
		}
		if err != nil {
			if err != io.EOF {
				select {
				case <-ctx.Done():
				case <-p.stopCh:
				default:
					p.log.Warn("turnproxy: TCP read stream=%d: %v", sess.streamID, err)
				}
			}
			break
		}
	}
	sess.conn.Close()
	close(sess.done)
}

// udpToTCP пишет данные из UDP (пришедшие через recvCh) в TCP.
func (p *Proxy) udpToTCP(sess *tcpSession) {
	for {
		select {
		case chunk, ok := <-sess.recvCh:
			if !ok {
				return
			}
			if _, err := sess.conn.Write(chunk); err != nil {
				sess.conn.Close()
				return
			}
		case <-sess.done:
			return
		}
	}
}

// readUDPLoop читает UDP датаграммы от relay сервера и диспетчирует по сессиям.
func (p *Proxy) readUDPLoop(ctx context.Context) {
	buf := make([]byte, udpReadBuf)
	for {
		n, err := p.udpConn.Read(buf)
		if err != nil {
			select {
			case <-ctx.Done():
			case <-p.stopCh:
			default:
				p.log.Warn("turnproxy: UDP read error: %v", err)
			}
			return
		}

		payload, err := decodeDTLS(buf[:n])
		if err != nil {
			p.log.Debug("turnproxy: DTLS decode: %v", err)
			continue
		}

		frame, err := decodeInner(payload)
		if err != nil {
			p.log.Debug("turnproxy: inner decode: %v", err)
			continue
		}

		switch frame.flags {
		case FlagPONG:
			// keepalive ответ — игнорируем
		case FlagDATA:
			p.mu.RLock()
			sess, ok := p.sessions[frame.streamID]
			p.mu.RUnlock()
			if ok {
				select {
				case sess.recvCh <- append([]byte(nil), frame.data...):
				default:
					p.log.Warn("turnproxy: recvCh full stream=%d — dropping", frame.streamID)
				}
			}
		case FlagFIN:
			p.mu.RLock()
			sess, ok := p.sessions[frame.streamID]
			p.mu.RUnlock()
			if ok {
				sess.close()
			}
		}
	}
}

// keepaliveLoop шлёт PING каждые keepaliveInterval.
func (p *Proxy) keepaliveLoop(ctx context.Context) {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.sendControl(0, FlagPING); err != nil {
				p.log.Warn("turnproxy: PING failed: %v — reconnecting UDP", err)
				p.reconnectUDP(ctx)
			}
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		}
	}
}

// reconnectUDP закрывает старый UDP сокет и открывает новый.
// Необходимо после рестарта Wintun: connected UDP сокет сохраняет
// src-адрес исчезнувшего TUN-интерфейса → WSAEINVAL на каждый Write.
// После реконнекта old.Close() разблокирует readUDPLoop → горутина выходит,
// новая стартует на свежем сокете.
func (p *Proxy) reconnectUDP(ctx context.Context) {
	relayUDP, err := net.ResolveUDPAddr("udp4", p.cfg.RelayAddr)
	if err != nil {
		p.log.Warn("turnproxy: reconnectUDP resolve: %v", err)
		return
	}
	newConn, err := net.DialUDP("udp4", nil, relayUDP)
	if err != nil {
		p.log.Warn("turnproxy: reconnectUDP dial: %v", err)
		return
	}

	p.mu.Lock()
	old := p.udpConn
	p.udpConn = newConn
	p.mu.Unlock()

	// Закрываем старый — разблокирует Read в readUDPLoop.
	_ = old.Close()
	// Перезапускаем reader на новом сокете.
	go p.readUDPLoop(ctx)

	// Сбрасываем счётчик ошибок — сокет успешно пересоздан.
	p.consecutiveUDPErrors.Store(0)
	p.log.Info("turnproxy: UDP reconnect успешен (src обновлён после смены интерфейса)")
}

// --- helpers ---

func (p *Proxy) sendData(streamID, seq uint32, data []byte) error {
	inner := encodeInner(innerFrame{streamID: streamID, seq: seq, flags: FlagDATA, data: data})
	dtls := encodeDTLS(inner, p.dtlsSeq.Add(1))
	_, err := p.udpConn.Write(dtls)
	return err
}

func (p *Proxy) sendControl(streamID uint32, flags byte) error {
	inner := encodeInner(innerFrame{streamID: streamID, flags: flags})
	dtls := encodeDTLS(inner, p.dtlsSeq.Add(1))
	_, err := p.udpConn.Write(dtls)
	return err
}

func (p *Proxy) removeSession(id uint32) {
	p.mu.Lock()
	delete(p.sessions, id)
	p.mu.Unlock()
}

// tcpSession — состояние одного TCP соединения.
type tcpSession struct {
	streamID  uint32
	conn      net.Conn
	recvCh    chan []byte
	done      chan struct{}
	seq       atomic.Uint32
	closeOnce sync.Once
}

func newTCPSession(id uint32, conn net.Conn) *tcpSession {
	return &tcpSession{
		streamID: id,
		conn:     conn,
		recvCh:   make(chan []byte, reorderBufSize),
		done:     make(chan struct{}),
	}
}

func (s *tcpSession) nextSeq() uint32 { return s.seq.Add(1) }

func (s *tcpSession) close() {
	s.closeOnce.Do(func() {
		s.conn.Close()
		close(s.recvCh)
	})
}
