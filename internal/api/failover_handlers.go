package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/connhistory"
)

func (h *ServersHandlers) handleFailoverSettings(w http.ResponseWriter, _ *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	h.server.respondJSON(w, http.StatusOK, settings.SmartFailover)
}

func (h *ServersHandlers) handleSetFailoverSettings(w http.ResponseWriter, r *http.Request) {
	settings, _ := config.LoadAppSettings(config.AppSettingsFile)
	var body config.SmartFailoverSettings
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if dec.More() {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	} else if !errors.Is(err, io.EOF) {
		h.server.respondError(w, http.StatusBadRequest, "invalid body")
		return
	}
	settings.SmartFailover = body
	if err := config.SaveAppSettings(config.AppSettingsFile, settings); err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.server.respondJSON(w, http.StatusOK, settings.SmartFailover)
}

func (h *ServersHandlers) handleFailoverStatus(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	list, err := loadServers()
	h.mu.RUnlock()
	if err != nil {
		h.server.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list = visibleServers(list)
	activeID := h.activeServerIDFromList(list)
	type score struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Active    bool   `json:"active"`
		LatencyMs int64  `json:"latency_ms"`
		OK        bool   `json:"ok"`
	}
	scores := make([]score, len(list))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	for i, srv := range list {
		ms, _, _, ok := pingServerWithProbes(ctx, srv.URL, 1)
		scores[i] = score{ID: srv.ID, Name: srv.Name, Active: srv.ID == activeID, LatencyMs: ms, OK: ok}
	}
	h.server.respondJSON(w, http.StatusOK, map[string]interface{}{
		"active_id": activeID,
		"servers":   scores,
	})
}

func (h *ServersHandlers) StartSmartFailover(ctx context.Context) {
	go func() {
		var lastSwitch time.Time
		for {
			settings, _ := config.LoadAppSettings(config.AppSettingsFile)
			interval := time.Duration(settings.SmartFailover.CheckIntervalSec) * time.Second
			if interval < 15*time.Second {
				interval = 60 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			settings, _ = config.LoadAppSettings(config.AppSettingsFile)
			if !settings.SmartFailover.Enabled || time.Since(lastSwitch) < 2*time.Minute {
				continue
			}
			if h.shouldFailover(ctx, settings.SmartFailover) {
				resp, _, err := h.doAutoConnect(ctx)
				if err != nil {
					h.server.logger.Warn("SmartFailover: %v", err)
					continue
				}
				if changed, _ := resp["changed"].(bool); changed {
					lastSwitch = time.Now()
					serverID, _ := resp["connected_id"].(string)
					connhistory.Global.Add(connhistory.Event{
						Time:   time.Now(),
						Kind:   connhistory.EventFailover,
						Server: serverID,
						Reason: "smart failover",
					})
				}
			}
		}
	}()
}

func (h *ServersHandlers) shouldFailover(ctx context.Context, settings config.SmartFailoverSettings) bool {
	h.mu.RLock()
	list, err := loadServers()
	h.mu.RUnlock()
	list = visibleServers(list)
	if err != nil || len(list) < 2 {
		return false
	}
	activeID := h.activeServerIDFromList(list)
	if activeID == "" {
		return true
	}
	for _, srv := range list {
		if srv.ID != activeID {
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ms, _, _, ok := pingServerWithProbes(pingCtx, srv.URL, 1)
		cancel()
		return !ok || (settings.MaxLatencyMs > 0 && ms > int64(settings.MaxLatencyMs))
	}
	return true
}
