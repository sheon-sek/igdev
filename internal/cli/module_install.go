package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/modules"
)

const (
	// moduleUploadLimit bounds the archive `module install` uploads.
	moduleUploadLimit = 256 << 20
	// moduleSettleInterval is how often `module install` asks whether the Gateway
	// lists the module it just installed.
	moduleSettleInterval = 500 * time.Millisecond
	// moduleStartGrace is how long a listed module gets to start before install
	// restarts the Gateway to start it.
	moduleStartGrace = 10 * time.Second
	// moduleActive is the state the Gateway reports for a running module.
	moduleActive = "ACTIVE"
)

// moduleInstallData is what `igdev module install` reports.
type moduleInstallData struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
	// Path is where the artifact is staged, so a later reset or a fresh volume
	// loads it too.
	Path string `json:"path"`
	// Accepted lists the module's own terms igdev accepted on the Gateway:
	// certificate, eula, or neither when the Gateway already had them.
	Accepted []string `json:"accepted"`
	// Restarted is true when the install replaced a running build and the
	// Gateway was restarted to finish the upgrade.
	Restarted bool `json:"restarted"`
	// Status is healthy (running), inactive (installed, not started), or
	// quarantined, and Module is the Gateway's own row.
	Status string        `json:"status"`
	Module gatewayModule `json:"module"`
}

// moduleUpload is the Gateway's answer to an upload.
type moduleUpload struct {
	ModuleID        string `json:"moduleId"`
	LicenseAccepted bool   `json:"licenseAccepted"`
	CertAccepted    bool   `json:"certAccepted"`
	ContainsEula    bool   `json:"containsEula"`
	ContainsCert    bool   `json:"containsCert"`
}

