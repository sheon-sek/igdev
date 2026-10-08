package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// agentUsage is the machine-facing "Agent usage" section of a command's help,
// keyed by command path. Every command that prints help has an entry: the
// section is what an agent reads instead of prose, so it states the --json data
// shape and the remediation flows, never the human story.
//
// It lives outside each command's Long for two reasons. The section has a fixed
// meaning, so it is worth being able to audit and render as a unit; and both
// consumers — the binary's help text and the generated reference under
// docs/reference/ — render it from this one table, so the two can never
// disagree. TestEveryCommandHasAgentUsage fails on a command with no entry, on
// an entry no command claims, and on help text that does not render it.
var agentUsage = map[string]string{
	"igdev": `pass --json for the machine contract. stdout is then a single JSON envelope
{ok, contract, code, message, remediation, data}; progress and notices go to stderr.
Exit levels are 0 success, 1 command failure, 2 usage error, 3 human action required:
exit 3 means a person must accept a legal term (Consent) and Remediation names the
exact command. A missing or stale Checkout Setup is refreshed automatically. With every argument supplied igdev runs in Silent Mode and never prompts.
Orient before acting: igdev status --json works everywhere, and igdev agent context
--json answers the whole orientation in one call.`,
	"igdev init": `pass --json for the machine contract. data carries one block per tracked
write — contract, gitignore, and agents — each with path, action (created, updated,
unchanged), the Contract Digest afterwards, and the unified diff, so a tracked change is
reviewed from the envelope instead of the terminal. init is the repair path for a
contract igdev cannot read (IGDEV_E_CONTRACT_SCHEMA_UNSUPPORTED) or that needs new
values: run it with the flags to write, then igdev setup. --modules-artifacts declares
the globs igdev build stages, so a module repository does not chain igdev module add
inside its own build. It never prompts when --json or --yes is passed, so a fully
specified invocation is the whole interface.`,
	"igdev setup": `pass --json for the machine contract. data carries instance_id, namespace,
ports, setup_path, files (each rendered runtime path with kind, action, and mode),
credentials (path, source, username — never the password), and consent_accepted. setup
materializes whether or not the EULA is accepted, and every project command runs it
on its own when the Checkout Setup is missing or stale, so calling it by hand is only
needed after a manual change under .igdev/. IGDEV_E_CONSENT_REQUIRED at exit 3 comes
from the verbs that start a Gateway: a person runs igdev setup --accept-eula, or sets
IGDEV_ACCEPT_EULA=Y in the environment's configuration, then the automated run repeats.
Read the admin password through igdev gateway credentials.`,
	"igdev status": `pass --json for the machine contract. data is the whole report:
initialized, project_root, working_dir, contract (path, present, schema_version,
schema_supported, digest), setup (present, path, stamp_state, instance_id, namespace,
ports), gateway (allow_unsigned_modules — the effective IGNITION_ALLOW_UNSIGNED_MODULES),
modules, consent (per-term acceptance), and config (tier_files plus every resolved key
with the tier that won). It succeeds before init and setup and outside a Project Root,
so it is always the safe first call; branch on the blocks, never on the exit level.`,
	"igdev consent": `consent carries a person's Consent record to an unattended runner. It never
records consent: only a person runs igdev setup --accept-eula. Use igdev consent export.`,
	"igdev consent export": `pass --json for the machine contract. data carries source, output,
terms (the accepted term ids exported), and record (the file's content). Agents may run it
only to hand the file to a person: storing it as a CI secret for IGDEV_CONSENT_FILE is
the person's step. A machine that has not accepted the EULA is IGDEV_E_CONSENT_REQUIRED
at exit 3. With IGDEV_CONSENT_FILE set, setup reads that file, refuses --accept-* with a
usage error, and a missing or invalid term is IGDEV_E_CONSENT_REQUIRED naming the file.`,
	"igdev doctor": `pass --json for the machine contract. data carries ready plus
prerequisites, one entry per tool with name, command, required, state (present, missing,
or failed), the version line the probe printed, and the error when there is none. doctor
never fails: the exit level stays 0, so branch on data.ready and the states. A missing
required prerequisite is a host change (install docker with its Compose plugin, or a
JVM), not a repository change.`,
	"igdev version": `pass --json for the machine contract. data carries version (the release
semver), commit, and contract (the CLI Contract Version that the envelope, the IGDEV_E_*
codes, and the exit levels are frozen against): pin automation on contract, not on
version. In human mode a cached update- available line may appear on stderr; --json and
IGDEV_NO_UPDATE_NOTIFIER=1 suppress it, so machine output never depends on the network.`,
	"igdev help": `pass --json for the machine contract: the rendered text arrives as
data.help with data.command and data.section, because stdout may then carry nothing but
the envelope. Use it to read one command's complete reference without a terminal; an
unknown topic is IGDEV_E_USAGE at exit 2, with Remediation pointing at igdev help.`,
	"igdev completion": `the completion script is data, not an envelope: it is written verbatim to
stdout whether or not --json is set, so capture it unconditionally and write it to the
shell's completion directory. An unsupported or missing shell name is IGDEV_E_USAGE at
exit 2. An agent driving an argv-shaped interface has no use for it.`,
	"igdev check": `pass --json for the machine contract. data carries stages, one entry per
stage that ran — module-validate, module-scan, declared-check, jython-check — each with
its status and per-stage detail (paths, checked, findings, command, file_count), plus
failed naming the stage that stopped the run. The stage order is a contract, so a
failure is always at the same place, and an earlier stage's result is still in the same
envelope. Fix the reported stage and re-run; a missing or stale Checkout Setup is
refreshed before the first stage, with a note on stderr. --all
appends the test and build stages, and --all --gateway the Gateway stages, with
gateway_url carrying the Gateway --gateway left running (stop it with igdev gateway
down); the --gateway half needs the EULA accepted, so without it the run stops at exit 3
with IGDEV_E_CONSENT_REQUIRED.`,
	"igdev test": `pass --json for the machine contract. data.stages carries the declared-
test stage with its command and status; an undeclared stage is reported skipped, never
failed, so a repository that declares no test command still passes the verb. The stage's
own non-zero exit propagates unchanged, and its output streams to stderr in both
dialects.`,
	"igdev build": `pass --json for the machine contract. data.stages carries the declared-
build stage and the module re-staging that follows it (stage module-restage), with
the status of each; an undeclared build stage is reported skipped. The re-staging
stages every match of the contract's [modules].artifacts globs — stage.artifacts
names each glob, the source it matched, the module id it declares, and the staged
file, with superseded listing any staged file it replaced — and then re-materializes
the runtime. A declared glob that matches nothing fails with
IGDEV_E_MODULE_ARTIFACT_MISSING. A failing stage propagates its own exit code and
stops the run before anything is re-staged, so a build that failed never republishes
modules. --install adds stage module-install: installs lists each artifact with id,
artifact, action (installed or unchanged — same sha256 as last time), status (healthy,
inactive, quarantined), and restarted; it is igdev module install for each changed
artifact and stops at the first failure. Use build --install as the SDK inner loop: the
running Gateway keeps its projects and configuration.`,
	"igdev jython": `pass --json for the machine contract. jython check carries data.version,
data.jar (the verified cache entry the compile ran against), file_count, files, and
diagnostics on a failed compile. The checker is pinned by sha256 in the embedded version
catalog and fetched into the machine-wide cache once, so a first run may need network
access and every later run does not.`,
	"igdev jython check": `pass --json for the machine contract. data carries version, jar,
file_count, files, and — when the compile failed — diagnostics, one entry per rejected
file. The exit level is 1 when any file failed and 0 when the tree is clean, and every
file is compiled before that decision, so one run reports every problem. A path that
does not exist is IGDEV_E_JYTHON_PATH_MISSING. The cache is disposable: a jar that fails
its pin is quarantined and re-fetched, so never work around a hash mismatch by hand.`,
	"igdev module": `pass --json for the machine contract. Each verb has its own data member:
list carries ignition_version, enabled_all, whitelist, and the built_in/private rows;
require carries capabilities, one entry per argument. A capability that needs a module the whitelist does not name fails
with IGDEV_E_MODULE_NOT_ENABLED and a Remediation naming igdev module enable; an enabled
module nothing stages fails with IGDEV_E_MODULE_ARTIFACT_MISSING and names igdev module
add. enable moves the Contract Digest, so the next project command wants igdev setup;
add and clear only change what is staged and leave the Setup Stamp current.`,
	"igdev module list": `pass --json for the machine contract. data carries ignition_version,
enabled_all (the empty whitelist means every module loads), whitelist, and the selected
rows: built_in (id, artifact, enabled) and private (id, name, version, artifact, source,
status, error). A private row with status MISSING-ARTIFACT is a whitelist entry nothing
stages and the reason a Gateway would refuse to load; UNREADABLE is an artifact whose
module.xml could not be read; "enabled (staged)" is a staged private module the whitelist
does not name — staging is what enables it, so no contract write is needed. Stage the
missing one with igdev module add; this verb only reports.`,
	"igdev module require": `pass --json for the machine contract. data.capabilities carries one entry
per argument, in argument order, with capability, kind, platform, the modules it needs,
and the layer that resolved it (core or overlay). Every argument is checked before the
run exits, so one call reports every problem; a mixed run reports the resolutions that
passed in the same envelope as the fault. Remediation on the fault names igdev module
enable for a module the whitelist misses and igdev module add for one nothing stages.`,
	"igdev lookup": `pass --json for the machine contract. A query reports data.query, kind
(all, function, or rest), ignition_version, indexes, and results[], best first, each with
kind, name, score, summary, module (platform, a module id, ids joined by commas, or
unknown), module_enabled (absent outside a Project Root), scope (functions: all or the
scopes joined by commas) or call (endpoints: a ready-to-run igdev gateway api line with
the body's Content-Type), and source (the bundle, catalog, openapi, or embedded). --name
reports one entry in full: params with defaults and returns for a function; params,
request_types, request_body and responses (schemas inlined) for an endpoint. An unknown
--name is IGDEV_E_UNKNOWN_CAPABILITY. indexes.rest.source embedded means no Gateway
answered: run igdev gateway ensure, then igdev lookup --refresh. No Ignition image of the
version on the machine (neither an igdev Gateway image nor the official one) is
IGDEV_E_DOCKER; igdev gateway ensure or docker pull fixes it. Look a function up before writing
Jython that calls it, and an endpoint before calling igdev gateway api.`,
	"igdev module enable": `pass --json for the machine contract. data carries contract (path, action,
the Contract Digest afterwards, and the unified diff), whitelist, added,
already_enabled, unrestricted, and setup_stale. The write is tracked, so it follows the
contract rule: atomic, diffed, no backup file. When data.setup_stale is true the
Checkout Setup no longer matches the contract — run igdev setup before the next project
command.`,
	"igdev module add": `pass --json for the machine contract. data carries the artifact's id, name,
version, source, artifact, and staged path, action (created or replaced), bytes,
modules_dir, staged, count, runtime_dir, and the runtime files re-rendered. Nothing is
staged unless the archive's module.xml can be read, and nothing is written to the
contract: a staged private module is enabled by being staged, so no whitelist entry is
needed. The staged id is a first-class id for igdev module require and igdev module
list reports it as "enabled (staged)".`,
	"igdev module install": `pass --json for the machine contract. data carries id, name, version,
source, path (the staged copy, so a later reset or fresh volume loads it too), accepted
(the module's own terms igdev accepted: certificate, eula), restarted, status (healthy,
inactive, quarantined), and module (the Gateway's row: version, state, on_startup,
reason). Use it instead of module add plus gateway reset whenever the Gateway's data
must survive. A pending upgrade or a module that did not start is finished with an
in-place restart. A quarantine is IGDEV_E_MODULE_QUARANTINED with the Gateway's reason
(unsigned: igdev init --allow-unsigned-modules). inactive means a [modules].enabled
whitelist left the new id disabled until the Gateway is recreated.`,
	"igdev module clear": `pass --json for the machine contract. data carries modules_dir, removed
(the artifact file names deleted), count, staged (what is left), runtime_dir, and the
runtime files re-rendered. Clearing an empty staging area is a successful no-op.`,
	"igdev gateway": `pass --json for the machine contract. Every URL-producing verb reports the
same address block — instance_id, namespace, url, ports (http, https, debug) — read from
the Checkout Setup, so an agent never assumes a port. up and reset add capacity
(measured, available_mb, required_mb, headroom_mb, forced); wait --smoke adds checks; status
adds state and services; down adds volumes_removed; logs carries the log text;
credentials carries the password. Every verb refreshes a missing or stale Checkout
Setup first. The verbs that start a Gateway (up, reset, ensure) need the EULA accepted
(exit 3, IGDEV_E_CONSENT_REQUIRED); low free memory is only a warning on stderr. Starting a Gateway accepts the private
modules this checkout staged, by module id (ACCEPT_MODULE_LICENSES and
ACCEPT_MODULE_CERTS); a contract with [modules] require_private_module_consent = true
instead requires the machine-global module-license and module-cert terms, and without
them the run stops at exit 3 (ADR 0006).`,
	"igdev gateway api": `pass --json for the machine contract. data carries method, path, url,
status, headers (content-type, content-length, location, etag when present), body (parsed
JSON, else text, null when empty), body_bytes, and truncated (true when the answer was
over 4 MiB: body is then the first bytes as text). It is the shortest way to a REST call
on the Instance, since it presents the token itself; the path must start with /, and the
token and origin headers cannot be overridden. Human mode prints the whole body, and
--data takes up to 1 GiB. A 4xx or 5xx answer is IGDEV_E_GATEWAY_API with the
same data; no answer is IGDEV_E_GATEWAY_UNHEALTHY, fixed with igdev gateway ensure.
With --output <file> the whole body goes to the file, with no cap, and data carries
method, path, url, status, headers, output (the absolute path), and body_bytes instead of
body: use it for /openapi.json and any other answer over 4 MiB. --output alone writes
the path's last segment (GET /openapi.json --output writes openapi.json).`,
	"igdev gateway ensure": `pass --json for the machine contract. data carries action
(reused, started, reset), reason (healthy, not_running, fresh, faulted, token_rejected,
trial_short), instance_id, namespace, url, ports, container, host_address, trial (or
null), capacity (null when reused), and note when --min-trial did not apply. Call it
once before e2e work instead of chaining up, wait, and status: when it returns the
Gateway answered RUNNING. A refusal is IGDEV_E_CONSENT_REQUIRED at exit 3 and discards
nothing. IGDEV_E_DOCKER_DAEMON means Docker is not running: start it (sudo systemctl
start docker, or sudo dockerd in a container) and retry, never reset. Pass --fresh to discard the data on purpose.`,
	"igdev gateway up": `pass --json for the machine contract. data carries instance_id, namespace,
url, ports, and capacity (measured, available_mb, required_mb, headroom_mb, forced). up
returns as soon as the container is started: follow it with igdev gateway wait (--smoke to
check the declared endpoints too), and never assume the URL from the address block is answering yet. A
machine without the EULA accepted is IGDEV_E_CONSENT_REQUIRED at exit 3; low free memory
is a warning on stderr and capacity reports the numbers. The
staged private modules are accepted by module id as part of starting (ADR 0006); with
[modules] require_private_module_consent the run stops at exit 3 until the
module-license and module-cert terms are recorded.`,
	"igdev gateway down": `pass --json for the machine contract. data carries instance_id, namespace,
and volumes_removed. Stopping an Instance that is already stopped succeeds. --volumes
discards the Gateway's data, which with a staged Baseline is reproduced by the next
igdev gateway reset.`,
	"igdev gateway reset": `pass --json for the machine contract. data is the up shape — instance_id,
namespace, url, ports, capacity — because reset ends with a started, waited-for Gateway.
Consent is checked before anything is discarded, so a refusal at exit 3 costs no
data; otherwise the volume is removed, the container is recreated, and the staged
Baseline is applied on the fresh launch.`,
	"igdev gateway restart": `pass --json for the machine contract. data is the address block:
instance_id, namespace, url, and ports. The named volume is kept, so nothing is restored
and no state is lost; use igdev gateway reset when a fresh Gateway is what is wanted.`,
	"igdev restart": `pass --json for the machine contract. data carries the address block,
modules (healthy[] with id, name, version, state, on_startup, pending_upgrade, staged;
quarantined[] with id, name, version, reason, staged), and pending_upgrade (before,
after, finalized). Use it instead of gateway restart plus wait plus a REST call: it
returns once the Gateway reports RUNNING. A module this checkout stages that comes back
quarantined is IGDEV_E_MODULE_QUARANTINED. The modules commissioning step is finished
for any module (only staged ones under require_private_module_consent); any other
commissioning step stops at exit 3 for a person.`,
	"igdev project": `project import and export move a project between a directory (or zip) and
the running Gateway through its REST API with the Instance token. Keep projects in git as
directories: export into the directory, import from it.`,
	"igdev project import": `pass --json for the machine contract. data carries name, source,
zipped (true when a directory was zipped), bytes, overwrite, and changes (the projects the
Gateway reports it changed). A directory without project.json, or a name the Gateway
already has without --overwrite, is a usage error at exit 2 whose Remediation names the
fix. Never hand-build the zip or call the import endpoint through gateway api.`,
	"igdev project export": `pass --json for the machine contract. data carries name, output,
unpacked, bytes, and files (when unpacked). An --output that does not end in .zip is a
directory left holding exactly the exported files, so git shows additions and deletions;
it must be absent, empty, or already hold project.json. An unknown project is a usage
error at exit 2.`,
	"igdev gateway wait": `pass --json for the machine contract. data is the address block. The
wait ends when the Gateway reports RUNNING on its readiness endpoint (/StatusPing) —
the root document answering is not readiness, Jetty serves it while the Gateway is
still starting — so a returned wait means the Gateway is usable, including its module
routes. On timeout the command fails with the tail of the Gateway's own log in the
message, because that log holds the reason; the Remediation names igdev gateway logs and
igdev gateway status for the follow-up. --timeout takes seconds or a duration like 3m
(default 180s). --smoke then checks the root document and the contract's
[gateway] smoke_endpoints: data becomes instance_id, url, and checks, one entry per
request in order with path, url, status, ok, and the error when it did not answer, and a
failing check is IGDEV_E_GATEWAY_UNHEALTHY naming the endpoint.`,
	"igdev gateway status": `pass --json for the machine contract. data carries the address block plus
state (the compose project's overall verdict) and services, one entry per service with
name, service, state, and status as the container engine reports them, so an agent reads
the engine's own words instead of parsing human output. trial is the Gateway's own trial
report (license_mode, seconds_left, expired) while it runs, and null otherwise.`,
	"igdev gateway trial": `pass --json for the machine contract. data carries instance_id, url,
trial_reset (the effective [gateway] trial_reset: auto or off), and trial (license_mode,
seconds_left, expired), read from the Gateway's unauthenticated trial endpoint. A Gateway
that does not answer is IGDEV_E_GATEWAY_UNHEALTHY. Never rebuild a Gateway because its
trial runs low: with trial_reset = auto the trial keeper resets it in place the moment it
expires, and igdev gateway trial reset does the same on demand.`,
	"igdev gateway trial reset": `pass --json for the machine contract. data carries instance_id, url,
reset, before, and after (null when not reset). A trial that has not expired is a
successful no-op with reset false: Ignition accepts a reset only after expiry. A refused
reset is IGDEV_E_TRIAL_RESET; HTTP 401 means the Gateway's data predates the Instance API
token, and the Remediation is igdev gateway reset, which discards the Gateway's data:
run it when the task can afford a fresh Gateway.`,
	"igdev gateway exec": `pass --json for the machine contract. Run igdev's flags before the command
(or before --). data carries instance_id, container, user, command, exit_code, stdout,
stderr, stdout_bytes, stderr_bytes, and truncated: each stream is kept up to 1 MiB and
the byte counts are the full sizes. A non-zero exit is IGDEV_E_EXEC_FAILED at the
command's own exit level, with the same data. A Gateway that is not running is
IGDEV_E_GATEWAY_UNHEALTHY with igdev gateway up as Remediation. It saves building the
compose invocation for this Instance; --user root runs as root.`,
	"igdev gateway data": `pass --json for the machine contract. put and get address this Instance's
container without a compose invocation: a relative Gateway-side path is under the data
directory, an absolute one anywhere in the container. --user root reads and writes as
root instead of the Gateway's user.`,
	"igdev gateway data put": `pass --json for the machine contract. data carries instance_id, path,
container_path, local, bytes, and sha256 of what was written. Parent directories are
created, and the file is owned by the Gateway's user (2003:0), or by root with --user
root. A container-side failure is IGDEV_E_EXEC_FAILED.`,
	"igdev gateway data get": `pass --json for the machine contract. data carries instance_id, path,
container_path, bytes, and sha256, plus local when a <local> path was given, or
content_base64 (up to 1 MiB; a larger file is IGDEV_E_USAGE naming the <local> form).
No regular file at the path is IGDEV_E_EXEC_FAILED, and a failed get leaves nothing at
<local>.`,
	"igdev gateway logs": `pass --json for the machine contract. In human mode the log streams to
stdout; with --json the same text arrives as data.logs with instance_id and namespace,
because stdout may then carry nothing but the envelope. --tail limits how much of the
engine's buffer is read (0 is everything it holds).`,
	"igdev gateway url": `pass --json for the machine contract. data is the address block, and
data.url is the only URL to script against: the port came from the Checkout Setup, so
nothing in igdev or its callers may assume 8088.`,
	"igdev gateway credentials": `pass --json for the machine contract. Both dialects print the
development Gateway's credentials; human mode adds the source and the URL to log in at.
data carries username, password, and api_token: the Instance's own API token, the
X-Ignition-API-Token value that administers this Gateway (empty for a checkout set up
before igdev seeded one). A missing password (IGDEV_E_CONFIG_INVALID) is repaired with
igdev setup; IGDEV_GATEWAY_ADMIN_PASSWORD overrides both tiers for CI.`,
	"igdev baseline": `pass --json for the machine contract. set and status report the staged
Baseline as staged, path, bytes, source, sha256, staged_at, and restore_args (what a
fresh launch applies); clear reports removed. The staged file is the state and the
record is only a note about it, so a hand- placed restore.gwbk counts even without
provenance. Stage a Baseline before igdev gateway reset, which is the only launch that
restores it.`,
	"igdev baseline set": `pass --json for the machine contract. data carries staged, path, bytes,
source, sha256, staged_at, and restore_args. The digest is the review surface: it
identifies the copy the Gateway will restore from. A missing source is
IGDEV_E_BASELINE_MISSING and a source that is not a readable .gwbk is
IGDEV_E_BASELINE_INVALID; nothing is written in either case. The next fresh launch —
igdev gateway reset — applies it.`,
	"igdev baseline status": `pass --json for the machine contract. data carries staged, and — when a
file is staged — path, bytes, source, sha256, staged_at, and restore_args. staged true
without provenance fields (a hand-placed restore.gwbk) is deliberate: the file is the
state and the record is a note.`,
	"igdev baseline clear": `pass --json for the machine contract. data.removed lists the file names
that were deleted and is empty when nothing was staged, which is a successful no-op. The
Baseline directory stays: clearing means the next fresh launch restores nothing.`,
	"igdev catalog": `pass --json for the machine contract. catalog status carries the two layers
with their digests and counts; catalog import-openapi carries what the write did. The
digests are the layer identity and the review surface; the counts are for the reader. An
overlay row that would shadow a core row is IGDEV_E_OVERLAY_CONFLICT, so an overlay may
add knowledge and may never silently redefine it.`,
	"igdev catalog status": `pass --json for the machine contract. data carries ignition_version, core
(source, digest, counts), overlay (source, digest, paths, counts), and effective (the
layer sum). Compare digests across checkouts to prove the same capabilities resolve; a
version this binary carries no catalog for fails with IGDEV_E_CATALOG_VERSION_MISSING.`,
	"igdev catalog import-openapi": `pass --json for the machine contract. data carries ignition_version, source
(path and sha256 of the document), output (the file written and whether it changed),
operations, written, added, preserved, removed, and redundant. Re-importing an unchanged
document reports unchanged and writes nothing. A conflicting row is
IGDEV_E_OVERLAY_CONFLICT and nothing is written, so a refused import never leaves a
half-rewritten overlay; --prune is the only way previously imported rows are dropped.`,
	"igdev agent": `agent exists for Silent Mode: neither verb ever prompts. agent context
--json is the one call that orients a session — start there, then follow the verb whose
state the envelope reports. agent skill-install writes the workflow document this binary
ships, so the guidance an agent follows can never be out of date with the tool.`,
	"igdev agent context": `agent context --json is the orientation entry point. data.project_root,
data.lifecycle (initialized, setup_state, consent), data.versions, data.instance,
data.gateway, data.modules (staged ids, allow_unsigned_modules, auto_accepted,
require_private_module_consent), data.catalog,
data.capabilities, and data.commands are all reported in every lifecycle state,
initialized or not, so one call replaces reading files. Never mutate on its word: run
the verb whose state it reports, or the Remediation of the fault a verb returned. The
field-by-field reference is generated at docs/reference/agent-context.md.`,
	"igdev agent skill-install": `pass --json for the machine contract. data carries scope, path (the
first target's SKILL.md), action (created, updated, or unchanged, summed over the
targets), version (the CLI Contract Version the installed frontmatter records), harness
(all, claude, or agents), and targets: one entry per skills root with its harness, path,
and action. Installation is idempotent: identical
files are left untouched, so a re-install never moves their mtimes, and pages in
references/ the embedded skill no longer carries are removed. igdev owns SKILL.md and
references/; other files beside them are left alone, and a symlinked skill directory or
references/ is written through, never replaced. --scope repo requires a Project Root
(IGDEV_E_NOT_INITIALIZED otherwise) and writes .claude/skills/igdev/ and
.agents/skills/igdev/ into it.`,
	"igdev ci-local": `pass --json for the machine contract. data carries the exact invocation
(event, job, offline, command, args, workdir), act's exit code, and the tail of its
output. act's own output streams to stderr in both dialects, so a human reads the
workflow run and an agent reads the envelope. A run act cannot complete is
IGDEV_E_ACT_FAILED at act's own exit code. Without act on PATH, ci-local fetches a
pinned act into igdev's cache with go install once; only a host with neither act nor Go
is IGDEV_E_ACT_MISSING, with the install commands in Remediation. --with-gateway ensures the Gateway first and
adds data.gateway (action, reason, url, host_url, network); the job reads
IGDEV_GATEWAY_URL and IGDEV_GATEWAY_TOKEN, and the token never appears in argv.`,
	"igdev cleanup": `pass --json for the machine contract. data carries scope (checkout or
machine), deinit, uninstall, dry_run, project_root, namespace, the counts removed,
would_remove, kept and failed, and items: one entry per object with kind, ref, action
(would_remove, removed, kept, failed), reason, and diff for an in-place edit. Run
--dry-run --json first and show the items to the person when the scope is not obvious.
cleanup and --deinit act when named, like gateway reset; --machine and --uninstall need
--yes, and without it a non-terminal run is IGDEV_E_USAGE at exit 2 with the plan in
data. Use them only when a person asked for that scope. It never refreshes the Checkout
Setup and runs with no igdev.toml. A stopped Docker daemon is IGDEV_E_DOCKER_DAEMON and
nothing is removed. A failed item is IGDEV_E_CLEANUP_PARTIAL: fix its reason and run the
same command again. Done when a --dry-run lists no would_remove item.`,
}

// AgentUsage returns the machine-facing section for cmd, or "" when the command
// carries none. The binary's help text and the generated reference both render
// it from here.
func AgentUsage(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	return agentUsage[cmd.CommandPath()]
}

// NewDocumentationTree builds the command tree for the generated reference. It
// is the tree the binary runs, so docs/reference/ is rendered from the same
// definitions --help is.
func NewDocumentationTree() *cobra.Command {
	return New(io.Discard, io.Discard, nil).newRoot()
}
