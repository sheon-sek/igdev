package cli

import (
	"fmt"

	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
	"github.com/sheon-sek/igdev/internal/semver"
)

// guardVolumeVersion runs before a verb starts the Gateway on an existing data
// volume. The Ignition image's entrypoint upgrades a volume older than the image
// one way, and refuses one newer than the image by exiting, which under
// restart: unless-stopped loops until igdev's wait times out. So an older volume
// gets a note on stderr, and a newer one stops here with both versions named.
// Verbs that delete the volume first (reset, ensure --fresh) skip this.
func (a *App) guardVolumeVersion(g *gateway) error {
	want := g.doc.Ignition.Version
	volume := docker.GatewayDataVolume(g.stamp.Namespace())
	have, exists := docker.VolumeVersion(volume, "inductiveautomation/ignition:"+want)
	if !exists || have == "" || have == want {
		return nil
	}
	haveV, err1 := semver.Parse(have)
	wantV, err2 := semver.Parse(want)
	if err1 != nil || err2 != nil {
		return nil
	}
	switch {
	case haveV.Less(wantV):
		a.stage("note: data volume %s was created by Ignition %s; starting %s upgrades it one way, and it cannot go back to %s",
			volume, have, want, have)
	case wantV.Less(haveV):
		return contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
			fmt.Sprintf("data volume %s was last run by Ignition %s, newer than the contract's [ignition].version %s; "+
				"the Ignition image refuses to start on a newer volume, so the Gateway was not started", volume, have, want)).
			WithRemediation(contract.Remediation{
				Command: "igdev setup",
				Why:     fmt.Sprintf("after setting [ignition].version back to %s in igdev.toml", have),
			}, contract.Remediation{
				Command: "igdev gateway reset",
				Why:     fmt.Sprintf("start %s on a new volume; this deletes the volume's data", want),
			})
	}
	return nil
}
