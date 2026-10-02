package cli

import (
	"fmt"
	"strings"

	"github.com/sheon-sek/igdev/internal/catalog"
	"github.com/sheon-sek/igdev/internal/config"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gate"
	"github.com/sheon-sek/igdev/internal/modules"
	"github.com/sheon-sek/igdev/internal/project"
)

// knowledge is what the knowledge verbs resolve against: the Gate's verdict on
// the Project Contract, the Effective Catalog (Core Catalog plus Project
// Overlay), and the private module artifacts this checkout has staged.
type knowledge struct {
	res       *config.Resolution
	doc       project.Doc
	eff       *catalog.Effective
	records   []modules.Record
	whitelist []string
	set       moduleSet
}

// moduleSet answers the capability layer's three module questions from the
// Effective Catalog, the contract's whitelist, and the staged artifacts.
type moduleSet struct {
	eff       *catalog.Effective
	whitelist []string
	records   []modules.Record
}

// Enabled reports whether this checkout loads a module id: the contract's
// whitelist selects it, or a staged private artifact declares it — a staged module
// is enabled by being staged (the rendered list carries it), so a whitelist that
// does not name one is not a verdict about it.
func (s moduleSet) Enabled(id string) bool {
	return modules.Enabled(s.whitelist, id) || modules.Has(s.records, id)
}

// Builtin reports whether the module ships in the Ignition image.
func (s moduleSet) Builtin(id string) bool { return modules.Builtin(s.eff.Builtin, id) }

// Artifact reports whether a staged `.modl` declares the module id.
func (s moduleSet) Artifact(id string) bool { return modules.Has(s.records, id) }

// catalogContext resolves the Effective Catalog for this run. Outside a Project
// Root there is no Project Overlay, so the answer is the Core Catalog alone:
// `igdev catalog status` works before init, because which versions this binary
// carries is a property of the binary.
func (a *App) catalogContext() (project.Found, *config.Resolution, project.Doc, *catalog.Effective, error) {
	found, res, err := a.gate()
	if err != nil {
		return found, res, project.Doc{}, nil, err
	}
	var doc project.Doc
	if found.InProject() {
		_, parsed, fault := gate.ContractOnly(a.gateInput(found))
		if fault != nil {
			return found, res, doc, nil, fault
		}
		doc = parsed
	}
	overlay, fault := catalog.LoadOverlay(found.Root, doc.Catalog.OverlayPaths)
	if fault != nil {
		return found, res, doc, nil, fault
	}
	eff, fault := catalog.New(res.String("ignition.version"), overlay)
	if fault != nil {
		return found, res, doc, nil, fault
	}
	return found, res, doc, eff, nil
}

// projectKnowledge is catalogContext plus the requirement that this checkout is
// a Project Root with a contract this CLI speaks. The module verbs resolve
// capabilities against the contract's whitelist and overlay, so they need both —
// but not a current Setup Stamp, because knowledge does not depend on a
// materialized checkout.
func (a *App) projectKnowledge() (*knowledge, error) {
	found, res, doc, eff, err := a.catalogContext()
	if err != nil {
		return nil, err
	}
	if fault := gate.RequireProject(a.gateInput(found)); fault != nil {
		return nil, fault
	}
	return stagedKnowledge(found, res, doc, eff)
}

// projectWrite is projectKnowledge plus the Gate's Setup Stamp stage: the module
// write verbs change what a materialized checkout stages, so they need one. A
// checkout that was never set up, or whose contract moved since, is refused with
// the same faults every other project command reports.
func (a *App) projectWrite() (project.Found, *knowledge, error) {
	return a.projectStaged()
}

// projectStaged is projectKnowledge plus the Gate's Setup Stamp stage. It is what
// the check pipeline runs on: validating the enabled modules means comparing them
// against what this checkout actually stages, which only exists once the checkout
// has been materialized.
func (a *App) projectStaged() (project.Found, *knowledge, error) {
	found, res, doc, eff, err := a.catalogContext()
	if err != nil {
		return found, nil, err
	}
	if fault := gate.Require(a.gateInput(found)); fault != nil {
		return found, nil, fault
	}
	k, err := stagedKnowledge(found, res, doc, eff)
	return found, k, err
}

