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
		LastOutput: Sanitize(output),
		ErrorMsg:   Sanitize(errMsg),
		MemoryMB:   memoryMB,
	}
	if data, err := os.ReadFile(configPath); err == nil {
		report.ConfigSafe = Sanitize(string(data))
	}
	return report
}

func (r *CrashReport) SanitizedForUpload() CrashReport {
	if r == nil {
		return CrashReport{}
	}
	cp := *r
	cp.LastOutput = Sanitize(cp.LastOutput)
	cp.ConfigSafe = Sanitize(cp.ConfigSafe)
	cp.ErrorMsg = Sanitize(cp.ErrorMsg)
	cp.MemoryMB = 0
	return cp
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

func Sanitize(s string) string {
	s = maskUUIDs(s)
	ipv4 := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	s = ipv4.ReplaceAllString(s, "<ip>")
	base64Like := regexp.MustCompile(`\b[A-Za-z0-9+/=_-]{21,}\b`)
	s = base64Like.ReplaceAllString(s, "<redacted>")
	return s
}