func (a *App) newModuleInstallCmd() *cobra.Command {
	var timeoutRaw string
	cmd := &cobra.Command{
		Use:   "install <file.modl>",
		Short: "Hot-install a module into the running Gateway, keeping its data",
		Long: `install puts a module into this Instance's running Gateway through its REST API,
so projects, tags and configuration stay as they are. ` + "`module add`" + ` only stages a
file, which a Gateway loads after ` + "`gateway reset`" + ` — and a reset wipes the data.

  1. validate  the archive, under the same zip-bomb guard as module add
  2. stage     the file in .igdev/modules/, so a later reset or fresh volume loads it
  3. upload    POST /data/api/v1/modules/upload
  4. accept    the module's own certificate and EULA when the Gateway has not yet
  5. install   POST /data/api/v1/modules/install
  6. settle    wait until the Gateway lists the module and runs ` + "`igdev restart`" + ` when
               it needs one: it replaced a running build of the same id and waits as
               a pending upgrade, or it is enabled but did not start

The artifact is the developer's own, so its terms are accepted without a human step
(ADR 0006). With ` + "`[modules] require_private_module_consent = true`" + ` the
machine-global module-license and module-cert terms come first, and a missing one is
IGDEV_E_CONSENT_REQUIRED at exit level 3.

The run reports the module healthy (running), inactive (installed but not started: a
` + "`[modules].enabled`" + ` whitelist starts only the modules it named when the Gateway
started), or fails with IGDEV_E_MODULE_QUARANTINED and the reason the Gateway gives;
an unsigned build needs ` + "`allow_unsigned_modules = true`" + `.`,
		Example: `  igdev module install build/com.acme.vision.modl
  igdev module install build/com.acme.vision.modl --timeout 5m --json`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("module install", "file",
					"igdev module install build/com.acme.vision.modl",
					"name the module archive to install into the running Gateway")
			}
			if len(args) > 1 {
				return extraArguments("module install", 1, args)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			timeout, err := parseTimeout(timeoutRaw, gatewayWaitDefault)
			if err != nil {
				return err
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			data, err := a.installModule(g, args[0], timeout)
			if err != nil {
				return err
			}
			a.emit(g.res, data, func() { a.printModuleInstall(data) })
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "",
		"how long to wait for the module and any restart: seconds, or a duration like 3m (default 180s)")
	return cmd
}

// installModule is `module install` minus printing, so `build --install` installs
// each artifact through the same path.
func (a *App) installModule(g *gateway, file string, timeout time.Duration) (moduleInstallData, error) {
	record, fault := modules.Validate(file)
	if fault != nil {
		return moduleInstallData{}, fault
	}
	if err := g.requireRuntimeFiles(); err != nil {
		return moduleInstallData{}, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return moduleInstallData{}, contract.NewFault(contract.CodeModuleArchiveInvalid, contract.ExitFailure,
			fmt.Sprintf("%s cannot be read: %v", file, err)).WithCause(err)
	}
	if len(raw) > moduleUploadLimit {
		return moduleInstallData{}, contract.NewFault(contract.CodeModuleArchiveInvalid, contract.ExitFailure,
			fmt.Sprintf("%s is over the %d-byte upload limit", file, moduleUploadLimit))
	}

	dir := modules.Dir(g.found.Root)
	staged, _, fault := modules.StageReplacing(dir, file)
	if fault != nil {
		return moduleInstallData{}, fault
	}
	if _, err := a.restage(g.found, g.doc); err != nil {
		return moduleInstallData{}, err
	}

	name := filepath.Base(file)
	a.stage("module install: uploading %s (%s %s)", name, record.ID, record.Version)
	var upload moduleUpload
	if fault := g.restJSON(http.MethodPost, "/data/api/v1/modules/upload?fileName="+url.QueryEscape(name),
		bytes.NewReader(raw), "application/octet-stream", &upload); fault != nil {
		return moduleInstallData{}, fault
	}
	id := firstNonEmpty(upload.ModuleID, record.ID)
	accepted, fault := a.acceptModuleTerms(g, id, upload)
	if fault != nil {
		return moduleInstallData{}, fault
	}
	a.stage("module install: installing %s", id)
	if fault := g.restJSON(http.MethodPost, "/data/api/v1/modules/install?moduleId="+url.QueryEscape(id),
		nil, "", nil); fault != nil {
		return moduleInstallData{}, fault
	}

	data := moduleInstallData{
		ID: id, Name: record.Name, Version: record.Version, Source: file, Path: staged.Path, Accepted: accepted,
	}
	state, fault := a.settleModule(g, id, record.Version, timeout)
	if fault != nil {
		return data, fault
	}
	if reason := restartReason(state, id, record.Version); reason != "" {
		a.stage("module install: %s %s; restarting", id, reason)
		restarted, err := a.restartGateway(g, timeout)
		// A quarantine is reported below, for the installed module.
		if err != nil && contract.AsFault(err).Code != contract.CodeModuleQuarantined {
			return data, err
		}
		state, data.Restarted = restarted.Modules, true
	}
	module, found, quarantined := state.find(id)
	if !found {
		return data, contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
			fmt.Sprintf("the Gateway does not list %s after installing it", id)).
			WithRemediation(contract.Remediation{Command: "igdev gateway logs --tail 200", Why: "read what the Gateway did with the module"})
	}
	data.Module, data.Status = module, "healthy"
	switch {
	case quarantined:
		data.Status = "quarantined"
	case module.State != moduleActive:
		// Installed and healthy, but not started: a [modules].enabled whitelist
		// fixes the module list when the Gateway starts, so a new id stays
		// disabled until the Gateway is recreated with a list that names it.
		data.Status = "inactive"
		a.stage("module install: %s is installed but not running (state %s, %s on startup); "+
			"a [modules].enabled whitelist starts only the modules it named when the Gateway started", id, module.State, module.OnStartup)
	}
	if fault := quarantineFault(state, data); fault != nil {
		return data, fault
	}
	return data, nil
}

// settleModule waits until the Gateway lists id, then gives it moduleStartGrace
// to run the installed version, be quarantined, or wait as a pending upgrade: the
// Gateway marks a pending upgrade a moment after the install answers, and lists
// the running build until then.
func (a *App) settleModule(g *gateway, id, version string, timeout time.Duration) (gatewayModules, *contract.Fault) {
	staged, fault := stagedIDs(g)
	if fault != nil {
		return gatewayModules{}, fault
	}
	deadline := time.Now().Add(timeout)
	var listed time.Time
	for {
		m, fault := g.readModules(staged)
		if fault != nil {
			return gatewayModules{}, fault
		}
		if module, found, quarantined := m.find(id); found {
			if listed.IsZero() {
				listed = time.Now()
			}
			running := module.State == moduleActive && sameVersion(module.Version, version)
			if quarantined || module.PendingUpgrade || running ||
				time.Since(listed) >= moduleStartGrace || !time.Now().Before(deadline) {
				return m, nil
			}
		} else if !time.Now().Before(deadline) {
			return gatewayModules{}, contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
				fmt.Sprintf("the Gateway did not list %s within %s of installing it", id, timeout)).
				WithRemediation(contract.Remediation{Command: "igdev gateway logs --tail 200", Why: "read what the Gateway did with the module"})
		}
		time.Sleep(moduleSettleInterval)
	}
}

