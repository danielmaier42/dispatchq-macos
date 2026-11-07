package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultInterval is how often the background worker re-checks for a new
// release.
const DefaultInterval = time.Hour

// Service keeps the latest-release check cached in memory and refreshes it on a
// ticker, so the API can answer instantly without hitting the network on every
// poll. It also downloads this system's installer on demand and hands it to the
// platform's opener. It is safe for concurrent use.
type Service struct {
	fetcher  Fetcher
	interval time.Duration

	// download configuration (injectable for tests)
	client  *http.Client
	destDir string
	target  Target
	open    func(ctx context.Context, path string) error

	// checkMu serializes fetches so a manual check and the hourly worker never
	// overlap (they'd otherwise double-fetch and race the checking flag).
	checkMu sync.Mutex

	mu          sync.Mutex
	releases    []Release
	lastErr     error
	checkedAt   time.Time
	checking    bool
	downloading bool
}

// Snapshot is a point-in-time view of the cached check result. Releases holds
// recent published releases (newest first); Latest is the highest-versioned of
// them.
type Snapshot struct {
	Releases    []Release
	Err         string
	CheckedAt   time.Time
	Checking    bool
	Downloading bool
}

// Latest returns the highest-versioned cached release, or nil if none.
func (s Snapshot) Latest() *Release { return Latest(s.Releases) }

// Option configures a [Service].
type Option func(*Service)

// WithHTTPClient sets the HTTP client used to download the installer.
func WithHTTPClient(c *http.Client) Option { return func(s *Service) { s.client = c } }

// WithDownloadDir sets the directory downloads are written to (default
// ~/Downloads).
func WithDownloadDir(dir string) Option { return func(s *Service) { s.destDir = dir } }

// WithOpener sets how a downloaded installer is handed to the user: the macOS
// Installer for a `.pkg`, the installer's own window for a Windows `.exe`. The
// platform provides it; without one, Download leaves the file in place and
// reports that this system cannot open it.
func WithOpener(fn func(ctx context.Context, path string) error) Option {
	return func(s *Service) { s.open = fn }
}

// WithTarget sets the system whose installers are downloaded (default
// [CurrentTarget]). Download refuses a file whose ending is not the target's.
func WithTarget(t Target) Option { return func(s *Service) { s.target = t } }

// NewService builds a Service. interval <= 0 uses [DefaultInterval].
func NewService(f Fetcher, interval time.Duration, opts ...Option) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
	s := &Service{
		fetcher:  f,
		interval: interval,
		client:   &http.Client{Timeout: 5 * time.Minute},
		target:   CurrentTarget(),
		open:     noOpener,
	}
	for _, o := range opts {
		o(s)
	}
	if s.destDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			s.destDir = filepath.Join(home, "Downloads")
		} else {
			s.destDir = os.TempDir()
		}
	}
	return s
}

// Run checks once immediately, then re-checks every interval until ctx is done.
// It is meant to be run in its own goroutine.
func (s *Service) Run(ctx context.Context) {
	_, _ = s.CheckNow(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.CheckNow(ctx)
		}
	}
}

// CheckNow fetches recent releases synchronously and updates the cache. Checks
// are serialized, so a manual check while the hourly worker is mid-fetch waits
// rather than issuing a second overlapping request.
func (s *Service) CheckNow(ctx context.Context) ([]Release, error) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()

	s.mu.Lock()
	s.checking = true
	s.mu.Unlock()

	releases, err := s.fetcher.Releases(ctx)

	s.mu.Lock()
	s.checking = false
	s.checkedAt = time.Now()
	if err != nil {
		s.lastErr = err
	} else {
		s.releases = releases
		s.lastErr = nil
	}
	s.mu.Unlock()
	return releases, err
}

// Snapshot returns the cached check result without touching the network.
func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Releases:    s.releases,
		CheckedAt:   s.checkedAt,
		Checking:    s.checking,
		Downloading: s.downloading,
	}
	if s.lastErr != nil {
		snap.Err = s.lastErr.Error()
	}
	return snap
}

// Download fetches the cached latest release's installer into the download
// directory and opens it with the platform's opener. It returns the path of the
// downloaded file. Only one download runs at a time.
func (s *Service) Download(ctx context.Context) (string, error) {
	s.mu.Lock()
	rel := Latest(s.releases)
	if s.downloading {
		s.mu.Unlock()
		return "", errors.New("a download is already in progress")
	}
	if rel == nil || rel.PkgURL == "" {
		s.mu.Unlock()
		return "", errors.New("no installer is available to download")
	}
	s.downloading = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.downloading = false
		s.mu.Unlock()
	}()

	dest, err := s.downloadInstaller(ctx, rel)
	if err != nil {
		return "", err
	}
	if err := s.open(ctx, dest); err != nil {
		// The file is on disk; surface where it landed so the user can open it
		// manually if launching the Installer failed.
		return dest, fmt.Errorf("open installer %q: %w", dest, err)
	}
	return dest, nil
}

// installerFileName is the bare file name rel's installer is saved under. A
// crafted asset name (e.g. "../../evil.pkg") is reduced to its last element so
// it can never write outside the download directory, and a name whose ending is
// not this system's installer ending is refused, so the opener is never handed
// another kind of file.
func (s *Service) installerFileName(rel *Release) (string, error) {
	ext := s.target.Ext()
	if ext == "" {
		return "", errors.New("no installer is published for this system")
	}
	name := rel.PkgName
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return fmt.Sprintf("dispatchq-%s-%s%s", rel.Version, s.target, ext), nil
	}
	if !strings.EqualFold(filepath.Ext(name), ext) || len(name) == len(ext) {
		return "", fmt.Errorf("installer %q is not a %s file for %s", name, ext, s.target)
	}
	return name, nil
}

// downloadInstaller streams the release's installer to a temp file and renames
// it into place, so an interrupted download never leaves a truncated installer
// behind.
func (s *Service) downloadInstaller(ctx context.Context, rel *Release) (string, error) {
	name, err := s.installerFileName(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.destDir, 0o755); err != nil {
		return "", fmt.Errorf("create download dir: %w", err)
	}
	dest := filepath.Join(s.destDir, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.PkgURL, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download installer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download installer: unexpected status %s", resp.Status)
	}

	tmp, err := os.CreateTemp(s.destDir, ".dispatchq-dl-*"+s.target.Ext())
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write installer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close installer: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", fmt.Errorf("finalize installer: %w", err)
	}
	return dest, nil
}

// noOpener is the opener of a Service built without [WithOpener].
func noOpener(context.Context, string) error {
	return errors.New("this system cannot open the installer; open the downloaded file yourself")
}
