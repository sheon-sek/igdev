package itest

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheon-sek/igdev/internal/project"
)

// The real-Gateway soak (soak_test.go) records observations, never verdicts about
// the Gateway's insides: a series that rises is a trend to read, not a leak. These
// helpers turn the samples into that report. They are pure, so the suite checks
// them on every run even though the soak itself is opt-in.

// soakSample is one reading of every metric a soak step could measure.
type soakSample struct {
	At      time.Time          `json:"at"`
	Phase   string             `json:"phase"`
	Label   string             `json:"label"`
	Metrics map[string]float64 `json:"metrics"`
}

// soakTrend summarises one metric over one phase.
type soakTrend struct {
	Phase, Metric            string
	N                        int
	First, Last, Min, Max    float64
	PerHour                  float64 // least-squares slope over wall time; 0 with fewer than two samples
	SpanSeconds              float64
	firstAt, lastAt          time.Time
	sumT, sumV, sumTT, sumTV float64
}

// soakTrends groups the samples by phase and metric, in first-seen order.
func soakTrends(samples []soakSample) []soakTrend {
	index := map[string]*soakTrend{}
	var order []string
	for _, sample := range samples {
		names := make([]string, 0, len(sample.Metrics))
		for name := range sample.Metrics {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := sample.Metrics[name]
			key := sample.Phase + "\x00" + name
			trend, ok := index[key]
			if !ok {
				trend = &soakTrend{Phase: sample.Phase, Metric: name, First: value, Min: value, Max: value, firstAt: sample.At}
				index[key] = trend
				order = append(order, key)
			}
			x := sample.At.Sub(trend.firstAt).Hours()
			trend.N++
			trend.Last, trend.lastAt = value, sample.At
			trend.Min, trend.Max = math.Min(trend.Min, value), math.Max(trend.Max, value)
			trend.sumT += x
			trend.sumV += value
			trend.sumTT += x * x
			trend.sumTV += x * value
		}
	}
	out := make([]soakTrend, 0, len(order))
	for _, key := range order {
		trend := *index[key]
		n := float64(trend.N)
		if denominator := n*trend.sumTT - trend.sumT*trend.sumT; trend.N > 1 && denominator > 0 {
			trend.PerHour = (n*trend.sumTV - trend.sumT*trend.sumV) / denominator
		}
		trend.SpanSeconds = trend.lastAt.Sub(trend.firstAt).Seconds()
		out = append(out, trend)
	}
	return out
}

// soakTrendTable renders the trends as a Markdown table.
func soakTrendTable(trends []soakTrend) string {
	var b strings.Builder
	b.WriteString("| phase | metric | n | span | first | last | min | max | slope/h |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, trend := range trends {
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s | %s | %s |\n",
			trend.Phase, trend.Metric, trend.N,
			(time.Duration(trend.SpanSeconds) * time.Second).String(),
			soakNumber(trend.First), soakNumber(trend.Last), soakNumber(trend.Min), soakNumber(trend.Max),
			soakNumber(trend.PerHour))
	}
	return b.String()
}

func soakNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// parseDockerSize reads the human sizes docker stats and docker system df print —
// "1.5GiB", "512MiB", "1.2GB", "0B" — as bytes.
func parseDockerSize(s string) (float64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		scale  float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"B", 1},
	}
	for _, unit := range units {
		if number, ok := strings.CutSuffix(s, unit.suffix); ok {
			value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
			if err != nil {
				return 0, fmt.Errorf("size %q: %w", s, err)
			}
			return value * unit.scale, nil
		}
	}
	return 0, fmt.Errorf("size %q has no unit", s)
}

// soakForeignRemovals lists the refs among a cleanup's removed items that the soak
// does not own: a container, volume or network outside its namespace, an image
// that was not attributed to it just before, or an official image that was on the
// engine before the soak started. namespaces is every Instance the soak minted.
// A soak may remove nothing else.
func soakForeignRemovals(namespaces []string, items []soakCleanupItem, owned map[string]bool, preexisting map[string]bool, checkout string) []string {
	inNamespace := func(ref, separator string) bool {
		for _, ns := range namespaces {
			if strings.HasPrefix(ref, ns+separator) {
				return true
			}
		}
		return false
	}
	var foreign []string
	for _, item := range items {
		if item.Action != "removed" {
			continue
		}
		ok := false
		switch item.Kind {
		case "container":
			ok = inNamespace(item.Ref, "-") || owned[item.Ref]
		case "volume", "network":
			ok = inNamespace(item.Ref, "_")
		case "image":
			ok = owned[item.Ref]
		case "base_image":
			ok = !preexisting[item.Ref]
		case "checkout_setup":
			ok = strings.HasPrefix(item.Ref, checkout+"/") || item.Ref == checkout
		}
		if !ok {
			foreign = append(foreign, item.Kind+" "+item.Ref)
		}
	}
	return foreign
}

