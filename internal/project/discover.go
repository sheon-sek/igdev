// Package project implements the discovery stage of the Gate: the upward
// search for the Project Contract file that defines the Project Root.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

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

// Contract is what discovery knows about a Project Contract file: where it is,
// that it is there, and the schema version it declares. Validating that schema
// and the file's contents is the Gate's job (internal/gate).
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
	// Setup reports whether a Checkout Setup record exists. The Gate compares
	// the record's Setup Stamp against the contract; discovery only reads it.
	Setup bool
	// SetupPath is the expected path of the Checkout Setup record.
	SetupPath string
	// SetupRaw is the record's bytes, nil when it is absent or unreadable.
	SetupRaw []byte
	// SetupReadError names why an existing record could not be read; empty
	// otherwise. The Gate reports it as a stale Setup, not as a crash.
	SetupReadError string
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
			found.Contract = Contract{Path: candidate, Present: true, SchemaVersion: DeclaredSchema(raw)}
			found.ContractTOML = raw
			found.SetupPath = filepath.Join(current, StateDir, SetupRecord)
			if _, statErr := os.Stat(found.SetupPath); statErr == nil {
				found.Setup = true
				// A record that cannot be read is a stale Setup the caller can
				// repair with `igdev setup`, not a reason to refuse to report
				// where the project is.
				if raw, readErr := os.ReadFile(found.SetupPath); readErr == nil {
					found.SetupRaw = raw
				} else {
					found.SetupReadError = readErr.Error()
				}
			}
			local := filepath.Join(current, StateDir, LocalConfig)
			localRaw, localErr := os.ReadFile(local)
			switch {
			case localErr == nil:
				found.LocalConfigPath = local
				found.LocalConfigTOML = localRaw
			case os.IsNotExist(localErr), errors.Is(localErr, syscall.ENOTDIR):
				// No checkout-local tier, or no .igdev directory yet: absent.
			default:
				// A tier that exists but cannot be read must not be silently
				// dropped: the run would use config the user did not ask for.
				return Found{}, contract.NewFault(contract.CodeConfigInvalid, contract.ExitFailure,
					fmt.Sprintf("local config %s cannot be read: %v", local, localErr)).
					WithCause(localErr).WithRemediation(contract.Remediation{
					Command: fmt.Sprintf("chmod u+r %s", local),
					Why:     "igdev will not resolve config from a tier it cannot read",
				})
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

// DeclaredSchema reads the top-level `schema` key leniently. A file that does
// not parse reports 0 and fails later in the Gate, which owns the error contract
// for a contract igdev cannot read.
func DeclaredSchema(raw []byte) int {
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
