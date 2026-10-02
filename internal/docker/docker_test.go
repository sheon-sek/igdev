package docker

import "testing"

// Both wordings the docker CLI has used for a daemon it cannot reach are a
// stopped daemon; an engine that received the call and refused it is not.
func TestDaemonDownMatchesEveryCLIWording(t *testing.T) {
	for _, output := range []string{
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
		"failed to connect to the docker API at unix:///var/run/docker.sock; check if the path is correct and if the daemon is running: dial unix /var/run/docker.sock: connect: no such file or directory",
		"error during connect: Get \"http://%2F%2F.%2Fpipe%2FdockerDesktopLinuxEngine/v1.47/containers/json\": open //./pipe/dockerDesktopLinuxEngine: The system cannot find the file specified.",
	} {
		if !DaemonDown(output) {
			t.Errorf("DaemonDown(%q) = false", output)
		}
	}
	for _, output := range []string{
		"service \"gateway\" refused to start",
		"Error response from daemon: driver failed programming external connectivity",
	} {
		if DaemonDown(output) {
			t.Errorf("DaemonDown(%q) = true", output)
		}
	}
}
