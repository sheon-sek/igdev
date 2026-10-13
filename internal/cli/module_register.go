package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/modules"
)

// registerStagedModules installs every staged artifact the running Gateway does
// not list (igdev#131). The Gateway keeps its module registry in the volume's
// data/modules.json, so a `.modl` staged after the volume exists is bind-mounted
// into user-lib/modules yet never loaded, and the Gateway says nothing about it.
// A fresh volume loads every staged artifact itself, so this is a no-op there.
func (a *App) registerStagedModules(g *gateway, timeout time.Duration) error {
	dir := modules.Dir(g.found.Root)
	records, fault := modules.Scan(dir)
	if fault != nil {
		return fault
	}
	if len(modules.IDs(records)) == 0 {
		return nil
	}
	if !validToken(g.token) {
		a.stage("staged modules: no API token, so igdev cannot check that the Gateway loaded %v", modules.IDs(records))
		return nil
	}
	state, fault := g.readModules(modules.IDs(records))
	if fault != nil {
		return fault
	}
	for _, record := range unregistered(records, state) {
		path := filepath.Join(dir, record.Artifact)
		raw, err := os.ReadFile(path)
		if err != nil {
			return contract.NewFault(contract.CodeModuleArchiveInvalid, contract.ExitFailure,
				fmt.Sprintf("%s cannot be read: %v", path, err)).WithCause(err)
		}
		if len(raw) > moduleUploadLimit {
			return contract.NewFault(contract.CodeModuleArchiveInvalid, contract.ExitFailure,
				fmt.Sprintf("%s is over the %d-byte upload limit", path, moduleUploadLimit))
		}
		a.stage("staged modules: the Gateway's volume predates %s, so it is installed through the REST API", record.ID)
		if _, err := a.registerModule(g, path, path, record, raw, timeout); err != nil {
			return err
		}
	}
	return nil
}

// unregistered lists the staged artifacts the Gateway lists neither as healthy
// nor as quarantined.
func unregistered(records []modules.Record, state gatewayModules) []modules.Record {
	out := []modules.Record{}
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		if _, found, _ := state.find(record.ID); !found {
			out = append(out, record)
		}
	}
	return out
}
