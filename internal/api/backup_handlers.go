package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
)

// B-8: Backup & Restore handlers

// handleBackup GET /api/backup — crear ZIP архив со всеми конфигами
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	buf := bytes.NewBuffer(nil)
	zw := zip.NewWriter(buf)
	// БАГ 6: defer zw.Close() удалён — он вызывался ПОСЛЕ явного Close ниже,
	// что приводило к двойной записи центрального каталога ZIP → невалидный архив.

	// B-8: backup_meta.json с информацией о версии
	meta := map[string]interface{}{
		"schema_version": 1,
		"timestamp":      time.Now().Unix(),
		"backup_date":    time.Now().Format("2006-01-02 15:04:05"),
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		http.Error(w, "Failed to encode backup metadata", http.StatusInternalServerError)
		return
	}
	if f, err := zw.Create("backup_meta.json"); err == nil {
		if _, err := f.Write(metaData); err != nil {
			http.Error(w, "Failed to write backup metadata", http.StatusInternalServerError)
			return
		}
	} else {
		http.Error(w, "Failed to create backup metadata", http.StatusInternalServerError)
		return
	}

	// B-8: Архивируем необходимые файлы
	filesToBackup := []string{
		"servers.json",
		"data/routing.json",
	}

	for _, filename := range filesToBackup {
		data, err := os.ReadFile(filename)
		if err == nil {
			f, err := zw.Create(filename)
			if err != nil {
				http.Error(w, "Failed to add file to ZIP", http.StatusInternalServerError)
				return
			}
			if _, err := f.Write(data); err != nil {
				http.Error(w, "Failed to write file to ZIP", http.StatusInternalServerError)
				return
			}
		}
	}

	// FIX 43: profiles хранятся в profiles/ (не data/profiles/).
	// B-8: Архивируем все файлы из profiles/ директории
	profilesDir := "profiles"
	if entries, err := os.ReadDir(profilesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				path := filepath.Join(profilesDir, entry.Name())
				if data, err := os.ReadFile(path); err == nil {
					f, err := zw.Create(path)
					if err != nil {
						http.Error(w, "Failed to add profile to ZIP", http.StatusInternalServerError)
						return
					}
					if _, err := f.Write(data); err != nil {
						http.Error(w, "Failed to write profile to ZIP", http.StatusInternalServerError)
						return
					}
				}
			}
		}
	}

	// БАГ 6: проверяем ошибку Close — повреждённый архив не отправляем клиенту.
	if err := zw.Close(); err != nil {
		http.Error(w, "Failed to finalize ZIP", http.StatusInternalServerError)
		return
	}

	// B-8: Отправляем ZIP файл
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=proxy-backup-%s.zip", time.Now().Format("2006-01-02")))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		s.logger.Warn("handleBackup: write response: %v", err)
	}
}

