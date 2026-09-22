package testrig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/config"
)

// ReleaseServer is a loopback stand-in for the GitHub Releases API endpoint the
// update notice queries. Tests point igdev at it through the frozen config tier
// `updater.releases_api_url`, so no test ever reaches the real network.
type ReleaseServer struct {
	// URL is the endpoint igdev queries.
	URL string
	// Env is the environment assignment that points igdev at this server.
	Env string

	server *httptest.Server
	mu     sync.Mutex
	hits   int
	tag    string
	status int
	// body overrides the normal JSON payload, for malformed-response tests.
	body    string
	latency time.Duration
}

// ServeLatestRelease starts a loopback server advertising one release tag and
// registers its URL for golden normalization.
func ServeLatestRelease(t *testing.T, tag string) *ReleaseServer {
	t.Helper()
	rs := &ReleaseServer{tag: tag, status: http.StatusOK}
	rs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.hits++
		latency, status, body, tag := rs.latency, rs.status, rs.body, rs.tag
		rs.mu.Unlock()
		if latency > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(latency):
			}
		}
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, body)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   tag,
			"name":       tag,
			"draft":      false,
			"prerelease": false,
		}); err != nil {
			t.Errorf("serve release payload: %v", err)
		}
	}))
	rs.URL = rs.server.URL + "/repos/sheon-sek/igdev/releases/latest"
	rs.Env = EnvFor("updater.releases_api_url") + "=" + rs.URL
	t.Cleanup(rs.Close)
	return rs
}

// Hits reports how many queries the server answered; the cache tests assert that
// a fresh cache means no new query.
func (rs *ReleaseServer) Hits() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.hits
}

// SetStatus makes every response fail with an HTTP status, standing in for a
// rate-limited or missing release.
func (rs *ReleaseServer) SetStatus(status int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.status = status
	rs.body = ""
}

// SetLatency makes every response wait, to prove the timeout ceiling holds.
func (rs *ReleaseServer) SetLatency(d time.Duration) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.latency = d
}

// SetBody serves a raw payload, for malformed-response behaviour.
func (rs *ReleaseServer) SetBody(body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.body = body
	rs.status = http.StatusOK
}

// Close shuts the server down.
func (rs *ReleaseServer) Close() { rs.server.Close() }

// ServeDir starts a loopback HTTP file server over dir, standing in for the
// GitHub Releases download endpoint that serves a packaged artifact tree. The
// server shuts down with the test.
func ServeDir(t *testing.T, dir string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(dir)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

// DownloadServer is a loopback stand-in for the Maven host the pinned Jython
// standalone checker is fetched from. It counts every request, so a test can
// assert that parallel processes fetched the artifact exactly once, and it can
// be pointed at a corrupted tree instead.
type DownloadServer struct {
	// URL is the repository base igdev is pointed at.
	URL string

	server *httptest.Server
	dir    string
	mu     sync.Mutex
	hits   int
	status int
}

// ServeDownloadDir starts a counting file server over dir, which must hold the
// Maven layout `<version>/jython-standalone-<version>.jar`.
func ServeDownloadDir(t *testing.T, dir string) *DownloadServer {
	t.Helper()
	ds := &DownloadServer{dir: dir, status: http.StatusOK}
	ds.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ds.mu.Lock()
		ds.hits++
		status := ds.status
		ds.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		http.FileServer(http.Dir(ds.dir)).ServeHTTP(w, r)
	}))
	ds.URL = ds.server.URL
	t.Cleanup(ds.Close)
	return ds
}

// Hits reports how many requests the server answered.
func (ds *DownloadServer) Hits() int {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return ds.hits
}

// SetStatus makes every response fail with an HTTP status, standing in for a
// mirror that is down.
func (ds *DownloadServer) SetStatus(status int) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.status = status
}

// Close shuts the server down.
func (ds *DownloadServer) Close() { ds.server.Close() }

// PointAtDownload makes this scratch environment fetch the pinned artifact from
// ds, and collapses the URL in goldens.
func (e *Env) PointAtDownload(ds *DownloadServer) {
	e.RegisterReplacement(ds.URL, "<MAVEN_URL>")
	e.SetBaseEnv(EnvFor("jython.maven_base_url") + "=" + ds.URL)
}

// EnvFor returns the IGDEV_* environment name that sets a frozen config key. It
// panics on an unknown key: the schema is frozen, so that is a rig bug.
func EnvFor(path string) string {
	for _, k := range config.Schema {
		if k.Path == path {
			return k.Env
		}
	}
	panic(fmt.Sprintf("config key %q is not in the frozen schema", path))
}

// PointAt makes this scratch environment use rs as its releases endpoint: the
// env tier is set for every run and the URL collapses to a token in goldens.
func (e *Env) PointAt(rs *ReleaseServer) {
	e.RegisterReplacement(rs.URL, "<RELEASE_URL>")
	e.RegisterReplacement(rs.server.URL, "<RELEASE_SERVER>")
	e.SetBaseEnv(rs.Env)
}
