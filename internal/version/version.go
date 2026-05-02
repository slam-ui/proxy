package version

// Values are overridden by build.ps1 and CI through -ldflags -X.
var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info returns build metadata for API/UI surfaces.
func Info() map[string]string {
	return map[string]string{
		"version":    Version,
		"commit":     Commit,
		"build_time": BuildTime,
	}
}
