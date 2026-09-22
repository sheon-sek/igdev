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
| `internal/cli` | cobra tree, the Gate's discovery call, envelope/exit mapping |
| `internal/contract` | frozen envelope, `IGDEV_E_*` codes, exit levels, CLI Contract Version |
| `internal/config` | five-tier resolver (flags > `IGDEV_*` > `.igdev/local.toml` > `igdev.toml` > defaults) |
| `internal/project` | Project Root discovery (`igdev.toml` search, setup presence) |
| `internal/updater` | notice-only update check with a 24 h XDG cache |
| `internal/semver` | version ordering used by the notice |
| `internal/xdg` | `~/.cache/igdev`, `~/.config/igdev`, `~/.local/state/igdev` |
| `internal/testrig` | seam S1 harness: scratch HOME, PATH shims, loopback server, goldens, gates |
| `itest` | behaviour tests and the goldens that freeze the contract |
| `packaging` | `package.sh` (linux/darwin x amd64/arm64 tarballs + `checksums.txt`), `install.sh` |
| `.github/workflows/igdev-ci.yml` | build, vet, gofmt, goldens, resource gates |
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
error, `3` human action required. Codes are namespaced `IGDEV_E_*`; ticket 01
emits `IGDEV_E_USAGE`, `IGDEV_E_MISSING_ARGUMENT`, `IGDEV_E_CONFIG_INVALID`, and
`IGDEV_E_INTERNAL`. A typo in a `--config` flag is a usage error (the invocation
was wrong); a value igdev read from a tier is `IGDEV_E_CONFIG_INVALID` (machine or
project state is wrong). `contract` is the CLI Contract Version and bumps only when
the envelope, codes, or exit levels break — never alongside release semver.

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
`ok: true` with `initialized: false`; inside one it reports the discovered root,
the contract's declared schema version, whether a Checkout Setup record exists, and
every config key with the tier that won. Setup *stamp* validation is ticket 02.

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
(`internal/config`, `internal/contract`, `internal/semver`). Do not add a new seam:
if a behaviour cannot be observed from argv, exit level, stdout, stderr, the
filesystem, or the shim's call log, it is not yet a testable requirement.

### Rig entry points

```go
env := testrig.NewEnv(t)              // scratch root: HOME, TMPDIR, shim PATH; built binary
root := env.Project("repo", contract) // writes repo/igdev.toml, returns the absolute dir
env.LocalConfig(root, "…")            // .igdev/local.toml tier
env.SetupRecord(root, "…")            // .igdev/setup.json record
env.ShimDocker()                      // PATH shim; returns nothing, records to state/docker-calls.jsonl
res := env.RunIn(root, "status", "--json")
res := env.Run(testrig.Run{Args: …, Dir: …, Env: []string{"K=V"}, SampleRSS: true, Stdin: "…"})
env.MustRun("version")                // fails the test if the process could not launch
testrig.WantExit(t, res, contract.ExitUsage)
testrig.WantCode(t, testrig.Envelope(t, res.Stdout), contract.CodeUsage)
testrig.Status(t, res.Stdout)         // decoded status data, with ResolvedSource(key)
env.Golden(t, "name.json", res.Stdout)
res.AssertNoLeaks(t)                  // tree unchanged since launch, TMPDIR empty, no .tmp debris
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
typed diff. `env.DockerOrphans()` tallies by object identity (`--name`, or the
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

### Adding a config key

Add a `config.Key` to `config.Schema` with its path, `IGDEV_*` name, kind, default,
and description. It is immediately resolvable from every tier, reported by
`igdev status --json`, and settable by tests through `testrig.EnvFor(path)`.
`internal/testrig` maps the same schema, so nothing has to be kept in sync by hand.

## Deliberate limits of ticket 01

No `init`/`setup` (tickets 02, 03), no contract *validation* beyond reading the
declared schema version, no Setup Stamp check, no catalogs, no gateway control, no
prompts, no config writes, no Agent Skill. The repository is not yet dogfooding its
own `igdev.toml`; that arrives with the conversion in ticket 13. Until then the root
`README.md` documents the legacy `devctl` foundation and `AGENTS.md` its command
contract; this file is the igdev reference.
