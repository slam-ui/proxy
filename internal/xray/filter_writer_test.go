package xray

import (
	"bytes"
	"strings"
	"testing"
)

func TestFilterWriter_SuppressesNoisePatterns(t *testing.T) {
	noiseLines := []string{
		// idle timeout forcibly closed
		`ERROR[0514] [924430285 7m30s] connection: open connection to push.services.mozilla.com:443 using outbound/vless[proxy-out]: read tcp 192.168.0.104:54200->38.244.128.202:29528: wsarecv: An existing connection was forcibly closed by the remote host.`,
		// download closed forcibly
		`ERROR[0205] [2216982058 21.53s] connection: connection download closed: raw-read tcp 192.168.0.104:50354->149.154.167.99:443: An existing connection was forcibly closed by the remote host.`,
		// upload closed forcibly
		`ERROR[0028] [2274567319 176ms] connection: connection upload closed: raw-read tcp 127.0.0.1:10807->127.0.0.1:62762: An existing connection was forcibly closed by the remote host.`,
		// startup race: use of closed connection
		`ERROR[0002] [760012232 353ms] inbound/http[http-in]: process connection from 127.0.0.1:53256: read http request: read tcp 127.0.0.1:10807->127.0.0.1:53256: use of closed network connection`,
		// startup race: EOF
		`ERROR[0000] [3870117135 0ms] inbound/http[http-in]: process connection from 127.0.0.1:54340: read http request: EOF`,
		// client aborted (wsasend)
		`ERROR[0003] [2215111674 435ms] inbound/http[http-in]: process connection from 127.0.0.1:63331: write tcp 127.0.0.1:10807->127.0.0.1:63331: wsasend: An established connection was aborted by the software in your host machine.`,
		// local DNS refused
		`ERROR[0026] [465153480 1ms] connection: open connection to 127.0.0.1:53 using outbound/direct[direct]: dial tcp 127.0.0.1:53: connectex: No connection could be made because the target machine actively refused it.`,
		// cloud metadata unreachable / irrelevant noise
		`ERROR[0008] [2847837787 9ms] connection: open connection to 169.254.169.254:80 using outbound/direct[direct]: dial tcp 169.254.169.254:80: connectex: A socket operation was attempted to an unreachable host.`,
		`ERROR[0008] [2847837787 11ms] inbound/http[http-in]: process connection from 127.0.0.1:51700: (open connection to 169.254.169.254:80 using outbound/direct[direct]: dial tcp 169.254.169.254:80: connectex: A socket operation was attempted to an unreachable host. | Get "http://169.254.169.254/metadata/instance/compute": io: read/write on closed pipe)`,
	}

	for _, line := range noiseLines {
		var buf bytes.Buffer
		fw := NewFilterWriter(&buf)
		_, err := fw.Write([]byte(line + "\n"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("expected noise line to be suppressed, but got: %q\nline was: %q", buf.String(), line)
		}
	}
}

func TestFilterWriter_PassesThroughImportantErrors(t *testing.T) {
	importantLines := []string{
		// xray started
		`INFO  XRay успешно запущен с PID: 15784`,
		// real connection timeout (19s - server not responding)
		`ERROR[0088] [1218703920 19.27s] connection: open connection to detectportal.firefox.com:80 using outbound/vless[proxy-out]: read tcp 192.168.0.104:63717->38.244.128.202:443: wsarecv: A connection attempt failed because the connected party did not properly respond after a period of time`,
		// crash keywords
		`FATAL  panic: runtime error: index out of range`,
		// generic info
		`[08:20:35.476] INFO  sing-box готов`,
	}

	for _, line := range importantLines {
		var buf bytes.Buffer
		fw := NewFilterWriter(&buf)
		_, err := fw.Write([]byte(line + "\n"))
		if err != nil {
			t.Errorf("Write error: %v", err)
		}
		if !strings.Contains(buf.String(), line) {
			t.Errorf("expected important line to pass through, but it was suppressed: %q", line)
		}
	}
}

func TestFilterWriter_MultipleLines(t *testing.T) {
	input := "line1 important\n" +
		"ERROR[0000] [123 0ms] inbound/http[http-in]: process connection from 127.0.0.1:1: read http request: EOF\n" +
		"line3 important\n"

	var buf bytes.Buffer
	fw := NewFilterWriter(&buf)
	_, err := fw.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "line1 important") {
		t.Error("line1 should pass through")
	}
	if strings.Contains(out, "EOF") {
		t.Error("EOF noise line should be suppressed")
	}
	if !strings.Contains(out, "line3 important") {
		t.Error("line3 should pass through")
	}
}

func TestFilterWriter_ChunkedWrite(t *testing.T) {
	// Пишем строку по одному байту — проверяем буферизацию
	line := "ERROR[0000] [0 0ms] inbound/http[http-in]: process connection from 127.0.0.1:1: read http request: EOF\n"
	var buf bytes.Buffer
	fw := NewFilterWriter(&buf)
	for i := 0; i < len(line); i++ {
		_, err := fw.Write([]byte{line[i]})
		if err != nil {
			t.Fatalf("Write error at byte %d: %v", i, err)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("chunked noise line should be suppressed, got: %q", buf.String())
	}
}

func TestIsNoiseLine(t *testing.T) {
	tests := []struct {
		line      string
		wantNoise bool
	}{
		{`wsarecv: An existing connection was forcibly closed by the remote host`, true},
		{`connection download closed: raw-read tcp x->y:443: An existing connection was forcibly closed`, true},
		{`connection upload closed: raw-read tcp x->y:443: An existing connection was forcibly closed`, true},
		{`use of closed network connection`, true},
		{`inbound/http[http-in]: process connection from 127.0.0.1:1: read http request: EOF`, true},
		{`wsasend: An established connection was aborted by the software in your host machine`, true},
		{`dial tcp 127.0.0.1:53: connectex: No connection could be made because the target machine actively refused it`, true},
		{`connection: open connection to 169.254.169.254:80 using outbound/direct[direct]: dial tcp 169.254.169.254:80: connectex: A socket operation was attempted to an unreachable host`, true},
		{`Get "http://169.254.169.254/metadata/instance/compute": io: read/write on closed pipe`, true},
		// НЕ шум:
		{`wsarecv: A connection attempt failed because the connected party did not properly respond`, false},
		{`Xray started`, false},
		{`panic: runtime error`, false},
		{``, false},
	}

	for _, tt := range tests {
		got := isNoiseLine([]byte(tt.line))
		if got != tt.wantNoise {
			t.Errorf("isNoiseLine(%q) = %v, want %v", tt.line, got, tt.wantNoise)
		}
	}
}