// handleBackupRestore POST /api/backup/restore — восстановить конфиги из ZIP
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	// B-8: Ограничиваем размер 5MB
	const maxSize = 5 * 1024 * 1024

	// Сначала проверяем Content-Length (быстрый ранний выход).
	if r.ContentLength > maxSize {
		http.Error(w, "File too large (max 5MB)", http.StatusRequestEntityTooLarge)
		return
	}

	// MaxBytesReader ограничивает тело запроса даже если Content-Length не задан.
	// +32KB — запас на multipart-заголовки и boundary.
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+32*1024)

	// B-8: Парсим multipart form
	if err := r.ParseMultipartForm(maxSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "File too large (max 5MB)", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
		}
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// B-8: Читаем ZIP архив с проверкой размера.
	// io.CopyN читает до maxSize+1 байт — если скопировано больше maxSize, файл слишком большой.
	fileData := bytes.NewBuffer(nil)
	n, _ := io.CopyN(fileData, file, int64(maxSize)+1)
	if n > int64(maxSize) {
		http.Error(w, "File too large (max 5MB)", http.StatusRequestEntityTooLarge)
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(fileData.Bytes()), int64(fileData.Len()))
	if err != nil {
		http.Error(w, "Invalid ZIP file", http.StatusBadRequest)
		return
	}

	// FIX 44: Валидируем backup_meta.json перед восстановлением.
	// Защищаемся от случайных или повреждённых ZIP-архивов.
	metaValid := false
	for _, f := range zr.File {
		if f.Name == "backup_meta.json" {
			rc, err := f.Open()
			if err != nil {
				break
			}
			raw, readErr := io.ReadAll(io.LimitReader(rc, 4096))
			if closeErr := rc.Close(); closeErr != nil {
				s.logger.Warn("handleBackupRestore: close metadata: %v", closeErr)
			}
			if readErr != nil {
				break
			}
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil {
				if sv, ok := m["schema_version"]; ok {
					if v, ok := sv.(float64); ok {
						metaValid = v == 1
					}
				}
			}
			break
		}
	}
	if !metaValid {
		http.Error(w, "Invalid backup: missing or incompatible schema_version in backup_meta.json", http.StatusBadRequest)
		return
	}

	// FIX 42: вычисляем абсолютный путь рабочей директории для Zip Slip защиты.
	workDir, err := filepath.Abs(".")
	if err != nil {
		http.Error(w, "Internal error: cannot determine work dir", http.StatusInternalServerError)
		return
	}

	restored := 0
	skipped := 0
	overwrite := r.FormValue("overwrite") == "true"

	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}

		// B-8: Пропускаем secret.key если он есть
		if strings.Contains(file.Name, "secret.key") {
			continue
		}

		// FIX 42: Zip Slip защита — проверяем что итоговый путь внутри рабочей директории.
		cleanName := filepath.Clean(file.Name)
		if cleanName == "." || filepath.IsAbs(cleanName) || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) || cleanName == ".." {
			skipped++
			continue
		}
		destPath := filepath.Join(workDir, cleanName)
		absDestPath, absErr := filepath.Abs(destPath)
		rel, relErr := filepath.Rel(workDir, absDestPath)
		if absErr != nil || relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			// Путь выходит за пределы рабочей директории — пропускаем.
			skipped++
			continue
		}

		// B-8: Проверяем параметр overwrite
		if !overwrite && fileExists(absDestPath) {
			skipped++
			continue
		}

		// B-8: Создаём директорию если нужно
		if dir := filepath.Dir(absDestPath); dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				skipped++
				continue
			}
		}

		// B-8: Читаем и пишем файл атомарно.
		// FIX Bug5: ограничиваем размер распакованного файла — защита от zip bomb.
		// Сжатый архив 5MB может раскрыться в гигабайты без LimitReader → OOM.
		const maxExtractedFileSize = 10 * 1024 * 1024 // 10 MB на один файл
		rc, err := file.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxExtractedFileSize+1))
		if closeErr := rc.Close(); closeErr != nil {
			s.logger.Warn("handleBackupRestore: close %s: %v", file.Name, closeErr)
		}
		if err != nil {
			skipped++
			continue
		}
		if int64(len(data)) > maxExtractedFileSize {
			skipped++
			continue
		}
		if info, err := os.Lstat(absDestPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			skipped++
			continue
		}
		if pathHasSymlink(workDir, absDestPath) {
			skipped++
			continue
		}

		if err := fileutil.WriteAtomic(absDestPath, data, 0644); err == nil {
			restored++
		}
	}

	// CHANGE-16: После восстановления файлов перезагружаем in-memory routing из disk
	// и применяем конфиг — иначе tunHandlers продолжает работать со старыми правилами.
	// Условие: tunHandlers инициализирован (TUN запущен) и routing.json был среди файлов.
	if s.tunHandlers != nil {
		routingPath := config.DataDir + "/routing.json"
		if newRouting, err := config.LoadRoutingConfig(routingPath); err == nil {
			s.tunHandlers.mu.Lock()
			s.tunHandlers.routing = newRouting
			s.tunHandlers.mu.Unlock()
			if applyErr := s.tunHandlers.TriggerApply(); applyErr != nil {
				s.logger.Warn("handleBackupRestore: TriggerApply: %v", applyErr)
			} else {
				s.logger.Info("handleBackupRestore: routing перезагружен и применён из восстановлённого backup")
			}
		} else {
			s.logger.Warn("handleBackupRestore: не удалось загрузить routing после restore: %v", err)
		}
	}

	// B-8: Возвращаем статус восстановления
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"restored": restored,
		"skipped":  skipped,
		"message":  "Backup restored successfully",
	}); err != nil {
		s.logger.Debug("handleBackupRestore: encode response: %v", err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pathHasSymlink(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		if err != nil && !os.IsNotExist(err) {
			return true
		}
	}
	return false
}
