// Package updater implements the notice-only "update available" check: a cached
// GitHub Releases query that can tell a human a newer binary exists. It never
// updates anything (ADR 0002) and never fails a command.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sheon-sek/igdev/internal/buildinfo"
	"github.com/sheon-sek/igdev/internal/semver"
)

// CacheFileName is the 24 h result inside the XDG cache directory.
const CacheFileName = "update-check.json"

// maxTimeout bounds whatever a config tier asks for, so a bad value can never
// make the CLI wait.
const maxTimeout = 10 * time.Second

// defaultTimeout applies when the configured timeout is missing or not positive.
const defaultTimeout = 2 * time.Second

// Cache is the persisted result of one release query.
type Cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// Options wires one notice check. Everything a test needs to control is here.
type Options struct {
	// APIURL is the releases endpoint (config tier `updater.releases_api_url`).
	APIURL string
	// CachePath is the absolute path of the 24 h cache file.
	CachePath string
	// Current is the running binary's version.
	Current string
	// TTL is how long a cached result is reused (24 h by default).
	TTL time.Duration
	// Timeout caps a single release fetch.
	Timeout time.Duration
	// Now defaults to time.Now; injectable for deterministic staleness.
	Now func() time.Time
	// HTTPClient defaults to a client bounded by Timeout.
	HTTPClient *http.Client
}

// Notice returns the update-available sentence, or "" when there is nothing to
// report. Every failure mode (offline, 404, malformed payload, unwritable
// cache) yields "" so `igdev` keeps working.
func Notice(o Options) string {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	stamp := now()

	cached, haveCache := readCache(o.CachePath)
	if ttl := o.TTL; ttl <= 0 {
		o.TTL = 24 * time.Hour
	}
	if !haveCache || stamp.Sub(cached.CheckedAt) >= o.TTL {
		cached = refreshUnderLock(o, stamp)
	}

	if cached.Latest == "" {
		return ""
	}
	newest, err := semver.Parse(cached.Latest)
	if err != nil {
		return ""
	}
	mine, err := semver.Parse(o.Current)
	if err != nil {
		return ""
	}
	if !newest.Greater(mine) {
		return ""
	}
	// The cache keeps the tag as advertised; the notice shows a version.
	return fmt.Sprintf("igdev %s is available (you have %s). Update with: %s",
		strings.TrimPrefix(cached.Latest, "v"), o.Current, buildinfo.InstallCommand)
}

// LockFileName is the sibling lock that serialises refreshes. Parallel igdev runs
// (many agent worktrees on one machine) must cost one release query, not one each,
// and a slow failing writer must not overwrite a result a faster peer just stored.
const LockFileName = "update-check.lock"

// refreshUnderLock re-reads the cache while holding the lock, so the first
// process through the door does the fetch and the rest reuse its result.
func refreshUnderLock(o Options, stamp time.Time) Cache {
	release, locked := lockCache(o.CachePath)
	if locked {
		defer release()
	}
	current, have := readCache(o.CachePath)
	ttl := o.TTL
	if have && stamp.Sub(current.CheckedAt) < ttl {
		// Another igdev fetched it while we waited for the lock.
		return current
	}
	next := Cache{CheckedAt: stamp, Latest: current.Latest}
	if latest, err := fetch(context.Background(), o); err == nil {
		next.Latest = latest
	}
	writeCache(o.CachePath, next)
	return next
}

// lockCache takes an exclusive advisory lock on the cache directory entry. A
// failure to lock (unwritable cache, unsupported platform) degrades to fetching
// without serialisation rather than blocking the command.
func lockCache(cachePath string) (func() bool, bool) {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() bool { return false }, false
	}
	file, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() bool { return false }, false
	}
	if err := flockExclusive(file); err != nil {
		file.Close()
		return func() bool { return false }, false
	}
	return func() bool {
		err := syscall.Flock(int(file.Fd()), lockUnblock)
		file.Close()
		return err == nil
	}, true
}

func fetch(ctx context.Context, o Options) (string, error) {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.Timeout > maxTimeout {
		o.Timeout = maxTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: o.Timeout}
	} else if o.HTTPClient.Timeout <= 0 || o.HTTPClient.Timeout > o.Timeout {
		o.HTTPClient.Timeout = o.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.APIURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("igdev/%s", o.Current))

	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("releases endpoint returned %s", resp.Status)
	}
	var payload struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Draft || payload.Prerelease || payload.TagName == "" {
		return "", fmt.Errorf("releases endpoint advertised no stable release")
	}
	return payload.TagName, nil
}

func readCache(path string) (Cache, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, false
	}
	var c Cache
	if err := json.Unmarshal(raw, &c); err != nil || c.CheckedAt.IsZero() {
		return Cache{}, false
	}
	return c, true
}

// writeCache stores the result atomically inside the cache directory, so a
// killed process can never leave a half-written file behind.
func writeCache(path string, c Cache) {
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return
	}
	_ = os.Rename(name, path)
}
