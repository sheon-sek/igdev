package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sheon-sek/igdev/internal/atomicfile"
	"github.com/sheon-sek/igdev/internal/contract"
	"github.com/sheon-sek/igdev/internal/docker"
)

// GatewayDataDir is the Gateway's data directory inside its container, the root
// every `gateway data` path is relative to.
const GatewayDataDir = "/usr/local/bin/ignition/data"

const (
	// execStreamCap bounds each captured stream of `gateway exec --json`. A longer
	// stream is cut there and the cut is reported (truncated, *_bytes), never
	// hidden.
	execStreamCap = 1 << 20
	// dataJSONCap bounds a file `gateway data get --json` carries inline. A larger
	// one is refused with a pointer to the <local> form.
	dataJSONCap = 1 << 20
	// dataOwner is the uid:gid `gateway data` reads and writes as: the Gateway's
	// own user and the group its data directory belongs to, so the Gateway can
	// read and delete what igdev puts there.
	dataOwner = "2003:0"
	// exitNotAFile is how the get script reports a path with no regular file;
	// any other non-zero exit is the command's own failure.
	exitNotAFile = 98
)

// containScript resolves $1 inside the container, symlinks included. The rest of
// each data script works on $target. The Gateway container is disposable, so any
// path in it is fair game (igdev#103).
const containScript = `set -eu
target=$(realpath -m -- "$1")
`

const (
	putScript = containScript + `mkdir -p -- "$(dirname -- "$target")"
cat > "$target"
`
	getScript = containScript + `[ -f "$target" ] || { echo "no regular file at $1" >&2; exit 98; }
cat -- "$target"
`
)

// gatewayExecData is `gateway exec --json`: what ran, as whom, and what it said.
type gatewayExecData struct {
	Instance  string   `json:"instance_id"`
	Container string   `json:"container"`
	User      string   `json:"user"`
	Command   []string `json:"command"`
	ExitCode  int      `json:"exit_code"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
	// StdoutBytes and StderrBytes are the full stream sizes; Truncated says a
	// stream was longer than the cap and only its first bytes are in the envelope.
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
	Truncated   bool  `json:"truncated"`
}

// gatewayDataResult is `gateway data put|get --json`.
type gatewayDataResult struct {
	Instance string `json:"instance_id"`
	// Path is the data-relative path, and ContainerPath where it is in the
	// container.
	Path          string `json:"path"`
	ContainerPath string `json:"container_path"`
	// Local is the host file read or written; empty when get wrote to the
	// envelope or stdout.
	Local  string `json:"local,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// ContentBase64 is the file, for `get --json` without a <local> path.
	ContentBase64 *string `json:"content_base64,omitempty"`
}

