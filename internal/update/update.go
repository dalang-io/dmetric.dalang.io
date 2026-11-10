// Package update checks the public GitHub release and installs the binary
// for the current OS/architecture.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultRepo is the public repository that publishes DMetric releases.
const DefaultRepo = "dalang-io/dmetric.dalang.io"

const (
	cacheTTL       = 10 * time.Minute
	maxAssetSize   = 200 << 20
	maxChecksumLen = 2 << 20
)

// Info describes the newest release compared with the running version.
type Info struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	Tag             string `json:"tag,omitempty"`
	ReleaseURL      string `json:"release_url,omitempty"`
	Notes           string `json:"notes,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	AssetName       string `json:"asset_name,omitempty"`
	AssetSize       int64  `json:"asset_size,omitempty"`
	Checksum        bool   `json:"checksum"`
	Error           string `json:"error,omitempty"`

	assetURL     string
	checksumURL  string
	checksumName string
}

// Result is a successful installation.
type Result struct {
	Version    string `json:"version"`
	ReleaseURL string `json:"release_url"`
	Message    string `json:"message"`
	// Restart replaces or restarts the process. Server code calls it after
	// the HTTP response has been written.
	Restart func() error `json:"-"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type release struct {
	Tag         string         `json:"tag_name"`
	Name        string         `json:"name"`
	HTMLURL     string         `json:"html_url"`
	Body        string         `json:"body"`
	PublishedAt time.Time      `json:"published_at"`
	Assets      []releaseAsset `json:"assets"`
}

// Manager checks and installs updates for one repository.
type Manager struct {
	repo           string
	current        string
	dataDir        string
	apiURL         string
	client         *http.Client
	downloadClient *http.Client

	mu       sync.Mutex
	cached   Info
	cachedAt time.Time

	// Automatic update loop (auto.go). The knobs are fields so tests can
	// run the loop in milliseconds.
	autoMu       sync.Mutex
	auto         AutoStatus
	startupDelay time.Duration
	autoPoll     time.Duration
	canInstall   func() error
	installFn    func(downloaded string) (func() error, error)
}

// New creates an updater. repo may be empty for DefaultRepo.
func New(dataDir, current, repo string) *Manager {
	if repo == "" {
		repo = DefaultRepo
	}
	return &Manager{
		repo:           repo,
		current:        current,
		dataDir:        dataDir,
		apiURL:         "https://api.github.com/repos/" + repo + "/releases/latest",
		client:         &http.Client{Timeout: 30 * time.Second},
		downloadClient: &http.Client{Timeout: 10 * time.Minute},
		startupDelay:   30 * time.Second,
		autoPoll:       time.Minute,
		canInstall:     CanSelfInstall,
		installFn:      installUpdate,
	}
}

// Repo returns the configured owner/name.
func (m *Manager) Repo() string { return m.repo }

func (m *Manager) Check(ctx context.Context, force bool) Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkLocked(ctx, force)
}

func (m *Manager) checkLocked(ctx context.Context, force bool) Info {
	if !force && !m.cachedAt.IsZero() && time.Since(m.cachedAt) < cacheTTL {
		return m.cached
	}
	info := Info{CurrentVersion: m.current}
	rel, err := m.fetchRelease(ctx)
	if err != nil {
		info.Error = err.Error()
		m.cached, m.cachedAt = info, time.Now()
		return info
	}
	info.Tag = rel.Tag
	info.LatestVersion = normalizeVersion(rel.Tag)
	info.ReleaseURL = rel.HTMLURL
	info.PublishedAt = rel.PublishedAt.Format(time.RFC3339)
	info.Notes = truncate(rel.Body, 16000)
	info.UpdateAvailable = compareVersions(info.LatestVersion, m.current) > 0

	assetName := assetName()
	for _, asset := range rel.Assets {
		if asset.Name == assetName {
			info.AssetName = asset.Name
			info.assetURL = asset.URL
			info.AssetSize = asset.Size
			break
		}
	}
	for _, asset := range rel.Assets {
		if isChecksumAsset(asset.Name) {
			info.Checksum, info.checksumURL, info.checksumName = true, asset.URL, asset.Name
			break
		}
	}
	if info.AssetName == "" {
		info.Error = fmt.Sprintf("rilis %s tidak menyertakan asset %s", rel.Tag, assetName)
	}
	m.cached, m.cachedAt = info, time.Now()
	return info
}

func (m *Manager) fetchRelease(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dmetric/"+m.current)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("repo/rilis %s belum tersedia atau belum publik", m.repo)
	case http.StatusForbidden:
		return nil, fmt.Errorf("batas API GitHub tercapai; coba lagi beberapa menit lagi")
	default:
		return nil, fmt.Errorf("GitHub mengembalikan HTTP %d", resp.StatusCode)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("respons rilis tidak valid: %w", err)
	}
	if rel.Tag == "" {
		return nil, errors.New("rilis tidak memiliki tag")
	}
	return &rel, nil
}

