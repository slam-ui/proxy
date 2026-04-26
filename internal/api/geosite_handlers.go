package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"proxyclient/internal/config"
	"proxyclient/internal/fileutil"
	"strings"
	"time"
)

// KnownGeosite — список популярных geosite категорий
type KnownGeosite struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Только подтверждённые имена из SagerNet/sing-geosite rule-set branch.
// discord/reddit/soundcloud/spotify/tiktok удалены — .srs файлов нет ни в одном источнике
// (проверено по 404 во всех трёх источниках geositeSources).
var knownGeosites = []KnownGeosite{
	// Подтверждённые имена (v2fly/domain-list-community → sing-geosite)
	{Name: "youtube", Description: "YouTube"},
	{Name: "instagram", Description: "Instagram"},
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
	{Name: "telegram", Description: "Telegram"},
	{Name: "openai", Description: "OpenAI / ChatGPT / Codex"},
	{Name: "anthropic", Description: "Anthropic / Claude API"},
	{Name: "pinterest", Description: "Pinterest"},
}

// Несколько источников SRS файлов, пробуем по очереди
var geositeSources = []string{
	// rule-set branch — файлы доступны напрямую без редиректа
	"https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-%s.srs",
	"https://raw.githubusercontent.com/lyc8503/sing-box-rules/rule-set-geosite/geosite-%s.srs",
}

var geositeProxyAddr = config.ProxyAddr

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
			info.FileSize = fi.Size()
			info.Available = config.IsSingBoxRuleSetFile(binPath)
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
			items = append(items, GeositeInfo{
				Name:      name,
				Available: config.IsSingBoxRuleSetFile(path),
				FileSize:  sz,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(GeositeListResponse{Items: items})
}

type GeositeDownloadRequest struct {
	Name  string `json:"name"`
	Apply *bool  `json:"apply,omitempty"`
}

func isValidGeositeName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '!' || r == '+' {
			continue
		}
		return false
	}
	return true
}

func geositeRuleNamesFromConfig(routing *config.RoutingConfig) []string {
	if routing == nil {
		return nil
	}
	seen := map[string]bool{}
	names := make([]string, 0)
	for _, rule := range routing.Rules {
		val := strings.ToLower(strings.TrimSpace(rule.Value))
		if rule.Type != config.RuleTypeGeosite && !strings.HasPrefix(val, "geosite:") {
			continue
		}
		name := strings.TrimPrefix(val, "geosite:")
		if !isValidGeositeName(name) || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func (s *Server) geositeRuleNames() []string {
	if s.tunHandlers != nil {
		s.tunHandlers.mu.RLock()
		routing := &config.RoutingConfig{
			DefaultAction: s.tunHandlers.routing.DefaultAction,
			Rules:         make([]config.RoutingRule, len(s.tunHandlers.routing.Rules)),
			BypassEnabled: s.tunHandlers.routing.BypassEnabled,
			DNS:           s.tunHandlers.routing.DNS,
		}
		copy(routing.Rules, s.tunHandlers.routing.Rules)
		s.tunHandlers.mu.RUnlock()
		return geositeRuleNamesFromConfig(routing)
	}

	routing, err := config.LoadRoutingConfig(filepath.Join(config.DataDir, "routing.json"))
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("geosite: не удалось прочитать routing.json: %v", err)
		}
		return nil
	}
	return geositeRuleNamesFromConfig(routing)
}

func (s *Server) handleGeositeDownload(w http.ResponseWriter, r *http.Request) {
	var req GeositeDownloadRequest
	decodeErr := json.NewDecoder(r.Body).Decode(&req)
	if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		s.respondError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}
	shouldApply := req.Apply == nil || *req.Apply

	// Пустое тело или {"name":""} = обновить только geosite, реально используемые в правилах.
	// Раньше это скачивало весь knownGeosites, что создавало лишние файлы и 404-ошибки.
	if strings.TrimSpace(req.Name) == "" {
		names := s.geositeRuleNames()
		var updated, errs []string
		for _, name := range names {
			select {
			case <-r.Context().Done():
				// клиент отключился — прерываем
				return
			default:
			}
			if err := downloadGeositeFile(r.Context(), name); err != nil {
				errs = append(errs, name+": "+err.Error())
			} else {
				updated = append(updated, name)
			}
		}
		// BUG-NEW-1 FIX: применяем конфиг если хотя бы один файл обновлён.
		bulkApplyErr := ""
		if shouldApply && len(updated) > 0 && s.tunHandlers != nil {
			if err := s.tunHandlers.TriggerApply(); err != nil {
				s.logger.Warn("handleGeositeDownload bulk: TriggerApply: %v", err)
				bulkApplyErr = err.Error()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":          len(errs) == 0,
			"requested":   names,
			"updated":     updated,
			"errors":      errs,
			"apply_error": bulkApplyErr,
		})
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !isValidGeositeName(name) {
		http.Error(w, `{"error":"invalid name"}`, http.StatusBadRequest)
		return
	}

	if err := downloadGeositeFile(r.Context(), name); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrGeositeNotFound) {
			status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	size := int64(0)
	if fi, err := os.Stat(filepath.Join(config.DataDir, "geosite-"+name+".bin")); err == nil {
		size = fi.Size()
	}

	// BUG-NEW-1 FIX: применяем конфиг после скачивания — sing-box должен подхватить
	// новый geosite файл немедленно, а не ждать следующего ручного apply.
	applyErr := ""
	if shouldApply && s.tunHandlers != nil {
		if err := s.tunHandlers.TriggerApply(); err != nil {
			s.logger.Warn("handleGeositeDownload: TriggerApply: %v", err)
			applyErr = err.Error()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"name":        name,
		"size":        size,
		"apply_error": applyErr,
	})
}

