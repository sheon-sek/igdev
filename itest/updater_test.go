package itest

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/testrig"
	"github.com/sheon-sek/igdev/internal/updater"
)

// enableNotifier clears the frozen kill switch for this environment only.
func enableNotifier(env *testrig.Env) {
	env.SetBaseEnv("IGDEV_NO_UPDATE_NOTIFIER=")
}

// The notice is fetched at most once per 24 h and cached under the XDG cache
// directory; a second run within the window must not touch the network again.
func TestUpdateNoticeIsCachedFor24Hours(t *testing.T) {
	env := testrig.NewEnv(t)
	enableNotifier(env)
	server := testrig.ServeLatestRelease(t, "v9.9.9")
	env.PointAt(server)

	first := env.MustRun("version")
	testrig.WantExit(t, first, contract.ExitOK)
	env.Golden(t, "update_notice.txt", first.Stderr)
	if !strings.Contains(first.Stderr, "9.9.9") {
		t.Fatalf("no update notice on stderr:\n%s", first.Stderr)
	}
	if server.Hits() != 1 {
		t.Errorf("first run queried the endpoint %d times, want 1", server.Hits())
	}

	cache := env.CachePath(updater.CacheFileName)
	if !fileExists(cache) {
		t.Fatalf("no cache written at %s\nscratch tree:\n%s", env.Normalize(cache), env.Tree())
	}
	if info, err := os.Stat(cache); err == nil && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("cache mode = %v, want private", info.Mode().Perm())
	}

	second := env.MustRun("version")
	if second.Stderr != first.Stderr {
		t.Errorf("cached notice differs from the fetched one:\nfirst:  %q\nsecond: %q", first.Stderr, second.Stderr)
	}
	if server.Hits() != 1 {
		t.Errorf("second run queried the endpoint (hits = %d), want the 24 h cache reused", server.Hits())
	}

	// Machine output stays free of the notice, and free of the network.
	jsonRun := env.MustRun("version", "--json")
	testrig.WantExit(t, jsonRun, contract.ExitOK)
	if jsonRun.Stderr != "" {
		t.Errorf("--json emitted stderr: %q", jsonRun.Stderr)
	}
	if strings.Contains(jsonRun.Stdout, "available") {
		t.Errorf("--json leaked the update notice into stdout:\n%s", jsonRun.Stdout)
	}
	if server.Hits() != 1 {
		t.Errorf("--json run queried the endpoint (hits = %d)", server.Hits())
	}
	// status behaves the same way as version in human mode.
	statusRun := env.MustRun("status")
	if !strings.Contains(statusRun.Stderr, "9.9.9") {
		t.Errorf("status did not print the notice:\n%s", statusRun.Stderr)
	}
	if server.Hits() != 1 {
		t.Errorf("status queried the endpoint again (hits = %d)", server.Hits())
	}
}

// A cache older than the TTL triggers exactly one refetch and is rewritten with
// the new result.
func TestUpdateNoticeRefetchesStaleCache(t *testing.T) {
	env := testrig.NewEnv(t)
	enableNotifier(env)
	server := testrig.ServeLatestRelease(t, "v9.9.9")
	env.PointAt(server)

	stale := updater.Cache{
		CheckedAt: time.Now().Add(-25 * time.Hour),
		Latest:    "v0.5.0",
	}
	cache := env.CachePath(updater.CacheFileName)
	writeCache(t, cache, stale)

	res := env.MustRun("version")
	testrig.WantExit(t, res, contract.ExitOK)
	if server.Hits() != 1 {
		t.Fatalf("stale cache was not refetched (hits = %d)", server.Hits())
	}
	if !strings.Contains(res.Stderr, "igdev 9.9.9 is available") {
		t.Errorf("notice still advertises the stale version:\n%s", res.Stderr)
	}
	got := readCache(t, cache)
	if got.Latest != "v9.9.9" {
		t.Errorf("cache latest = %q, want the freshly fetched v9.9.9", got.Latest)
	}
	if time.Since(got.CheckedAt) > time.Minute {
		t.Errorf("cache checked_at = %v, want rewritten to now", got.CheckedAt)
	}
}

// IGDEV_NO_UPDATE_NOTIFIER=1 skips the check entirely: no query, no cache file.
// The rig's base environment sets it, so this is the default for every test.
func TestUpdateNoticeDisabledByEnvironment(t *testing.T) {
	env := testrig.NewEnv(t)
	server := testrig.ServeLatestRelease(t, "v9.9.9")
	env.PointAt(server)

	res := env.MustRun("version")
	testrig.WantExit(t, res, contract.ExitOK)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want silent with the notifier disabled", res.Stderr)
	}
	if server.Hits() != 0 {
		t.Errorf("disabled notifier still queried the endpoint %d times", server.Hits())
	}
	if fileExists(env.CachePath(updater.CacheFileName)) {
		t.Errorf("disabled notifier wrote a cache file")
	}
}

