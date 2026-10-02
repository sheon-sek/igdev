// Package runtimeassets renders the runtime files one Instance runs from:
// the Compose file, the Compose environment, the image build file, the trial
// keeper script, and the Gateway configuration seed the image carries.
//
// The templates are embedded in the binary with //go:embed, so `igdev setup`
// materializes a checkout without reading any repository file and without a
// network. Rendering is a pure function of the Instance identity, the allocated
// ports, the Project Contract, and the hash of the Instance's API token: no
// timestamp, random value, or host path outside Input reaches a rendered file, so
// identical inputs produce byte-identical output.
package runtimeassets

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/sheon-sek/igdev/internal/ports"
)

// The generated file names, as they appear inside `.igdev/runtime/`.
const (
	// ComposeFileName is the Compose file; `.igdev/runtime` is its build context.
	ComposeFileName = "compose.yaml"
	// EnvFileName is the environment file `docker compose --env-file` reads.
	EnvFileName = "compose.env"
	// DockerfileFileName is the Instance image build file.
	DockerfileFileName = "Dockerfile"
	// KeeperFileName is the trial keeper script the image carries (ADR 0008).
	KeeperFileName = "trial-keeper.sh"
)

//go:embed templates
var files embed.FS

// Input is everything a rendered runtime file depends on. Every field is either
// recorded state (the Instance identity, the ports) or Project Contract data,
// which is what makes rendering deterministic.
type Input struct {
	// InstanceID is the Instance's UUID.
	InstanceID string
	// Namespace is the Instance's Docker namespace, igdev-<short-id>: the
	// compose project name, the gateway name, and the image prefix.
	Namespace string
	// IgnitionVersion selects the Gateway image tag.
	IgnitionVersion string
	// JythonVersion is rendered into the Compose environment for later tickets.
	JythonVersion string
	// Edition is the Ignition module edition.
	Edition string
	// Modules are the module ids the contract enables.
	Modules []string
	// MemoryMB is the Gateway heap the contract requests. The Capacity Gate
	// (ADR 0003, ticket 10) compares this against the host's MemAvailable before
	// `gateway up` starts a container; setup only records the request.
	MemoryMB int
	// Timezone is the Gateway timezone.
	Timezone string
	// AllowUnsignedModules is the contract's request that the Gateway load a
	// module artifact that carries no valid signature. It is rendered as
	// IGNITION_ALLOW_UNSIGNED_MODULES, which the Compose file hands the Gateway as
	// -Dignition.allowunsignedmodules.
	AllowUnsignedModules bool
	// Ports are the loopback ports allocated to this Instance.
	Ports ports.Triplet
	// RuntimeDir is the absolute path of the build context, `.igdev/runtime`.
	RuntimeDir string
	// ModulesDir is the absolute staged-modules path, mounted into the Gateway.
	// The staging content is later tickets; setup creates the mount point.
	ModulesDir string
	// BaselineDir is the absolute Baseline restore path, mounted read-only.
	BaselineDir string
	// APITokenHash is the hash of the Instance's own API token, which the seed
	// gives the Gateway before its first start (ADR 0007). Empty renders no seed.
	APITokenHash string
	// CreatedAtMillis is when the Instance was created, in epoch milliseconds:
	// the creation time the seeded token resource records. It comes from the
	// Setup Stamp, so it is stable across setups.
	CreatedAtMillis int64
	// TrialAutoReset is the contract's [gateway] trial_reset = "auto": the
	// compose project then runs the trial keeper next to the Gateway (ADR 0008).
	TrialAutoReset bool
}

// view is what a template sees: Input plus the derived strings that would
// otherwise need template logic.
type view struct {
	Input
	// BindAddress is the loopback interface every published port binds to.
	BindAddress string
	// ModuleList is the enabled modules as Compose wants them: comma-separated.
	ModuleList string
	// DockerfileFileName names the build file from inside the context.
	DockerfileFileName string
	// EnvFileName names the environment file, for the comment that tells the
	// reader how it is consumed.
	EnvFileName string
	// SeedDir names the seed directory from inside the build context.
	SeedDir string
	// TrialKeeper is whether the compose project runs the trial keeper: the
	// contract asks for it and there is a token for it to reset with.
	TrialKeeper bool
}

// File is one rendered runtime file: its name inside `.igdev/runtime/` and its
// bytes.
type File struct {
	Name string
	Data []byte
}

// Materialize renders every runtime file for one Instance, in the frozen order
// they are written. It is what `igdev setup` writes; the per-file functions below
// exist for tests that assert one rendering at a time. A seed file's name is a
// slash-separated path below the runtime directory.
func Materialize(in Input) ([]File, error) {
	out := make([]File, 0, 10)
	for _, name := range []string{ComposeFileName, EnvFileName, DockerfileFileName, KeeperFileName} {
		data, err := render(name, in)
		if err != nil {
			return nil, err
		}
		out = append(out, File{Name: name, Data: data})
	}
	seed, err := seedFiles(in)
	if err != nil {
		return nil, err
	}
	return append(out, seed...), nil
}

// Compose renders the Instance's Compose file.
func Compose(in Input) ([]byte, error) { return render(ComposeFileName, in) }

// ComposeEnv renders the environment file `docker compose --env-file` loads.
// It carries no secret: the Gateway admin credentials stay in `.igdev/local.toml`
// and reach Compose from the process environment, so this file is safe to render,
// diff, and golden.
func ComposeEnv(in Input) ([]byte, error) { return render(EnvFileName, in) }

// Dockerfile renders the Instance's image build file.
func Dockerfile(in Input) ([]byte, error) { return render(DockerfileFileName, in) }

func render(name string, in Input) ([]byte, error) {
	raw, err := files.ReadFile("templates/" + name + ".tmpl")
	if err != nil {
		return nil, fmt.Errorf("read embedded template %s: %w", name, err)
	}
	tmpl, err := template.New(name).Funcs(template.FuncMap{"quote": quote}).Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse embedded template %s: %w", name, err)
	}
	out := &view{
		Input:              in,
		BindAddress:        ports.BindAddress,
		ModuleList:         strings.Join(in.Modules, ","),
		DockerfileFileName: DockerfileFileName,
		EnvFileName:        EnvFileName,
		SeedDir:            SeedDir,
		TrialKeeper:        in.TrialAutoReset && in.APITokenHash != "",
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, out); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// quote renders a strings-safe double-quoted value for the rendered files.
func quote(value string) string { return fmt.Sprintf("%q", value) }
