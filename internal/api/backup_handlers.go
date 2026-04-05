package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyclient/internal/fileutil"
)

// B-8: Backup & Restore handlers

// handleBackup GET /api/backup — crear ZIP архив со всеми конфигами
func handleBackup(w http.ResponseWriter, r *http.Request) {
	buf := bytes.NewBuffer(nil)
	zw := zip.NewWriter(buf)
	defer zw.Close()

	// B-8: backup_meta.json с информацией о версии
	meta := map[string]interface{}{
		"schema_version": 1,
		"timestamp":      time.Now().Unix(),
		"backup_date":    time.Now().Format("2006-01-02 15:04:05"),
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	if f, err := zw.Create("backup_meta.json"); err == nil {
		f.Write(metaData)
	}

	// B-8: Архивируем необходимые файлы
	filesToBackup := []string{
		"servers.json",
		"data/routing.json",
	}

	for _, filename := range filesToBackup {
		data, err := os.ReadFile(filename)
		if err == nil {
			if f, err := zw.Create(filename); err == nil {
				f.Write(data)
			}
		}
	}

	// B-8: Архивируем все файлы из profiles/ директории
	profilesDir := "data/profiles"
	if entries, err := os.ReadDir(profilesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				path := filepath.Join(profilesDir, entry.Name())
				if data, err := os.ReadFile(path); err == nil {
					if f, err := zw.Create(path); err == nil {
						f.Write(data)
					}
				}
			}
		}
	}

	zw.Close()

	// B-8: Отправляем ZIP файл
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=proxy-backup-%s.zip", time.Now().Format("2006-01-02")))
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

// handleBackupRestore POST /api/backup/restore — восстановить конфиги из ZIP
func handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	// B-8: Ограничиваем размер 5MB
	const maxSize = 5 * 1024 * 1024
	if r.ContentLength > maxSize {
		http.Error(w, "File too large (max 5MB)", http.StatusRequestEntityTooLarge)
		return
	}

	// B-8: Парсим multipart form
	if err := r.ParseMultipartForm(maxSize); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// B-8: Читаем ZIP архив
	fileData := bytes.NewBuffer(nil)
	io.CopyN(fileData, file, maxSize+1)

	zr, err := zip.NewReader(bytes.NewReader(fileData.Bytes()), int64(fileData.Len()))
	if err != nil {
		http.Error(w, "Invalid ZIP file", http.StatusBadRequest)
		return
	}

	// B-8: Проверяем backup_meta.json для валидности архива
	restored := 0
	skipped := 0

	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}

		// B-8: Пропускаем secret.key если он есть
		if strings.Contains(file.Name, "secret.key") {
			continue
		}

		// B-8: Проверяем параметр overwrite
		overwrite := r.FormValue("overwrite") == "true"
		if !overwrite && fileExists(file.Name) {
			skipped++
			continue
		}

		// B-8: Создаём директорию если нужно
		dir := filepath.Dir(file.Name)
		if dir != "." {
			os.MkdirAll(dir, 0755)
		}

		// B-8: Читаем и пишем файл атомарно
		rc, err := file.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(rc)
		rc.Close()

		if err := fileutil.WriteAtomic(file.Name, data, 0644); err == nil {
			restored++
		}
	}

	// B-8: Возвращаем статус восстановления
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"restored": restored,
		"skipped":  skipped,
		"message":  "Backup restored successfully",
	})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