// isProxyConnectError возвращает true если ошибка связана с недоступностью прокси 10807.
// Используется для переключения на прямое соединение когда XRay перезапускается.
func isProxyConnectError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "proxyconnect") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "actively refused") ||
		strings.Contains(s, ":10807")
}

// ErrGeositeNotFound возвращается downloadGeositeFile когда все источники вернули 404.
// checkAndUpdate использует его чтобы подавить повторный спам через os.Chtimes
// вместо бесконечного 7-дневного цикла предупреждений.
var ErrGeositeNotFound = fmt.Errorf("not found in any source (404)")

func geositeHTTPClients(ctx context.Context) []struct {
	name   string
	client *http.Client
} {
	directClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	proxyURL, _ := url.Parse("http://" + geositeProxyAddr)
	proxyClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	if isTCPReachable(ctx, geositeProxyAddr, 250*time.Millisecond) {
		return []struct {
			name   string
			client *http.Client
		}{
			{name: "proxy", client: proxyClient},
			{name: "direct", client: directClient},
		}
	}
	return []struct {
		name   string
		client *http.Client
	}{{name: "direct", client: directClient}}
}

func isTCPReachable(ctx context.Context, addr string, timeout time.Duration) bool {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// downloadGeositeFile скачивает geosite-{name}.bin в data/ директорию.
// Используется GeoAutoUpdater для фонового обновления файлов по расписанию.
// Пробует несколько источников по очереди через системный прокси (10807).
// БАГ 2: если прокси недоступен (XRay перезапускается) — делает повторную попытку
// через directClient без прокси, чтобы не потерять geo-обновление целиком.
// БАГ 4b: возвращает ErrGeositeNotFound если все источники вернули 404/tiny,
// и собирает ошибки всех источников — не перезаписывает одной последней.
func downloadGeositeFile(ctx context.Context, name string) error {
	if err := os.MkdirAll(config.DataDir, 0755); err != nil {
		return fmt.Errorf("не удалось создать data/ директорию: %w", err)
	}
	destPath := filepath.Join(config.DataDir, "geosite-"+name+".bin")

	doGet := func(client *http.Client, u string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		return client.Do(req)
	}

	var data []byte
	// БАГ 4b: накапливаем все ошибки.
	var errs []string
	allNotFound := true // все источники вернули 404 — ErrGeositeNotFound
	clients := geositeHTTPClients(ctx)
	for i, urlFmt := range geositeSources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		u := fmt.Sprintf(urlFmt, name)
		for _, c := range clients {
			resp, err := doGet(c.client, u)
			if err != nil {
				errs = append(errs, fmt.Sprintf("src%d/%s: %s", i+1, c.name, err.Error()))
				allNotFound = false
				continue
			}
			if resp.StatusCode == 404 {
				resp.Body.Close()
				errs = append(errs, fmt.Sprintf("src%d/%s: 404", i+1, c.name))
				break // для этого источника файл отсутствует; прямой повтор не поможет
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				errs = append(errs, fmt.Sprintf("src%d/%s: HTTP %d", i+1, c.name, resp.StatusCode))
				allNotFound = false
				continue
			}
			allNotFound = false
			var readErr error
			data, readErr = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if readErr != nil {
				errs = append(errs, fmt.Sprintf("src%d/%s: read: %s", i+1, c.name, readErr.Error()))
				data = nil
				continue
			}
			if !config.IsSingBoxRuleSetData(data) {
				errs = append(errs, fmt.Sprintf("src%d/%s: не binary SRS (%d байт)", i+1, c.name, len(data)))
				data = nil
				continue
			}
			break
		}
		if data != nil {
			break
		}
	}
	if data == nil {
		if allNotFound {
			return fmt.Errorf("geosite-%s %w (%s)", name, ErrGeositeNotFound, strings.Join(errs, "; "))
		}
		return fmt.Errorf("geosite-%s не удалось скачать (%s)", name, strings.Join(errs, "; "))
	}
	// FIX 4: атомарная запись.
	return fileutil.WriteAtomic(destPath, data, 0644)
}