func (a *App) newGatewayExecCmd() *cobra.Command {
	var user, workdir string
	cmd := &cobra.Command{
		Use:   "exec [--user ignition|root] [--workdir <dir>] -- <command> [args...]",
		Short: "Run a command inside this Instance's Gateway container",
		Long: `exec runs one command in the running Gateway container through this Instance's
compose project, the way every other gateway verb addresses it, so nothing has to
rebuild the ` + "`docker compose --project-name … --file … --env-file …`" + ` call.

The command runs without a TTY as the Gateway's own user, ignition (2003), unless
--user root asks for root. In human mode its output streams through and igdev exits
with the command's own exit code. With --json both streams are captured into data, up
to 1 MiB each; a longer stream is cut there and data says so. A Gateway that is not
running is IGDEV_E_GATEWAY_UNHEALTHY.`,
		Example: `  igdev gateway exec -- ls /usr/local/bin/ignition/data
  igdev gateway exec --user root -- cat /etc/os-release
  igdev gateway exec --json -- curl -s http://host.docker.internal:4840/`,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("gateway exec", "command",
					"igdev gateway exec -- ls /usr/local/bin/ignition/data", "name the command to run in the Gateway container")
			}
			if user != "ignition" && user != "root" {
				return contract.UsageFault(fmt.Sprintf("--user must be ignition or root, got %q", user),
					contract.Remediation{Command: "igdev help gateway exec", Why: "show the users exec runs as"})
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRunning(); err != nil {
				return err
			}
			data := gatewayExecData{
				Instance:  g.stamp.InstanceID,
				Container: docker.GatewayContainer(g.compose.Namespace),
				User:      user,
				Command:   args,
			}
			req := docker.ExecRequest{User: user, Workdir: workdir, Argv: args}
			var stdout, stderr *capWriter
			if g.res.IsJSON() {
				stdout, stderr = &capWriter{limit: execStreamCap}, &capWriter{limit: execStreamCap}
				req.Stdout, req.Stderr = stdout, stderr
			} else {
				req.Stdout, req.Stderr = a.Stdout, a.Stderr
				if a.Stdin != nil {
					req.Stdin = a.Stdin
				}
			}
			code, runErr := g.compose.Exec(req)
			if runErr != nil {
				return docker.Fault("docker compose exec", "", runErr)
			}
			data.ExitCode = code
			if stdout != nil {
				data.Stdout, data.StdoutBytes = stdout.String(), stdout.total
				data.Stderr, data.StderrBytes = stderr.String(), stderr.total
				data.Truncated = stdout.cut || stderr.cut
			}
			if code != 0 {
				return contract.NewFault(contract.CodeExecFailed, contract.Exit(code),
					fmt.Sprintf("%s exited with code %d in %s", args[0], code, data.Container)).
					WithData(data)
			}
			a.emit(g.res, data, func() {})
			return nil
		},
	}
	// Everything after the command name belongs to the command: igdev's own flags
	// go before it (or before --).
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&user, "user", "ignition", "the container user: ignition (the Gateway's, 2003) or root")
	cmd.Flags().StringVar(&workdir, "workdir", "", "the working directory inside the container")
	return cmd
}

func (a *App) newGatewayDataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Move files into and out of the Gateway container",
		Long: `data copies one file between the host and the running Gateway container. A relative
path on the Gateway side is relative to its data directory (` + GatewayDataDir + `);
an absolute path names any place in the container, such as
/usr/local/bin/ignition/user-lib/jdbc for a JDBC driver. Files are read and written as
the Gateway's own user (2003:0), so the Gateway can read and delete what put leaves
there; --user root reads and writes as root.`,
		Example: `  igdev gateway data put marker.once engineering-tools/proof/run.once
  igdev gateway data put postgresql.jar /usr/local/bin/ignition/user-lib/jdbc/postgresql.jar
  igdev gateway data get engineering-tools/proof/report.json report.json`,
		Args: rejectUnknownCommand,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newGatewayDataPutCmd(), a.newGatewayDataGetCmd())
	return cmd
}

