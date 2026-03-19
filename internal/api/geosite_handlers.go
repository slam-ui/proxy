package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"proxyclient/internal/config"
	"strings"
	"time"
)

// KnownGeosite — список популярных geosite категорий
type KnownGeosite struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Только те имена, которые точно есть хотя бы в одном источнике
// Только подтверждённые имена из SagerNet/sing-geosite
var knownGeosites = []KnownGeosite{
	// Уже есть у пользователя (100% рабочие)
	{Name: "youtube", Description: "YouTube"},
	{Name: "discord", Description: "Discord"},
	{Name: "instagram", Description: "Instagram"},
	{Name: "tiktok", Description: "TikTok"},
	{Name: "spotify", Description: "Spotify"},
	{Name: "reddit", Description: "Reddit"},
	{Name: "soundcloud", Description: "SoundCloud"},
	// Стандартные имена из v2fly/domain-list-community (основа sing-geosite)
	{Name: "google", Description: "Google сервисы"},
	{Name: "github", Description: "GitHub"},
	{Name: "twitter", Description: "Twitter / X"},
	{Name: "facebook", Description: "Facebook"},
	{Name: "netflix", Description: "Netflix"},
	{Name: "twitch", Description: "Twitch"},
	{Name: "amazon", Description: "Amazon"},
	{Name: "microsoft", Description: "Microsoft"},
	{Name: "apple", Description: "Apple"},
	{Name: "cn", Description: "Китайские сайты"},
	{Name: "geolocation-!cn", Description: "Не-Китайские сайты"},
	{Name: "category-ads-all", Description: "Реклама (все)"},
}

// Несколько источников SRS файлов, пробуем по очереди
var geositeSources = []string{
	// rule-set branch — файлы доступны напрямую без редиректа
	"https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-%s.srs",
	// releases fallback
	"https://github.com/SagerNet/sing-geosite/releases/latest/download/geosite-%s.srs",
	"https://github.com/Loyalsoldier/sing-box-rules/releases/latest/download/geosite-%s.srs",
}

type GeositeInfo struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	FileSize  int64  `json:"file_size,omitempty"`
}

type GeositeListResponse struct {
	Items []GeositeInfo `json:"items"`
}

func (s *Server) handleGeositeList(w http.ResponseWriter, r *http.Request) {
	// BUG FIX: файлы geosite хранятся в data/ подпапке (config.DataDir)

	knownNames := map[string]bool{}
	for _, kg := range knownGeosites {
		knownNames[kg.Name] = true
	}

	items := make([]GeositeInfo, 0, len(knownGeosites))
	for _, kg := range knownGeosites {
		info := GeositeInfo{Name: kg.Name}
		binPath := filepath.Join(config.DataDir, "geosite-"+kg.Name+".bin")
		if fi, err := os.Stat(binPath); err == nil {
			info.Available = true
			info.FileSize = fi.Size()
		}
		items = append(items, info)
	}

	// Добавляем локальные файлы которых нет в known list
	entries, _ := filepath.Glob(filepath.Join(config.DataDir, "geosite-*.bin"))
	for _, path := range entries {
		base := filepath.Base(path)
		name := strings.TrimPrefix(strings.TrimSuffix(base, ".bin"), "geosite-")
		if !knownNames[name] {
			fi, _ := os.Stat(path)
			sz := int64(0)
			if fi != nil {
				sz = fi.Size()
			}
			items = append(items, GeositeInfo{Name: name, Available: true, FileSize: sz})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GeositeListResponse{Items: items})
}

type GeositeDownloadRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleGeositeDownload(w http.ResponseWriter, r *http.Request) {
	var req GeositeDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		http.Error(w, `{"error":"invalid name"}`, http.StatusBadRequest)
		return
	}

	// BUG FIX: файлы geosite должны сохраняться в data/ подпапке,
	// именно оттуда config/singbox.go читает пути при генерации конфига.
	// Ранее файлы сохранялись в корень дистрибутива → скачанные geosite никогда не работали.
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		http.Error(w, `{"error":"не удалось создать data/ директорию"}`, http.StatusInternalServerError)
		return
	}
	destPath := filepath.Join(config.DataDir, "geosite-"+name+".bin")

	// Пробуем источники по очереди
	// BUG FIX: http.Get использует дефолтный клиент без таймаута.
	// При медленном или недоступном сервере запрос висел вечно, блокируя горутину.
	dlClient := &http.Client{Timeout: 30 * time.Second}

	var data []byte
	var lastErr string
	for _, urlFmt := range geositeSources {
		url := fmt.Sprintf(urlFmt, name)
		resp, err := dlClient.Get(url)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		// Явное закрытие вместо defer: defer внутри цикла не закрывает
		// body до выхода из функции, накапливая открытые соединения.
		if resp.StatusCode == 404 {
			resp.Body.Close()
			lastErr = fmt.Sprintf("404 в %s", url)
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Sprintf("HTTP %d в %s", resp.StatusCode, url)
			continue
		}
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err.Error()
			continue
		}
		break // успех
	}

	if data == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("geosite-%s не найден ни в одном источнике (%s)", name, lastErr),
		})
		return
	}

	if err := os.WriteFile(destPath, data, 0644); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "ошибка записи: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"name": name,
		"size": len(data),
	})
}
