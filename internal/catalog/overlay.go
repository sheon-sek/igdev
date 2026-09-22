package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// overlayDirective introduces a plane switch inside an overlay file. The rest of
// the line names the plane.
const overlayDirective = "plane:"

// OverlayPlanes are the three knowledge planes a Project Overlay may extend. The
// built-in module list is not one of them: it is a fact about the Docker image,
// not something a repository can add to.
var OverlayPlanes = []string{string(KindNativeFunction), string(KindAlias), string(KindREST)}

// planeOf maps an overlay directive to the data plane it feeds.
func planeOf(name string) (Plane, bool) {
	switch name {
	case string(KindNativeFunction):
		return PlaneFunctions, true
	case string(KindAlias), "capability-rule", "rule":
		return PlaneRules, true
	case string(KindREST):
		return PlaneREST, true
	default:
		return "", false
	}
}

// Overlay is a parsed Project Overlay: the tracked capability knowledge a
// repository adds to the Core Catalog, plus where it was read from.
//
// The format is one TSV per file with the same column shapes as the Core Catalog
// planes, so a row is reviewable next to the embedded row it belongs with. A
// comment line switches the active plane:
//
//	# igdev Project Overlay
//	# plane: rest
//	GET	/data/acme/api/v1/widgets	module	com.acme.widgets
//	# plane: native-function
//	system.acme.ping	module	com.acme.widgets
//	# plane: capability-rule
//	prefix	acme.widgets.	com.acme.widgets	Acme widget scripting helpers
//
// Rows before the first directive are refused: a row whose plane the reader has
// to guess is a row that resolves differently than its author meant.
type Overlay struct {
	// Paths are the declared overlay files, in resolution order.
	Paths []string
	// Digest is the sha256 of the declared files' bytes, in that order.
	Digest string
	// Catalog is the parsed overlay rows.
	Catalog *Catalog
}

// LoadOverlay reads and parses the Project Overlay files a Project Contract
// declares, in order, relative to the Project Root. An empty list is the common
// case and yields an empty overlay with a digest over no bytes.
func LoadOverlay(root string, paths []string) (*Overlay, *contract.Fault) {
	overlay := &Overlay{Paths: append([]string(nil), paths...), Catalog: &Catalog{}}
	rows := map[Plane][]tsvRow{}
	digestRows := make([]digestRow, 0, len(paths))
	for _, declared := range paths {
		full, err := overlayPath(root, declared)
		if err != nil {
			return nil, err
		}
		raw, readErr := os.ReadFile(full)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return nil, contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
					fmt.Sprintf("project overlay %s is declared in the Project Contract but does not exist", declared)).
					WithRemediation(contract.Remediation{
						Command: "igdev catalog status --json",
						Why:     "list the overlay files this contract declares",
					})
			}
			return nil, contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
				fmt.Sprintf("project overlay %s cannot be read: %v", declared, readErr)).WithCause(readErr)
		}
		fileRows, parseErr := overlayRows(raw, declared)
		if parseErr != nil {
			return nil, parseErr
		}
		for plane, planeRows := range fileRows {
			rows[plane] = append(rows[plane], planeRows...)
		}
		digestRows = append(digestRows, digestRow{name: declared, data: raw})
	}
	catalog, err := parsePlanes("", rows, LayerOverlay)
	if err != nil {
		return nil, err
	}
	overlay.Catalog = catalog
	overlay.Digest = digest(digestRows)
	return overlay, nil
}

// overlayFromBytes parses one overlay file's bytes, with the declared path used
// when a row has to be named in a fault. `catalog import-openapi` validates the
// content it is about to write this way, so a file the reader would refuse never
// reaches the disk.
func overlayFromBytes(declared string, raw []byte) (*Overlay, *contract.Fault) {
	rows, err := overlayRows(raw, declared)
	if err != nil {
		return nil, err
	}
	parsed, err := parsePlanes("", rows, LayerOverlay)
	if err != nil {
		return nil, err
	}
	return &Overlay{
		Paths:   []string{declared},
		Digest:  digest([]digestRow{{name: declared, data: raw}}),
		Catalog: parsed,
	}, nil
}

// overlayPath resolves one declared overlay path. The contract validation
// already refuses absolute paths and parent traversal; this is the belt to that
// brace, because a path outside the repository is never a tracked file.
func overlayPath(root, declared string) (string, *contract.Fault) {
	if strings.TrimSpace(declared) == "" {
		return "", contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
			"the Project Contract declares an empty overlay path")
	}
	if filepath.IsAbs(declared) {
		return "", contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
			fmt.Sprintf("project overlay %s is an absolute path; overlay files are tracked and repository-relative", declared))
	}
	full := filepath.Join(root, declared)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
			fmt.Sprintf("project overlay %s resolves outside the Project Root", declared))
	}
	return full, nil
}

// overlayRows splits one overlay file into the four planes it feeds, keeping the
// line numbers so a bad row is named where the author can see it.
func overlayRows(raw []byte, declared string) (map[Plane][]tsvRow, *contract.Fault) {
	out := map[Plane][]tsvRow{}
	plane := Plane("")
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		number := i + 1
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			directive := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if !strings.HasPrefix(directive, overlayDirective) {
				continue
			}
			name := strings.TrimSpace(strings.TrimPrefix(directive, overlayDirective))
			resolved, ok := planeOf(name)
			if !ok {
				return nil, contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
					fmt.Sprintf("project overlay %s line %d: plane %q is not one of %s",
						declared, number, name, strings.Join(OverlayPlanes, ", ")))
			}
			plane = resolved
			continue
		}
		if trimmed == "" {
			continue
		}
		if plane == "" {
			return nil, contract.NewFault(contract.CodeOverlayInvalid, contract.ExitFailure,
				fmt.Sprintf("project overlay %s line %d: row appears before any `# %s <plane>` directive; add one of %s",
					declared, number, overlayDirective, strings.Join(OverlayPlanes, ", ")))
		}
		out[plane] = append(out[plane], tsvRow{line: number, cells: strings.Split(line, "\t")})
	}
	return out, nil
}