// restartReason says why the installed module needs a restart, or "" when it
// does not. Each was measured on 8.3.8: a module installed over a running build
// waits as a pending upgrade; an unsigned module whose shared acceptance the
// Gateway already holds is quarantined until a restart reads it; and a first
// install that is enabled stays inactive until a restart starts it. A module the
// Gateway disabled ([modules].enabled whitelist) is not restarted for: an in-place
// restart keeps the module list the Gateway started with.
func restartReason(m gatewayModules, id, version string) string {
	module, found, quarantined := m.find(id)
	switch {
	case !found:
		return ""
	case quarantined && strings.Contains(strings.ToLower(module.Reason), "not been accepted"):
		return "is quarantined until a restart reads its accepted terms"
	case quarantined:
		return ""
	case module.PendingUpgrade:
		return "replaced a running build and waits as a pending upgrade"
	case module.OnStartup == "disabled":
		return ""
	case module.State != moduleActive:
		return "is installed and enabled but did not start"
	case !sameVersion(module.Version, version):
		return fmt.Sprintf("still runs version %s", module.Version)
	}
	return ""
}

// sameVersion compares the version the Gateway reports, "1.2.3 (b45)", with the
// one module.xml declares, "1.2.3" or "1.2.3.45".
func sameVersion(reported, declared string) bool {
	base, build, found := strings.Cut(reported, " (b")
	build = strings.TrimSuffix(build, ")")
	if declared == base || declared == reported {
		return true
	}
	return found && declared == base+"."+build
}

// acceptModuleTerms accepts the module's own certificate and EULA on the Gateway
// when the upload says they are not accepted yet (ADR 0006).
func (a *App) acceptModuleTerms(g *gateway, id string, upload moduleUpload) ([]string, *contract.Fault) {
	accepted := []string{}
	// An unsigned module carries no certificate, yet the Gateway quarantines it
	// until its "certificate" is accepted all the same (measured on 8.3.8), so
	// the answer's accepted flags decide, not the contains flags.
	needCert := !upload.CertAccepted
	needEula := !upload.LicenseAccepted
	if !needCert && !needEula {
		return accepted, nil
	}
	if g.doc.Modules.RequirePrivateModuleConsent {
		if fault := a.consentLocation().CheckAll(consent.ModuleLicense, consent.ModuleCert); fault != nil {
			return nil, fault
		}
	}
	query := "?moduleId=" + url.QueryEscape(id)
	for _, term := range []struct {
		need bool
		name string
	}{{needCert, "certificate"}, {needEula, "eula"}} {
		if !term.need {
			continue
		}
		path := "/data/api/v1/modules/" + term.name + query
		status, raw, fault := g.restCall(http.MethodPost, path, nil, "", restAnswerLimit)
		if fault != nil {
			return nil, fault
		}
		// 409 is the Gateway saying it already holds the acceptance: every
		// unsigned module shares one, so the upload of a second one still says
		// not accepted (measured on 8.3.8).
		if status == http.StatusConflict {
			continue
		}
		if status >= 400 {
			return nil, restStatusFault(http.MethodPost, path, status, raw)
		}
		accepted = append(accepted, term.name)
	}
	return accepted, nil
}

// printModuleInstall tells a human what is running now.
func (a *App) printModuleInstall(data moduleInstallData) {
	fmt.Fprintf(a.Stdout, "installed: %s %s (%s)\n", data.Name, data.Version, data.ID)
	fmt.Fprintf(a.Stdout, "status:    %s (%s)\n", data.Status, data.Module.State)
	fmt.Fprintf(a.Stdout, "staged:    %s\n", data.Path)
	if len(data.Accepted) > 0 {
		fmt.Fprintf(a.Stdout, "accepted:  the module's own %s\n", strings.Join(data.Accepted, " and "))
	}
	if data.Restarted {
		fmt.Fprintln(a.Stdout, "restarted: yes, to finish loading it")
	}
}
