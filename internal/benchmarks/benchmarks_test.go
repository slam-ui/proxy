package benchmarks

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"
)

const benchmarkDownloadSize = 100 << 20

func BenchmarkThroughputDownload100MB(b *testing.B) {
	payload := make([]byte, 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		for sent := 0; sent < benchmarkDownloadSize; sent += len(payload) {
			_, _ = w.Write(payload)
		}
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	b.SetBytes(benchmarkDownloadSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			_ = resp.Body.Close()
			b.Fatal(err)
		}
		_ = resp.Body.Close()
	}
}

func BenchmarkConnectionsPerSecond(b *testing.B) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-done:
					return
				default:
					return
				}
			}
			_ = conn.Close()
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
		if err != nil {
			b.Fatal(err)
		}
		_ = conn.Close()
	}
	b.StopTimer()
	close(done)
	_ = ln.Close()
	wg.Wait()
}

func BenchmarkMemoryIdleSnapshot(b *testing.B) {
	var stats runtime.MemStats
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runtime.ReadMemStats(&stats)
	}
}

func BenchmarkMemoryUnderLoadBuffers(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := make([]byte, 64<<10)
		for j := range buf {
			buf[j] = byte(j)
		}
	}
}

func BenchmarkCPUIdleSchedulerYield(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runtime.Gosched()
	}
}

func BenchmarkCPUStreamingCopy(b *testing.B) {
	const streamSize = 256 << 20
	buf := make([]byte, 64<<10)
	b.SetBytes(streamSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.CopyBuffer(io.Discard, io.LimitReader(deterministicReader{}, streamSize), buf); err != nil {
			b.Fatal(err)
		}
	}
}

type deterministicReader struct{}

func (deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i)
	}
	return len(p), nil
}
