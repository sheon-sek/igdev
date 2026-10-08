package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sheon-sek/igdev/internal/consent"
	"github.com/sheon-sek/igdev/internal/contract"
)

// Ignition 8.3 asks for its modules step again when a Gateway that is already
// commissioned starts with a module whose certificate or license it has not
// recorded at start-up: an unsigned module installed over REST is one (measured on
// 8.3.8). The Gateway then answers /StatusPing with RUNNING and details
// COMMISSIONING, and serves nothing but the commissioning app until someone accepts
// the module's terms. These are that app's own endpoints, served without
// authentication while the Gateway commissions.
const (
	commissioningDetail = "COMMISSIONING"
	commissionModules   = "modules"
	commissionFinished  = "finished"
)

// commissionedState is one /StatusPing answer: the state and its details.
type commissionedState struct {
	State   string `json:"state"`
	Details string `json:"details"`
}

// statusPing reads /StatusPing once.
func statusPing(client *http.Client, base string) (commissionedState, error) {
	resp, err := client.Get(base + readinessPath)
	if err != nil {
		return commissionedState{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, readinessBodyLimit))
	var out commissionedState
	if err := json.Unmarshal(body, &out); err != nil {
		return commissionedState{}, fmt.Errorf("answered %s", strings.TrimSpace(string(body)))
	}
	return out, nil
}

// finishModuleCommissioning completes the modules step a restart left the
// Gateway in, accepting the terms of every module it asks about: the modules on
// a disposable development Gateway are the developer's own choice, accepted
// without a human step as at start-up (ADR 0006), behind the machine-global terms
// when the contract requires them — and then only for modules this checkout
// stages. A step other than modules stops the run for a person. It then waits
// until the Gateway runs again.
func (a *App) finishModuleCommissioning(g *gateway, staged []string, timeout time.Duration) error {
	client := &http.Client{Timeout: gatewayProbeTimeout}
	state, err := statusPing(client, g.url())
	if err != nil || state.Details != commissioningDetail {
		return nil
	}
	var bootstrap struct {
		Steps map[string]string `json:"steps"`
	}
	if err := commissioningCall(client, http.MethodGet, g.url()+"/bootstrap", nil, &bootstrap); err != nil {
		return commissioningFault(g, "the commissioning steps cannot be read: "+err.Error())
	}
	var steps []string
	for step := range bootstrap.Steps {
		steps = append(steps, step)
	}
	sort.Strings(steps)
	if len(steps) != 1 || steps[0] != commissionModules {
		return commissioningFault(g, fmt.Sprintf("the Gateway asks for the commissioning steps %v, which need a person", steps))
	}
	var pending struct {
		Certificate []struct {
			ModuleID string `json:"moduleId"`
		} `json:"certificate"`
		License []struct {
			ModuleID string `json:"moduleId"`
		} `json:"license"`
	}
	if err := commissioningCall(client, http.MethodGet, g.url()+"/get-step?step="+commissionModules, nil, &pending); err != nil {
		return commissioningFault(g, "the modules step cannot be read: "+err.Error())
	}
	mine := map[string]bool{}
	for _, id := range staged {
		mine[id] = true
	}
	certificates, licenses := []string{}, []string{}
	var foreign []string
	for _, c := range pending.Certificate {
		certificates = append(certificates, c.ModuleID)
		if !mine[c.ModuleID] {
			foreign = append(foreign, c.ModuleID)
		}
	}
	for _, l := range pending.License {
		licenses = append(licenses, l.ModuleID)
		if !mine[l.ModuleID] {
			foreign = append(foreign, l.ModuleID)
		}
	}
	// A module the checkout does not stage — installed straight over REST, say —
	// was still put there by whoever works on this disposable Gateway, so its
	// terms are accepted the same way (ADR 0006, amendment 3). Only the strict
	// contract keeps it a person's decision.
	if len(foreign) > 0 && g.doc.Modules.RequirePrivateModuleConsent {
		return commissioningFault(g, fmt.Sprintf("the Gateway asks to accept the terms of %s, which this checkout does not stage",
			strings.Join(foreign, ", ")))
	}
	if g.doc.Modules.RequirePrivateModuleConsent {
		if fault := a.consentLocation().CheckAll(consent.ModuleLicense, consent.ModuleCert); fault != nil {
			return fault
		}
	}
	a.stage("restart: accepting the terms of %s to finish the Gateway's modules step",
		strings.Join(append(append([]string{}, certificates...), licenses...), ", "))
	accept := map[string]any{"id": commissionModules, "step": commissionModules,
		"data": map[string]any{"acceptedLicenses": licenses, "acceptedCertificates": certificates}}
	if err := commissioningCall(client, http.MethodPost, g.url()+"/post-step", accept, nil); err != nil {
		return commissioningFault(g, "accepting the module terms failed: "+err.Error())
	}
	finish := map[string]any{"id": commissionFinished, "step": commissionFinished, "data": map[string]any{"startGateway": true}}
	if err := commissioningCall(client, http.MethodPost, g.url()+"/post-step", finish, nil); err != nil {
		return commissioningFault(g, "finishing commissioning failed: "+err.Error())
	}
	return a.waitCommissioned(g, client, timeout)
}

// waitCommissioned waits until the Gateway runs without commissioning details.
func (a *App) waitCommissioned(g *gateway, client *http.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		state, err := statusPing(client, g.url())
		if err == nil && state.State == readinessState && state.Details == "" {
			return nil
		}
		if !time.Now().Before(deadline) {
			return contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
				fmt.Sprintf("the Gateway at %s did not run again within %s of finishing commissioning", g.url(), timeout)).
				WithRemediation(contract.Remediation{Command: "igdev gateway logs --tail 50", Why: "read the Gateway's own log"})
		}
		time.Sleep(gatewayPollInterval)
	}
}

// commissioningCall sends one JSON request to the commissioning app.
func commissioningCall(client *http.Client, method, url string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, restAnswerLimit))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s answered %s", method, req.URL.Path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// commissioningFault stops a run whose Gateway waits in commissioning for a step
// igdev does not take on a person's behalf.
func commissioningFault(g *gateway, why string) *contract.Fault {
	return contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitHumanAction,
		fmt.Sprintf("the Gateway at %s is waiting in commissioning: %s", g.url(), why)).
		WithRemediation(contract.Remediation{Command: "igdev gateway url", Why: "open the Gateway in a browser and finish commissioning"})
}
