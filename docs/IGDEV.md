# igdev — the Go toolchain

`igdev` is the standalone, globally installed Ignition development toolchain. This
repository is the CLI's home: the Go source lives under `cmd/` and `internal/`, and the
tree dogfoods the binary it builds — `igdev.toml` is its own Project Contract and
`.igdev/` its disposable Checkout Setup. The predecessor vendored bash foundation was
retired by the cutover ticket (issue #19) once the golden suite proved parity; nothing
in this tree carries that layout any more. Vocabulary lives in
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
make reference    # regenerate docs/reference/ from the command definitions
make reference-check  # CI's drift check: committed reference == generated reference
```

`go test ./...` is hermetic: it needs no network and no docker. The rig builds the
real binary once per test process, and the release tests run `packaging/package.sh`
and `packaging/install.sh` for real against a loopback file server.

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/igdev` | main; three lines that call `cli.Execute` |
| `cmd/igdev-docs` | writes the generated command reference (`docs/reference/`); `-check` is the drift check |
| `internal/cli` | cobra tree, the Gate's discovery call, envelope/exit mapping, `init`, `setup`, `doctor`, the `gateway` verbs, the `baseline` verbs, the `module` and `catalog` knowledge verbs, the pipeline verbs, and `ci-local`; `agentusage.go` holds the Agent usage section of every command |
| `internal/docsgen` | renders `docs/reference/` from the cobra definitions and the agent-context field table, and fails when the committed copy is not what they render |
| `internal/contract` | frozen envelope, `IGDEV_E_*` codes, exit levels, CLI Contract Version |
| `internal/config` | five-tier resolver (flags > `IGDEV_*` > `.igdev/local.toml` > `igdev.toml` > defaults) |
| `internal/baseline` | the staged Baseline: copy, streamed digest, provenance record, restore arguments, mount point |
| `internal/catalog` | the embedded Core Catalog keyed by Ignition version, the Project Overlay format, the Effective Catalog resolver, and the capability scanner |
| `internal/modules` | private `.modl` metadata (`module.xml`) under a zip-bomb guard, artifact staging/clearing, and the module whitelist semantics |
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
installed). The Baseline commands add `IGDEV_E_BASELINE_MISSING` (the `baseline set`
source path is not there) and `IGDEV_E_BASELINE_INVALID` (the source exists but cannot
be staged as a backup file); a source that is not a `.gwbk` is a usage error. The
knowledge layer (ticket 12) adds `IGDEV_E_UNKNOWN_CAPABILITY` (nothing in the
Effective Catalog owns the capability), `IGDEV_E_CAPABILITY_AMBIGUOUS` (one capability
matches rows that disagree about its owner), `IGDEV_E_MODULE_NOT_ENABLED` (a required
module is outside the whitelist), `IGDEV_E_MODULE_ARTIFACT_MISSING` (an enabled module
is neither built-in nor backed by a staged `.modl`), `IGDEV_E_OVERLAY_INVALID` (a
declared Project Overlay file cannot be read or does not follow the format),
`IGDEV_E_OVERLAY_CONFLICT` (an overlay row would shadow a row already in force) and
`IGDEV_E_CATALOG_VERSION_MISSING` (no Core Catalog for the resolved Ignition version).
The module write verbs (ticket 13) add `IGDEV_E_MODULE_UNKNOWN` (`module enable` was
handed an id that is neither built-in nor declared by a staged `.modl`, so enabling it
would whitelist a module nothing can load) and `IGDEV_E_MODULE_ARCHIVE_INVALID`
(`module add` was handed a file that is not a readable module archive: not a zip, no
`module.xml`, no usable id, or an archive the zip-bomb guard refuses).
`igdev ci-local` adds `IGDEV_E_ACT_MISSING` (no act on PATH: the remediation carries
the install commands and no subprocess is attempted) and `IGDEV_E_ACT_FAILED` (act
exited non-zero — the exit level is act's own exit code, and the envelope's `data`
carries the invocation and the tail of act's output).
A typo in
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
names (`setup.instance_id`, `setup.namespace`, `setup.ports`), the Gateway option the contract
states (`gateway.allow_unsigned_modules`), the machine Consent term
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
`[modules] enabled|artifacts|require_private_module_consent`, `[scan] jython|capabilities`,
`[catalog] overlay_paths`
(omitted when empty), `[commands] check|test|build|smoke`
(omitted when empty), `[gateway] memory_mb|timezone|smoke_endpoints|allow_unsigned_modules`.
A key igdev does not know is refused, and a version-like field holds a version. Fields
the file leaves out fall back to the embedded defaults, so a minimal `schema = 1`
contract is valid. Four keys are optional and omitted from the rendered contract while
they hold the schema default: `smoke_endpoints` (a checkout that does not state it smokes
the root document alone), `modules.artifacts` (a contract that does not state it has
`igdev build` stage nothing on its own), `gateway.allow_unsigned_modules` (default false:
the Gateway loads only signed module artifacts), and
`modules.require_private_module_consent` (default false: the private modules the checkout
staged are accepted as part of starting the Gateway, ADR 0006). Optional means additive — a
contract written before either key existed parses, renders, and behaves exactly as it
did, and stating an optional key is an edit of a tracked file, so it goes through
`igdev init` and makes the Checkout Setup stale until `igdev setup` re-materializes it.
`init` merges: a flag
overrides the value it names and everything else the file holds is preserved; an empty
value (`--modules ""`) clears a field.

`[catalog] overlay_paths` is additive to schema v1, so a contract written before this
key existed keeps parsing and behaves exactly as it did — the Effective Catalog is then
the Core Catalog alone. Adding it *is* an edit of a tracked file, so it goes through
`igdev init` (or `igdev setup` if the file was hand-edited): a contract whose bytes
moved makes the Checkout Setup stale, and the Gate says so until `igdev setup` runs.

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
  baseline/             the staged Baseline (.gwbk), mounted read-only at /restore
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
context is `.igdev/runtime/` itself, and staged modules and the Baseline directory are
mounted — so the repository contributes no Docker input of its own and never needs a
`.dockerignore`. The rendered Compose file is the same whether or not a Baseline is
staged: the restore argument reaches the Gateway through the process environment, not
through a rendered file.

**Credentials.** setup generates the Gateway admin password (24 characters from a
printable, quote-safe alphabet, `crypto/rand`) or takes `IGDEV_GATEWAY_ADMIN_PASSWORD`
(the `--admin-password` flag wins, then the environment, then what the checkout
already holds). It is written to `.igdev/local.toml` mode 0600 and never printed: not
in the JSON envelope, not on stdout, not on stderr. `igdev gateway credentials --json`
is the only way to read it back out — human `credentials` prints the username and the
source and says so. The rendered runtime files carry
no secret at all — the Compose environment only *references* the credential variables,
which the igdev process supplies at `gateway up` time.

**Gateway options.** One contract key reaches the Gateway through the rendered Compose
environment: `[gateway] allow_unsigned_modules` becomes `IGNITION_ALLOW_UNSIGNED_MODULES`,
which the rendered Compose file passes to the Gateway as
`-Dignition.allowunsignedmodules`. It is the switch a module repository that builds
unsigned artifacts needs — a `.modl` with no valid signature otherwise never loads. The
rendered value is `false` unless a contract asks for `true`, so the file a checkout
materializes without the key is byte-identical to the one it materialized before the key
existed.

**Consent.** The record is machine-global: `~/.config/igdev/accepted.toml`, keyed by
term (`ignition-eula`, `module-license`, `module-cert`) and holding when each was
accepted and by which CLI. Only a human-invoked command writes it — `igdev setup
--accept-eula` today — and a missing term stops the run with `IGDEV_E_CONSENT_REQUIRED`
at exit level 3, naming the exact command in Remediation. Consent is checked before
anything is written, so a refused setup leaves the tree untouched. Re-accepting on a
machine that already consented is a successful no-op: the first acceptance is the
legal fact, so its timestamp does not move.

**Private module acceptance.** The private modules a checkout stages are its own
artifacts — in a module repository the developer is their author — so starting a Gateway
accepts them without a human step: `gateway up`/`reset` hand the module id of every
staged artifact to the container as `ACCEPT_MODULE_LICENSES` and `ACCEPT_MODULE_CERTS`
(comma-separated, the way the image reads them), which is what lets a `.modl` that
declares a license or carries a self-signed certificate load. Built-in modules are never
named, because only the checkout's own staging directory is read. A contract that wants
the strict gate states `[modules] require_private_module_consent = true`: the
machine-global `module-license` and `module-cert` terms are then required before any id
is passed, and a checkout without them stops at exit level 3 naming
`igdev setup --accept-module-license` and `--accept-module-certificate`. `agent context`
reports the ids igdev would pass (`modules.auto_accepted`) and whether the opt-in is in
force, so an unattended run knows which behaviour it gets (ADR 0006).

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
credentials, the staged Baseline's restore arguments, and the accepted private module
ids supplied from the process environment — so a parallel worktree's Instance can never
be addressed by mistake, and no rendered file carries a secret or has to be rewritten
when the staged set changes.

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

## The Baseline

```
igdev baseline set <file.gwbk>      validate a Gateway backup and stage it here
igdev baseline status               report staged-or-empty, with the restore wiring
igdev baseline clear                remove the staged backup
```

A Baseline is the `.gwbk` a disposable Gateway is seeded from. `set` requires an existing
`.gwbk` (`IGDEV_E_BASELINE_MISSING` when the path is not there, `IGDEV_E_BASELINE_INVALID`
when it is a directory or cannot be read, a usage error when it is not a `.gwbk` at all),
streams the copy through one `sha256` into `.igdev/baseline/restore.gwbk`, and records
where it came from in `.igdev/baseline/baseline.json`. The copy is the Baseline: the
original is free to move or disappear afterwards. Staging a second time replaces the
staged file — checkout-local user state, so there is no tracked file to diff and nothing
to confirm.

`.igdev/baseline/` is deliberately not `.igdev/runtime/`, which setup owns and
re-renders: re-running setup — including the run that repairs a stale Setup Stamp after
a contract edit — keeps the Baseline staged. setup creates the directory as the mount
point and never touches what is inside it.

Ignition applies a restore only on a fresh Gateway launch, so the wiring is what makes
a Baseline matter to `igdev gateway reset`. Whenever `restore.gwbk` is staged, every
Gateway verb hands Compose `GATEWAY_RESTORE_ARGS=-r /restore/restore.gwbk`; the rendered
Compose file's launcher command interpolates `${GATEWAY_RESTORE_ARGS:-}`, and the
Baseline directory is mounted at `/restore` read-only. The value travels in the process
environment, the way the admin credentials do, so the rendered Compose file is
byte-identical whether or not a Baseline is staged, and neither staging nor clearing one
re-renders anything (a Compose service command has no CLI override, so the variable is
the only seam that keeps this state out of generated files). `clear` removes the staged
file and its record, so the next launch restores nothing.

`status` reports `{staged, path, bytes, source, sha256, staged_at, restore_args}`, or
`{staged: false}`. The staged file is the state and the record is only a note about it:
a record that is missing, unreadable, or no longer describes the file (a different size)
leaves the Baseline staged without provenance instead of failing. No Baseline verb
touches the container engine, and all three pass the Gate first — a Baseline needs a
materialized checkout — but not Consent: staging a backup is not running a Gateway.

## Capability knowledge

The knowledge layer answers one question before a Gateway starts: does this project
use something the environment cannot provide? Its data is the **Effective Catalog**:
the **Core Catalog** embedded in the binary plus this repository's tracked **Project
Overlay** (ADR 0005). Three planes, resolved together — the native function map, the
capability rules, and the REST catalog — and the `.modl` archive is never a capability
source: it only says what is installed.

The Core Catalog is embedded per Ignition version under
`internal/catalog/assets/<version>/` (`builtin-modules.tsv`,
`native-system-functions.tsv`, `capability-modules.tsv`, `rest-endpoints.tsv`), ported
from the predecessor toolchain's catalogs at the cutover. Nothing reads a repository file
at runtime. v0.1 carries exactly `8.3.8`; any other resolved version fails
closed with `IGDEV_E_CATALOG_VERSION_MISSING` rather than borrowing another version's
rows. Adding a version is adding data plus a digest, not changing code.

Integrity is a digest, never a row count: `catalog.CoreDigest` hashes the four plane
files in a frozen order (name, NUL, bytes, NUL) and a unit test pins the result, so any
byte change to the embedded data fails the suite. Row counts are reported by
`igdev catalog status` because a human wants to see the shape of the knowledge, never
asserted.

### The Project Overlay format

A Project Overlay is a project-owned, tracked TSV whose rows have the same column
shapes as the Core Catalog planes. A comment line switches the active plane:

```tsv
# igdev Project Overlay for this repository.
# plane: rest
GET	/data/acme/api/v1/widgets	module	com.acme.widgets
# plane: native-function
system.acme.widget.ping	module	com.acme.widgets
# plane: capability-rule
prefix	acme.widget.	com.acme.widgets	Acme widget scripting helpers
```

- `# plane: rest` — `method`, `path_template`, `owner_kind`
  (`platform`/`module`/`private-module`), `required_module_ids` (comma-separated, `-`
  for none). A `{param}` template segment matches exactly one concrete segment.
- `# plane: native-function` (`native-function`) — `function`, `classification`
  (`platform`/`module`/`conditional`), `required_module_ids`, `notes`.
- `# plane: capability-rule` (also `alias`, `rule`) — `kind` (`exact`/`prefix`),
  `pattern`, `required_module_ids`, `description`.

Blank lines and `#` comments are ignored anywhere. A row before the first directive, an
unknown plane name, the wrong column count, or an invalid value is
`IGDEV_E_OVERLAY_INVALID` naming the file and line. Files are read in declaration
order, and the overlay digest hashes them in that order, so the digest covers exactly
what was resolved.

The overlay is consulted core-first: it may **add** a function, a rule, or an endpoint
the core does not carry — which is how a private module's API becomes preflight-visible
without a new igdev release — but a row whose key the core (or an earlier overlay row)
already holds is `IGDEV_E_OVERLAY_CONFLICT`, naming both rows. A silently redefined core
row is not reviewable, so it is refused instead.

Overlay discovery is `[catalog] overlay_paths` in the Project Contract: repository-
relative paths, validated at parse time (no absolute path, no `..`). A declared file
that does not exist is `IGDEV_E_OVERLAY_INVALID` too — the contract would be lying.

An overlay file may hold both kinds of knowledge: the REST plane a Gateway's OpenAPI
document yields goes into a block the file marks as generated
(`# igdev catalog import-openapi: …`), and that block is the only part an import
rewrites. The native-function and capability-rule planes are hand-authored, because no
OpenAPI document knows about them.

### The knowledge verbs

`igdev catalog status` reports both layers: `core` (embedded, with its version) and
`overlay` (project, with its declared files), each with its `digest` and its row
`counts`, plus the `effective` sum. It works outside a Project Root, where the answer
is the Core Catalog alone; that is also how a caller asks which versions this binary
carries. The resolved Ignition version comes from the config precedence chain, so
`--config ignition.version=8.1.21` is a clean `IGDEV_E_CATALOG_VERSION_MISSING`.

`igdev module list` reports three things: every module that ships in the image with
whether the whitelist selects it, every `.modl` staged in `.igdev/modules/` with its
`module.xml` metadata, and the `[modules].enabled` whitelist. An artifact whose
`module.xml` cannot be read is still listed as `UNREADABLE` (it is in the directory),
and a whitelisted module no artifact declares is `MISSING-ARTIFACT`. A staged artifact
the whitelist does not name reads as `enabled (staged)`: staging is what enables it (see
`EnabledList`), so the id never has to enter the contract. `--built-in` and
`--private` select one group; the human dialect keeps the legacy group headings, and
`--json` omits the group a flag did not select. An empty whitelist is not "nothing
enabled": it is the Gateway image's own semantics for `GATEWAY_MODULES_ENABLED` —
every module loads — which is why the JSON carries `enabled_all`.

`igdev module require <capability>...` resolves each capability and checks the modules
it requires. A capability is a `system.*` function, a REST request (`METHOD /data/path`
with the method optional and case-insensitive, or a bare `/data/path` with the query
string ignored and a trailing slash tolerated), or an explicit `module:<id>` / `com.*`
id. Every argument is checked, so one run reports every problem: an unknown capability
is `IGDEV_E_UNKNOWN_CAPABILITY`; a required module outside the whitelist is
`IGDEV_E_MODULE_NOT_ENABLED` with `igdev module enable <ids>` in Remediation; an enabled
module that is neither built-in nor staged is
`IGDEV_E_MODULE_ARTIFACT_MISSING` with the `module add` command in Remediation.

`igdev module scan [path]...` reads project files (directories are walked for `.py`,
`.json`, `.js`, `.ts`, `.tsx`, `.java`, `.kt`, `.sh`; an explicit file is read whatever
its extension) and finds every `system.*` reference — nested namespaces included, so
`system.historian.types.dataPoint` is found — and every REST path. A method reference
supersedes the bare path it contains, exactly as the legacy scanner did. Findings carry
`file` and `line`, and every distinct capability is checked, so a failure message names
the offending location. Without arguments the contract's `[scan].capabilities` paths
are used. A path that does not exist is a warning, not a failure.

The knowledge verbs run the Gate's contract stages but not the Setup Stamp: what a
capability requires depends on the Project Contract — its whitelist and its overlay
declarations — not on whether the checkout has been materialized, so
`igdev module require` answers before the first `igdev setup`. They do need a Project
Root: outside one the answer is `IGDEV_E_NOT_INITIALIZED`.

### Importing a Gateway OpenAPI document

`igdev catalog import-openapi <openapi.json>` ports the retired bash generator: it reads
a Gateway `/openapi.json` snapshot and
writes the REST plane of this repository's tracked Project Overlay, one row per
operation, mapping the path template to its owner kind and required modules with the
generator's own rules — the route-prefix table, the `/data/api/v1/resources/<module>`
aliases (`mcp` → `private-module`, `sip-notification` →
`com.inductiveautomation.phone-notification`, `opcua.drivers.bacnet` → bacnet *and*
opcua), the `com.*` module segment, and the platform default. A `{param}` segment is
preserved as a template, so the imported row resolves the concrete requests the
generator's rows resolve.

A row the Core Catalog already carries with the same owner is not copied into the
overlay — a duplicate would be refused as a conflict on the next read — so importing
the full document of an Ignition version this binary already knows adds nothing, and
the overlay grows by what the core does not know. A row that would shadow a core row
with a *different* owner is `IGDEV_E_OVERLAY_CONFLICT` and nothing is written, exactly
as a hand-authored row would be: an overlay may add knowledge and may not redefine it.
A hand-authored REST row in the same file that shadows the core is caught the same way,
by validating the file before it is written.

The file written is `--output`, or the first `[catalog].overlay_paths` entry, or
`catalog/rest-overlay.tsv`; it has to be repository-relative and inside the Project
Root, and a file the contract does not declare is warned about on stderr, because an
overlay nothing declares is never resolved. The Ignition version is
`--ignition-version` or the resolved `ignition.version`; a version this binary carries
no Core Catalog for fails closed with `IGDEV_E_CATALOG_VERSION_MISSING` — there would be
nothing to check the rows against — and a value that is not a version is a usage error.
A document that cannot be read, is not valid JSON, has no object-valued `paths`, or
declares no operation is `IGDEV_E_USAGE` naming the file; the empty document is refused
in particular because importing it with `--prune` would empty a tracked overlay.

The write is atomic and prints the unified diff, like every other tracked write.
Re-importing an unchanged document reports `unchanged` and writes nothing at all; a
changed document updates the overlay and the `# source_sha256=` digest its header
records. Rows a previous import wrote that the document no longer declares are kept — a
document that stopped listing an endpoint is not proof the project stopped using it —
and `--prune` is the opt-in that drops them. A row the Core Catalog has come to carry
itself is dropped either way, with or without `--prune`: keeping it would rebuild the
conflict an overlay may not have. The `data` member reports `operations`,
`written`, `added`, `preserved`, `removed`, and `redundant`, so an import says exactly
what it changed. After an import, `module require METHOD /path` resolves the endpoint
with `layer: "overlay"`, and `catalog status` reports the overlay layer's digest.

### Writing modules

`igdev module enable <id>...` is a contract write, and it follows the same rule as
`igdev init`: the Project Contract is rewritten atomically (never in place, never with
a `.bak`) and the unified diff is printed, so a tracked change stays reviewable. The
JSON reports the file, the action, the Contract Digest after the write, and the diff;
the whitelist, the ids added, and the ids that were already enabled.

An id has to be one this environment can load — a built-in module from the Core
Catalog, a solution-suite selector, or an id a staged `.modl` declares. One unknown id
refuses the whole run with `IGDEV_E_MODULE_UNKNOWN`, names the closest built-in ids,
and stages nothing. Enabling what the whitelist already names changes nothing and
writes nothing, so re-running is safe; an id a staged artifact declares is reported as
already enabled for the same reason — it is loaded by being staged, and its id is local
build output that does not belong in the tracked contract. When the whitelist is *empty*
every module already loads, so `enable` reports that state instead of writing a list
that would restrict the Gateway; an explicit whitelist comes from `igdev init --modules`.

Because the write moves the Contract Digest, the Checkout Setup becomes stale: the Gate
refuses the next project command with `IGDEV_E_SETUP_STALE` until `igdev setup`
re-materializes the staging the Gateway mounts. That is the same rule a hand-edit of the
contract follows.

`igdev module add <file.modl>` validates the archive, reports the metadata it declares
(id, name, version), copies the file into `.igdev/modules/` atomically, and re-renders
the runtime so the next `igdev gateway up` mounts it. The archive is read under a
zip-bomb guard — 64 MiB of declared expansion, 1 MiB of `module.xml`, and no entry
stored at more than a 200:1 ratio — so a bomb is refused before anything is
decompressed; a file that is not a readable zip carrying a `module.xml` with a usable
id is `IGDEV_E_MODULE_ARCHIVE_INVALID` with the reason. The artifact is metadata-only:
id, name, and version is everything igdev reads out of a `.modl`, and the archive is
never a source of capability knowledge (ADR 0005). Re-adding a file name already staged
replaces it, which is how a newer build of one module is staged. There is no
"also enable it" step and no `--enable` flag: a staged private module is enabled by
being staged (see below), so that write could only ever put a local artifact's id into
the tracked contract.

Staging is not a contract change, so the Setup Stamp stays current: what moved is what
is staged, not what the contract asked for. Staging a private module is also what
enables it: the rendered `GATEWAY_MODULES_ENABLED` is the whitelist plus every staged
private id (`modules.EnabledList`), so a whitelist that does not name the module still
loads it, and `module list` reports it as `enabled (staged)`. A staged private module is
a first-class id, so `module require module:<id>` accepts it once the artifact is there.
`igdev module clear` deletes every staged `.modl` and re-renders the runtime; the
whitelist survives, so a whitelisted module whose artifact was cleared reads as
`MISSING-ARTIFACT` — the state that makes a Gateway refuse it.

Both write verbs pass the full Gate, so they need a current Checkout Setup, and both
report what the re-materialization did to each runtime file.

A repository whose own build produces the artifacts declares that instead of staging by
hand: `[modules].artifacts` holds repository-relative globs, and after a successful
build stage `igdev build` resolves each one against the Project Root and stages every
match exactly as `module add` would — id, name, and version read the same way, the copy
atomic, and no contract change. Staging an artifact also removes any other staged file
that declares the same module id, so a version bump (a new file name for the same
module) replaces the previous build instead of leaving two builds of one module mounted
beside each other. A glob that matches nothing fails the build with
`IGDEV_E_MODULE_ARTIFACT_MISSING`: a contract that declares what its build produces and
finds none of it has nothing to stage, and silence would look like success. Artifacts
whose module id the build no longer produces are left alone — they are still cleared
with `module clear`.

`igdev module cache-path` prints the machine-wide module cache directory for the
resolved Ignition version (`<XDG cache>/igdev/modules/<version>`), as a bare path in the
human dialect so a script can use it. It is a property of the machine and the version,
not of the checkout, so it works outside a Project Root; an absent directory is reported
(`exists: false`), never failed on, because the cache is disposable. Opting a module
into that cache is `module add --global`, deferred.

```json
{
  "ok": true,
  "contract": "1",
  "code": "",
  "message": "ok",
  "remediation": [],
  "data": {
    "contract": {
      "path": "/repo/igdev.toml",
      "action": "updated",
      "digest": "sha256:...",
      "diff": "--- a/igdev.toml\n+++ b/igdev.toml\n@@ -9,7 +9,7 @@\n [modules]\n-enabled = [\"com.inductiveautomation.perspective\"]\n+enabled = [\"com.inductiveautomation.perspective\", \"com.inductiveautomation.opcua\"]\n"
    },
    "whitelist": ["com.inductiveautomation.perspective", "com.inductiveautomation.opcua"],
    "added": ["com.inductiveautomation.opcua"],
    "already_enabled": [],
    "unrestricted": false,
    "setup_stale": true
  }
}
```

Human diagnostics keep the legacy `[module-preflight]` vocabulary — `OK`, `NOTE` for a
conditional function's note — and the legacy closing line of `module scan`. Failures
are contract faults, so their wording lives in `message` and their fix in
`remediation`, which is what makes them machine-readable.

## The check pipeline

`igdev check` is the fast, fixed gate: four stages in a frozen order, each one
stopping the run when it fails.

1. **module-validate** — the `[modules].enabled` whitelist against what this
   checkout can load: a built-in module, a solution-suite selector, or an id a
   staged `.modl` declares. A whitelisted id nothing can load is
   `IGDEV_E_MODULE_ARTIFACT_MISSING`; the OPC UA driver rule is ported with it (an
   enabled `com.inductiveautomation.opcua.drivers.*` needs
   `com.inductiveautomation.opcua`). An empty whitelist means every module loads, so
   there is nothing to validate.
2. **module-scan** — the capability references under `[scan].capabilities`, resolved
   against the Effective Catalog. It is the same finding set `igdev module scan`
   reports, because both answer from `scanCapabilities`.
3. **declared-check** — `[commands].check`, run with `bash` at the Project Root.
   Undeclared means skipped, never failed; a stage that exits non-zero propagates its
   own exit code as the process exit level.
4. **jython-check** — every `.py` file under `[scan].jython`, compiled by one JVM.

Contract-declared `[scan]` paths are repository-relative: the pipeline resolves them
against the Project Root, so a run from a subdirectory scans the same files.

`--json` reports one entry per stage in `data.stages` (`stage`, `status`, and the
stage's own detail), the stage that stopped the run in `data.failed`, and — for
`verify --gateway` — the running Gateway's URL in `data.gateway_url`. A failure
carries the same stage list in `data`, so an agent reads how far the run got instead
of guessing from the message.

Every pipeline verb passes the Gate's Setup Stamp stage, because validating the
enabled modules means comparing them against what this checkout actually stages.

```json
{
  "ok": false,
  "contract": "1",
  "code": "IGDEV_E_JYTHON_SYNTAX",
  "message": "/repo/src/bad.py:3: unmatched ')'",
  "remediation": [],
  "data": {
    "stages": [
      { "stage": "module-validate", "status": "passed", "message": "whitelist is empty: every module loads" },
      { "stage": "module-scan", "status": "passed", "paths": ["/repo/src"] },
      { "stage": "declared-check", "status": "skipped", "message": "no [commands].check declared" },
      { "stage": "jython-check", "status": "failed", "paths": ["/repo/src"], "jar": "/home/u/.cache/igdev/jython/2.7.4/jython-standalone-2.7.4.jar",
        "file_count": 2, "diagnostics": [{ "file": "/repo/src/bad.py", "line": 3, "message": "unmatched ')'" }] }
    ],
    "failed": "jython-check"
  }
}
```

### The Jython checker and its cache

`igdev jython check <path>...` is the last stage on its own: a directory is walked
for `.py` files, a file argument is compiled as given, and **one** JVM compiles all
of them, however many there are — one launch, not one per file. The driver is a small
script embedded in the binary and handed to the interpreter with `-c`; it compiles
each file, prints `path:line: message` for every file it rejected, and exits
non-zero. All files are checked before the exit level is decided, so one run reports
every problem. Failures are `IGDEV_E_JYTHON_SYNTAX`, a path that does not exist is
`IGDEV_E_JYTHON_PATH_MISSING`, and a machine without a JVM is
`IGDEV_E_JAVA_MISSING`.

The standalone jar is a downloaded artifact pinned by sha256 in the embedded
version-keyed table (`jython.Pins`; 2.7.4 is the Ignition 8.3.8 target). The cache
entry is `<XDG cache>/igdev/jython/<version>/jython-standalone-<version>.jar`, and
every access follows the frozen cache policy:

- a per-entry `flock` (`jython.lock`) is taken first, so N parallel igdev processes on
  one machine fetch the artifact exactly once: the first downloads, the rest verify
  what it stored;
- the artifact is downloaded to a temp file *inside the cache directory* and renamed
  into place only after it verified, so no partial file is ever visible under the
  final name and nothing round-trips through `$TMPDIR`;
- a cached entry is re-hashed on every use; bytes that do not match the pin are moved
  aside as `jython-standalone-<version>.jar.corrupt-<timestamp>` and refetched;
- a download that does not match the pin is quarantined the same way and retried
  once; a second mismatch is `IGDEV_E_CHECKSUM_MISMATCH`, and no JVM ever sees the
  bytes.

Deleting the whole cache directory is safe: it costs a re-download and nothing else.
Two config keys exist for CI. `jython.maven_base_url` points the fetch at a loopback
stand-in, and `jython.sha256` replaces the expected digest of a pinned version. The
override cannot make an unpinned version supported — the table still decides which
versions igdev speaks — so an unknown `[ignition].jython_version` fails closed with
`IGDEV_E_JYTHON_VERSION_UNSUPPORTED`.

### test, build, and verify

`igdev test` dispatches `[commands].test` at the Project Root. `igdev build`
dispatches `[commands].build` and then re-materializes the module staging the Gateway
mounts — the same re-render `module add` performs, reported as a `module-restage`
stage. Both skip an undeclared stage with a note rather than failing it, and both
propagate a declared stage's exit code (`IGDEV_E_COMMAND_FAILED`) as their own exit
level, stopping before anything that follows.

`igdev verify` is check, then test, then build, each stopping the run. `--gateway`
adds the runtime half — the Capacity Gate, `up`, `wait` (240 s), and `smoke` — and
leaves the Gateway running so the URL it reports can be opened by hand; stop it with
`igdev gateway down`. The Gateway half needs recorded Consent like every gateway
verb, so on a machine that has not accepted the EULA it fails with
`IGDEV_E_CONSENT_REQUIRED` at exit 3.

## Running the project's CI locally

```
igdev ci-local --job <name> [--event <event>] [--offline] [--json] [-- <act args>...]
```

`igdev ci-local` is the port of the bash specification's `cmd_ci_local`: it runs one job
of this project's GitHub Actions workflows on the local machine through
[act](https://github.com/nektos/act). act is invoked as a subprocess from the Project
Root — `act <event> -j <job>` — and its exit code is this command's exit level, so a
workflow that fails fails the run the same way it fails CI (`IGDEV_E_ACT_FAILED`). act's
own output streams through: stdout and stderr stay apart for a human, and in machine
mode both land on stderr, because stdout is the envelope.

`--event` defaults to `pull_request`. `--offline` hands act `--pull=false
--action-offline-mode`; it is a resolved setting like any other, so `IGDEV_ACT_OFFLINE=1`
and the checkout-local `.igdev/local.toml` tier have the same effect, and the flag wins
when the tiers disagree. The tracked Project Contract cannot carry it: its schema has no
`[act]` section, so `act.offline` is deliberately machine-local (as ports are, ADR 0003).
Arguments after `--` reach act verbatim and in order, so an act flag igdev does not
model (`--reuse`, `--container-architecture`) still gets through.

The verb passes the Gate (a current Checkout Setup) but not Consent: no Gateway starts
and no license is accepted. A host without act fails with `IGDEV_E_ACT_MISSING` — the
remediation names `pipx install act` and `brew install act` — and nothing is spawned.
`act` is an optional `igdev doctor` prerequisite for the same reason.

In machine mode `data` carries `event`, `job`, `offline`, `command`, `args`, `workdir`,
`exit`, and `output_tail` (the last 50 lines act printed). The golden tests drive the
whole verb against a PATH-shimmed act that records argv, working directory, and the
environment act inherited, so no docker, no network, and no real workflow is involved.

## Host prerequisites

`igdev doctor` audits the host read-only and never fails: the exit level stays 0 and
the audit is the payload, so an agent reads the report rather than an error string.
Each prerequisite is reported with the exact probe, whether it is required, and either
the version line the tool printed or the reason there is none (`missing` when it is not
on PATH, `failed` when it is but printed no version). docker and its Compose plugin
(the Gateway) and a JVM (the Jython check) are required; Gradle is optional because a
project only needs it when its contract declares a Gradle command, and act is optional
because only `igdev ci-local` needs it. `data.ready` is false when a required
prerequisite is not present.

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
still a leak, while `run --rm` is not one), a batched-Jython timing gate (`igdev check`
over a 500-file tree, artifact already cached and the JVM shimmed, completes well under
three seconds — the JVM's own cost is deferred to the real-Docker e2e tier), and the
same rules for TMPDIR. Measured here on an i7-13700KF: P95 2 ms, peak RSS
6.4 MiB, binary 7.2 MiB.

## Working in this tree

### The one seam

S1 is the compiled binary's process boundary. Behaviour tests live in `itest/` and
run the real `igdev` through `internal/testrig`; they never import `internal/cli`
and call it in-process. The only in-process unit tests are for pure functions
(`internal/config`, `internal/contract`, `internal/project`, `internal/gate`,
`internal/semver`, `internal/textdiff`, `internal/instance`, `internal/ports`,
`internal/consent`, `internal/localconfig`, `internal/runtimeassets`,
`internal/doctor`, `internal/catalog`, `internal/modules`, `internal/jython`). Do not add a new seam: if a behaviour cannot
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
env.DockerCalls(t)                    // each recorded call: argv, cwd, and the GATEWAY_* env the engine received
call.GatewayEnv("GATEWAY_RESTORE_ARGS")  // the Baseline restore wiring, as the engine received it
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
env.ShimJava()                        // PATH shim for the JVM; records to state/java-calls.jsonl
env.JavaCalls(t)                      // each launch: call.Argv, call.Jar(), call.Files()
download := testrig.ServeDownloadDir(t, dir)  // counting loopback Maven stand-in; download.Hits()
env.PointAtDownload(download)         // points jython.maven_base_url at that server
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

Also give it a `Long` description, at least one `Example`, and an entry in
`internal/cli/agentusage.go`: the Agent usage section is the machine-facing half of the
reference (the `--json` data shape and the remediation flows), and
`TestEveryCommandHasAgentUsage` fails without one. Then `make reference` and commit the
result.

A command that reads or mutates project state calls `gate.Require(a.gateInput(found))`
first and returns the fault unchanged; `init` and `setup` are the repair paths and do
not. The `gateway` verbs are the first enforcing consumers: `a.gatewayContext()`
passes the Gate and the Consent check once for every verb, and freezes its refusals in
`itest/gateway_test.go` (`gate_test.go` still pins the Gate's own envelopes).

### Regenerating the reference

`docs/reference/` is generated, never hand-edited: `cmd/igdev-docs` renders one page per
command (plus the index and the `agent context` field table) from the cobra definitions
and the agent-usage table, so the reference, `--help`, and the goldens are three views of
one definition. `internal/docsgen`'s drift test compares the committed pages against a
fresh render on every `go test ./...`, and the `igdev-ci` "Reference docs up to date"
step runs `go run ./cmd/igdev-docs -check`; both fail when a command definition changed
without `make reference`. Because the reference is generated from the definitions, a
command's help is the only thing to write — there is no hand-maintained command table
anywhere.

### Adding an Ignition version to the Core Catalog

Copy the four plane files of the new version under
`internal/catalog/assets/<version>/`, add a `CoreDigest` constant to
`internal/catalog/catalog_test.go`, and add the version to the matrix the release
pipeline exercises. No code changes: `catalog.Core` reads whatever the embedded
directory tree carries, and a version that is not there fails closed. Row counts are
never asserted — the digest is the integrity test, exactly as ADR 0005 requires.

### Adding a config key

Add a `config.Key` to `config.Schema` with its path, `IGDEV_*` name, kind, default,
and description. It is immediately resolvable from every tier, reported by
`igdev status --json`, and settable by tests through `testrig.EnvFor(path)`.
`internal/testrig` maps the same schema, so nothing has to be kept in sync by hand.

## Deliberate limits of tickets 01-05, 12, 13, 14, 15, 16, 17, 18, and 20

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
Ticket 05: the Baseline (`baseline set|status|clear` stages a `.gwbk` into the Checkout
Setup and wires the restore argument into every Gateway launch, so `gateway reset`
seeds the Gateway from it).

Ticket 12: the knowledge layer (the embedded Core Catalog, the Project Overlay, the
Effective Catalog, and `module list|require|scan`, plus `catalog status`).
Ticket 13: the module write verbs (`module enable|add|clear|cache-path` — the contract
whitelist write, the `.modl` staging the Gateway mounts, the machine cache path, and
the staged-artifact report in `status`).
Ticket 14: the check pipeline (`check|test|build|verify`, the module-validate and
module-scan stages, the declared-stage dispatch, and `verify --gateway`) and the Jython
layer (the pinned, lock-guarded standalone-artifact cache and the one-JVM batched
compile behind `igdev jython check`). The 500-file timing gate lives in
real-docker half is deferred to the e2e tier, exactly as the
real-docker half is.
Ticket 15: the overlay generator (`catalog import-openapi` — the port of the retired
bash generator: the generator's mapping rules over a Gateway
`/openapi.json` snapshot, writing the REST plane of the tracked overlay inside the
block the file marks as generated, idempotent re-import with `--prune` for the rows a
document no longer declares, and fail-closed conflicts against the Core Catalog). The
native-function and capability-rule planes stay hand-authored: `catalog add-function`
and `catalog sync --gateway` are still deferred.

Ticket 20: running the project's CI locally (`ci-local` invokes act as a subprocess from
the Project Root, with the event, the job, the offline mode, and verbatim passthrough).
`act` itself is never bundled and no real workflow runs in the hermetic suite: the tests
shim act on PATH, so a run that pulls images or downloads actions is the user's own.
The verb is not part of the check pipeline and has no e2e tier of its own.

`module validate` as its own verb is not part of ticket 14: what it used to mean is the
pipeline's first stage (`module-validate`), and the predecessor's REST-catalog structural
validation is covered instead by the embedded Core Catalog's digest tests. The
module-license and module-certificate Consent terms are demanded only where a contract
opts in with `[modules] require_private_module_consent = true`; by default igdev accepts
the private modules a checkout staged itself when it starts a Gateway (ADR 0006), and the
module surface is otherwise read-only.

Ticket 19 is the cutover, and the "not yet dogfooding" state above is gone: this
repository carries its own Project Contract and runs on the installed binary. `igdev.toml`
declares Ignition `8.3.8`, Jython `2.7.4`, the built-in modules the image ships, the
Makefile targets behind `[commands]` check/test/build, 2048 MiB of Gateway heap, and no
`[scan]` paths, because after the retirement the tree holds no Jython sources (`igdev
check` reports the scan and Jython stages as skipped). The whitelist stops short of the
predecessor's fifth module, `com.inductiveautomation.mcp`: that one is a private artifact
the image does not ship, and an enabled module nothing can load is
`IGDEV_E_MODULE_ARTIFACT_MISSING` by design. A private `.modl` is staged per checkout
with `igdev module add`, never named by a committed contract, so a fresh clone's
`igdev check` is green and `igdev init` is a no-op against the committed bytes.
`AGENTS.md` was rewritten for the igdev world — safe commands, escalation, human-only
Consent, version changes through `igdev.toml` plus `igdev setup`, the private-module
rule, and the frozen golden/reference contract — while keeping the managed block
byte-identical so `init` stays idempotent. The vendored foundation (its dispatcher,
scripts, catalogs, Compose assets, hooks, `.env` template, its two workflows, its
architecture and troubleshooting notes, and the README appendix) was deleted in the same
change with no dead references left. The PR gate `igdev-ci` gained a self-dogfood job
next to the Go suite: package, install the tarball into a temp prefix, then
`init` → `setup --accept-eula` → `status --json` → `check` → `doctor` in a scratch clone;
`igdev-gateway-e2e` stays the real-Docker tier. The GitHub repository rename to `igdev`
is still pending — the Go module path and the install docs already anticipate it.

Ticket 16 adds the Wizards: `init` (7 steps), `setup` (7 steps), and `module add` (3
steps — staging is what enables a private module, so there is no whitelist question). A
Wizard prompts only when a required value is missing, stdin is a terminal,
and neither `--json` nor `--yes` was asked for; `--interactive` forces it, `--yes`
takes the same defaults silently, and `--json` never prompts whatever the terminal is.
Every Wizard answer becomes the flag value the silent path would have been given, so
`init --yes`, `init` with those flags, and the Wizard's accepted defaults write
byte-identical files. Setup's Consent step shows `igdev setup --accept-eula` and stops
at exit level 3 until the machine record itself says the term is accepted; a prompt
answer is never an acceptance. The Wizards are the only verbs that touch a terminal,
and the tests drive them through a pseudo-terminal.

Ticket 17 adds the agent surface. `agent context --json` returns one call's orientation
— lifecycle (initialized, the Setup Stamp state, and the machine Consent terms),
versions (CLI, CLI Contract, Ignition, Jython), the recorded Instance and its ports, the
Gateway's running state and URL, the staged modules, the Core Catalog and Project
Overlay digests, the Effective Catalog's row counts, and which project verbs are
available — as a frozen envelope that works before `init` and `setup`. Its field list is
frozen by `itest/testdata/golden/agent_context_*.json` and documented field by field in
the generated `docs/reference/agent-context.md`, which is rendered from the
`AgentContextFields` table in `internal/cli/agent.go` (a test checks that table against
the envelope's own struct tags). `agent skill-install` writes the embedded Agent Skill
(`internal/agentskill/SKILL.md`,
frontmatter carrying the CLI Contract Version) globally at `~/.agents/skills/igdev/` by
default, or into the repository's `.agents/skills/igdev/` with `--scope repo`; it is
idempotent and updates an install left by an older binary in place. `init` now also
maintains a minimal, version-free managed block in `AGENTS.md`: created when absent,
replaced in place when present, never duplicated, and reported with the same diff as its
other tracked writes. Neither agent verb ever prompts.

Ticket 18 is the documentation pass. Every command's help carries its description, its
options, at least one example, and an Agent usage section stating the `--json` data shape
and the remediation flows; the sections live in `internal/cli/agentusage.go`, so `--help`
and the generated reference render the same text, and a test fails when a command has no
entry. `cmd/igdev-docs` renders `docs/reference/` — one page per command, an index, and
the field table for `agent context --json` — from those definitions; `internal/docsgen`'s
drift test and the `igdev-ci` step both fail when the committed reference is not what the
definitions render, so CLI behaviour and docs cannot drift. The root `README.md` became
install + lifecycle + agent-workflow narrative with no hand-maintained command tables,
and `README.zh-CN.md` shrank to an onboarding guide that carries none either (the public
reference is English-only by decision). With the cutover, the README's appendix and the
two per-concern notes it pointed at are gone: troubleshooting starts from the generated
reference plus `igdev gateway logs`/`igdev gateway status`, and the architecture notes
are this file. Deferred: a rendered docs site (v0.1 publishes Markdown), and any
command-table content in the zh-CN guide.
