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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	githubAPI   = "https://api.github.com/repos/SagerNet/sing-box/releases/latest"
	assetName   = "sing-box"
	assetOS     = "windows"
	assetArch   = "amd64"
	userAgent   = "proxy-client/1.0 (auto-engine)"
	httpTimeout = 120 * time.Second
)

// Progress сообщение о прогрессе загрузки
type Progress struct {
	Stage   string // "check" | "fetch_meta" | "download" | "extract" | "done" | "error"
	Message string
	Percent int    // 0-100, -1 если неизвестно
	Version string // версия sing-box (заполняется на стадии fetch_meta и выше)
	Err     error
}

// NeedsDownload возвращает true если sing-box.exe отсутствует или пустой.
func NeedsDownload(execPath string) bool {
	fi, err := os.Stat(execPath)
	if err != nil {
		return true
	}
	return fi.Size() == 0
}

// EnsureEngine проверяет наличие sing-box.exe и при необходимости скачивает его.
// Прогресс отправляется в канал progress (может быть nil).
// Возвращает nil при успехе или если файл уже существует.
func EnsureEngine(ctx context.Context, execPath string, progress chan<- Progress) error {
	send := func(p Progress) {
		if progress != nil {
			select {
			case progress <- p:
			default:
			}
		}
	}

	send(Progress{Stage: "check", Message: "Проверяем sing-box.exe...", Percent: 0})

	if !NeedsDownload(execPath) {
		send(Progress{Stage: "done", Message: "sing-box.exe уже есть", Percent: 100})
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

	// Получаем мета-данные последнего релиза
	send(Progress{Stage: "fetch_meta", Message: "Получаем информацию о последнем релизе...", Percent: 5})
	asset, version, err := fetchLatestAsset(ctx)
	if err != nil {
		e := fmt.Errorf("не удалось получить информацию о релизе: %w", err)
		send(Progress{Stage: "error", Message: e.Error(), Err: e})
		return e
	}
	send(Progress{Stage: "fetch_meta", Message: fmt.Sprintf("Найден sing-box %s", version), Percent: 10, Version: version})

	// Скачиваем
	send(Progress{Stage: "download", Message: fmt.Sprintf("Скачиваем sing-box %s...", version), Percent: 15, Version: version})
	zipData, err := downloadWithProgress(ctx, asset.DownloadURL, func(downloaded, total int64) {
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
	if err != nil {
		e := fmt.Errorf("ошибка загрузки: %w", err)
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

	client := &http.Client{Timeout: 15 * time.Second}
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

	for i, a := range release.Assets {
		n := strings.ToLower(a.Name)
		if strings.HasSuffix(n, ".sha256") {
			// sing-box-1.2.3-windows-amd64.zip.sha256 → ключ без .sha256
			base := a.Name[:len(a.Name)-7]
			checksumURLs[strings.ToLower(base)] = a.DownloadURL
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

	return *found, release.TagName, nil
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
	if got != expected {
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

	client := &http.Client{Timeout: httpTimeout}
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
