// vk-turn-relay — серверная сторона TURN туннеля.
//
// Запускается на VPS рядом с sing-box.
// Принимает UDP датаграммы (DTLS 1.2) от клиентского turnproxy,
// разбирает внутренний протокол и пересылает данные в локальный VLESS бэкенд по TCP.
//
// Использование:
//
//	vk-turn-relay -listen 0.0.0.0:3478 -backend 127.0.0.1:1080
//
// Флаги:
//
//	-listen   UDP адрес для приёма (default: 0.0.0.0:3478)
//	-backend  TCP адрес локального sing-box VLESS inbound (default: 127.0.0.1:1080)
//	-log      уровень логирования: debug|info|warn|error (default: info)
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// --- DTLS 1.2 constants (дублируем из клиентского пакета) ---

const (
	dtlsContentTypeAppData = 23
	dtlsVersionMajor       = 0xFE
	dtlsVersionMinor       = 0xFD
	dtlsHeaderLen          = 13
	innerHeaderLen         = 9

	FlagSYN  byte = 0x01
	FlagFIN  byte = 0x02
	FlagDATA byte = 0x00
	FlagPING byte = 0x04
	FlagPONG byte = 0x08

	maxChunkSize = 1200
	udpReadBuf   = 65536

	// sessionTimeout — закрываем сессию если нет активности.
	sessionTimeout = 5 * time.Minute
)

// --- DTLS encode/decode ---

func encodeDTLS(payload []byte, dtlsSeq uint64) []byte {
	out := make([]byte, dtlsHeaderLen+len(payload))
	out[0] = dtlsContentTypeAppData
	out[1] = dtlsVersionMajor
	out[2] = dtlsVersionMinor
	binary.BigEndian.PutUint16(out[3:5], 1) // epoch
	binary.BigEndian.PutUint16(out[5:7], uint16(dtlsSeq>>32))
	binary.BigEndian.PutUint32(out[7:11], uint32(dtlsSeq))
	binary.BigEndian.PutUint16(out[11:13], uint16(len(payload)))
	copy(out[13:], payload)
	return out
}

func decodeDTLS(data []byte) ([]byte, error) {
	if len(data) < dtlsHeaderLen {
		return nil, fmt.Errorf("too short: %d", len(data))
	}
	if data[0] != dtlsContentTypeAppData {
		return nil, fmt.Errorf("not app_data: %d", data[0])
	}
	if data[1] != dtlsVersionMajor || data[2] != dtlsVersionMinor {
		return nil, fmt.Errorf("bad version")
	}
	length := binary.BigEndian.Uint16(data[11:13])
	if int(length) > len(data)-dtlsHeaderLen {
		return nil, fmt.Errorf("length overflow")
	}
	return data[dtlsHeaderLen : dtlsHeaderLen+int(length)], nil
}

type innerFrame struct {
	streamID uint32
	seq      uint32
	flags    byte
	data     []byte
}

func encodeInner(f innerFrame) []byte {
	out := make([]byte, innerHeaderLen+len(f.data))
	binary.BigEndian.PutUint32(out[0:4], f.streamID)
	binary.BigEndian.PutUint32(out[4:8], f.seq)
	out[8] = f.flags
	copy(out[9:], f.data)
	return out
}

func decodeInner(b []byte) (innerFrame, error) {
	if len(b) < innerHeaderLen {
		return innerFrame{}, io.ErrUnexpectedEOF
	}
	return innerFrame{
		streamID: binary.BigEndian.Uint32(b[0:4]),
		seq:      binary.BigEndian.Uint32(b[4:8]),
		flags:    b[8],
		data:     b[innerHeaderLen:],
	}, nil
}

// --- Relay ---

type clientKey struct {
	ip   [16]byte
	port int
}

func addrKey(addr *net.UDPAddr) clientKey {
	var k clientKey
	copy(k.ip[:], addr.IP.To16())
	k.port = addr.Port
	return k
}

type session struct {
	streamID  uint32
	tcpConn   net.Conn
	sendCh    chan []byte // данные для отправки обратно клиенту
	lastSeen  time.Time
	closeOnce sync.Once
}

func (s *session) touch() { s.lastSeen = time.Now() }

type clientState struct {
	mu       sync.Mutex
	sessions map[uint32]*session
	addr     *net.UDPAddr
	dtlsSeq  atomic.Uint64
}

type relay struct {
	listenAddr  string
	backendAddr string
	conn        *net.UDPConn
	logger      *log.Logger

	mu      sync.RWMutex
	clients map[clientKey]*clientState
}

func newRelay(listenAddr, backendAddr string) *relay {
	return &relay{
		listenAddr:  listenAddr,
		backendAddr: backendAddr,
		logger:      log.New(os.Stdout, "[vk-turn-relay] ", log.LstdFlags),
		clients:     make(map[clientKey]*clientState),
	}
}

func (r *relay) run() error {
	addr, err := net.ResolveUDPAddr("udp4", r.listenAddr)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}
	r.conn = conn
	r.logger.Printf("слушаю UDP %s → backend TCP %s", r.listenAddr, r.backendAddr)

	// Чистим протухшие сессии каждую минуту.
	go r.gcLoop()

	buf := make([]byte, udpReadBuf)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			r.logger.Printf("UDP read error: %v", err)
			return err
		}
		go r.handleDatagram(buf[:n], clientAddr)
	}
}

