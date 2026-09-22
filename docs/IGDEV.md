# igdev — the Go toolchain

`igdev` is the standalone, globally installed Ignition development toolchain that
replaces the repository-vendored `devctl` foundation. This tree now holds both:
the legacy bash foundation (`devctl`, `scripts/`, `config/`, `docker/`, `.env`,
`.runtime/`) still works exactly as before and is retired by ticket 13, while the
Go CLI is built here under `cmd/` and `internal/`. Vocabulary lives in
[`CONTEXT.md`](../CONTEXT.md); the hard-to-reverse choices in
[`docs/adr/`](adr/).

Ticket 01 (issue #7) delivered the walking skeleton: install → `igdev status --json`,
with the agent contract frozen by golden tests and the release path in CI.
Ticket 02 (issue #8) delivered the Project Contract: `igdev init` writes and edits
`igdev.toml`, every write prints a unified diff, and the Gate validates the contract
schema and the Checkout Setup's Setup Stamp before any project command does work.
Ticket 03 (issue #9) delivered the Checkout Setup: `igdev setup` mints the Instance
identity, allocates the ports, renders the runtime files from the embedded templates,
records the human-only Consent, and `igdev doctor` audits the host.

## Install

```bash
curl -fsSL https://github.com/sheon-sek/igdev/releases/latest/download/install.sh | bash
```

It publishes and installs every target it recognises: `linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64` (WSL takes the Linux binary; Windows native is
unsupported).

The published `install.sh` detects OS and CPU architecture, fetches the release's
`checksums.txt`, verifies the tarball's sha256 **before** placing anything, then
atomically renames the binary into `~/.local/bin/igdev`. If that prefix is not on
`PATH`, the installer registers it once in the first login profile it finds
(`~/.bash_profile`, `~/.bash_login`, `~/.profile`, creating the last) so a brand-new
shell runs `igdev` by name; the write is idempotent and is skipped entirely when the
prefix is already visible. It never updates a binary that is already installed
(ADR 0002): the CLI only *notices* that a newer release exists.

Options are `--version`, `--prefix`, `--repo-url`, and `--base-url` (plus
`IGDEV_INSTALL_*` equivalents). A pinned `--version` resolves that release's own
`releases/download/v<version>/` directory, never the "latest" one, and the version
token is validated before it is ever spliced into a URL or a filename. `--base-url`
overrides the whole layout — that is what lets the suite install a locally packaged
tree over loopback HTTP.

## Build and validate

Toolchain: **Go 1.27.1** (`linux/amd64`), the version recorded in `go.mod` and used
by both workflows through `actions/setup-go` with `go-version-file: go.mod`. On this
machine it lives in `~/.local/go` with `go` and `gofmt` symlinked into `~/.local/bin`.
`GOTOOLCHAIN=local` in CI keeps a build from silently fetching another toolchain.

```bash
make build        # bin/igdev (also: make test, make vet, make fmt-check)
make test         # the whole suite, resource gates included
make test-quick   # behaviour only: skips TestGate, TestPackage, TestInstaller
make gates        # prints the measured startup P95, peak RSS, binary size
make package      # dist/<version>/*.tar.gz + checksums.txt
make goldens      # rewrite goldens after an intentional contract change
```

`go test ./...` is hermetic: it needs no network and no docker. The rig builds the
real binary once per test process, and the release tests run `packaging/package.sh`
and `packaging/install.sh` for real against a loopback file server.

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/igdev` | main; three lines that call `cli.Execute` |
| `internal/cli` | cobra tree, the Gate's discovery call, envelope/exit mapping, `init`, `setup`, `doctor`, the `gateway` verbs |
| `internal/contract` | frozen envelope, `IGDEV_E_*` codes, exit levels, CLI Contract Version |
| `internal/config` | five-tier resolver (flags > `IGDEV_*` > `.igdev/local.toml` > `igdev.toml` > defaults) |
| `internal/project` | Project Root discovery, the `igdev.toml` schema v1 model: parse, validate, render, Contract Digest |
| `internal/gate` | the Gate: contract schema, Setup Stamp (digest, schema, CLI Contract, Instance identity and ports), `[tool].min_version` — `Evaluate` / `Require` / `Decode` |
| `internal/instance` | the Instance identity: UUID minting, validation, and the `igdev-<short-id>` namespace |
| `internal/ports` | dynamic loopback port allocation by bind probe, and the free-port check a refresh uses |
| `internal/capacity` | the Capacity Gate: `MemAvailable` measurement and the refusal arithmetic (ADR 0003) |
| `internal/docker` | the `docker compose` client: one Instance's project, `up` / `down` / `ps` / `logs` / `restart`, and the running-project listing the Capacity Gate reports |
| `internal/consent` | the machine-global human-only Consent record: term table, load/accept, the exit-3 check |
| `internal/runtimeassets` | the embedded Compose / Compose-env / Dockerfile templates and their deterministic render |
| `internal/localconfig` | the checkout-local tier `.igdev/local.toml`: Gateway credentials, password generation, key-preserving writes |
| `internal/doctor` | the host prerequisite probes and their report |
| `internal/atomicfile` | the temp-file-plus-rename write every generated file goes through |
| `internal/textdiff` | the unified diff every tracked write prints |
| `internal/updater` | notice-only update check with a 24 h XDG cache |
| `internal/semver` | version ordering used by the notice |
| `internal/xdg` | `~/.cache/igdev`, `~/.config/igdev`, `~/.local/state/igdev` |
| `internal/testrig` | seam S1 harness: scratch HOME, PATH shims, loopback server, goldens, gates |
| `itest` | behaviour tests and the goldens that freeze the contract |
| `packaging` | `package.sh` (linux/darwin x amd64/arm64 tarballs + `checksums.txt`), `install.sh` |
| `.github/workflows/igdev-ci.yml` | build, vet, gofmt, goldens, resource gates |
| `.github/workflows/igdev-gateway-e2e.yml` | the non-hermetic tier: real docker, real Ignition image, the whole Gateway lifecycle |
| `.github/workflows/igdev-release.yml` | tag push → checksummed GitHub Release |

## Agent contract (frozen)

Every command emits the same envelope. With `--json`, stdout is *only* that JSON;
progress and notices go to stderr. Without it, humans get prose on stdout and
errors on stderr.

```json
{
  "ok": false,
  "contract": "1",
  "code": "IGDEV_E_USAGE",
  "message": "unknown command \"stat\" for \"igdev\"",
  "remediation": [{ "command": "igdev --help", "why": "list the available commands" }],
  "data": {}
}
```

Exit levels: `0` success, `1` command failure (the `code` says how), `2` usage
error, `3` human action required. Codes are namespaced `IGDEV_E_*`; `igdev` emits
`IGDEV_E_USAGE`, `IGDEV_E_MISSING_ARGUMENT`, `IGDEV_E_CONFIG_INVALID`,
`IGDEV_E_INTERNAL` (ticket 01), from the Gate (ticket 02)
`IGDEV_E_NOT_INITIALIZED`, `IGDEV_E_SETUP_REQUIRED`, `IGDEV_E_SETUP_STALE`,
`IGDEV_E_CONTRACT_SCHEMA_UNSUPPORTED`, `IGDEV_E_VERSION_UNSUPPORTED`, and from the
Checkout Setup (ticket 03) `IGDEV_E_CONSENT_REQUIRED` (exit 3: a person must accept a
legal term) and `IGDEV_E_PORT_ALLOC` (the host refused three loopback binds). The
Gateway suite adds `IGDEV_E_CAPACITY` (exit 3: the Capacity Gate refused to start a
Gateway and a person frees memory or passes `--force`), `IGDEV_E_GATEWAY_UNHEALTHY`
(a Gateway did not answer before the deadline, or answered a smoke check with an error
status) and `IGDEV_E_DOCKER` (a container-engine call failed, or the engine is not
installed). A typo in
a `--config` flag is a usage error (the invocation was wrong); a value igdev read from
a tier is `IGDEV_E_CONFIG_INVALID` (machine or project state is wrong). `contract` is
the CLI Contract Version and bumps only when the envelope, codes, or exit levels
break — never alongside release semver.

`igdev help`, `igdev <command> --help`, and a bare `igdev` are documentation, and in
machine mode they are envelopes too: the rendered reference arrives in `data.help`
with `data.command` and `data.section`, never as raw text on a JSON stdout.

`igdev help`, `igdev <command> --help`, and a bare `igdev` are documentation, and in
machine mode they are envelopes too: the rendered reference arrives in `data.help`
with `data.command` and `data.section`, never as raw text on a JSON stdout.

An explicit `--json=false` is a flag-tier value, so it outranks a lower tier that
asked for JSON; and because a failure report must not be lost, the dialect is peeled
for `output.format` alone (`config.DialectIsJSON`), skipping any tier that cannot
state it.

`igdev status` is the safe first call. Outside a Project Root it reports
`ok: true` with `initialized: false`; inside one it reports the discovered root, the
contract's declared schema version, whether this CLI supports that schema
(`contract.schema_supported`), the Contract Digest, and the Checkout Setup's
`setup.stamp_state` (`required` / `current` / `stale`) — plus the Instance the record
names (`setup.instance_id`, `setup.namespace`, `setup.ports`), the machine Consent term
by term (`consent`), and every config key with the tier that won. status reports the
Gate's verdict, it never enforces it.

## The Project Contract and the Gate

`igdev init` is the only writer of tracked files. It writes `igdev.toml` and the
managed `.igdev/` entry in `.gitignore` atomically (temp file plus rename in the
target directory, never a `.bak`), and prints a unified diff of every change — on
stderr for humans, inside `data.contract.diff` / `data.gitignore.diff` for agents,
each with its `action` (`created` / `updated` / `unchanged`) and, for the contract,
the new `digest`. A run that changes nothing writes nothing and prints nothing.

The schema v1 layout is closed and version-checked: `schema = 1`, then `[project]
name`, `[tool] min_version`, `[ignition] version|jython_version|edition`,
`[modules] enabled`, `[scan] jython|capabilities`, `[commands] check|test|build|smoke`
(omitted when empty), `[gateway] memory_mb|timezone|smoke_endpoints`. A key igdev does
not know is refused, and a version-like field holds a version. Fields the file leaves
out fall back to the embedded defaults, so a minimal `schema = 1` contract is valid.
`smoke_endpoints` is the one optional list: it is omitted from the rendered contract
unless it is non-empty, and a checkout that does not state it smokes the root document
alone. `init` merges: a flag overrides the value it names and everything else the file
holds is preserved; an empty value (`--modules ""`) clears a field.

The Contract Digest is `sha256:` over the contract bytes. `.igdev/setup.json` holds
the Setup Stamp — the digest, the contract schema version, the CLI Contract Version,
the Instance identity and its ports, and when the Instance was created — and the Gate
compares its stamp fields before any project command does work: no record is
`IGDEV_E_SETUP_REQUIRED`, a moved digest or stamp field is `IGDEV_E_SETUP_STALE`
(both remediate with `igdev setup`, which materializes the checkout),
a schema above v1 is `IGDEV_E_CONTRACT_SCHEMA_UNSUPPORTED` (never partially parsed,
never auto-downgraded), and a contract requiring a newer igdev than the running one
is `IGDEV_E_VERSION_UNSUPPORTED`. `init` and `setup` are the repair paths and run
whatever the contract says, so re-running `init` always brings a hand-edited,
malformed, or future-schema contract back into a shape igdev speaks — with the diff
as the review of what that cost.

## The Checkout Setup

`igdev setup` materializes the checkout. It is a Gate repair path: a missing or stale
Setup Stamp is what it exists to clear, while everything else the Gate refuses — no
contract, an unsupported schema, a contract this CLI is too old for — still stops it.
A refused run writes nothing; the record is written last, so a run that dies part way
leaves the old stamp and setup simply runs again.

```
.igdev/
  setup.json            the Setup Stamp (0600): instance_id, digest, schema, CLI
                        contract, ports, created_at
  local.toml            checkout-local tier (0600): Gateway admin credentials
  runtime/              the materialized build context
    compose.yaml        the Instance's Compose file
    compose.env         the environment docker compose reads with --env-file
    Dockerfile          the Instance image, FROM the Ignition version in the contract
  modules/              staged Private Modules, mounted into the Gateway
  restore/              Baseline restore path, mounted read-only
```

**Instance identity.** `instance_id` is a random UUID v4 minted at first setup from
`crypto/rand`. It is never derived from the checkout path and never regenerated, so
moving a directory keeps the Instance and two worktrees of one repository are two
Instances. The Docker namespace is `igdev-<first 8 hex of the UUID>`, which names the
Compose project, the Gateway container, and the image.

**Ports.** The triplet (HTTP, HTTPS, debug) is allocated by bind probe: igdev asks the
kernel for three free loopback ports (`127.0.0.1:0`), holding each listener open until
all three are chosen so they are distinct. Re-running setup keeps the recorded triplet
when all three ports are still free, and re-allocates when any of them was taken — the
record is never allowed to describe a collision. Ports live in the record and in
`.igdev/local.toml`, never in the tracked contract, and nothing may assume 8088
(ADR 0003). A hand-edited `local.toml` keeps every line igdev does not own: setup
writes the two credential keys and preserves the rest, including any port pin.

**Materialization.** The Compose file, the Compose environment, and the Dockerfile come
from templates embedded in the binary (`internal/runtimeassets`), rendered into
`.igdev/runtime/`. Rendering is a pure function of the Instance identity, the ports,
and the contract: no timestamp, no random value, and no path outside that input reaches
a rendered file, so identical inputs are byte-identical and a second setup on a current
checkout re-materializes nothing. Every referenced host path is absolute, the build
context is `.igdev/runtime/` itself, and staged modules and the Baseline restore
directory are mounted — so the repository contributes no Docker input of its own and
never needs a `.dockerignore`.

**Credentials.** setup generates the Gateway admin password (24 characters from a
printable, quote-safe alphabet, `crypto/rand`) or takes `IGDEV_GATEWAY_ADMIN_PASSWORD`
(the `--admin-password` flag wins, then the environment, then what the checkout
already holds). It is written to `.igdev/local.toml` mode 0600 and never printed: not
in the JSON envelope, not on stdout, not on stderr. `igdev gateway credentials --json`
is the only way to read it back out — human `credentials` prints the username and the
source and says so. The rendered runtime files carry
no secret at all — the Compose environment only *references* the credential variables,
which the igdev process supplies at `gateway up` time.

**Consent.** The record is machine-global: `~/.config/igdev/accepted.toml`, keyed by
term (`ignition-eula`, `module-license`, `module-cert`) and holding when each was
accepted and by which CLI. Only a human-invoked command writes it — `igdev setup
--accept-eula` today — and a missing term stops the run with `IGDEV_E_CONSENT_REQUIRED`
at exit level 3, naming the exact command in Remediation. Consent is checked before
anything is written, so a refused setup leaves the tree untouched. Re-accepting on a
machine that already consented is a successful no-op: the first acceptance is the
legal fact, so its timestamp does not move.

## The Gateway lifecycle

`igdev gateway` drives the Ignition container the Checkout Setup describes. Every verb
passes the Gate (a current Checkout Setup) and the machine Consent record first, so a
refused verb is the same frozen shape `setup` produces, and no verb ever talks to the
engine before both hold.

```
igdev gateway up [--force]          build if needed, then `docker compose up --detach
                                    --build` for this Instance's project
igdev gateway down [--volumes]      stop and remove the containers; --volumes also
                                    discards the Gateway's data volume
igdev gateway reset [--force] [--timeout N]   down --volumes, then up, then wait
igdev gateway restart               restart the container in place
igdev gateway wait [--timeout N]    poll the recorded URL until it answers below 400
igdev gateway smoke [--timeout N]   wait, then GET the root document and the
                                    contract's [gateway] smoke_endpoints, in order
igdev gateway status                `docker compose ps` for this project plus the
                                    recorded URL
igdev gateway logs [--tail N]       the Gateway's log: streamed for humans, in
                                    data.logs with --json
igdev gateway url                   the recorded URL, and nothing else
igdev gateway credentials --json    {username, password}; human mode never prints the
                                    password
```

Every engine call is `docker compose --project-name igdev-<instance> --file
<runtime>/compose.yaml --env-file <runtime>/compose.env <verb>`, with the admin
credentials supplied from the process environment — so a parallel worktree's Instance
can never be addressed by mistake, and no rendered file carries a secret.

`wait` accepts bare seconds (`--timeout 240`) or a Go duration (`--timeout 3m`);
180 s is the default, 60 s for `smoke`. A failed wait reports the last 50 log lines on
stderr, because the reason a Gateway never came up is in its own log. `smoke` fails
with the failing endpoint named in the message, and the transport error or status is
in `data.checks[].error` for a passing run's report.

**Capacity Gate.** `up` and `reset` read `MemAvailable` from `/proc/meminfo` (ADR
0003) and refuse to start another Gateway when it is below the contract's
`[gateway] memory_mb` plus 512 MiB headroom: `IGDEV_E_CAPACITY` at exit level 3, with
the running `igdev-*` compose projects named in the message and `--force` in
Remediation. `--force` starts the Gateway anyway and says so on stderr. A host whose
free memory cannot be read (no `MemAvailable`, another platform) is not refused: there
is no evidence of a shortage, and the warning on stderr records that the guard did not
apply. The rig points the gate at a fixture through `capacity.MeminfoEnv`
(`IGDEV_SHIM_MEMINFO`), which is what makes the refusal reachable in a hermetic test.

## Host prerequisites

`igdev doctor` audits the host read-only and never fails: the exit level stays 0 and
the audit is the payload, so an agent reads the report rather than an error string.
Each prerequisite is reported with the exact probe, whether it is required, and either
the version line the tool printed or the reason there is none (`missing` when it is not
on PATH, `failed` when it is but printed no version). docker and its Compose plugin
(the Gateway) and a JVM (the Jython check) are required; Gradle is optional because a
project only needs it when its contract declares a Gradle command. `data.ready` is
false when a required prerequisite is not present.

## Update notice

`igdev version` and `igdev status` may print one line on stderr when a newer
release exists. It is cached for 24 h (config key `updater.cache_ttl_hours`) in
`$XDG_CACHE_HOME/igdev/update-check.json`, fetched with a bounded timeout, and
skipped entirely when `IGDEV_NO_UPDATE_NOTIFIER=1`, when `updater.enabled` is
false, or whenever `--json` is in effect — so machine output never depends on the
network and never varies between runs. The endpoint is the config key
`updater.releases_api_url`, which is how the tests point it at a loopback server.

## Resource and hygiene gates

Asserted through the real binary in `itest/gates_test.go`, on this machine and in
CI: non-docker startup P95 under 100 ms, peak RSS under 64 MB (read from the
kernel's own `VmHWM` for the child), binary under 30 MB, zero temp-file leaks per
run (`env.Snapshot()` fingerprints paths *with mode and content*, so adding,
rewriting, chmod-ing, or deleting anything in the scratch tree fails the assertion,
and `TMPDIR` must be empty), zero orphan docker objects (the `docker` shim records
every argv, and `DockerOrphans` tallies by object identity — `--name`, or the
synthetic container id the shim echoes — so `create leaked` plus `rm unrelated` is
still a leak, while `run --rm` is not one). Measured here on an i7-13700KF: P95 2 ms, peak RSS
6.4 MiB, binary 7.2 MiB.

## Working in this tree

### The one seam

S1 is the compiled binary's process boundary. Behaviour tests live in `itest/` and
run the real `igdev` through `internal/testrig`; they never import `internal/cli`
and call it in-process. The only in-process unit tests are for pure functions
(`internal/config`, `internal/contract`, `internal/project`, `internal/gate`,
`internal/semver`, `internal/textdiff`, `internal/instance`, `internal/ports`,
`internal/consent`, `internal/localconfig`, `internal/runtimeassets`,
`internal/doctor`). Do not add a new seam: if a behaviour cannot
be observed from argv, exit level, stdout, stderr, the filesystem, or the shim's call
log, it is not yet a testable requirement.

### Rig entry points

```go
env := testrig.NewEnv(t)              // scratch root: HOME, TMPDIR, shim PATH; built binary
root := env.Project("repo", contract) // writes repo/igdev.toml, returns the absolute dir
env.LocalConfig(root, "…")            // .igdev/local.toml tier
env.SetupRecord(root, "…")            // .igdev/setup.json record
env.SetupStamp("repo", contractPath)  // a Setup Stamp current for that contract (the CLI writes the real one)
env.ShimDocker()                      // PATH shim; returns the call-log path, records to state/docker-calls.jsonl
res := env.RunIn(root, "status", "--json")
res := env.Run(testrig.Run{Args: …, Dir: …, Env: []string{"K=V"}, SampleRSS: true, Stdin: "…"})
env.MustRun("version")                // fails the test if the process could not launch
testrig.WantExit(t, res, contract.ExitUsage)
testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeUsage)
testrig.Status(t, res.Stdout)         // decoded status data, with ResolvedSource(key)
env.Golden(t, "name.json", res.Stdout)
res.AssertNoLeaks(t)                  // tree unchanged since launch, TMPDIR empty, no .tmp debris
res.AssertNoLeaksOutside(t, roots…)   // scoped form: writes inside roots are expected, anywhere else leaks
env.ChangesOutside(base, roots…)      // the same scoping, for tests that assert the exact change set
env.AssertNoDockerCalls(t)            // status/startup must stay off the engine
env.AssertNoDockerOrphans(t)          // created objects were all removed
server := testrig.ServeLatestRelease(t, "v9.9.9"); env.PointAt(server); server.Hits()
testrig.ServeDir(t, dist)             // loopback file server over a directory of artifacts
testrig.EnvFor("updater.enabled")     // the IGDEV_* name that sets a frozen config key
env.SetBaseEnv("IGDEV_NO_UPDATE_NOTIFIER=")  // turn the notice on for this env only
testrig.RunScriptIn(root, env, "packaging/package.sh", version)
```

`Result` carries `Exit`, `Stdout`, `Stderr`, `Duration`, `PeakRSSKB`, `Samples`.
`env.Snapshot()` fingerprints content and mode, so `AssertNoLeaks` sees additions,
modifications (including a chmod), and removals; `env.Changes(base)` returns the
typed diff. A command that is *expected* to write (setup writes the Checkout Setup and
the Consent record) is held to `AssertNoLeaksOutside(base, roots…)`: anything outside
the fixture and the scratch HOME still fails, TMPDIR still has to be empty, and the
test asserts the exact added set separately.
`env.DockerOrphans()` tallies by object identity (`--name`, or the
container id the shim echoes), so creating one object and removing another still
counts as a leak, and `run --rm` is not one. The
base environment is deliberately minimal: `HOME`, `TMPDIR`, `PATH` (shim dir first,
then the go toolchain and the system dirs), `LANG`/`LC_ALL=C`, and
`IGDEV_NO_UPDATE_NOTIFIER=1`. XDG variables stay **unset** so runs exercise igdev's
real defaults; set one with `env.SetBaseEnv` when a test is specifically about it.

### Adding a golden

1. Write the test and call `env.Golden(t, "status_inside_project.json", res.Stdout)`.
2. `IGDEV_UPDATE_GOLDENS=1 go test ./itest/ -run TestYourCase` — the first run fails
   with "golden does not exist" and prints what it observed.
3. **Read the written file.** A golden is the contract; never accept one you have
   not reviewed. `git diff itest/testdata/golden` is the review surface.
4. `go test ./itest/ -run TestYourCase` to confirm it now passes.

Volatile strings collapse through `env.RegisterReplacement(value, "<TOKEN>")`
(already wired for the scratch root/HOME/TMP/shim/binary and the loopback release
URL); anything left unnormalized makes the suite flaky on the next machine.

### Running one test

```bash
go test ./itest/ -run 'TestStatusInsideProjectJSON' -v
go test ./itest/ -run 'TestUpdateNotice' -v          # table by prefix
go test ./itest/ -run 'TestCompletionScripts/bash'   # one subtest
go test ./internal/config/ -run TestResolvePrecedence
make test-quick                                      # everything except the slow gates
```

### Adding a command

Register it in `internal/cli/root.go` (`newRoot`), give it `Args: rejectArgs(name)`
or a validator that returns a `contract.Fault`, resolve config with `a.gate()`, and
finish with `a.emit(res, data, humanPrinter)` so dialect selection, envelope
rendering, and the update notice stay in one place. Exit levels come only from the
fault: never call `os.Exit` and never write to stdout from a helper.

A command that reads or mutates project state calls `gate.Require(a.gateInput(found))`
first and returns the fault unchanged; `init` and `setup` are the repair paths and do
not. The `gateway` verbs are the first enforcing consumers: `a.gatewayContext()`
passes the Gate and the Consent check once for every verb, and freezes its refusals in
`itest/gateway_test.go` (`gate_test.go` still pins the Gate's own envelopes).

### Adding a config key

Add a `config.Key` to `config.Schema` with its path, `IGDEV_*` name, kind, default,
and description. It is immediately resolvable from every tier, reported by
`igdev status --json`, and settable by tests through `testrig.EnvFor(path)`.
`internal/testrig` maps the same schema, so nothing has to be kept in sync by hand.

## Deliberate limits of tickets 01-04

Ticket 01: the walking skeleton — install, `status`, `version`, help, completion, the
five-tier resolver, the Gate's discovery stage, and the resource/hygiene gates.
Ticket 02: the Project Contract (`init` reads, validates, renders, and diffs
`igdev.toml`; the Gate checks schema and Setup Stamp).
Ticket 03: the Checkout Setup (`setup` mints the Instance identity, allocates the ports,
materializes the runtime from the embedded templates, records the machine-global
Consent, and writes the admin credential; `doctor` audits the host).
Ticket 04: the Gateway suite (`gateway up|down|reset|restart|wait|smoke|status|logs|url`
drives the Instance's compose project through the Gate, the Consent record, and the
Capacity Gate, and `gateway credentials --json` is the one readable home of the admin
password). Its non-hermetic half is `.github/workflows/igdev-gateway-e2e.yml`, which
boots a real Ignition image: dispatched or nightly, never a PR check.

No Baseline commands (ticket 11), no module commands and therefore no module-license or
module-certificate acceptance path (ticket 12), no Jython download (setup materializes
state only), no Wizard prompts (ticket 16), no AGENTS.md managed block, no catalogs, and
no pipeline verbs — `check|test|build|verify` — so the declared `[commands]` stages are
still only data. The repository is not yet dogfooding its own `igdev.toml`; that
arrives with the conversion ticket. Until then the root `README.md` documents the
legacy `devctl` foundation and `AGENTS.md` its command contract; this file is the
igdev reference.
