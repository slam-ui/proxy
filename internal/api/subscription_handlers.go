package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"proxyclient/internal/config"
	"proxyclient/internal/subscription"

	"github.com/gorilla/mux"
)

func newManagedSubscriptionHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: noProxyTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("subscription redirect must use https")
			}
			if err := validateSubscriptionURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect denied: %w", err)
			}
			return nil
		},
	}
}

func SetupSubscriptionRoutes(s *Server) {
	api := s.router.PathPrefix("/api").Subrouter()
	api.HandleFunc("/subscriptions", s.handleSubscriptionsList).Methods("GET", "OPTIONS")
	api.HandleFunc("/subscriptions", s.handleSubscriptionsAdd).Methods("POST", "OPTIONS")
	api.HandleFunc("/subscriptions/{id}/update", s.handleSubscriptionUpdate).Methods("POST", "OPTIONS")
	api.HandleFunc("/subscriptions/{id}", s.handleSubscriptionRemove).Methods("DELETE", "OPTIONS")
}

func (s *Server) handleSubscriptionsList(w http.ResponseWriter, _ *http.Request) {
	if s.subscriptions == nil {
		s.respondJSON(w, http.StatusOK, map[string]any{"subscriptions": []subscription.Subscription{}})
		return
	}
	list := s.subscriptions.List()
	for _, sub := range list {
		sub.URL = maskSubscriptionURL(sub.URL)
	}
	s.respondJSON(w, http.StatusOK, map[string]any{"subscriptions": list})
}

func (s *Server) handleSubscriptionsAdd(w http.ResponseWriter, r *http.Request) {
	if s.subscriptions == nil {
		s.respondError(w, http.StatusServiceUnavailable, "subscriptions are not available")
		return
	}
	var req struct {
		Name          string `json:"name"`
		URL           string `json:"url"`
		UpdateEvery   string `json:"update_every"`
		UpdateEveryMs int64  `json:"update_every_ms"`
		UserAgent     string `json:"user_agent"`
	}
	if !decodeStrictJSON(w, r, &req, maxServersRequestBytes) {
		return
	}
	interval, err := parseSubscriptionInterval(req.UpdateEvery, req.UpdateEveryMs)
	if err != nil {
		s.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub := &subscription.Subscription{
		Name:        strings.TrimSpace(req.Name),
		URL:         strings.TrimSpace(req.URL),
		UpdateEvery: interval,
		UserAgent:   strings.TrimSpace(req.UserAgent),
	}
	if err := s.subscriptions.Add(sub); err != nil {
		s.respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.subscriptions.UpdateNow(r.Context(), sub.ID)
	if err != nil {
		s.respondJSON(w, http.StatusAccepted, map[string]any{
			"success":      false,
			"subscription": maskSubscription(sub),
			"error":        err.Error(),
		})
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"subscription": maskSubscription(sub),
		"result":       result,
	})
}

func (s *Server) handleSubscriptionUpdate(w http.ResponseWriter, r *http.Request) {
	if s.subscriptions == nil {
		s.respondError(w, http.StatusServiceUnavailable, "subscriptions are not available")
		return
	}
	id := mux.Vars(r)["id"]
	result, err := s.subscriptions.UpdateNow(r.Context(), id)
	if err != nil {
		s.respondError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.respondJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
}

func (s *Server) handleSubscriptionRemove(w http.ResponseWriter, r *http.Request) {
	if s.subscriptions == nil {
		s.respondError(w, http.StatusServiceUnavailable, "subscriptions are not available")
		return
	}
	id := mux.Vars(r)["id"]
	if err := s.subscriptions.Remove(id); err != nil {
		s.respondError(w, http.StatusNotFound, err.Error())
		return
	}
	if s.serversHandlers != nil {
		if err := s.serversHandlers.removeSubscriptionServers(id); err != nil {
			s.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.respondJSON(w, http.StatusOK, MessageResponse{Success: true, Message: "subscription removed"})
}

func (h *ServersHandlers) applySubscriptionServers(ctx context.Context, sub subscription.Subscription, result subscription.UpdateResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	list, err := loadServers()
	if err != nil {
		return fmt.Errorf("read servers: %w", err)
	}
	existing := map[string]int{}
	for i, server := range list {
		if server.SubscriptionID == sub.ID && server.SubscriptionKey != "" {
			existing[server.SubscriptionKey] = i
		}
	}
	seen := map[string]bool{}
	now := time.Now().Unix()
	for _, incoming := range result.Servers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		key := subscriptionServerKey(incoming.URI)
		if key == "" {
			continue
		}
		parsed, parseErr := config.ParseServerContent(incoming.URI)
		if parseErr != nil {
			continue
		}
		seen[key] = true
		name := strings.TrimSpace(incoming.Name)
		if name == "" {
			name = parsed.DisplayName
		}
		if name == "" {
			name = "Сервер"
		}
		if idx, ok := existing[key]; ok {
			list[idx].Name = name
			list[idx].URL = incoming.URI
			list[idx].Deleted = false
			continue
		}
		list = append(list, ServerEntry{
			ID:              subscriptionServerID(sub.ID, key),
			Name:            name,
			URL:             incoming.URI,
			CountryCode:     "??",
			AddedAt:         now,
			SubscriptionID:  sub.ID,
			SubscriptionKey: key,
		})
	}
	for i := range list {
		if list[i].SubscriptionID == sub.ID && !seen[list[i].SubscriptionKey] {
			list[i].Deleted = true
		}
	}
	if err := saveServers(list); err != nil {
		return fmt.Errorf("write servers: %w", err)
	}
	if len(visibleServers(list)) == len(result.Servers) && len(result.Servers) > 0 {
		active, readErr := config.ReadSecretKey(h.secretKey)
		if readErr != nil || strings.TrimSpace(active) == "" {
			_ = config.WriteSecretKey(h.secretKey, result.Servers[0].URI)
			config.InvalidateVLESSCache()
			if h.server.config.SecretKeyUpdatedFn != nil {
				h.server.config.SecretKeyUpdatedFn()
			}
		}
	}
	return nil
}

func (h *ServersHandlers) removeSubscriptionServers(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	list, err := loadServers()
	if err != nil {
		return fmt.Errorf("read servers: %w", err)
	}
	out := make([]ServerEntry, 0, len(list))
	for _, server := range list {
		if server.SubscriptionID != id {
			out = append(out, server)
		}
	}
	if err := saveServers(out); err != nil {
		return fmt.Errorf("write servers: %w", err)
	}
	return nil
}

func parseSubscriptionInterval(raw string, ms int64) (time.Duration, error) {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "manual":
		return 0, nil
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "24h", "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported update interval")
	}
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	var extra struct{}
	if err := dec.Decode(&extra); err == nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	} else if !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	return true
}

func maskSubscription(sub *subscription.Subscription) *subscription.Subscription {
	cp := *sub
	cp.URL = maskSubscriptionURL(cp.URL)
	return &cp
}

func maskSubscriptionURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/..."
}

func subscriptionServerKey(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	user := ""
	if parsed.User != nil {
		user = parsed.User.String()
	}
	return strings.ToLower(parsed.Scheme + "://" + user + "@" + parsed.Host)
}

func subscriptionServerID(subID, key string) string {
	sum := sha256.Sum256([]byte(subID + "\x00" + key))
	return "sub-" + hex.EncodeToString(sum[:8])
}
