package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/gatewayapi"
)

// The REST calls the running-Gateway verbs (restart, module install, project
// import and export) make with the Instance token.
const (
	// restTimeout bounds one call; an upload or an import of a large project is
	// still one call.
	restTimeout = 120 * time.Second
	// restAnswerLimit bounds a JSON answer igdev decodes. Module and project
	// answers are small; a larger one is refused, not cut.
	restAnswerLimit = 4 << 20
)

// restCall sends one request to the Instance's Gateway with its token and returns
// the answer read up to limit bytes. A 4xx or 5xx answer is returned, not turned
// into a fault, so each verb can say what the status means for it.
func (g *gateway) restCall(method, path string, body io.Reader, contentType string, limit int64) (int, []byte, *contract.Fault) {
	extra := http.Header{}
	if contentType != "" {
		extra.Set("Content-Type", contentType)
	}
	req, err := gatewayapi.NewRequest(method, g.url(), path, body, g.token, extra)
	if err != nil {
		return 0, nil, contract.NewFault(contract.CodeInternal, contract.ExitFailure, err.Error()).WithCause(err)
	}
	resp, err := (&http.Client{Timeout: restTimeout}).Do(req)
	if err != nil {
		return 0, nil, contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
			fmt.Sprintf("the Gateway at %s did not answer %s %s (%v)", g.url(), method, path, err)).
			WithCause(err).
			WithRemediation(contract.Remediation{Command: "igdev gateway ensure", Why: "leave this Instance with a running Gateway"})
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, nil, contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
			fmt.Sprintf("the Gateway's answer to %s %s could not be read (%v)", method, path, err)).WithCause(err)
	}
	if int64(len(raw)) > limit {
		return resp.StatusCode, nil, contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure,
			fmt.Sprintf("the Gateway's answer to %s %s is over the %d-byte limit", method, path, limit))
	}
	return resp.StatusCode, raw, nil
}

// restJSON sends one request and decodes a 2xx JSON answer into out. Any other
// answer is IGDEV_E_GATEWAY_API, carrying the Gateway's own message.
func (g *gateway) restJSON(method, path string, body io.Reader, contentType string, out any) *contract.Fault {
	status, raw, fault := g.restCall(method, path, body, contentType, restAnswerLimit)
	if fault != nil {
		return fault
	}
	if status >= 400 {
		return restStatusFault(method, path, status, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure,
			fmt.Sprintf("the Gateway answered %s %s with a body igdev cannot read (%v)", method, path, err)).WithCause(err)
	}
	return nil
}

// restStatusFault reports a refused call with the reason the Gateway gave, when
// it gave one.
func restStatusFault(method, path string, status int, raw []byte) *contract.Fault {
	message := fmt.Sprintf("%s %s answered %d %s", method, path, status, http.StatusText(status))
	if reason := gatewayProblem(raw); reason != "" {
		message += ": " + reason
	}
	return contract.NewFault(contract.CodeGatewayAPI, contract.ExitFailure, message).
		WithRemediation(contract.Remediation{Command: "igdev gateway logs --tail 50", Why: "read why the Gateway refused it"})
}

