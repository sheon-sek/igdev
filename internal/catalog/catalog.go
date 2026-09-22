// Package catalog implements the capability knowledge layer: the Core Catalog
// embedded in the binary and keyed by Ignition version, the tracked Project
// Overlay, the Effective Catalog that resolves the two together, and the scanner
// that finds the capabilities project code intends to use.
//
// The data is domain knowledge, not project state: nothing here reads a tracked
// catalog file at runtime, and a version with no embedded catalog fails closed
// instead of borrowing another version's rows (ADR 0005).
//
// The lookup order, the REST path matching, the classification vocabulary, and
// the diagnostic wording were ported from the predecessor bash toolchain, which
// was the executable specification until this package's tests pinned them; that
// toolchain is retired, so this package and its goldens are the specification now.
package catalog

import (
	"fmt"
	"strings"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Target is the Ignition version igdev 0.1 ships a Core Catalog for. Adding a
// version means adding embedded data and its digest, never changing code.
const Target = "8.3.8"

// Layer names where a resolved row came from.
type Layer string

const (
	// LayerCore is the embedded Core Catalog.
	LayerCore Layer = "core"
	// LayerOverlay is the project's tracked Project Overlay.
	LayerOverlay Layer = "overlay"
)

// Kind is the plane a capability resolved through.
type Kind string

const (
	// KindNativeFunction is a Gateway-scope system.* scripting function.
	KindNativeFunction Kind = "native-function"
	// KindREST is a REST method and path.
	KindREST Kind = "rest"
	// KindModule is a capability naming a module id directly
	// ("module:<id>" or a bare com.* id).
	KindModule Kind = "module"
	// KindAlias is a capability mapped by a capability rule.
	KindAlias Kind = "alias"
)

// Classification is a native function's classification column.
type Classification string

const (
	// ClassPlatform is a function that ships with the platform itself. It needs
	// no optional module.
	ClassPlatform Classification = "platform"
	// ClassModule is a function that requires at least one module.
	ClassModule Classification = "module"
	// ClassConditional is a function whose module requirement depends on how it
	// is used; its note says what to check.
	ClassConditional Classification = "conditional"
)

// RuleKind is a capability rule's kind column.
type RuleKind string

const (
	// RuleExact matches a capability by equality.
	RuleExact RuleKind = "exact"
	// RulePrefix matches a capability by prefix.
	RulePrefix RuleKind = "prefix"
)

// OwnerKind is a REST row's owner column.
type OwnerKind string

const (
	// OwnerPlatform is an endpoint the platform itself serves.
	OwnerPlatform OwnerKind = "platform"
	// OwnerModule is an endpoint served by a shipped module.
	OwnerModule OwnerKind = "module"
	// OwnerPrivateModule is an endpoint served by a licensed or EA module that
	// never ships in the image. The row still resolves; whether the artifact is
	// present is the module check's business.
	OwnerPrivateModule OwnerKind = "private-module"
)

// BuiltinModule is one row of a built-in module catalog: a module id and the
// artifact file name the Docker image carries.
type BuiltinModule struct {
	ID       string
	Artifact string
}

// NativeFunction is one row of a native function catalog.
type NativeFunction struct {
	Function string
	Class    Classification
	// Modules are the required module ids. Empty means no optional module is
	// required, which is the platform classification's meaning too.
	Modules []string
	// Note is the free-text note the conditional classification carries.
	Note string
}

// CapabilityRule is one row of a capability rule catalog: the alias plane.
type CapabilityRule struct {
	Kind    RuleKind
	Pattern string
	Modules []string
	// Description is free text for humans.
	Description string
}

// RestOperation is one row of a REST catalog.
type RestOperation struct {
	// Method is the upper-case HTTP method.
	Method string
	// Path is the path template; a "{param}" segment matches exactly one
	// concrete segment.
	Path    string
	Owner   OwnerKind
	Modules []string
}

// Catalog is one layer's knowledge: the three planes the toolchain resolves
// capabilities from.
type Catalog struct {
	// Version is the Ignition version this layer was authored for. The overlay
	// carries the core version it extends.
	Version        string
	BuiltinModules []BuiltinModule
	Functions      []NativeFunction
	Rules          []CapabilityRule
	REST           []RestOperation
}

// Counts is how many rows each plane carries. It is reported, never asserted:
// integrity is a digest, not a row count.
type Counts struct {
	BuiltinModules  int `json:"builtin_modules"`
	NativeFunctions int `json:"native_functions"`
	CapabilityRules int `json:"capability_rules"`
	RestOperations  int `json:"rest_operations"`
}

// Counts returns this layer's row counts.
func (c *Catalog) Counts() Counts {
	if c == nil {
		return Counts{}
	}
	return Counts{
		BuiltinModules:  len(c.BuiltinModules),
		NativeFunctions: len(c.Functions),
		CapabilityRules: len(c.Rules),
		RestOperations:  len(c.REST),
	}
}

// Resolution is one capability mapped to its owner(s).
type Resolution struct {
	// Capability is the input, trimmed.
	Capability string
	Kind       Kind
	// Platform reports that the capability is served by the platform itself and
	// requires no optional module.
	Platform bool
	// Modules are the required module ids, in catalog order.
	Modules []string
	// Layer is where the deciding row came from.
	Layer Layer
	// Class and Note are the native function classification and its note.
	Class Classification
	Note  string
	// Method and Template describe the matched REST row: the upper-case method
	// (empty when the capability named a bare path) and the catalog's template.
	Method   string
	Template string
}

// ModuleSet is what the checkout knows about a module id. The catalog layer
// asks these three questions and never reads a directory itself.
type ModuleSet interface {
	// Enabled reports whether the module id is inside the module whitelist.
	Enabled(id string) bool
	// Builtin reports whether the module id ships in the Ignition image.
	Builtin(id string) bool
	// Artifact reports whether a readable `.modl` in the checkout declares the
	// module id.
	Artifact(id string) bool
}

// Verify resolves one capability and checks every module it requires against the
// checkout. It is the whole of `module require`'s logic, minus printing.
//
// The fault vocabulary is the preflight answer: unknown capability, ambiguity,
// a module outside the whitelist, or an enabled module with no artifact.
func (e *Effective) Verify(capability string, set ModuleSet) (Resolution, *contract.Fault) {
	res, err := e.Resolve(capability)
	if err != nil {
		return res, err
	}
	if res.Platform {
		return res, nil
	}
	type problem struct {
		id   string
		code contract.Code
		why  string
	}
	var (
		problems  []problem
		toEnable  []string
		needFiles []string
	)
	for _, id := range res.Modules {
		switch {
		case !set.Enabled(id):
			problems = append(problems, problem{id, contract.CodeModuleNotEnabled, "not enabled by the module whitelist"})
			toEnable = append(toEnable, id)
		case !set.Builtin(id) && !set.Artifact(id):
			problems = append(problems, problem{id, contract.CodeModuleArtifactMissing, "no matching .modl artifact found"})
			needFiles = append(needFiles, id)
		}
	}
	if len(problems) == 0 {
		return res, nil
	}
	lines := make([]string, 0, len(problems))
	for _, p := range problems {
		lines = append(lines, fmt.Sprintf("%s requires %s (%s)", res.Capability, p.id, p.why))
	}
	var remediation []contract.Remediation
	if len(toEnable) > 0 {
		remediation = append(remediation, contract.Remediation{
			Command: "igdev module enable " + strings.Join(toEnable, " "),
			Why:     "add the required modules to the module whitelist",
		})
	}
	if len(needFiles) > 0 {
		remediation = append(remediation, contract.Remediation{
			Command: "igdev module add /path/to/module.modl",
			Why:     "stage the missing module artifact in this checkout",
		})
	}
	return res, contract.NewFault(problems[0].code, contract.ExitFailure, strings.Join(lines, "; ")).
		WithRemediation(remediation...)
}

// series is the X.Y series of a version, which is how the native function
// diagnostics name their target ("Ignition 8.3 native function").
func series(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}
