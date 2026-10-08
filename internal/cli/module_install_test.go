package cli

import "testing"

func TestSameVersionReadsTheGatewayForm(t *testing.T) {
	for _, c := range []struct {
		reported, declared string
		want               bool
	}{
		{"1.0.1 (b0)", "1.0.1", true},
		{"6.3.8 (b2026071409)", "6.3.8.2026071409", true},
		{"1.0.0 (b0)", "1.0.1", false},
		{"", "1.0.0", false},
	} {
		if got := sameVersion(c.reported, c.declared); got != c.want {
			t.Errorf("sameVersion(%q, %q) = %v", c.reported, c.declared, got)
		}
	}
}

// Each restart reason was measured on 8.3.8; a module a whitelist disabled and
// one that runs the installed build need none.
func TestRestartReason(t *testing.T) {
	row := func(m gatewayModule) gatewayModules {
		return gatewayModules{Healthy: []gatewayModule{m}}
	}
	for name, c := range map[string]struct {
		modules gatewayModules
		want    bool
	}{
		"running":  {row(gatewayModule{ID: "a", Version: "1.0.0 (b0)", State: "ACTIVE", OnStartup: "enabled"}), false},
		"pending":  {row(gatewayModule{ID: "a", Version: "1.0.0 (b0)", State: "ACTIVE", PendingUpgrade: true}), true},
		"older":    {row(gatewayModule{ID: "a", Version: "0.9.0 (b0)", State: "ACTIVE", OnStartup: "enabled"}), true},
		"inactive": {row(gatewayModule{ID: "a", Version: "1.0.0 (b0)", State: "INACTIVE", OnStartup: "enabled"}), true},
		"disabled": {row(gatewayModule{ID: "a", Version: "1.0.0 (b0)", State: "INACTIVE", OnStartup: "disabled"}), false},
		"terms":    {gatewayModules{Quarantined: []gatewayModule{{ID: "a", Reason: "Certificate has not been accepted."}}}, true},
		"unsigned": {gatewayModules{Quarantined: []gatewayModule{{ID: "a", Reason: "unsigned and developer mode not enabled"}}}, false},
	} {
		if got := restartReason(c.modules, "a", "1.0.0") != ""; got != c.want {
			t.Errorf("%s: restart = %v, want %v", name, got, c.want)
		}
	}
}