// InstallOptions tunes Install.
type InstallOptions struct {
	// RequireChecksum refuses a release that does not publish a verifiable
	// SHA256 for this platform's asset. Unattended installs set it.
	RequireChecksum bool
}

// Install downloads and places the newest binary, returning a restart
// callback. It refuses development builds and bundled macOS apps.
func (m *Manager) Install(ctx context.Context, requested string) (Result, error) {
	return m.InstallWith(ctx, requested, InstallOptions{})
}

// CanSelfInstall reports why this process cannot replace its own binary
// (macOS .app bundle, `go run`), or nil when it can.
func CanSelfInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("menemukan executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return selfInstallBlocker(exe)
}

// InstallWith is Install with options.
func (m *Manager) InstallWith(ctx context.Context, requested string, opts InstallOptions) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	info := m.checkLocked(ctx, true)
	if info.Error != "" {
		return Result{}, errors.New(info.Error)
	}
	if requested != "" && normalizeVersion(requested) != info.LatestVersion {
		return Result{}, fmt.Errorf("versi terbaru berubah menjadi %s; cek ulang", info.LatestVersion)
	}
	if !info.UpdateAvailable && (requested == "" || normalizeVersion(requested) != info.LatestVersion) {
		return Result{}, errors.New("aplikasi sudah memakai versi terbaru")
	}
	if info.assetURL == "" {
		return Result{}, errors.New("asset binary untuk platform ini tidak tersedia")
	}
	if info.AssetSize > maxAssetSize {
		return Result{}, fmt.Errorf("asset terlalu besar (%d byte)", info.AssetSize)
	}

	dir := filepath.Join(m.dataDir, "updates", safeFileName(info.LatestVersion))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("membuat direktori update: %w", err)
	}
	target := filepath.Join(dir, safeFileName(info.AssetName))

	expected := ""
	if info.checksumURL != "" {
		data, err := m.downloadBytes(ctx, info.checksumURL, maxChecksumLen)
		if err != nil {
			return Result{}, fmt.Errorf("mengunduh checksum: %w", err)
		}
		expected = checksumFor(data, info.AssetName)
		if expected == "" {
			return Result{}, fmt.Errorf("checksum untuk %s tidak ada di %s", info.AssetName, info.checksumName)
		}
	}
	if opts.RequireChecksum && expected == "" {
		return Result{}, errors.New("rilis tidak menyediakan SHA256 yang dapat diverifikasi; pemasangan otomatis ditolak")
	}
	if err := m.downloadFile(ctx, info.assetURL, target, expected); err != nil {
		return Result{}, err
	}

	restart, err := m.installFn(target)
	if err != nil {
		return Result{}, err
	}
	message := "Update terpasang; aplikasi akan restart."
	if expected == "" {
		message += " Rilis tidak menyediakan SHA256; integritas hanya dari HTTPS."
	} else {
		message += " SHA256 terverifikasi."
	}
	return Result{
		Version:    info.LatestVersion,
		ReleaseURL: info.ReleaseURL,
		Message:    message,
		Restart:    restart,
	}, nil
}

func (m *Manager) downloadBytes(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dmetric/"+m.current)
	resp, err := m.downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (m *Manager) downloadFile(ctx context.Context, url, target, expected string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "dmetric/"+m.current)
	resp, err := m.downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("mengunduh update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unduhan update HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".download-*")
	if err != nil {
		return fmt.Errorf("membuat file unduhan: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, maxAssetSize+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("menulis update: %w", err)
	}
	if written > maxAssetSize {
		return errors.New("asset update melebihi batas ukuran")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if expected != "" && !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum tidak cocok: dapat %s, ingin %s", actual, expected)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("menyimpan update: %w", err)
	}
	return nil
}

func assetName() string {
	name := "dmetric-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func isChecksumAsset(name string) bool {
	switch strings.ToLower(name) {
	case "sha256sums", "sha256sums.txt", "checksums.txt":
		return true
	}
	return false
}

func checksumFor(data []byte, asset string) string {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hash := strings.ToLower(fields[0])
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if len(hash) == 64 && filepath.Base(name) == asset {
			return hash
		}
	}
	return ""
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	return v
}

func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			na = pa[i]
		}
		if i < len(pb) {
			nb = pb[i]
		}
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	var parts []int
	for _, field := range strings.FieldsFunc(normalizeVersion(v), func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == '+'
	}) {
		n := 0
		seen := false
		for _, r := range field {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
			seen = true
		}
		if seen {
			parts = append(parts, n)
		}
	}
	return parts
}

func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "update"
	}
	return out
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n…"
}