func (r *relay) handleDatagram(data []byte, clientAddr *net.UDPAddr) {
	payload, err := decodeDTLS(data)
	if err != nil {
		return // молча дропаем невалидный пакет
	}
	frame, err := decodeInner(payload)
	if err != nil {
		return
	}

	key := addrKey(clientAddr)
	cs := r.getOrCreateClient(key, clientAddr)

	switch frame.flags {
	case FlagPING:
		r.sendControl(cs, 0, FlagPONG)

	case FlagSYN:
		r.openSession(cs, frame.streamID)

	case FlagFIN:
		cs.mu.Lock()
		sess, ok := cs.sessions[frame.streamID]
		cs.mu.Unlock()
		if ok {
			sess.tcpConn.Close()
		}

	case FlagDATA:
		cs.mu.Lock()
		sess, ok := cs.sessions[frame.streamID]
		cs.mu.Unlock()
		if !ok {
			// SYN потерялся — открываем сессию на лету.
			r.openSession(cs, frame.streamID)
			cs.mu.Lock()
			sess, ok = cs.sessions[frame.streamID]
			cs.mu.Unlock()
		}
		if ok {
			sess.touch()
			select {
			case sess.sendCh <- append([]byte(nil), frame.data...):
			default:
				r.logger.Printf("sendCh full stream=%d client=%s", frame.streamID, clientAddr)
			}
		}
	}
}

func (r *relay) openSession(cs *clientState, streamID uint32) {
	cs.mu.Lock()
	if _, exists := cs.sessions[streamID]; exists {
		cs.mu.Unlock()
		return
	}
	sess := &session{
		streamID: streamID,
		sendCh:   make(chan []byte, 64),
		lastSeen: time.Now(),
	}
	cs.sessions[streamID] = sess
	cs.mu.Unlock()

	// Подключаемся к бэкенду (sing-box VLESS inbound).
	tcpConn, err := net.DialTimeout("tcp4", r.backendAddr, 5*time.Second)
	if err != nil {
		r.logger.Printf("dial backend stream=%d: %v", streamID, err)
		cs.mu.Lock()
		delete(cs.sessions, streamID)
		cs.mu.Unlock()
		return
	}
	sess.tcpConn = tcpConn

	r.logger.Printf("новая сессия stream=%d client=%s → backend %s",
		streamID, cs.addr, r.backendAddr)

	// Горутина: данные из sendCh → TCP backend.
	go r.sessionWriter(cs, sess)
	// Горутина: ответы из TCP backend → UDP клиент.
	go r.sessionReader(cs, sess)
}

func (r *relay) sessionWriter(cs *clientState, sess *session) {
	for data := range sess.sendCh {
		if _, err := sess.tcpConn.Write(data); err != nil {
			sess.tcpConn.Close()
			return
		}
	}
}

func (r *relay) sessionReader(cs *clientState, sess *session) {
	defer func() {
		// Уведомляем клиента о закрытии.
		r.sendControl(cs, sess.streamID, FlagFIN)
		cs.mu.Lock()
		delete(cs.sessions, sess.streamID)
		cs.mu.Unlock()
		r.logger.Printf("сессия закрыта stream=%d", sess.streamID)
	}()

	buf := make([]byte, maxChunkSize)
	for {
		n, err := sess.tcpConn.Read(buf)
		if n > 0 {
			sess.touch()
			inner := encodeInner(innerFrame{
				streamID: sess.streamID,
				seq:      uint32(cs.dtlsSeq.Load()), // seq не критичен на сервере
				flags:    FlagDATA,
				data:     buf[:n],
			})
			dtls := encodeDTLS(inner, cs.dtlsSeq.Add(1))
			if _, sendErr := r.conn.WriteToUDP(dtls, cs.addr); sendErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *relay) sendControl(cs *clientState, streamID uint32, flags byte) {
	inner := encodeInner(innerFrame{streamID: streamID, flags: flags})
	dtls := encodeDTLS(inner, cs.dtlsSeq.Add(1))
	_, _ = r.conn.WriteToUDP(dtls, cs.addr)
}

func (r *relay) getOrCreateClient(key clientKey, addr *net.UDPAddr) *clientState {
	r.mu.RLock()
	cs, ok := r.clients[key]
	r.mu.RUnlock()
	if ok {
		return cs
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok = r.clients[key]; ok {
		return cs
	}
	cs = &clientState{
		addr:     addr,
		sessions: make(map[uint32]*session),
	}
	r.clients[key] = cs
	return cs
}

// gcLoop удаляет сессии без активности.
func (r *relay) gcLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		deadline := time.Now().Add(-sessionTimeout)
		r.mu.RLock()
		clients := make([]*clientState, 0, len(r.clients))
		for _, cs := range r.clients {
			clients = append(clients, cs)
		}
		r.mu.RUnlock()

		for _, cs := range clients {
			cs.mu.Lock()
			for id, sess := range cs.sessions {
				if sess.lastSeen.Before(deadline) {
					sess.tcpConn.Close()
					delete(cs.sessions, id)
					r.logger.Printf("GC: stream=%d timeout", id)
				}
			}
			cs.mu.Unlock()
		}
	}
}

// --- main ---

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:3478", "UDP адрес для приёма клиентских соединений")
	backendAddr := flag.String("backend", "127.0.0.1:1080", "TCP адрес бэкенда sing-box (VLESS inbound)")
	flag.Parse()

	r := newRelay(*listenAddr, *backendAddr)
	if err := r.run(); err != nil {
		log.Fatalf("relay: %v", err)
	}
}
