package lookup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
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

// ReadImage reads the documentation bundles of an image's own jars and its
// built-in modules. It creates a container without starting it, copies the two
// directories out as tar streams, and removes the container: nothing runs, so
// no license or EULA is involved, and no file is unpacked on disk beyond one
// archive at a time in scratch.
func ReadImage(image, scratch string) ([]bundle, error) {
	var stdout, stderr bytes.Buffer
	create := exec.Command("docker", "create", "--entrypoint", "true", image)
	create.Stdout, create.Stderr = &stdout, &stderr
	if err := create.Run(); err != nil {
		return nil, &EngineError{Action: "docker create " + image, Output: stderr.String(), Err: err}
	}
	id := strings.TrimSpace(stdout.String())
	defer func() { _ = exec.Command("docker", "rm", "--force", id).Run() }()
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

// copyOut streams one container directory as a tar archive into read.
func copyOut(id, dir string, read func(io.Reader) ([]bundle, error)) ([]bundle, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("docker", "cp", id+":"+dir, "-")
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
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