func (a *App) newGatewayDataPutCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "put <local> <gateway-path>",
		Short: "Copy a host file into the Gateway container",
		Long: `put copies one host file into the Gateway container, creating the parent
directories it needs. A relative <gateway-path> is under the data directory; an
absolute one is anywhere in the container. The file ends up owned by the Gateway's
user (2003:0), or by root with --user root.
--json reports {path, container_path, local, bytes, sha256}.`,
		Example: `  igdev gateway data put run.once engineering-tools/proof/run-udt-sdk-proof.once`,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) < 2 {
				return missingArgument("gateway data put", "gateway-path",
					"igdev gateway data put run.once engineering-tools/proof/run.once", "name the host file and where it goes under the data directory")
			}
			if len(args) > 2 {
				return extraArguments("gateway data put", 2, args)
			}
			local, rel := args[0], args[1]
			target, fault := dataPath(rel)
			if fault != nil {
				return fault
			}
			owner, fault := dataUser(user)
			if fault != nil {
				return fault
			}
			info, err := os.Stat(local)
			if err != nil || !info.Mode().IsRegular() {
				return contract.UsageFault(fmt.Sprintf("%s is not a readable regular file", local),
					contract.Remediation{Command: "igdev help gateway data put", Why: "pass a host file to copy"})
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRunning(); err != nil {
				return err
			}
			handle, err := os.Open(local)
			if err != nil {
				return contract.UsageFault(fmt.Sprintf("cannot read %s: %v", local, err),
					contract.Remediation{Command: "igdev help gateway data put", Why: "pass a host file to copy"})
			}
			defer func() { _ = handle.Close() }()
			sum := sha256.New()
			counter := &countWriter{}
			var stderr bytes.Buffer
			code, runErr := g.compose.Exec(docker.ExecRequest{
				User:   owner,
				Argv:   []string{"sh", "-c", putScript, "igdev-data-put", target},
				Stdin:  io.TeeReader(handle, io.MultiWriter(sum, counter)),
				Stdout: io.Discard, Stderr: &stderr,
			})
			if fault := dataFault("put", rel, code, runErr, stderr.String()); fault != nil {
				return fault
			}
			data := gatewayDataResult{
				Instance: g.stamp.InstanceID, Path: rel, ContainerPath: target, Local: local,
				Bytes: counter.n, SHA256: hex.EncodeToString(sum.Sum(nil)),
			}
			a.emit(g.res, data, func() {
				fmt.Fprintf(a.Stdout, "put %s -> %s (%d bytes)\n", local, target, data.Bytes)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "ignition", "the container user: ignition (2003:0) or root")
	return cmd
}

func (a *App) newGatewayDataGetCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "get <gateway-path> [<local>]",
		Short: "Copy a file out of the Gateway container",
		Long: `get copies one file out of the Gateway container: a relative <gateway-path> is
under the data directory, an absolute one anywhere in the container. With <local> it is
written there (atomically: a failed copy leaves nothing behind); without it the bytes
go to stdout, or with --json into data.content_base64, up to 1 MiB — a larger file
needs <local>. --json reports {path, container_path, local, bytes, sha256}.`,
		Example: `  igdev gateway data get engineering-tools/proof/udt-sdk-proof-latest.json report.json
  igdev gateway data get engineering-tools/proof/udt-sdk-proof-latest.json --json`,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArgument("gateway data get", "gateway-path",
					"igdev gateway data get engineering-tools/proof/report.json report.json", "name the file under the data directory")
			}
			if len(args) > 2 {
				return extraArguments("gateway data get", 2, args)
			}
			rel := args[0]
			target, fault := dataPath(rel)
			if fault != nil {
				return fault
			}
			owner, fault := dataUser(user)
			if fault != nil {
				return fault
			}
			g, err := a.gatewayContext()
			if err != nil {
				return err
			}
			if err := g.requireRunning(); err != nil {
				return err
			}
			data := gatewayDataResult{Instance: g.stamp.InstanceID, Path: rel, ContainerPath: target}
			sum := sha256.New()
			counter := &countWriter{}
			var stderr bytes.Buffer
			fetch := func(w io.Writer) (int, error) {
				return g.compose.Exec(docker.ExecRequest{
					User:   owner,
					Argv:   []string{"sh", "-c", getScript, "igdev-data-get", target},
					Stdout: io.MultiWriter(w, sum, counter), Stderr: &stderr,
				})
			}
			switch {
			case len(args) == 2:
				data.Local = args[1]
				var code int
				var runErr error
				_, err := atomicfile.Fill(data.Local, 0o644, 0o755, func(w io.Writer) (int64, error) {
					code, runErr = fetch(w)
					if runErr == nil && code != 0 {
						return 0, errExecFailed
					}
					return counter.n, runErr
				})
				if fault := dataFault("get", rel, code, runErr, stderr.String()); fault != nil {
					return fault
				}
				if err != nil {
					return contract.NewFault(contract.CodeInternal, contract.ExitFailure,
						fmt.Sprintf("cannot write %s: %v", data.Local, err)).WithCause(err)
				}
			case g.res.IsJSON():
				buf := &capWriter{limit: dataJSONCap}
				code, runErr := fetch(buf)
				if fault := dataFault("get", rel, code, runErr, stderr.String()); fault != nil {
					return fault
				}
				if buf.cut {
					return contract.UsageFault(
						fmt.Sprintf("%s is %d bytes, more than --json carries inline (%d)", rel, buf.total, dataJSONCap),
						contract.Remediation{Command: "igdev gateway data get " + rel + " <local>", Why: "write a large file to a host path"})
				}
				content := base64.StdEncoding.EncodeToString(buf.Bytes())
				data.ContentBase64 = &content
			default:
				code, runErr := fetch(a.Stdout)
				if fault := dataFault("get", rel, code, runErr, stderr.String()); fault != nil {
					return fault
				}
			}
			data.Bytes, data.SHA256 = counter.n, hex.EncodeToString(sum.Sum(nil))
			a.emit(g.res, data, func() {
				if data.Local != "" {
					fmt.Fprintf(a.Stdout, "get %s -> %s (%d bytes)\n", target, data.Local, data.Bytes)
				}
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "ignition", "the container user: ignition (2003:0) or root")
	return cmd
}

