package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxyclient/internal/netutil"
)

const (
	DefaultBaseURL = "https://example.com/safesky"
	defaultUA      = "SafeSky-Updater/1"
)

// Config controls update discovery and download behavior.
type Config struct {
	BaseURL        string
	Channel        string
	CurrentVersion string
	TempDir        string
	StatePath      string
	HTTPClient     *http.Client
	UserAgent      string
}

// Updater checks, downloads, and stages application updates.
type Updater struct {
	cfg    Config
	client *http.Client
}

// New returns an updater with secure defaults.
func New(cfg Config) (*Updater, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if err := requireHTTPS(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("base update URL: %w", err)
	}
	if cfg.Channel == "" {
		cfg.Channel = ChannelStable
	}
	if cfg.CurrentVersion == "" {
		cfg.CurrentVersion = "0.0.0-dev"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUA
	}
	client := cfg.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	return &Updater{cfg: cfg, client: client}, nil
}

func defaultHTTPClient() *http.Client {
	return netutil.SharedHTTPClient(35 * time.Second)
}

// CheckLatest downloads version.<channel>.json and compares it with the current version.
func (u *Updater) CheckLatest(ctx context.Context) (*CheckResult, error) {
	endpoint, err := u.versionURL()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", u.cfg.UserAgent)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch update metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch update metadata: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read update metadata: %w", err)
	}
	rel, err := decodeRelease(data)
	if err != nil {
		return nil, err
	}
	if rel.Channel != u.cfg.Channel {
		return nil, fmt.Errorf("release channel %q does not match requested %q", rel.Channel, u.cfg.Channel)
	}
	return compareRelease(u.cfg.CurrentVersion, rel, time.Now().UTC())
}

func (u *Updater) versionURL() (string, error) {
	base, err := url.Parse(u.cfg.BaseURL)
	if err != nil {
		return "", err
	}
	if base.Scheme != "https" || base.Host == "" {
		return "", fmt.Errorf("https update URL required")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/version." + u.cfg.Channel + ".json"
	return base.String(), nil
}

// Download downloads the release into a temporary file and verifies size and SHA256.
func (u *Updater) Download(ctx context.Context, rel *Release) (string, error) {
	if rel == nil {
		return "", fmt.Errorf("release is nil")
	}
	if err := rel.Validate(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", u.cfg.UserAgent)
	resp, err := u.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("download update: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 && resp.ContentLength != rel.SizeBytes {
		return "", fmt.Errorf("download size mismatch: content-length=%d want=%d", resp.ContentLength, rel.SizeBytes)
	}
	tmpDir := u.cfg.TempDir
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", fmt.Errorf("create update temp dir: %w", err)
	}
	f, err := os.CreateTemp(tmpDir, "safesky-update-*.exe")
	if err != nil {
		return "", fmt.Errorf("create update temp file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, rel.SizeBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		err = fmt.Errorf("write update temp file: %w", copyErr)
		return "", err
	}
	if closeErr != nil {
		err = fmt.Errorf("close update temp file: %w", closeErr)
		return "", err
	}
	if written != rel.SizeBytes {
		err = fmt.Errorf("download size mismatch: got=%d want=%d", written, rel.SizeBytes)
		return "", err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(got, rel.SHA256) {
		err = fmt.Errorf("sha256 mismatch: got=%s want=%s", got, rel.SHA256)
		return "", err
	}
	return tmpPath, nil
}

// Apply records a pending update. The Windows replacement is performed by cmd/proxy-updater.
func (u *Updater) Apply(_ context.Context, downloadedPath string) error {
	if downloadedPath == "" {
		return fmt.Errorf("downloaded path is required")
	}
	abs, err := filepath.Abs(downloadedPath)
	if err != nil {
		return fmt.Errorf("resolve downloaded path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("stat downloaded update: %w", err)
	}
	return SaveState(u.cfg.StatePath, State{
		PendingPath: abs,
		InstalledAt: time.Now().UTC(),
		Verified:    false,
	})
}