// stagedKnowledge pairs the resolved catalog and contract with the private
// artifacts this checkout stages, which is everything the module verbs answer
// from.
func stagedKnowledge(found project.Found, res *config.Resolution, doc project.Doc, eff *catalog.Effective) (*knowledge, error) {
	records, fault := modules.Scan(modules.Dir(found.Root))
	if fault != nil {
		return nil, fault
	}
	whitelist := doc.Modules.Enabled
	return &knowledge{
		res:       res,
		doc:       doc,
		eff:       eff,
		records:   records,
		whitelist: whitelist,
		set:       moduleSet{eff: eff, whitelist: whitelist, records: records},
	}, nil
}

// capabilityData is one resolved capability, the shape every knowledge verb
// reports: what the code asks for, who owns it, and which layer answered.
type capabilityData struct {
	Capability string   `json:"capability"`
	Kind       string   `json:"kind"`
	Platform   bool     `json:"platform"`
	Modules    []string `json:"modules"`
	Layer      string   `json:"layer"`
	Class      string   `json:"class,omitempty"`
	Note       string   `json:"note,omitempty"`
	Method     string   `json:"method,omitempty"`
	Template   string   `json:"template,omitempty"`
}

// capabilityOf renders one resolution for the machine contract. Modules is
// always a list, never null, so a consumer can iterate it without a nil check.
func capabilityOf(res catalog.Resolution) capabilityData {
	data := capabilityData{
		Capability: res.Capability,
		Kind:       string(res.Kind),
		Platform:   res.Platform,
		Modules:    append([]string{}, res.Modules...),
		Layer:      string(res.Layer),
		Class:      string(res.Class),
		Note:       res.Note,
		Method:     res.Method,
		Template:   res.Template,
	}
	return data
}

// capabilityLine is the preflight line a human reads, in the vocabulary the bash
// foundation used: OK for a capability that resolves, NOTE for a conditional
// requirement the author should look at.
func capabilityLine(res catalog.Resolution) string {
	if res.Platform {
		if res.Kind == catalog.KindNativeFunction {
			return "[module-preflight] OK: " + res.Capability + " -> " + string(res.Class) + " (no optional module required)"
		}
		return "[module-preflight] OK: " + res.Capability + " -> platform"
	}
	var out string
	for i, id := range res.Modules {
		if i > 0 {
			out += "\n"
		}
		out += "[module-preflight] OK: " + res.Capability + " -> " + id
	}
	if res.Kind == catalog.KindNativeFunction && res.Class == catalog.ClassConditional && res.Note != "" {
		out += "\n[module-preflight] NOTE: " + res.Capability + ": " + res.Note
	}
	return out
}

// verifyAll resolves every capability and reports one fault describing all the
// failures: a preflight run tells the caller everything that is wrong, not just
// the first thing.
func (k *knowledge) verifyAll(capabilities []string) ([]catalog.Resolution, *contract.Fault) {
	var (
		resolutions []catalog.Resolution
		messages    []string
		remediation []contract.Remediation
		code        contract.Code
	)
	for _, capability := range capabilities {
		res, fault := k.eff.Verify(capability, k.set)
		if fault == nil {
			resolutions = append(resolutions, res)
			continue
		}
		if code == "" {
			code = fault.Code
		}
		messages = append(messages, fault.Message)
		remediation = mergeRemediation(remediation, fault.Remediation...)
	}
	if len(messages) == 0 {
		return resolutions, nil
	}
	message := strings.Join(messages, "; ")
	if len(messages) > 1 {
		message = fmt.Sprintf("%d capabilities failed preflight: %s", len(messages), message)
	}
	return resolutions, contract.NewFault(code, contract.ExitFailure, message).WithRemediation(remediation...)
}

// mergeRemediation appends the steps not already carried, so a batch of failures
// reports one enabling command per distinct set of modules.
func mergeRemediation(have []contract.Remediation, add ...contract.Remediation) []contract.Remediation {
	for _, step := range add {
		duplicate := false
		for _, existing := range have {
			if existing == step {
				duplicate = true
				break
			}
		}
		if !duplicate {
			have = append(have, step)
		}
	}
	return have
}

// printCapabilityLines prints the preflight lines a human reads: the legacy
// OK/NOTE vocabulary, one line per capability.
func (a *App) printCapabilityLines(resolutions []catalog.Resolution) {
	for _, res := range resolutions {
		fmt.Fprintln(a.Stdout, capabilityLine(res))
	}
}
