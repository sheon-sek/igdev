// Package xdg resolves igdev's machine-level directories. Every path is
// overridable through the XDG environment variables so the test rig can point
// a run at a throwaway root.
package xdg

import (
	"os"
	"path/filepath"
)

// App is the name used in every XDG path.
const App = "igdev"

// Dir holds the three machine-level directories.
type Dir struct {
	Cache  string // downloaded and cached artifacts, always disposable
	Config string // per-machine settings, including Consent records
	State  string // per-machine state, including logs
}

// Home returns the effective HOME used to resolve fallbacks.
func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return string(filepath.Separator)
}

// Resolve returns igdev's XDG directories for the current environment.
func Resolve() Dir {
	home := Home()
	return Dir{
		Cache:  filepath.Join(envOr("XDG_CACHE_HOME", filepath.Join(home, ".cache")), App),
		Config: filepath.Join(envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config")), App),
		State:  filepath.Join(envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), App),
	}
}

// DataDir returns the per-machine data directory (global cache in later
// tickets). It follows XDG_DATA_HOME.
func DataDir() string {
	home := Home()
	return filepath.Join(envOr("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), App)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
