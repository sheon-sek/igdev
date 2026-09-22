// Package project implements the discovery stage of the Gate: the upward
// search for the Project Contract file that defines the Project Root.
package project

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/sheon-sek/igdev/internal/contract"
)

// Frozen names of the tracked and generated parts of a project checkout.
const (
	ContractFile  = "igdev.toml"
	StateDir      = ".igdev"
	LocalConfig   = "local.toml"
	SetupRecord   = "setup.json"
	LatestSchema  = 1
	CurrentSchema = LatestSchema
)

// Contract is what discovery knows about a Project Contract file. Schema
// validation is a later Gate stage; discovery only reports the declared
// schema version and the raw bytes.
type Contract struct {
	Path          string `json:"path"`
	Present       bool   `json:"present"`
	SchemaVersion int    `json:"schema_version"`
}

// Found is the outcome of the discovery stage for one working directory.
type Found struct {
	// Root is the Project Root, or "" when no Project Contract was found.
	Root string
	// StartDir is the directory the upward search began from.
	StartDir string
	// Searched lists each directory visited, nearest first (diagnostics).
	Searched []string
	Contract Contract
	// Setup reports whether a Checkout Setup record exists. Ticket 01 checks
	// existence only; the Setup Stamp is validated by ticket 02.
	Setup bool
	// SetupPath is the expected path of the Checkout Setup record.
	SetupPath string
	// LocalConfigPath and LocalConfigTOML are the checkout-local tier; the path
	// is empty when the file does not exist.
	LocalConfigPath string
	LocalConfigTOML []byte
	// ContractTOML is the raw Project Contract bytes, fed to the config
	// resolver as the contract tier.
	ContractTOML []byte
}

// InProject reports whether discovery found a Project Root.
func (f Found) InProject() bool { return f.Root != "" }

// Discover walks upward from dir looking for igdev.toml. The search stops at
// the filesystem root. Absence of a Project Contract is not an error: callers
// report it as "not initialized".
func Discover(dir string) (Found, error) {
	start, err := filepath.Abs(dir)
	if err != nil {
		return Found{}, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
			fmt.Sprintf("cannot resolve working directory: %v", err)).WithCause(err)
	}
	found := Found{StartDir: start}
	current := start
	for {
		found.Searched = append(found.Searched, current)
		candidate := filepath.Join(current, ContractFile)
		info, err := os.Stat(candidate)
		switch {
		case err == nil && info.IsDir():
			// A directory named igdev.toml is not a contract; keep searching.
		case err == nil:
			raw, readErr := os.ReadFile(candidate)
			if readErr != nil {
				return Found{}, contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
					fmt.Sprintf("project contract %s cannot be read: %v", candidate, readErr)).WithCause(readErr)
			}
			found.Root = current
			found.Contract = Contract{Path: candidate, Present: true, SchemaVersion: declaredSchema(candidate, raw)}
			found.ContractTOML = raw
			found.SetupPath = filepath.Join(current, StateDir, SetupRecord)
			if _, statErr := os.Stat(found.SetupPath); statErr == nil {
				found.Setup = true
			}
			local := filepath.Join(current, StateDir, LocalConfig)
			if localRaw, localErr := os.ReadFile(local); localErr == nil {
				found.LocalConfigPath = local
				found.LocalConfigTOML = localRaw
			}
			return found, nil
		case os.IsNotExist(err):
			// keep walking
		default:
			return Found{}, contract.NewFault(contract.CodeInternal, contract.ExitFailure,
				fmt.Sprintf("cannot search for %s: %v", candidate, err)).WithCause(err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			found.SetupPath = filepath.Join("", StateDir, SetupRecord)
			return found, nil
		}
		current = parent
	}
}

// declaredSchema reads the top-level `schema` key leniently. A file that does
// not parse reports 0 and fails later in the config resolver, which owns the
// error contract for malformed tiers.
func declaredSchema(path string, raw []byte) int {
	var head struct {
		Schema *int `toml:"schema"`
	}
	if err := toml.Unmarshal(raw, &head); err != nil {
		return 0
	}
	if head.Schema == nil {
		return 0
	}
	return *head.Schema
}
