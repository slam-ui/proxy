package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type store struct {
	mu   sync.Mutex
	seen map[string]map[string]time.Time
}

func main() {
	dnsAddr := flag.String("dns", ":5353", "DNS listen address")
	httpAddr := flag.String("http", ":8088", "HTTP listen address")
	domain := flag.String("domain", "dnsleak.example.com", "test domain suffix")
	flag.Parse()
	st := &store{seen: map[string]map[string]time.Time{}}
	go func() {
		if err := serveDNS(*dnsAddr, *domain, st); err != nil {
			log.Fatal(err)
		}
	}()
	http.HandleFunc("/api/dnsleak/check/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/dnsleak/check/")
		json.NewEncoder(w).Encode(map[string]any{"resolvers": st.resolvers(id)})
	})
	log.Printf("dnsleak HTTP listening on %s, DNS on %s", *httpAddr, *dnsAddr)
	log.Fatal(http.ListenAndServe(*httpAddr, nil))
}

func serveDNS(addr, domain string, st *store) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	buf := make([]byte, 1500)
	for {
		n, remote, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		msg := append([]byte(nil), buf[:n]...)
		go handleDNS(pc, remote, msg, domain, st)
	}
}

func handleDNS(pc net.PacketConn, remote net.Addr, msg []byte, domain string, st *store) {
	name, qEnd, ok := parseQuestion(msg)
	if !ok {
		return
	}
	if strings.HasSuffix(name, strings.TrimSuffix(domain, ".")+".") {
		id := strings.SplitN(strings.TrimSuffix(name, "."), "-", 2)[0]
		if host, _, err := net.SplitHostPort(remote.String()); err == nil {
			st.add(id, host)
		}
	}
	resp := buildAResponse(msg, qEnd)
	_, _ = pc.WriteTo(resp, remote)
}

func parseQuestion(msg []byte) (string, int, bool) {
	if len(msg) < 12 {
		return "", 0, false
	}
	i := 12
	var labels []string
	for {
		if i >= len(msg) {
			return "", 0, false
		}
		l := int(msg[i])
		i++
		if l == 0 {
			break
		}
		if i+l > len(msg) {
			return "", 0, false
		}
		labels = append(labels, string(msg[i:i+l]))
		i += l
	}
	if i+4 > len(msg) {
		return "", 0, false
	}
	return strings.Join(labels, ".") + ".", i + 4, true
}

func buildAResponse(query []byte, qEnd int) []byte {
	resp := append([]byte(nil), query[:qEnd]...)
	resp[2] = 0x81
	resp[3] = 0x80
	binary.BigEndian.PutUint16(resp[6:8], 1)
	answer := []byte{
		0xc0, 0x0c,
		0x00, 0x01,
		0x00, 0x01,
		0x00, 0x00, 0x00, 0x1e,
		0x00, 0x04,
		127, 0, 0, 1,
	}
	return append(resp, answer...)
}

func (s *store) add(id, resolver string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[id] == nil {
		s.seen[id] = map[string]time.Time{}
	}
	s.seen[id][resolver] = time.Now().UTC()
}

func (s *store) resolvers(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	cutoff := time.Now().Add(-30 * time.Second)
	for ip, ts := range s.seen[id] {
		if ts.After(cutoff) {
			out = append(out, ip)
		}
	}
	return out
}
