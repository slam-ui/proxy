package xray

import (
	"bytes"
	"io"
	"regexp"
	"sync"
)

// noisePattern описывает одно правило подавления строки.
// Если строка совпадает с pattern и (опционально) минимальная длительность
// в строке ≥ minDuration — строка считается «шумом» и не пишется в dst.
type noisePattern struct {
	re          *regexp.Regexp
	description string
}

// noisePatterns — список безопасных шаблонов которые не нужно показывать в логе.
// Все паттерны проверены на реальных логах proxy-client.
var noisePatterns = []noisePattern{
	// 1. Соединение принудительно закрыто удалённым хостом после долгого idle.
	//    Пример: [924430285 7m30s] connection: ... wsarecv: An existing connection was forcibly closed
	//    Это нормальное закрытие по таймауту — не ошибка.
	{
		re:          regexp.MustCompile(`wsarecv:.*An existing connection was forcibly closed by the remote host`),
		description: "idle timeout forcibly closed",
	},
	// 2. Аналогично для download/upload closed.
	//    Пример: connection download closed: raw-read tcp ...: An existing connection was forcibly closed
	{
		re:          regexp.MustCompile(`connection (?:download|upload) closed:.*An existing connection was forcibly closed`),
		description: "connection closed by remote (idle)",
	},
	// 3. use of closed network connection — возникает при старте когда браузер
	//    успевает отправить запросы до готовности sing-box. Всегда кратковременно.
	{
		re:          regexp.MustCompile(`use of closed network connection`),
		description: "startup race: use of closed connection",
	},
	// 4. EOF на inbound/http при старте — та же гонка запуска (оба варианта).
	{
		re:          regexp.MustCompile(`inbound/http\[.*\]:.*(?:read http request: EOF|process connection.*127\.0\.0\.1:\d+.*EOF)`),
		description: "startup TCP probe EOF noise",
	},
	// 5. wsasend: connection aborted — клиент (браузер) закрыл соединение раньше
	//    чем ответ был отправлен. Нормальное поведение браузера.
	{
		re:          regexp.MustCompile(`wsasend:.*An established connection was aborted by the software in your host machine`),
		description: "client aborted connection (browser closed tab/request)",
	},
	// 6. DNS на 127.0.0.1:53 недоступен — локальный резолвер не запущен,
	//    sing-box пробует прямой DNS и получает отказ. Не критично для работы.
	{
		re:          regexp.MustCompile(`dial tcp 127\.0\.0\.1:53:.*No connection could be made because the target machine actively refused it`),
		description: "local DNS 127.0.0.1:53 not available",
	},
	// 7. Локальный metadata-сервис AWS / облачный metadata оказался недоступен.
	//    Эти обращения идут внутрь прокси, но не влияют на обычную работу клиента.
	{
		re:          regexp.MustCompile(`connection: open connection to 169\.254\.169\.254:\d+ using outbound/direct\[direct\]: dial tcp 169\.254\.169\.254:\d+: connectex: A socket operation was attempted to an unreachable host`),
		description: "cloud metadata host unreachable",
	},
	{
		re:          regexp.MustCompile(`Get \"http://169\.254\.169\.254/metadata/instance/compute\": io: read/write on closed pipe`),
		description: "cloud metadata request aborted",
	},
	// 8. Закрытые pipe/context canceled появляются при штатном shutdown/restart,
	// когда sing-box отменяет активные inbound/http запросы.
	{
		re:          regexp.MustCompile(`inbound/http\[.*\]:.*(?:io: read/write on closed pipe|context canceled)`),
		description: "inbound request canceled during shutdown/restart",
	},
	{
		re:          regexp.MustCompile(`connection: open connection .*: operation was canceled`),
		description: "outbound dial canceled during shutdown/restart",
	},
}

// FilterWriter оборачивает dst и подавляет строки совпадающие с noisePatterns.
// Все остальные строки передаются dst без изменений.
// Потокобезопасен.
type FilterWriter struct {
	mu  sync.Mutex
	buf []byte
	dst io.Writer
}

// NewFilterWriter создаёт FilterWriter поверх dst.
func NewFilterWriter(dst io.Writer) *FilterWriter {
	return &FilterWriter{dst: dst}
}

// maxFilterLineBuf — максимальный размер внутреннего буфера FilterWriter.
// Защита от OOM при бинарном выводе или crash-дампе sing-box без '\n'.
// Аналогично healthTrackingWriter.maxLineBuf = 64KB.
const maxFilterLineBuf = 64 * 1024

// Write принимает байты (потенциально несколько строк), буферизует по '\n'
// и решает для каждой строки: пропустить или подавить.
func (f *FilterWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.buf = append(f.buf, p...)

	// BUG FIX: защита от бесконечного роста буфера при отсутствии '\n'.
	// При бинарном выводе или crash-дампе sing-box buf мог расти до OOM.
	// Аналогична защите в healthTrackingWriter (maxLineBuf = 64KB).
	if len(f.buf) > maxFilterLineBuf {
		if idx := bytes.LastIndexByte(f.buf[:len(f.buf)-1], '\n'); idx >= 0 {
			f.buf = f.buf[idx+1:]
		} else {
			f.buf = f.buf[len(f.buf)-maxFilterLineBuf:]
		}
	}
	for {
		idx := bytes.IndexByte(f.buf, '\n')
		if idx < 0 {
			break
		}
		line := f.buf[:idx+1] // включая '\n'
		f.buf = f.buf[idx+1:]

		if !isNoiseLine(line) {
			if _, err := f.dst.Write(line); err != nil {
				return len(p), err
			}
		}
	}
	return len(p), nil
}

// isNoiseLine возвращает true если строку нужно подавить.
func isNoiseLine(line []byte) bool {
	for _, np := range noisePatterns {
		if np.re.Match(line) {
			return true
		}
	}
	return false
}
