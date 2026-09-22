// Package jython owns the pinned Jython standalone checker: where its artifact
// lives on this machine, how it is fetched exactly once even under parallel
// igdev processes, and how project code is compiled against it in one JVM.
//
// The artifact is pinned by sha256 in an embedded, version-keyed table. Nothing
// is ever executed from an unverified download: a cached entry is re-hashed on
// every use, and bytes that do not match the pin are quarantined beside the
// entry rather than trusted (or silently reused).
package jython

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Pins maps a Jython version to the sha256 of the standalone jar that version
// publishes to Maven Central.
//
// 2.7.4 is the version Ignition 8.3.8 embeds, and the digest is the artifact's
// published one: `jython-standalone-2.7.4.jar` from
// https://repo1.maven.org/maven2/org/python/jython-standalone/2.7.4/ (the
// repository's own `.sha256` file agrees).
var Pins = map[string]string{
	"2.7.4": "1fba1769effcc8b19f5e10436bc8274a158ce988559f257927c24c73bb137f3c",
}

// DefaultBaseURL is the Maven repository the pinned artifacts come from. The
// config key `jython.maven_base_url` overrides it, which is how the test suite
// points a run at a loopback stand-in instead of the network.
const DefaultBaseURL = "https://repo1.maven.org/maven2/org/python/jython-standalone"

// DirName is the cache subdirectory, under the XDG cache root.
const DirName = "jython"

// LockFileName is the per-entry lock: one file, taken exclusively, so parallel
// processes on one machine fetch the artifact exactly once.
const LockFileName = "jython.lock"

// The download's own bounds. The standalone jar is roughly 50 MB; a response
// that wants to be far larger than any real one is refused rather than written.
const (
	defaultTimeout   = 120 * time.Second
	maxArtifactBytes = 256 << 20
	// downloadAttempts is how many times one Ensure call downloads before it
	// gives up: the first mismatch quarantines and retries once.
	downloadAttempts = 2
)

// Spec wires one access to the cache. Everything a caller (or a test) needs to
// control is explicit here.
type Spec struct {
	// Version is the Jython version to check against.
	Version string
	// BaseURL is the Maven repository base (no trailing version segment).
	BaseURL string
	// Digest overrides the embedded pin. Empty means "use Pins[Version]"; the
	// config key `jython.sha256` sets it so CI can verify an artifact served
	// from a loopback stand-in.
	Digest string
	// CacheRoot is the XDG cache directory (~/.cache/igdev).
	CacheRoot string
	// Client and Timeout bound the fetch; defaults apply when unset.
	Client  *http.Client
	Timeout time.Duration
	// Now stamps a quarantined file's name; defaults to time.Now.
	Now func() time.Time
}

// Dir is the per-version cache directory.
func (s Spec) Dir() string { return filepath.Join(s.CacheRoot, DirName, s.Version) }

// JarPath is the cached artifact's path: the shared, disposable location every
// igdev process on this machine reads.
func (s Spec) JarPath() string {
	return filepath.Join(s.Dir(), fmt.Sprintf("jython-standalone-%s.jar", s.Version))
}

// URL is where the artifact is downloaded from.
func (s Spec) URL() string {
	return strings.TrimRight(s.BaseURL, "/") + "/" + s.Version + "/" + filepath.Base(s.JarPath())
}

func (s Spec) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Spec) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

// expectedDigest resolves the digest the artifact must hash to.
//
// The embedded table decides which versions igdev speaks at all; the
// `jython.sha256` override can only change what a pinned version must hash to
// (which is how CI verifies an artifact served from a loopback stand-in). An
// unpinned version fails closed whether or not an override is set.
func (s Spec) expectedDigest() (string, *contract.Fault) {
	pin, ok := Pins[s.Version]
	if !ok {
		return "", contract.NewFault(contract.CodeJythonVersionUnsupported, contract.ExitFailure,
			fmt.Sprintf("igdev carries no pinned sha256 for Jython %s", s.Version)).
			WithRemediation(contract.Remediation{
				Command: "igdev init",
				Why:     "point [ignition].jython_version at a version igdev pins (2.7.4)",
			})
	}
	if override := strings.ToLower(strings.TrimSpace(s.Digest)); override != "" {
		if !isDigest(override) {
			return "", contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
				fmt.Sprintf("config key jython.sha256 = %q is not a sha256 digest", s.Digest)).
				WithRemediation(contract.Remediation{
					Command: "igdev status --json",
					Why:     "report the config tiers igdev resolved",
				})
		}
		return override, nil
	}
	return pin, nil
}

