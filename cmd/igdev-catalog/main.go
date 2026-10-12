// Command igdev-catalog generates Core Catalog data for a new Ignition version
// from the official image and a real Gateway's /openapi.json. It is a dev-only
// tool, run with `go run ./cmd/igdev-catalog`: it is not packaged and is not part
// of the CLI Contract. See "Adding an Ignition version" in docs/IGDEV.md.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/lookup"
)

const usage = `usage:
  igdev-catalog modules --image <ref> [--output <file>]
  igdev-catalog rest --openapi <openapi.json> --version <ver> [--compare <rest-endpoints.tsv>] [--output <file>]
  igdev-catalog functions-check --image <ref> --version <catalog version> [--output <file>]
`

// imageReader is what the image subcommands read; tests replace it.
type imageReader struct {
	id        func(image string) (string, error)
	modules   func(image, scratch string) ([]lookup.ModuleFile, error)
	functions func(image, scratch string, known []string) ([]lookup.Function, error)
}

var docker = imageReader{id: lookup.ImageID, modules: lookup.ImageModules, functions: lookup.ImageFunctions}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "igdev-catalog:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	image := fs.String("image", "", "Ignition image reference")
	openapi := fs.String("openapi", "", "a Gateway /openapi.json document")
	version := fs.String("version", "", "Ignition version")
	compare := fs.String("compare", "", "an existing rest-endpoints.tsv to diff against")
	output := fs.String("output", "", "write to this file instead of stdout")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var out []byte
	var err error
	switch args[0] {
	case "modules":
		if *image == "" {
			return errors.New("modules needs --image")
		}
		out, err = modules(*image)
	case "rest":
		if *openapi == "" || *version == "" {
			return errors.New("rest needs --openapi and --version")
		}
		out, err = rest(*openapi, *version, *compare, stderr)
	case "functions-check":
		if *image == "" || *version == "" {
			return errors.New("functions-check needs --image and --version")
		}
		out, err = functionsCheck(*image, *version)
	default:
		return fmt.Errorf("unknown subcommand %q\n%s", args[0], usage)
	}
	if err != nil {
		return err
	}
	if *output != "" {
		return os.WriteFile(*output, out, 0o644)
	}
	_, err = stdout.Write(out)
	return err
}

// modules renders the builtin-modules.tsv plane of an image.
func modules(image string) ([]byte, error) {
	id, err := docker.id(image)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (pull it first)", image, err)
	}
	scratch, err := os.MkdirTemp("", "igdev-catalog-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	found, err := docker.modules(image, scratch)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s ships no readable module archive", image)
	}
	var b strings.Builder
	b.WriteString("# Ignition built-in Docker image module identifiers.\n")
	fmt.Fprintf(&b, "# Source: %s (image id %s), user-lib/modules/*.modl module.xml.\n", image, id)
	b.WriteString("# Format: module_id<TAB>module_filename\n")
	for _, m := range found {
		fmt.Fprintf(&b, "%s\t%s\n", m.ID, m.Filename)
	}
	return []byte(b.String()), nil
}

// rest renders the core REST plane from an OpenAPI document and reports a
// summary, plus the rows added and removed against compare, on stderr.
func rest(openapi, version, compare string, stderr io.Writer) ([]byte, error) {
	operations, raw, fault := catalog.ParseOpenAPIFile(openapi, openapi)
	if fault != nil {
		return nil, errors.New(fault.Message)
	}
	sum := sha256.Sum256(raw)
	out := catalog.RenderCoreREST(version, hex.EncodeToString(sum[:]), operations)
	fmt.Fprintf(stderr, "operations: %d\n", len(operations))
	if compare != "" {
		old, err := os.ReadFile(compare)
		if err != nil {
			return nil, err
		}
		added, removed := diffRows(rows(old), rows(out))
		fmt.Fprintf(stderr, "added against %s: %d\n", compare, len(added))
		for _, row := range added {
			fmt.Fprintln(stderr, "+ "+row)
		}
		fmt.Fprintf(stderr, "removed against %s: %d\n", compare, len(removed))
		for _, row := range removed {
			fmt.Fprintln(stderr, "- "+row)
		}
	}
	return out, nil
}

