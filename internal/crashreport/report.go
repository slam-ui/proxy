package crashreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"proxyclient/internal/fileutil"
)

type CrashReport struct {
	Timestamp  string `json:"timestamp"`
	SingBoxVer string `json:"singbox_version,omitempty"`
	AppVer     string `json:"app_version,omitempty"`
	WindowsVer string `json:"windows_version,omitempty"`
	LastOutput string `json:"last_output,omitempty"`
	ConfigSafe string `json:"config_safe,omitempty"`
	MemoryMB   uint64 `json:"memory_mb,omitempty"`
	ErrorMsg   string `json:"error_message,omitempty"`
}

func Generate(output, errMsg, configPath string, memoryMB uint64) *CrashReport {
	report := &CrashReport{
		Timestamp:  time.Now().Format(time.RFC3339),
		LastOutput: output,
		ErrorMsg:   errMsg,
		MemoryMB:   memoryMB,
	}
	if data, err := os.ReadFile(configPath); err == nil {
		report.ConfigSafe = maskUUIDs(string(data))
	}
	return report
}

func (r *CrashReport) SaveToFile() (string, error) {
	if err := os.MkdirAll("data", 0755); err != nil {
		return "", err
	}
	path := filepath.Join("data", "crash-"+time.Now().Format("2006-01-02-150405")+".json")
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, fileutil.WriteAtomic(path, data, 0644)
}

func ListLatest(limit int) []string {
	files, _ := filepath.Glob(filepath.Join("data", "crash-*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files
}

func maskUUIDs(s string) string {
	re := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	return re.ReplaceAllString(s, "****-****")
}
