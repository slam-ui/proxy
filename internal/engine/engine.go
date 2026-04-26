// Package engine автоматически загружает sing-box.exe из GitHub Releases
// если исполняемый файл отсутствует или повреждён.
//
// Алгоритм:
//  1. Проверяем наличие sing-box.exe (stat + размер > 0)
//  2. Если нет — запрашиваем GitHub API последний релиз
//  3. Находим asset для windows/amd64
//  4. Скачиваем zip, извлекаем sing-box.exe атомарно
//  5. Сообщаем прогресс через канал
package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
)

// retryBaseDelay базовая задержка первой повторной попытки (1s → 2s → 4s).
// Переопределяется в тестах для ускорения проверок.
var retryBaseDelay = time.Second

var (
	ensureMu      sync.Mutex
	ensureRunning atomic.Bool
)

// pinnedVersion — зафиксированная версия sing-box.
// Используем конкретный тег вместо "latest", чтобы при выходе v1.14+
// поведение не менялось. Обновляй здесь при каждом плановом обновлении движка.
const pinnedVersion = "v1.13.7"

// githubAPI — var (не const) чтобы тесты могли подменять на httptest.Server.
// Запрашиваем конкретный тег вместо latest — предсказуемое поведение при выходе новых версий.
var githubAPI = "https://api.github.com/repos/SagerNet/sing-box/releases/tags/" + pinnedVersion

// noProxyTransport не использует системный прокси Windows.
// Загрузка sing-box происходит ДО его запуска: порт 10807 ещё не слушает,
// поэтому http.DefaultTransport (читает системный прокси 127.0.0.1:10807) даёт
// "proxyconnect: connectex: connection refused".
// Нулевой Transport.Proxy (nil) = без прокси (http.DefaultTransport явно задаёт
// Proxy: ProxyFromEnvironment, а пустой Transport оставляет nil = отключено).
var noProxyTransport = &http.Transport{}

const (
	assetName   = "sing-box"
	assetOS     = "windows"
	assetArch   = "amd64"
	userAgent   = "proxy-client/1.0 (auto-engine)"
	httpTimeout = 120 * time.Second
)

// Progress сообщение о прогрессе загрузки
type Progress struct {
	Stage            string // "check" | "fetch_meta" | "download" | "extract" | "done" | "error"
	Message          string
	Percent          int    // 0-100, -1 если неизвестно
	Version          string // версия sing-box (заполняется на стадии fetch_meta и выше)
	InstalledVersion string // уже установленная версия (заполняется на стадии "check")
	Err              error
}