// gatewayProblem reads the reason an Ignition REST answer carries: problem.message
// on the project routes, message or error elsewhere.
func gatewayProblem(raw []byte) string {
	var body struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Problem struct {
			Message string `json:"message"`
		} `json:"problem"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(body.Problem.Message, body.Message, body.Error))
}

// gatewayModule is one module as `restart` and `module install` report it.
type gatewayModule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// State is the Gateway's own word: ACTIVE, INACTIVE, …
	State string `json:"state,omitempty"`
	// OnStartup is enabled or disabled: whether the Gateway starts the module.
	OnStartup string `json:"on_startup,omitempty"`
	// PendingUpgrade is true when a newer build is installed and waits for a
	// restart to replace the running one.
	PendingUpgrade bool `json:"pending_upgrade,omitempty"`
	// Reason is why a quarantined module was quarantined.
	Reason string `json:"reason,omitempty"`
	// Staged is true for a module this checkout stages or installed: its
	// quarantine is a failure, another module's is only reported.
	Staged bool `json:"staged"`
}

// gatewayModules is the Gateway's module state.
type gatewayModules struct {
	Healthy     []gatewayModule `json:"healthy"`
	Quarantined []gatewayModule `json:"quarantined"`
}

// readModules asks the Gateway for its healthy and quarantined modules. staged
// names the module ids this checkout owns.
func (g *gateway) readModules(staged []string) (gatewayModules, *contract.Fault) {
	mine := map[string]bool{}
	for _, id := range staged {
		mine[id] = true
	}
	var healthy struct {
		Items []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Version       string `json:"version"`
			State         string `json:"state"`
			OnStartup     string `json:"onStartup"`
			ShouldUpgrade bool   `json:"shouldUpgrade"`
		} `json:"items"`
	}
	if fault := g.restJSON(http.MethodGet, "/data/api/v1/modules/healthy", nil, "", &healthy); fault != nil {
		return gatewayModules{}, fault
	}
	var quarantined struct {
		Items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Version string `json:"version"`
			Reason  string `json:"reason"`
		} `json:"items"`
	}
	if fault := g.restJSON(http.MethodGet, "/data/api/v1/modules/quarantined", nil, "", &quarantined); fault != nil {
		return gatewayModules{}, fault
	}
	out := gatewayModules{Healthy: []gatewayModule{}, Quarantined: []gatewayModule{}}
	for _, m := range healthy.Items {
		out.Healthy = append(out.Healthy, gatewayModule{
			ID: m.ID, Name: m.Name, Version: m.Version, State: m.State, OnStartup: m.OnStartup, PendingUpgrade: m.ShouldUpgrade, Staged: mine[m.ID],
		})
	}
	for _, m := range quarantined.Items {
		out.Quarantined = append(out.Quarantined, gatewayModule{
			ID: m.ID, Name: m.Name, Version: m.Version, Reason: m.Reason, Staged: mine[m.ID],
		})
	}
	sort.Slice(out.Healthy, func(i, j int) bool { return out.Healthy[i].ID < out.Healthy[j].ID })
	sort.Slice(out.Quarantined, func(i, j int) bool { return out.Quarantined[i].ID < out.Quarantined[j].ID })
	return out, nil
}

// find reports the module with id, and whether it is quarantined.
func (m gatewayModules) find(id string) (gatewayModule, bool, bool) {
	for _, q := range m.Quarantined {
		if q.ID == id {
			return q, true, true
		}
	}
	for _, h := range m.Healthy {
		if h.ID == id {
			return h, true, false
		}
	}
	return gatewayModule{}, false, false
}

// pendingUpgrade lists the modules that wait for a restart to finish upgrading.
func (m gatewayModules) pendingUpgrade() []string {
	out := []string{}
	for _, h := range m.Healthy {
		if h.PendingUpgrade {
			out = append(out, h.ID)
		}
	}
	return out
}

// quarantineFault fails a run in which a module this checkout owns is
// quarantined, naming the reason the Gateway gave. An unsigned module gets the
// contract switch that lets the Gateway load it.
func quarantineFault(m gatewayModules, data any) *contract.Fault {
	var parts []string
	unsigned := false
	for _, q := range m.Quarantined {
		if !q.Staged {
			continue
		}
		reason := q.Reason
		if reason == "" {
			reason = "no reason given; read the Gateway log"
		}
		if strings.Contains(strings.ToLower(reason), "unsigned") {
			unsigned = true
		}
		parts = append(parts, fmt.Sprintf("%s is quarantined (%s)", q.ID, reason))
	}
	if len(parts) == 0 {
		return nil
	}
	remediation := []contract.Remediation{}
	if unsigned {
		remediation = append(remediation,
			contract.Remediation{Command: "igdev init --allow-unsigned-modules", Why: "set [gateway] allow_unsigned_modules = true so the Gateway loads an unsigned build"},
			contract.Remediation{Command: "igdev setup", Why: "re-render the runtime with the new setting"},
			contract.Remediation{Command: "igdev gateway up", Why: "recreate the Gateway container with it, keeping the volume"})
	}
	remediation = append(remediation, contract.Remediation{Command: "igdev gateway logs --tail 200", Why: "read why the Gateway quarantined it"})
	return contract.NewFault(contract.CodeModuleQuarantined, contract.ExitFailure, strings.Join(parts, "; ")).
		WithData(data).
		WithRemediation(remediation...)
}