// rows are the data lines of a TSV plane.
func rows(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// diffRows lists what next has that old lacks, and the reverse, each sorted.
func diffRows(old, next []string) (added, removed []string) {
	in := func(list []string) map[string]bool {
		set := map[string]bool{}
		for _, s := range list {
			set[s] = true
		}
		return set
	}
	oldSet, nextSet := in(old), in(next)
	for _, s := range next {
		if !oldSet[s] {
			added = append(added, s)
		}
	}
	for _, s := range old {
		if !nextSet[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// unverifiable are the namespaces whose functions no bundle documents, so the
// image cannot confirm or refute their catalog rows.
var unverifiable = []string{"system.perspective."}

// functionsCheck compares the Gateway-scope functions an image's bundles
// document with the native function plane of a catalog version. It is a report:
// it never fails on a difference.
//
// The catalog lists Gateway-scope functions, so a Client- or Designer-only
// function and a constant (an upper-case leaf such as system.db.INTEGER) are not
// compared, and a deprecated function the catalog lacks is listed apart. A catalog row whose required module the image does not ship cannot
// be documented by it, and a namespace without bundles cannot be either; both
// are listed apart from the rows the image no longer documents.
func functionsCheck(image, version string) ([]byte, error) {
	core, fault := catalog.Core(version)
	if fault != nil {
		return nil, errors.New(fault.Message)
	}
	var known []string
	requires := map[string][]string{}
	for _, row := range core.Functions {
		known = append(known, row.Function)
		requires[row.Function] = row.Modules
	}
	scratch, err := os.MkdirTemp("", "igdev-catalog-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	shipped, err := docker.modules(image, scratch)
	if err != nil {
		return nil, err
	}
	inImage := map[string]bool{}
	for _, m := range shipped {
		inImage[m.ID] = true
	}
	found, err := docker.functions(image, scratch, known)
	if err != nil {
		return nil, err
	}
	// gone compares against every scope: a catalog row the image documents for
	// another scope only is still documented. missing compares Gateway-scope
	// functions only, and a deprecated one is reported apart.
	var documented, every []string
	deprecated := map[string]bool{}
	for _, fn := range found {
		every = append(every, fn.Name)
		if gatewayScope(fn) && !constant(fn.Name) {
			documented = append(documented, fn.Name)
			deprecated[fn.Name] = fn.Deprecated != ""
		}
	}
	added, _ := diffRows(known, documented)
	_, gone := diffRows(known, every)
	var missing, missingDeprecated []string
	for _, name := range added {
		if deprecated[name] {
			missingDeprecated = append(missingDeprecated, name)
		} else {
			missing = append(missing, name)
		}
	}
	var removed, noBundles, notShipped []string
	for _, name := range gone {
		switch {
		case hasPrefix(name, unverifiable):
			noBundles = append(noBundles, name)
		case !allShipped(requires[name], inImage):
			notShipped = append(notShipped, name)
		default:
			removed = append(removed, name)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s against the %s native function catalog: %d Gateway-scope functions documented, %d in the catalog.\n",
		image, version, len(documented), len(known))
	section(&b, "documented by the image, missing from the catalog", missing)
	section(&b, "documented by the image as deprecated, missing from the catalog", missingDeprecated)
	section(&b, "in the catalog, no longer documented by the image", removed)
	section(&b, "in the catalog, not verifiable from bundles ("+strings.Join(unverifiable, ", ")+"*)", noBundles)
	section(&b, "in the catalog, required module not shipped in the image", notShipped)
	return []byte(b.String()), nil
}

func gatewayScope(fn lookup.Function) bool {
	for _, s := range fn.Scopes {
		if s == lookup.ScopeGateway {
			return true
		}
	}
	return false
}

// constant reports whether a function name's leaf is a constant or a class,
// such as system.db.INTEGER or system.secrets.SecretException.
func constant(name string) bool {
	leaf := name[strings.LastIndex(name, ".")+1:]
	return leaf != "" && leaf[0] >= 'A' && leaf[0] <= 'Z'
}

func allShipped(modules []string, inImage map[string]bool) bool {
	for _, m := range modules {
		if !inImage[m] {
			return false
		}
	}
	return true
}

func section(b *strings.Builder, title string, names []string) {
	fmt.Fprintf(b, "## %s: %d\n", title, len(names))
	for _, name := range names {
		b.WriteString(name + "\n")
	}
}

func hasPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
