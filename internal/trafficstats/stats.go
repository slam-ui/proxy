package trafficstats

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"proxyclient/internal/fileutil"
)

const statsFile = "data/traffic_stats.json"

type Stats struct {
	TotalDownloadBytes   int64 `json:"total_download_bytes"`
	TotalUploadBytes     int64 `json:"total_upload_bytes"`
	TotalSessions        int64 `json:"total_sessions"`
	SessionDownloadBytes int64 `json:"session_download_bytes,omitempty"`
	SessionUploadBytes   int64 `json:"session_upload_bytes,omitempty"`
}

var (
	saveMu      sync.Mutex
	sessionDown atomic.Int64
	sessionUp   atomic.Int64

	statsCacheMu sync.RWMutex
	statsCache   cachedStats
)

type cachedStats struct {
	stats   Stats
	modTime time.Time
	size    int64
	loaded  bool
}

func AddSession(down, up int64) {
	sessionDown.Add(down)
	sessionUp.Add(up)
}

func Current() Stats {
	s := loadCached()
	s.SessionDownloadBytes = sessionDown.Load()
	s.SessionUploadBytes = sessionUp.Load()
	return s
}

func SaveToFile() error {
	saveMu.Lock()
	defer saveMu.Unlock()

	s := loadCached()
	down := sessionDown.Swap(0)
	up := sessionUp.Swap(0)
	s.TotalDownloadBytes += down
	s.TotalUploadBytes += up
	s.TotalSessions++
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := fileutil.WriteAtomic(statsFile, data, 0644); err != nil {
		sessionDown.Add(down)
		sessionUp.Add(up)
		return err
	}
	storeCache(s)
	return nil
}

func loadCached() Stats {
	fi, err := os.Stat(statsFile)
	if err != nil {
		return Stats{}
	}
	modTime := fi.ModTime()
	size := fi.Size()

	statsCacheMu.RLock()
	if statsCache.loaded && statsCache.modTime.Equal(modTime) && statsCache.size == size {
		s := statsCache.stats
		statsCacheMu.RUnlock()
		return s
	}
	statsCacheMu.RUnlock()

	data, err := os.ReadFile(statsFile)
	if err != nil {
		return Stats{}
	}
	var s Stats
	if err := json.Unmarshal(data, &s); err != nil {
		return Stats{}
	}
	statsCacheMu.Lock()
	statsCache = cachedStats{stats: s, modTime: modTime, size: size, loaded: true}
	statsCacheMu.Unlock()
	return s
}

func storeCache(s Stats) {
	fi, err := os.Stat(statsFile)
	if err != nil {
		return
	}
	statsCacheMu.Lock()
	statsCache = cachedStats{stats: s, modTime: fi.ModTime(), size: fi.Size(), loaded: true}
	statsCacheMu.Unlock()
}
