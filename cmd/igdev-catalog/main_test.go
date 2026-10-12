package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/sheon-sek/igdev/internal/lookup"
)

const gatewayOpenAPI = "../../itest/testdata/openapi/gateway.json"

func fake(t *testing.T, r imageReader) {
	t.Helper()
	saved := docker
	docker = r
	t.Cleanup(func() { docker = saved })
}

func TestRestRendersTheCorePlane(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"rest", "--openapi", gatewayOpenAPI, "--version", "9.9.9"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if lines[0] != "# Ignition 9.9.9 public REST operation catalog generated from a real Gateway /openapi.json." ||
		!strings.HasPrefix(lines[1], "# source_sha256=") || len(lines[1]) != len("# source_sha256=")+64 ||
		lines[2] != "# method<TAB>path_template<TAB>owner_kind(platform|module|private-module)<TAB>required_module_ids(comma-separated or -)" {
		t.Fatalf("header:\n%s", strings.Join(lines[:3], "\n"))
	}
	rows := lines[3:]
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range rows {
		if len(strings.Split(row, "\t")) != 4 {
			t.Errorf("row %q has not four cells", row)
		}
	}
	if !strings.Contains(stderr.String(), "operations: ") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRestCompareReportsAddedAndRemoved(t *testing.T) {
	var full, stderr bytes.Buffer
	if err := run([]string{"rest", "--openapi", gatewayOpenAPI, "--version", "9.9.9"}, &full, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(full.String(), "\n"), "\n")
	dropped := lines[3]
	old := strings.Join(append(append([]string{}, lines[:3]...), lines[4:]...), "\n") + "\nGET\t/gone\tplatform\t-\n"
	path := t.TempDir() + "/old.tsv"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if err := run([]string{"rest", "--openapi", gatewayOpenAPI, "--version", "9.9.9", "--compare", path}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	report := stderr.String()
	if !strings.Contains(report, "+ "+dropped+"\n") || !strings.Contains(report, "- GET\t/gone\tplatform\t-\n") {
		t.Errorf("report:\n%s", report)
	}
}

func TestModulesRendersSortedRows(t *testing.T) {
	fake(t, imageReader{
		id: func(string) (string, error) { return "sha256:abc", nil },
		modules: func(string, string) ([]lookup.ModuleFile, error) {
			return []lookup.ModuleFile{{ID: "a.one", Filename: "One-module.modl"}, {ID: "b.two", Filename: "Two-module.modl"}}, nil
		},
	})
	var stdout bytes.Buffer
	if err := run([]string{"modules", "--image", "img:1"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "img:1 (image id sha256:abc)") ||
		!strings.Contains(got, "# Format: module_id<TAB>module_filename\na.one\tOne-module.modl\nb.two\tTwo-module.modl\n") {
		t.Errorf("output:\n%s", got)
	}
}

func TestFunctionsCheckSeparatesPerspective(t *testing.T) {
	gateway := []string{lookup.ScopeGateway}
	fake(t, imageReader{
		// The image ships every module but the MongoDB connector.
		modules: func(string, string) ([]lookup.ModuleFile, error) {
			var out []lookup.ModuleFile
			for _, id := range []string{"com.inductiveautomation.opcua", "com.inductiveautomation.opcua.drivers.bacnet",
				"com.inductiveautomation.alarm-notification", "com.inductiveautomation.sfc", "com.inductiveautomation.reporting",
				"com.inductiveautomation.opccom", "com.inductiveautomation.secsgem", "com.inductiveautomation.twilio",
				"com.inductiveautomation.historian", "com.inductiveautomation.eventstream", "com.inductiveautomation.perspective",
				"com.inductiveautomation.vision", "com.inductiveautomation.sqlbridge", "com.inductiveautomation.dnp3",
				"com.inductiveautomation.eam", "com.inductiveautomation.connectors.kafka", "com.inductiveautomation.phone-notification",
				"com.inductiveautomation.webdev", "com.inductiveautomation.mcp", "com.inductiveautomation.symbol-factory"} {
				out = append(out, lookup.ModuleFile{ID: id})
			}
			return out, nil
		},
		functions: func(_, _ string, known []string) ([]lookup.Function, error) {
			var out []lookup.Function
			for _, name := range known {
				if name == "system.tag.readBlocking" || strings.HasPrefix(name, "system.perspective.") ||
					strings.HasPrefix(name, "system.mongodb.") {
					continue
				}
				out = append(out, lookup.Function{Name: name, Scopes: gateway})
			}
			return append(out,
				lookup.Function{Name: "system.new.thing", Scopes: gateway},
				lookup.Function{Name: "system.db.INTEGER", Scopes: gateway},
				lookup.Function{Name: "system.gui.messageBox", Scopes: []string{lookup.ScopeClient}},
				lookup.Function{Name: "system.db.runQuery", Scopes: gateway, Deprecated: "use execQuery"},
			), nil
		},
	})
	var stdout bytes.Buffer
	if err := run([]string{"functions-check", "--image", "img:1", "--version", "8.3.8"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "## documented by the image, missing from the catalog: 1\nsystem.new.thing\n") ||
		!strings.Contains(got, "## in the catalog, no longer documented by the image: 1\nsystem.tag.readBlocking\n") ||
		!strings.Contains(got, "not verifiable from bundles (system.perspective.*): ") ||
		!strings.Contains(got, "as deprecated, missing from the catalog: 1\nsystem.db.runQuery\n") ||
		!strings.Contains(got, "required module not shipped in the image: 12\nsystem.mongodb.aggregate\n") ||
		strings.Contains(got, "readBlocking\nsystem.perspective") {
		t.Errorf("output:\n%s", got)
	}
}
