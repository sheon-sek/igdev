package cli

import "testing"

// The Gateway's state is read from the gateway service alone: a trial keeper
// that runs next to a stopped or missing Gateway is not a running Gateway
// (igdev#147).
func TestGatewayStateReadsTheGatewayServiceAlone(t *testing.T) {
	gateway := func(state string) gatewayService { return gatewayService{Service: "gateway", State: state} }
	keeper := func(state string) gatewayService { return gatewayService{Service: "trial-keeper", State: state} }
	for _, tc := range []struct {
		name     string
		services []gatewayService
		want     string
	}{
		{"nothing", nil, "absent"},
		{"both running", []gatewayService{gateway("running"), keeper("running")}, "running"},
		{"gateway alone", []gatewayService{gateway("running")}, "running"},
		{"keeper alone", []gatewayService{keeper("running")}, "absent"},
		{"gateway exited, keeper running", []gatewayService{gateway("exited"), keeper("running")}, "stopped"},
		{"gateway restarting, keeper running", []gatewayService{keeper("running"), gateway("restarting")}, "stopped"},
		{"gateway running, keeper exited", []gatewayService{gateway("running"), keeper("exited")}, "running"},
	} {
		if got := gatewayState(tc.services); got != tc.want {
			t.Errorf("%s: gatewayState = %q, want %q", tc.name, got, tc.want)
		}
	}
}
