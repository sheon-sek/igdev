package docker

import (
	"bufio"
	"os/exec"
	"strings"
)

// GatewayDataVolume is the named volume Compose creates for the Instance whose
// project is namespace: the Gateway's data directory.
func GatewayDataVolume(namespace string) string {
	return namespace + "_gateway-data"
}

// containerInitConf is where the Ignition image records the version that last
// initialised a data volume, relative to the data directory.
const containerInitConf = ".container-init.conf"

// VolumeVersion reads volume_version from a Gateway data volume without starting
// Ignition: it cats the image's init record through a throwaway container of
// image. exists is false when the volume does not exist; version is empty when
// the volume, the file, or the key is missing or cannot be read. Either way the
// caller proceeds: the guard only speaks when it has a version to compare.
func VolumeVersion(volume, image string) (version string, exists bool) {
	if err := exec.Command("docker", "volume", "inspect", "--format", "{{.Name}}", volume).Run(); err != nil {
		return "", false
	}
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "cat",
		"-v", volume+":/d:ro", image, "/d/"+containerInitConf).Output()
	if err != nil {
		return "", true
	}
	return parseVolumeVersion(string(out)), true
}

// parseVolumeVersion returns the volume_version value of a .container-init.conf.
func parseVolumeVersion(conf string) string {
	scanner := bufio.NewScanner(strings.NewReader(conf))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if ok && strings.TrimSpace(key) == "volume_version" {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}