// soakCleanupItem is one item of a cleanup envelope's data.items.
type soakCleanupItem struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

func TestSoakTrendsReportSlopeAndRange(t *testing.T) {
	start := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	samples := []soakSample{
		{At: start, Phase: "long", Metrics: map[string]float64{"rss": 100, "flat": 7}},
		{At: start.Add(30 * time.Minute), Phase: "long", Metrics: map[string]float64{"rss": 150, "flat": 7}},
		{At: start.Add(time.Hour), Phase: "long", Metrics: map[string]float64{"rss": 200, "flat": 7}},
		{At: start.Add(2 * time.Hour), Phase: "other", Metrics: map[string]float64{"rss": 5}},
	}
	trends := soakTrends(samples)
	if len(trends) != 3 {
		t.Fatalf("trends = %+v, want flat and rss for long plus rss for other", trends)
	}
	byKey := map[string]soakTrend{}
	for _, trend := range trends {
		byKey[trend.Phase+"/"+trend.Metric] = trend
	}
	rss := byKey["long/rss"]
	if rss.N != 3 || rss.First != 100 || rss.Last != 200 || rss.Min != 100 || rss.Max != 200 {
		t.Errorf("long/rss = %+v", rss)
	}
	if math.Abs(rss.PerHour-100) > 1e-9 || rss.SpanSeconds != 3600 {
		t.Errorf("long/rss slope = %v per hour over %vs, want 100 over 3600s", rss.PerHour, rss.SpanSeconds)
	}
	if flat := byKey["long/flat"]; flat.PerHour != 0 {
		t.Errorf("a constant series has slope %v", flat.PerHour)
	}
	if single := byKey["other/rss"]; single.N != 1 || single.PerHour != 0 {
		t.Errorf("a single sample = %+v, want slope 0", single)
	}
	table := soakTrendTable(trends)
	if !strings.Contains(table, "| long | rss | 3 | 1h0m0s | 100 | 200 | 100 | 200 | 100 |") {
		t.Errorf("table row missing:\n%s", table)
	}
}

func TestSoakParsesDockerSizes(t *testing.T) {
	for in, want := range map[string]float64{
		"0B": 0, "512B": 512, "1.5KiB": 1536, "2MiB": 2 << 20, "1.25GiB": 1.25 * (1 << 30),
		"1.2kB": 1200, "3MB": 3e6, "4.5GB": 4.5e9, " 10MiB ": 10 << 20,
	} {
		got, err := parseDockerSize(in)
		if err != nil || got != want {
			t.Errorf("parseDockerSize(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "12", "abcMiB", "--"} {
		if _, err := parseDockerSize(in); err == nil {
			t.Errorf("parseDockerSize(%q) accepted", in)
		}
	}
}

// The soak's own guard: only what it owns may appear removed.
func TestSoakForeignRemovals(t *testing.T) {
	ns := "igdev-0123abcd"
	namespaces := []string{"igdev-ffff0000", ns}
	owned := map[string]bool{"sha256:aa": true}
	preexisting := map[string]bool{"inductiveautomation/ignition:8.3.8": true}
	items := []soakCleanupItem{
		{Kind: "container", Ref: ns + "-gateway-1", Action: "removed"},
		{Kind: "volume", Ref: ns + "_gateway-data", Action: "removed"},
		{Kind: "network", Ref: ns + "_default", Action: "removed"},
		{Kind: "image", Ref: "sha256:aa", Action: "removed"},
		{Kind: "base_image", Ref: "inductiveautomation/ignition:8.3.9", Action: "removed"},
		{Kind: "checkout_setup", Ref: "/tmp/soak/checkout/.igdev", Action: "removed"},
		{Kind: "image", Ref: "sha256:bb", Action: "kept"},
		{Kind: "container", Ref: "igdev-7bfb3a52-gateway-1", Action: "kept"},
	}
	if got := soakForeignRemovals(namespaces, items, owned, preexisting, "/tmp/soak/checkout"); len(got) != 0 {
		t.Fatalf("owned removals reported foreign: %v", got)
	}
	foreign := []soakCleanupItem{
		{Kind: "container", Ref: "igdev-7bfb3a52-gateway-1", Action: "removed"},
		{Kind: "volume", Ref: "igdev-7bfb3a52_gateway-data", Action: "removed"},
		{Kind: "image", Ref: "sha256:bb", Action: "removed"},
		{Kind: "base_image", Ref: "inductiveautomation/ignition:8.3.8", Action: "removed"},
		{Kind: "checkout_setup", Ref: "/elsewhere/.igdev", Action: "removed"},
		{Kind: "dir", Ref: "/home/x/.cache/igdev", Action: "removed"},
	}
	if got := soakForeignRemovals(namespaces, foreign, owned, preexisting, "/tmp/soak/checkout"); len(got) != len(foreign) {
		t.Fatalf("foreign removals = %v, want all %d", got, len(foreign))
	}
}

// A soak never adopts a checkout it did not create: an existing report directory,
// a symlink in its place, or an existing checkout is refused, and the Setup and
// data already there are left byte for byte as they were.
func TestSoakClaimDirsRefusesAnExistingCheckout(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "report")
	checkout := filepath.Join(existing, "checkout")
	files := map[string]string{
		filepath.Join(checkout, project.ContractFile):                  "[project]\nname = \"someone-else\"\n",
		filepath.Join(checkout, project.StateDir, project.SetupRecord): `{"instance_id":"7bfb3a52"}`,
		filepath.Join(checkout, "data", "project.json"):                "retained",
		filepath.Join(existing, "commands.jsonl"):                      "{\"args\":[\"earlier run\"]}\n",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"existing": existing, "symlink": link, "dangling symlink": dangling} {
		if got, _, err := soakClaimDirs(dir); err == nil {
			t.Errorf("%s: claimed %s, want a refusal", name, got)
		}
	}
	var after []string
	_ = filepath.WalkDir(existing, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			after = append(after, path)
		}
		return err
	})
	if len(after) != len(files) {
		t.Errorf("the existing checkout now holds %v, want only %d original files", after, len(files))
	}
	for path, body := range files {
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Errorf("%s = %q, %v; want it untouched (%q)", path, got, err, body)
		}
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dangling), "absent")); err == nil {
		t.Error("a dangling symlink's target was created")
	}
}

