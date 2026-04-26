package trafficstats

import (
	"encoding/json"
	"os"
	"sync/atomic"

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
	sessionDown atomic.Int64
	sessionUp   atomic.Int64
)

func AddSession(down, up int64) {
	sessionDown.Add(down)
	sessionUp.Add(up)
}

func Current() Stats {
	s := load()
	s.SessionDownloadBytes = sessionDown.Load()
	s.SessionUploadBytes = sessionUp.Load()
	return s
}

func SaveToFile() error {
	s := load()
	down := sessionDown.Swap(0)
	up := sessionUp.Swap(0)
	s.TotalDownloadBytes += down
	s.TotalUploadBytes += up
	s.TotalSessions++
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteAtomic(statsFile, data, 0644)
}

func load() Stats {
	data, err := os.ReadFile(statsFile)
	if err != nil {
		return Stats{}
	}
	var s Stats
	if err := json.Unmarshal(data, &s); err != nil {
		return Stats{}
	}
	return s
}
