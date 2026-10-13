package lookup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/sheon-sek/igdev/internal/interrupt"
)

// Install is where the Ignition image keeps the jars the function index reads.
const (
	imageInstall = "/usr/local/bin/ignition"
	imageCore    = imageInstall + "/lib/core"
	imageModules = imageInstall + "/user-lib/modules"
)

// Image is the official Ignition image of a version.
func Image(version string) string { return "inductiveautomation/ignition:" + version }

// ErrImageMissing is ImageID's answer for an image this machine has not pulled.
var ErrImageMissing = errors.New("the image is not on this machine")

// EngineError is a docker call that failed, with what the engine printed.
type EngineError struct {
	Action string
	Output string
	Err    error
}

func (e *EngineError) Error() string { return fmt.Sprintf("%s: %v", e.Action, e.Err) }
func (e *EngineError) Unwrap() error { return e.Err }

// ImageID is the local id of an image, which changes whenever the tag is pulled
// again, so it keys the function index.
func ImageID(image string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", image)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(strings.ToLower(stderr.String()), "no such image") {
			return "", ErrImageMissing
		}
		return "", &EngineError{Action: "docker image inspect " + image, Output: stderr.String(), Err: err}
	}
	id := strings.TrimSpace(stdout.String())
	if id == "" {
		return "", ErrImageMissing
	}
	return id, nil
}

// InstanceImages lists the Gateway images igdev built on this machine for a
// version, igdev-<instance>:<version>, sorted by name.
func InstanceImages(version string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("docker", "image", "ls", "--filter", "reference=igdev-*:"+version, "--format", "{{.Repository}}:{{.Tag}}")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, &EngineError{Action: "docker image ls", Output: stderr.String(), Err: err}
	}
	var out []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "igdev-") && strings.HasSuffix(line, ":"+version) {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadImage reads the documentation bundles of an image's own jars and its
// built-in modules. It creates a container without starting it, copies the two
// directories out as tar streams, and removes the container: nothing runs, so
// no license or EULA is involved, and no file is unpacked on disk beyond one
// archive at a time in scratch.
func ReadImage(image, scratch string) ([]bundle, error) {
	id, remove, err := createContainer(image)
	if err != nil {
		return nil, err
	}
	defer remove()
	core, err := copyOut(id, imageCore, func(r io.Reader) ([]bundle, error) { return readCoreTar(r, scratch) })
	if err != nil {
		return nil, err
	}
	modules, err := copyOut(id, imageModules, func(r io.Reader) ([]bundle, error) { return readModulesTar(r, scratch) })
	if err != nil {
		return nil, err
	}
	return append(core, modules...), nil
}

// createContainer creates a container of image without starting it and returns
// the release that removes it. The removal is registered with the interrupt
// handler before the creation is released, so a Ctrl-C or SIGTERM at any point
// after `docker create` succeeds still removes the container; one that makes it
// fail after it printed the container's id removes it at once.
func createContainer(image string) (string, func(), error) {
	held, ok := interrupt.Hold()
	if !ok {
		return "", nil, interrupt.ErrInterrupted
	}
	defer held()
	var stdout, stderr bytes.Buffer
	create := exec.Command("docker", "create", "--label", "dev.igdev.purpose=lookup", "--entrypoint", "true", image)
	create.Stdout, create.Stderr = &stdout, &stderr
	if err := create.Run(); err != nil {
		// A Ctrl-C can reach docker create after the engine created the
		// container and the CLI printed its id, but before the CLI exited.
		if id := strings.TrimSpace(stdout.String()); containerID.MatchString(id) {
			removeContainer(id)
		}
		return "", nil, &EngineError{Action: "docker create " + image, Output: stderr.String(), Err: err}
	}
	id := strings.TrimSpace(stdout.String())
	return id, interrupt.Defer(func() { removeContainer(id) }), nil
}

// containerID is the full id docker create prints for the container it created.
var containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func removeContainer(id string) {
	output, err := exec.Command("docker", "rm", "--force", id).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: removing lookup container %s failed: %v: %s\n", id, err, strings.TrimSpace(string(output)))
	}
}

// copyOut streams one container directory as a tar archive into read.
func copyOut[T any](id, dir string, read func(io.Reader) ([]T, error)) ([]T, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("docker", "cp", id+":"+dir, "-")
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// The interrupt path waits for docker cp to exit before it ends the process.
	held, ok := interrupt.Hold()
	if !ok {
		return nil, interrupt.ErrInterrupted
	}
	defer held()
	if err := cmd.Start(); err != nil {
		return nil, &EngineError{Action: "docker cp " + dir, Err: err}
	}
	found, readErr := read(pipe)
	// Drain what the reader left so docker cp can finish writing.
	_, _ = io.Copy(io.Discard, pipe)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("reading %s from the image: %w", dir, readErr)
	}
	if waitErr != nil {
		return nil, &EngineError{Action: "docker cp " + dir, Output: stderr.String(), Err: waitErr}
	}
	return found, nil
}