// A fresh report directory and its checkout are created empty; with none named,
// two runs started in the same second still get directories of their own.
func TestSoakClaimDirsCreatesFreshDirectories(t *testing.T) {
	named := filepath.Join(t.TempDir(), "nested", "report")
	report, checkout, err := soakClaimDirs(named)
	if err != nil {
		t.Fatal(err)
	}
	if report != named || checkout != filepath.Join(named, "checkout") {
		t.Errorf("claimed %s and %s, want %s and its checkout", report, checkout, named)
	}
	if entries, err := os.ReadDir(checkout); err != nil || len(entries) != 0 {
		t.Errorf("checkout = %v, %v; want an empty directory", entries, err)
	}

	t.Setenv("TMPDIR", t.TempDir())
	first, _, err := soakClaimDirs("")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := soakClaimDirs("")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Errorf("two default runs share %s", first)
	}
	for _, dir := range []string{first, second} {
		if !strings.HasPrefix(filepath.Base(dir), "igdev-soak-") || filepath.Dir(dir) != os.TempDir() {
			t.Errorf("default report directory %s, want igdev-soak-* under $TMPDIR", dir)
		}
	}
}

// soakPreexisting finds a namespace's objects on the engine as the soak found it,
// so a Setup naming an Instance the soak did not mint is refused.
func TestSoakPreexisting(t *testing.T) {
	ns := "igdev-0123abcd"
	before := soakEngine{
		containers: map[string]string{
			"c1": "/" + ns + "-gateway-1 exited 2026-10-01T00:00:00Z",
			"c2": "/igdev-7bfb3a52-gateway-1 running 2026-10-01T00:00:00Z",
		},
		volumes:  map[string]bool{ns + "_gateway-data": true, "igdev-7bfb3a52_gateway-data": true},
		networks: map[string]bool{"0f0f": true},
		images:   map[string]bool{"sha256:aa": true},
		tags:     map[string]bool{ns + ":8.3.10": true, "inductiveautomation/ignition:8.3.10": true},
	}
	want := []string{"container /" + ns + "-gateway-1", "image " + ns + ":8.3.10", "volume " + ns + "_gateway-data"}
	if got := soakPreexisting(ns, before); !slices.Equal(got, want) {
		t.Errorf("soakPreexisting = %v, want %v", got, want)
	}
	if got := soakPreexisting("igdev-ffff0000", before); len(got) != 0 {
		t.Errorf("a freshly minted namespace matched %v", got)
	}
}