// withRetry выполняет fn до attempts раз с экспоненциальным backoff.
// Базовая задержка: retryBaseDelay → retryBaseDelay*2 → retryBaseDelay*4 (± 20% jitter).
// Немедленно прекращает повторы при context.Canceled / context.DeadlineExceeded.
func withRetry(ctx context.Context, attempts int, fn func() error) error {
	var lastErr error
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		// Не повторяем при отмене контекста
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return lastErr
		}
		if i < attempts {
			// backoff: base * 2^(i-1) ± 20% jitter
			base := retryBaseDelay * (1 << uint(i-1))
			jitter := time.Duration(float64(base) * 0.4 * (rand.Float64() - 0.5)) //nolint:gosec
			wait := base + jitter
			if wait < 0 {
				wait = 0
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return lastErr
}

// versionFilePath возвращает путь к файлу с кэшем версии рядом с execPath.
func versionFilePath(execPath string) string {
	return execPath + ".version"
}

// InstalledVersion читает версию установленного sing-box из файла <execPath>.version.
// Возвращает пустую строку если файл не найден или не читается.
func InstalledVersion(execPath string) string {
	data, err := os.ReadFile(versionFilePath(execPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// LatestVersion получает последнюю доступную версию sing-box из GitHub Releases.
func LatestVersion(ctx context.Context) (string, error) {
	_, version, err := fetchLatestAsset(ctx)
	return version, err
}

// NeedsDownload возвращает true если sing-box.exe отсутствует или повреждён (< 1 MB).
// FIX 28+45: проверяем MinValidBinarySize — 0-байтный или усечённый файл считается отсутствующим.
func NeedsDownload(execPath string) bool {
	fi, err := os.Stat(execPath)
	if err != nil {
		return true
	}
	return fi.Size() < config.MinValidBinarySize
}

// EnsureInProgress reports whether an EnsureEngine call is currently downloading,
// extracting, or verifying sing-box.exe. Apply/restart paths use this to avoid
// executing a binary while Windows Defender/SmartScreen may still be scanning it.
func EnsureInProgress() bool {
	return ensureRunning.Load()
}

// WaitForEnsure blocks until the current EnsureEngine call finishes.
func WaitForEnsure(ctx context.Context) error {
	for EnsureInProgress() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

// EnsureEngine проверяет наличие sing-box.exe и при необходимости скачивает его.
// Прогресс отправляется в канал progress (может быть nil).
// Возвращает nil при успехе или если файл уже существует.
func EnsureEngine(ctx context.Context, execPath string, progress chan<- Progress) error {
	ensureMu.Lock()
	ensureRunning.Store(true)
	defer func() {
		ensureRunning.Store(false)
		ensureMu.Unlock()
	}()

	send := func(p Progress) {
		if progress != nil {
			select {
			case progress <- p:
			default:
			}
		}
	}

	installed := InstalledVersion(execPath)
	send(Progress{Stage: "check", Message: "Проверяем sing-box.exe...", Percent: 0, InstalledVersion: installed})

	if !NeedsDownload(execPath) {
		send(Progress{Stage: "done", Message: "sing-box.exe уже есть", Percent: 100, InstalledVersion: installed})
		return nil
	}

	// Резолвим абсолютный путь сразу — Windows не любит "./" в Rename
	if abs, err := filepath.Abs(execPath); err == nil {
		execPath = abs
	}
	if dir := filepath.Dir(execPath); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("не удалось создать директорию: %w", err)
		}
	}

	// Получаем мета-данные последнего релиза (с retry при сетевых ошибках)
	send(Progress{Stage: "fetch_meta", Message: "Получаем информацию о последнем релизе...", Percent: 5})
	var asset githubAsset
	var version string
	fetchAttempt := 0
	if fetchErr := withRetry(ctx, 3, func() error {
		fetchAttempt++
		if fetchAttempt > 1 {
			send(Progress{
				Stage:   "fetch_meta",
				Message: fmt.Sprintf("Попытка %d/3: получаем информацию о релизе...", fetchAttempt),
				Percent: 5,
			})
		}
		var e error
		asset, version, e = fetchLatestAsset(ctx)
		return e
	}); fetchErr != nil {
		// ВЫС-4: fallback — пробуем извлечь версию из HTML-редиректа GitHub
		send(Progress{Stage: "fetch_meta", Message: "GitHub API недоступен, пробуем fallback...", Percent: 5})
		fallbackClient := &http.Client{Timeout: 15 * time.Second, Transport: noProxyTransport}
		if fallbackAsset, fallbackVer, fallbackErr := fetchLatestAssetFallback(ctx, fallbackClient); fallbackErr == nil {
			asset = fallbackAsset
			version = fallbackVer
		} else {
			e := fmt.Errorf("не удалось получить информацию о релизе: %w (fallback: %v)", fetchErr, fallbackErr)
			send(Progress{Stage: "error", Message: e.Error(), Err: e})
			return e
		}
	}
	send(Progress{Stage: "fetch_meta", Message: fmt.Sprintf("Найден sing-box %s", version), Percent: 10, Version: version})

	// Скачиваем (с retry при сетевых ошибках)
	send(Progress{Stage: "download", Message: fmt.Sprintf("Скачиваем sing-box %s...", version), Percent: 15, Version: version})
	var zipData []byte
	downloadAttempt := 0
	if dlErr := withRetry(ctx, 3, func() error {
		downloadAttempt++
		if downloadAttempt > 1 {
			send(Progress{
				Stage:   "download",
				Message: fmt.Sprintf("Попытка %d/3: скачиваем sing-box %s...", downloadAttempt, version),
				Percent: 15,
				Version: version,
			})
		}
		var e error
		zipData, e = downloadWithProgress(ctx, asset.DownloadURL, func(downloaded, total int64) {
			pct := 15
			if total > 0 {
				pct = 15 + int(float64(downloaded)/float64(total)*70)
			}
			send(Progress{
				Stage:   "download",
				Message: fmt.Sprintf("Скачиваем sing-box %s (%s / %s)", version, fmtBytes(downloaded), fmtBytes(total)),
				Percent: pct,
				Version: version,
			})
		})
		return e
	}); dlErr != nil {
		e := fmt.Errorf("ошибка загрузки: %w", dlErr)
		send(Progress{Stage: "error", Message: e.Error(), Err: e})
		return e
	}

	// Верифицируем SHA256 перед распаковкой
	send(Progress{Stage: "extract", Message: "Проверяем контрольную сумму...", Percent: 87, Version: version})
	if asset.Checksum == "" {
		send(Progress{Stage: "extract", Message: "⚠️ .sha256 не найден в релизе — верификация пропущена", Percent: 87, Version: version})
	}
	if err := verifyChecksum(zipData, asset.Checksum); err != nil {
		e := fmt.Errorf("ошибка верификации: %w", err)
		send(Progress{Stage: "error", Message: e.Error(), Err: e})
		return e
	}

	// Извлекаем sing-box.exe из zip
	send(Progress{Stage: "extract", Message: "Распаковываем...", Percent: 88, Version: version})
	if err := extractExeFromZip(zipData, execPath); err != nil {
		e := fmt.Errorf("ошибка извлечения: %w", err)
		send(Progress{Stage: "error", Message: e.Error(), Err: e})
		return e
	}

	// Санитарная проверка: запускаем "sing-box version" чтобы убедиться что бинарник
	// исполняемый. Защита от повреждённых zip-архивов когда .sha256 недоступен.
	//
	// Retry важен: Windows Defender и другие AV сканируют свежескачанный .exe при первом
	// запуске и могут вернуть 0xc0000005 (ACCESS VIOLATION) на первой попытке.
	// Облачная проверка SmartScreen занимает до 2 минут — используем 30 попыток × 5s = 150s.
	// При полном провале — удаляем повреждённый файл и возвращаем ошибку: позволяет
	// пользователю увидеть правильную диагностику и при следующем запуске NeedsDownload
	// обнаружит отсутствие файла и перескачает бинарник заново.
	send(Progress{Stage: "extract", Message: "Проверяем бинарник...", Percent: 95, Version: version})
	{
		const verifyAttempts = 30
		verifyDelay := 5 * retryBaseDelay
		var verifyErr error
		for i := 1; i <= verifyAttempts; i++ {
			verifyErr = verifySingBoxBinaryFn(ctx, execPath)
			if verifyErr == nil {
				break
			}
			if i < verifyAttempts {
				send(Progress{
					Stage:   "extract",
					Message: fmt.Sprintf("Бинарник ещё не готов (попытка %d/%d), ждём %v...", i, verifyAttempts, verifyDelay),
					Percent: 95, Version: version,
				})
				select {
				case <-time.After(verifyDelay):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		if verifyErr != nil {
			_ = os.Remove(execPath)
			e := fmt.Errorf("бинарник повреждён или несовместим (%w) — файл удалён, попробуйте перезапустить", verifyErr)
			send(Progress{Stage: "error", Message: e.Error(), Err: e})
			return e
		}
	}

	// A-3: атомарно сохраняем версию рядом с exe для InstalledVersion()
	// FIX 52: fileutil.WriteAtomic предотвращает повреждение файла при сбое питания.
	if err := fileutil.WriteAtomic(versionFilePath(execPath), []byte(version), 0644); err != nil {
		send(Progress{Stage: "warn", Message: fmt.Sprintf("Не удалось сохранить версию sing-box: %v", err), Percent: 99, Version: version})
	}

	send(Progress{Stage: "done", Message: fmt.Sprintf("sing-box %s готов ✓", version), Percent: 100, Version: version})
	return nil
}

// ── GitHub API ────────────────────────────────────────────────────────────────

type githubAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
	// Checksum заполняется из отдельного .sha256 asset'а (если есть в релизе).
	Checksum string `json:"-"`
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

func fetchLatestAsset(ctx context.Context) (githubAsset, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI, nil)
	if err != nil {
		return githubAsset{}, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{Timeout: 15 * time.Second, Transport: noProxyTransport}
	resp, err := client.Do(req)
	if err != nil {
		return githubAsset{}, "", fmt.Errorf("GitHub API недоступен: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return githubAsset{}, "", fmt.Errorf("GitHub API вернул %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubAsset{}, "", fmt.Errorf("ошибка разбора ответа GitHub: %w", err)
	}

	// Ищем zip для windows-amd64 и соответствующий .sha256 asset
	var found *githubAsset
	checksumURLs := map[string]string{} // zipName → sha256DownloadURL

	var sha256SumsURL string
	for i, a := range release.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".sha256") {
			// sing-box-1.2.3-windows-amd64.zip.sha256 → ключ без .sha256
			base := a.Name[:len(a.Name)-7]
			checksumURLs[strings.ToLower(base)] = a.DownloadURL
		}
		// БАГ-5: поддержка глобального SHA256SUMS файла (sing-box v1.9+).
		// n уже lowercase — покрываем SHA256SUMS, sha256sum, sha256sums.txt и варианты без 's'.
		if n == "sha256sums" || n == "sha256sums.txt" || n == "sha256sum" || n == "sha256sum.txt" {
			sha256SumsURL = a.DownloadURL
		}
		if strings.Contains(n, assetOS) && strings.Contains(n, assetArch) && strings.HasSuffix(n, ".zip") {
			found = &release.Assets[i]
		}
	}

	if found == nil {
		return githubAsset{}, "", fmt.Errorf("не найден asset windows-amd64 в релизе %s", release.TagName)
	}

	// Подтягиваем SHA256 если он есть в релизе
	if shaURL, ok := checksumURLs[strings.ToLower(found.Name)]; ok {
		if sum, err := fetchChecksumFile(ctx, shaURL, client); err == nil {
			found.Checksum = sum
		}
		// Ошибка получения контрольной суммы не блокирует загрузку —
		// но будет задокументирована в verifyChecksum.
	}
	// БАГ-2: fallback на SHA256SUMS файл если per-file .sha256 не найден (sing-box v1.9+)
	if found.Checksum == "" && sha256SumsURL != "" {
		if sum, err := fetchChecksumFromSums(ctx, sha256SumsURL, found.Name, client); err == nil {
			found.Checksum = sum
		}
	}

	return *found, release.TagName, nil
}

// fetchLatestAssetFallback конструирует URL для скачивания pinned версии напрямую,
// без HTTP-запросов к GitHub API. Используется когда GitHub API недоступен (rate limit, блокировка).
//
// БАГ 5: ранее функция не заполняла asset.Checksum — верификация SHA256 всегда пропускалась
// в fallback-пути. Теперь пробуем скачать sha256sums.txt и заполнить Checksum.
// Если sha256sums.txt недоступен — логируем WARN и продолжаем без верификации.
func fetchLatestAssetFallback(ctx context.Context, client *http.Client) (githubAsset, string, error) {
	version := pinnedVersion
	ver := strings.TrimPrefix(version, "v")
	zipName := fmt.Sprintf("sing-box-%s-%s-%s.zip", ver, assetOS, assetArch)
	downloadURL := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/%s", version, zipName)
	a := githubAsset{
		Name:        zipName,
		DownloadURL: downloadURL,
	}

	// БАГ 5: пробуем получить SHA256 из sha256sums.txt того же релиза.
	sumsURL := fmt.Sprintf("https://github.com/SagerNet/sing-box/releases/download/%s/sha256sums.txt", version)
	if sum, err := fetchChecksumFromSums(ctx, sumsURL, zipName, client); err == nil {
		a.Checksum = sum
	}
	// При ошибке (файл недоступен, сеть заблокирована) — продолжаем без верификации.
	// verifyChecksum залогирует предупреждение если Checksum == "".

	return a, version, nil
}

// fetchChecksumFile скачивает .sha256-файл и возвращает hex-строку контрольной суммы.
// Формат файла: "<hex64>  filename" (стандартный sha256sum).
func fetchChecksumFile(ctx context.Context, url string, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	// Берём первые 64 символа (hex SHA256)
	line := strings.TrimSpace(string(body))
	if len(line) >= 64 {
		return strings.ToLower(line[:64]), nil
	}
	return "", fmt.Errorf("неожиданный формат .sha256: %q", line)
}

// fetchChecksumFromSums парсит SHA256SUMS файл формата "sha256sum" и возвращает
// контрольную сумму для указанного файла.
// Формат строки: "<hex64>  <filename>" или "<hex64> *<filename>" (binary mode).
func fetchChecksumFromSums(ctx context.Context, url string, filename string, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	targetLower := strings.ToLower(filename)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 66 {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		checksum := strings.ToLower(parts[0])
		name := strings.TrimPrefix(strings.ToLower(parts[1]), "*")
		if name == targetLower && len(checksum) == 64 {
			return checksum, nil
		}
	}
	return "", fmt.Errorf("файл %q не найден в SHA256SUMS", filename)
}

// verifyChecksum сравнивает SHA256 данных с ожидаемой суммой.
// Если ожидаемая сумма пуста — верификация пропускается с предупреждением.
func verifyChecksum(data []byte, expected string) error {
	if expected == "" {
		// .sha256 asset не был найден в релизе — продолжаем без верификации.
		// Это допустимо для старых релизов, но логируется в вызывающей стороне.
		return nil
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("контрольная сумма не совпадает: ожидалось %s, получено %s", expected, got)
	}
	return nil
}

// ── Download ─────────────────────────────────────────────────────────────────

func downloadWithProgress(ctx context.Context, url string, onProgress func(downloaded, total int64)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: httpTimeout, Transport: noProxyTransport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d при скачивании", resp.StatusCode)
	}

	total := resp.ContentLength
	var buf bytes.Buffer
	buf.Grow(int(max64(total, 8*1024*1024)))

	reader := &progressReader{r: resp.Body, total: total, onProgress: onProgress}
	if _, err := io.Copy(&buf, reader); err != nil {
		return nil, fmt.Errorf("ошибка чтения: %w", err)
	}
	return buf.Bytes(), nil
}

type progressReader struct {
	r            io.Reader
	downloaded   int64
	total        int64
	lastReported int64
	onProgress   func(int64, int64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	pr.downloaded += int64(n)
	// Репортим прогресс не чаще чем каждые 256KB — иначе лог засоряется сотнями строк
	if pr.onProgress != nil && pr.downloaded-pr.lastReported >= 256*1024 {
		pr.lastReported = pr.downloaded
		pr.onProgress(pr.downloaded, pr.total)
	}
	return n, err
}

// ── Zip extraction ────────────────────────────────────────────────────────────

func extractExeFromZip(data []byte, destPath string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("неверный zip: %w", err)
	}

	for _, f := range r.File {
		if strings.HasSuffix(strings.ToLower(f.Name), "sing-box.exe") {
			return extractFile(f, destPath)
		}
	}
	return fmt.Errorf("sing-box.exe не найден в архиве")
}

func extractFile(f *zip.File, destPath string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// Резолвим абсолютный путь — os.Rename на Windows не работает с "./"-путями
	absDest, err := filepath.Abs(destPath)
	if err != nil {
		absDest = destPath // fallback
	}

	// Гарантируем существование директории
	if dir := filepath.Dir(absDest); dir != "" {
		if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
			return fmt.Errorf("не удалось создать директорию %s: %w", dir, mkErr)
		}
	}

	tmp := absDest + ".download"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл %s: %w", tmp, err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("ошибка записи: %w", err)
	}
	out.Close()

	// Удаляем старый если есть, переименовываем
	_ = os.Remove(absDest)
	if err := os.Rename(tmp, absDest); err != nil {
		// Fallback: copy+delete (на случай cross-drive rename на Windows)
		if copyErr := copyFile(tmp, absDest); copyErr != nil {
			os.Remove(tmp)
			return fmt.Errorf("ошибка переименования и копирования: rename=%w, copy=%v", err, copyErr)
		}
		os.Remove(tmp)
	}
	return nil
}

// copyFile копирует файл побайтово (fallback когда os.Rename не работает)
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// verifySingBoxBinaryFn — var (не func-literal) чтобы тесты могли подменить на no-op
// (аналог githubAPI и retryBaseDelay). В продакшене всегда равен verifySingBoxBinary.
var verifySingBoxBinaryFn = verifySingBoxBinary

// verifySingBoxBinary запускает "sing-box version" и проверяет что бинарник исполняемый.
// Защита от повреждённых архивов когда .sha256 недоступен в релизе.
func verifySingBoxBinary(ctx context.Context, execPath string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(timeoutCtx, execPath, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("'%s version' завершился с ошибкой: %w (вывод: %s)", execPath, err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(strings.ToLower(string(out)), "sing-box") {
		return fmt.Errorf("неожиданный вывод 'sing-box version': %q", strings.TrimSpace(string(out)))
	}
	return nil
}

func fmtBytes(b int64) string {
	if b < 0 {
		return "?"
	}
	if b < 1024 {
		return fmt.Sprintf("%dB", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(b)/1024/1024)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
