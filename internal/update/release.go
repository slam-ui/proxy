package update

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// Release describes version.json published by the update server.
type Release struct {
	Channel                   string    `json:"channel"`
	Version                   string    `json:"version"`
	ReleasedAt                time.Time `json:"released_at"`
	MinVersionForDirectUpdate string    `json:"min_version_for_direct_update"`
	DownloadURL               string    `json:"download_url"`
	SHA256                    string    `json:"sha256"`
	SizeBytes                 int64     `json:"size_bytes"`
	ChangelogURL              string    `json:"changelog_url,omitempty"`
	Critical                  bool      `json:"critical"`
	ForceDowngrade            bool      `json:"force_downgrade,omitempty"`
}

// CheckResult is returned after comparing the remote release with the current app.
type CheckResult struct {
	CurrentVersion       string    `json:"current_version"`
	Latest               *Release  `json:"latest,omitempty"`
	UpdateAvailable      bool      `json:"update_available"`
	ManualUpdateRequired bool      `json:"manual_update_required"`
	Reason               string    `json:"reason,omitempty"`
	CheckedAt            time.Time `json:"checked_at"`
}

func decodeRelease(data []byte) (*Release, error) {
	var rel Release
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("decode version.json: %w", err)
	}
	if err := rel.Validate(); err != nil {
		return nil, err
	}
	return &rel, nil
}

// Validate rejects unsafe or incomplete update metadata.
func (r *Release) Validate() error {
	if strings.TrimSpace(r.Channel) == "" {
		return fmt.Errorf("release channel is required")
	}
	if _, err := semver.NewVersion(r.Version); err != nil {
		return fmt.Errorf("invalid release version %q: %w", r.Version, err)
	}
	if r.MinVersionForDirectUpdate != "" {
		if _, err := semver.NewVersion(r.MinVersionForDirectUpdate); err != nil {
			return fmt.Errorf("invalid min_version_for_direct_update %q: %w", r.MinVersionForDirectUpdate, err)
		}
	}
	if r.SizeBytes <= 0 {
		return fmt.Errorf("size_bytes must be > 0")
	}
	if len(r.SHA256) != 64 {
		return fmt.Errorf("sha256 must be a 64 character hex digest")
	}
	if err := requireHTTPS(r.DownloadURL); err != nil {
		return fmt.Errorf("download_url: %w", err)
	}
	if r.ChangelogURL != "" {
		if err := requireHTTPS(r.ChangelogURL); err != nil {
			return fmt.Errorf("changelog_url: %w", err)
		}
	}
	return nil
}

func requireHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("https URL required")
	}
	return nil
}

func compareRelease(current string, rel *Release, now time.Time) (*CheckResult, error) {
	cur, err := semver.NewVersion(current)
	if err != nil {
		return nil, fmt.Errorf("invalid current version %q: %w", current, err)
	}
	latest, err := semver.NewVersion(rel.Version)
	if err != nil {
		return nil, fmt.Errorf("invalid latest version %q: %w", rel.Version, err)
	}
	res := &CheckResult{
		CurrentVersion: current,
		Latest:         rel,
		CheckedAt:      now,
	}
	if rel.MinVersionForDirectUpdate != "" {
		min, err := semver.NewVersion(rel.MinVersionForDirectUpdate)
		if err != nil {
			return nil, err
		}
		if cur.LessThan(min) {
			res.ManualUpdateRequired = true
			res.Reason = "current version is below min_version_for_direct_update"
			return res, nil
		}
	}
	if rel.ForceDowngrade {
		res.UpdateAvailable = !latest.Equal(cur)
		return res, nil
	}
	res.UpdateAvailable = latest.GreaterThan(cur)
	return res, nil
}