// dataUser maps --user onto the uid:gid the data scripts run as.
func dataUser(user string) (string, *contract.Fault) {
	switch user {
	case "ignition":
		return dataOwner, nil
	case "root":
		return "root", nil
	}
	return "", contract.UsageFault(fmt.Sprintf("--user must be ignition or root, got %q", user),
		contract.Remediation{Command: "igdev help gateway data", Why: "show the users data runs as"})
}

// errExecFailed tells atomicfile.Fill to discard what a failed get wrote.
var errExecFailed = errors.New("the container command failed")

// dataPath resolves a Gateway-side path to its place in the container: an
// absolute path as given, a relative one under the data directory. Only an empty
// path is refused. Symlinks are resolved in the container itself.
func dataPath(rel string) (string, *contract.Fault) {
	if strings.TrimSpace(rel) == "" {
		return "", contract.UsageFault("the Gateway-side path is empty",
			contract.Remediation{Command: "igdev help gateway data", Why: "show how data paths are written"})
	}
	slashed := filepath.ToSlash(rel)
	if strings.HasPrefix(slashed, "/") {
		return path.Clean(slashed), nil
	}
	return path.Join(GatewayDataDir, slashed), nil
}

// dataFault maps the in-container script's result onto the contract.
func dataFault(verb, rel string, code int, runErr error, stderr string) *contract.Fault {
	detail := strings.TrimSpace(stderr)
	switch {
	case runErr != nil:
		return docker.Fault("docker compose exec", stderr, runErr)
	case code == 0:
		return nil
	case code == exitNotAFile:
		return contract.NewFault(contract.CodeExecFailed, contract.ExitFailure,
			fmt.Sprintf("gateway data %s: no regular file at %s", verb, rel))
	default:
		return contract.NewFault(contract.CodeExecFailed, contract.ExitFailure,
			fmt.Sprintf("gateway data %s %s failed in the container (exit %d): %s", verb, rel, code, detail))
	}
}

// requireRunning refuses a verb that needs the Gateway container when it is not
// running, naming `gateway up`.
func (g *gateway) requireRunning() error {
	if err := g.requireRuntimeFiles(); err != nil {
		return err
	}
	services, fault := g.compose.Ps()
	if fault != nil {
		return fault
	}
	reported := make([]gatewayService, 0, len(services))
	for _, s := range services {
		if s.Service == docker.GatewayServiceName {
			reported = append(reported, gatewayService(s))
		}
	}
	if gatewayState(reported) == "running" {
		return nil
	}
	return contract.NewFault(contract.CodeGatewayUnhealthy, contract.ExitFailure,
		fmt.Sprintf("the Gateway container of %s is not running", g.compose.Namespace)).
		WithRemediation(contract.Remediation{Command: "igdev gateway up", Why: "start this Instance's Gateway"})
}

// capWriter keeps the first limit bytes written to it and counts the rest.
//
// The buffer is a field, not embedded: an embedded bytes.Buffer would hand
// io.Copy its ReadFrom, which bypasses Write and with it the cap and the count.
type capWriter struct {
	buf   bytes.Buffer
	limit int
	total int64
	cut   bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	room := c.limit - c.buf.Len()
	switch {
	case room <= 0:
		c.cut = c.cut || len(p) > 0
	case len(p) > room:
		c.buf.Write(p[:room])
		c.cut = true
	default:
		c.buf.Write(p)
	}
	return len(p), nil
}

// String is what was kept.
func (c *capWriter) String() string { return c.buf.String() }

// Bytes is what was kept.
func (c *capWriter) Bytes() []byte { return c.buf.Bytes() }

// countWriter counts the bytes that pass through it.
type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