// The config tier can switch the notice off without the environment variable,
// and the frozen kill switch outranks every tier.
func TestUpdateNoticeDisabledByConfigTier(t *testing.T) {
	env := testrig.NewEnv(t)
	enableNotifier(env)
	server := testrig.ServeLatestRelease(t, "v9.9.9")
	env.PointAt(server)

	res := env.MustRun("version", "--config", "updater.enabled=false")
	testrig.WantExit(t, res, contract.ExitOK)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want silent with updater.enabled=false", res.Stderr)
	}
	if server.Hits() != 0 {
		t.Errorf("updater.enabled=false still queried the endpoint %d times", server.Hits())
	}

	// Same tier, but the kill switch wins.
	env.SetBaseEnv("IGDEV_NO_UPDATE_NOTIFIER=1")
	res = env.MustRun("version", "--config", "updater.enabled=true")
	testrig.WantExit(t, res, contract.ExitOK)
	if server.Hits() != 0 {
		t.Errorf("IGDEV_NO_UPDATE_NOTIFIER did not outrank the flag tier (%d hits)", server.Hits())
	}
}

// A release that is not newer than the running binary produces no notice, and a
// draft or prerelease is never advertised.
func TestUpdateNoticeOnlyForNewerStableRelease(t *testing.T) {
	env := testrig.NewEnv(t)
	enableNotifier(env)
	server := testrig.ServeLatestRelease(t, "v0.1.0")
	env.PointAt(server)

	res := env.MustRun("version")
	testrig.WantExit(t, res, contract.ExitOK)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want no notice when the binary is current", res.Stderr)
	}

	// A prerelease tag is not an offered update.
	server.SetBody(`{"tag_name":"v9.9.9-rc1","prerelease":true,"draft":false}`)
	writeCache(t, env.CachePath(updater.CacheFileName), updater.Cache{
		CheckedAt: time.Now().Add(-25 * time.Hour),
	})
	res = env.MustRun("version")
	testrig.WantExit(t, res, contract.ExitOK)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want no notice for a prerelease", res.Stderr)
	}
}

// An unreachable or broken endpoint must not change the exit level, print
// anything, or leave debris.
func TestUpdateNoticeFailureIsSilent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, env *testrig.Env, server *testrig.ReleaseServer)
	}{
		{"404", func(_ *testing.T, _ *testrig.Env, server *testrig.ReleaseServer) {
			server.SetStatus(http.StatusNotFound)
		}},
		{"malformed json", func(_ *testing.T, _ *testrig.Env, server *testrig.ReleaseServer) {
			server.SetBody("<html>rate limited</html>")
		}},
		{"timeout", func(_ *testing.T, _ *testrig.Env, server *testrig.ReleaseServer) {
			server.SetLatency(3 * time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testrig.NewEnv(t)
			enableNotifier(env)
			server := testrig.ServeLatestRelease(t, "v9.9.9")
			env.PointAt(server)
			tc.prepare(t, env, server)

			args := []string{"version"}
			if tc.name == "timeout" {
				// Prove the ceiling holds: the client gives up in 1 s.
				args = append(args, "--config", "updater.timeout_seconds=1")
			}
			res := env.MustRun(args...)
			testrig.WantExit(t, res, contract.ExitOK)
			if res.Stderr != "" {
				t.Errorf("stderr = %q, want a failed check to stay silent", res.Stderr)
			}
			if tc.name == "timeout" && res.Duration > 2500*time.Millisecond {
				t.Errorf("run took %v, want under the 1 s timeout ceiling", res.Duration)
			}
			// A failed check is still cached, so the next run does not retry.
			hitsBefore := server.Hits()
			env.MustRun("version")
			if server.Hits() != hitsBefore {
				t.Errorf("failed check was not cached: endpoint queried again")
			}
			env.AssertNoPartialWrites(t)
		})
	}
}

// The cache follows XDG_CACHE_HOME, which is what lets a test (and a user with a
// unusual layout) redirect it.
func TestUpdateNoticeHonoursXDGCacheHome(t *testing.T) {
	env := testrig.NewEnv(t)
	enableNotifier(env)
	xdgCache := env.Mkdir("xdgcache")
	env.SetBaseEnv("XDG_CACHE_HOME=" + xdgCache)
	server := testrig.ServeLatestRelease(t, "v9.9.9")
	env.PointAt(server)

	res := env.MustRun("version")
	testrig.WantExit(t, res, contract.ExitOK)
	if !strings.Contains(res.Stderr, "9.9.9") {
		t.Fatalf("no notice printed:\n%s", res.Stderr)
	}
	want := filepath.Join(xdgCache, "igdev", updater.CacheFileName)
	if !fileExists(want) {
		t.Errorf("cache not written under XDG_CACHE_HOME (%s)\nscratch tree:\n%s", env.Normalize(want), env.Tree())
	}
	if fileExists(env.CachePath(updater.CacheFileName)) {
		t.Errorf("cache written under HOME even though XDG_CACHE_HOME was set")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeCache(t *testing.T, path string, c updater.Cache) {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("encode cache: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}

func readCache(t *testing.T, path string) updater.Cache {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var c updater.Cache
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("decode cache %s: %v", raw, err)
	}
	return c
}