// Ensure returns the path of a sha256-verified standalone jar, downloading it
// once when it is not already cached.
//
// The order is what makes the cache safe under parallelism and corruption:
// take the per-entry flock, re-hash what is there, quarantine anything that does
// not match, download to a temp file inside the cache directory, and rename it
// into place only after it verified. A second mismatch is a fault, never a
// usable artifact.
func Ensure(s Spec) (string, *contract.Fault) {
	expected, fault := s.expectedDigest()
	if fault != nil {
		return "", fault
	}
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		return "", fetchFault(err)
	}
	release, err := acquire(filepath.Join(s.Dir(), LockFileName))
	if err != nil {
		return "", fetchFault(err)
	}
	defer release()

	jar := s.JarPath()
	if sum, err := digestOf(jar); err == nil && sum == expected {
		return jar, nil
	}
	// Whatever is there is not the pinned artifact: move it aside so no path can
	// pick it up again.
	quarantine(jar, s.now())

	var last string
	for range downloadAttempts {
		sum, err := s.download(jar)
		if err != nil {
			return "", fetchFault(err)
		}
		if sum == expected {
			return jar, nil
		}
		last = sum
		quarantine(jar, s.now())
	}
	return "", mismatchFault(s, expected, last)
}

// download fetches the artifact to a temp file inside the cache directory and
// returns its sha256. The temp file never becomes the artifact by being renamed
// away from the cache: the rename happens in the cache directory, so no path
// crosses a filesystem boundary.
func (s Spec) download(jar string) (string, error) {
	resp, err := s.client().Get(s.URL())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", s.URL(), resp.Status)
	}
	tmp, err := os.CreateTemp(s.Dir(), ".download-")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	// A no-op once the rename below succeeds, and the cleanup path otherwise.
	defer os.Remove(name)

	sum := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(resp.Body, maxArtifactBytes+1))
	if err != nil {
		tmp.Close()
		return "", err
	}
	if written > maxArtifactBytes {
		tmp.Close()
		return "", fmt.Errorf("%s is larger than the %d byte limit", s.URL(), maxArtifactBytes)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(name, jar); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// quarantine moves a rejected artifact beside itself, named for when it was
// rejected. It is a rename, not a deletion: the bytes stay inspectable, and the
// cache directory still reclaims all of them when deleted.
func quarantine(path string, now time.Time) {
	if _, err := os.Stat(path); err != nil {
		return
	}
	target := path + ".corrupt-" + now.UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, target); err != nil {
		// A cache entry that cannot be moved aside must not be reusable either.
		_ = os.Remove(path)
	}
}

// digestOf hashes a file, or reports why it could not be read.
func digestOf(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func isDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func fetchFault(err error) *contract.Fault {
	return contract.NewFault(contract.CodeJythonFetch, contract.ExitFailure,
		fmt.Sprintf("cannot fetch the pinned Jython artifact: %v", err)).WithCause(err).
		WithRemediation(contract.Remediation{
			Command: "igdev doctor",
			Why:     "audit the toolchain and download prerequisites igdev needs",
		})
}

func mismatchFault(s Spec, want, got string) *contract.Fault {
	return contract.NewFault(contract.CodeChecksumMismatch, contract.ExitFailure,
		fmt.Sprintf("the Jython %s artifact failed sha256 verification %d times: want %s, got %s",
			s.Version, downloadAttempts, want, got)).
		WithRemediation(contract.Remediation{
			Command: "igdev doctor",
			Why:     "audit the toolchain and download prerequisites igdev needs",
		})
}
