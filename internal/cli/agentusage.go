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
exit 3 means a person must act (Consent, the Capacity Gate) and Remediation names the
exact command. With every argument supplied igdev runs in Silent Mode and never prompts.
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
is the repair path for IGDEV_E_SETUP_REQUIRED and IGDEV_E_SETUP_STALE, which every
dependent verb reports with a Remediation naming it. IGDEV_E_CONSENT_REQUIRED at exit 3
is the human handoff: a person runs igdev setup --accept-eula, then the automated run
repeats. Read the admin password only through igdev gateway credentials --json.`,
	"igdev status": `pass --json for the machine contract. data is the whole report:
initialized, project_root, working_dir, contract (path, present, schema_version,
schema_supported, digest), setup (present, path, stamp_state, instance_id, namespace,
ports), gateway (allow_unsigned_modules — the effective IGNITION_ALLOW_UNSIGNED_MODULES),
modules, consent (per-term acceptance), and config (tier_files plus every resolved key
with the tier that won). It succeeds before init and setup and outside a Project Root,
so it is always the safe first call; branch on the blocks, never on the exit level.`,
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
envelope. Fix the reported stage and re-run; a missing or stale Checkout Setup arrives
as IGDEV_E_SETUP_REQUIRED or IGDEV_E_SETUP_STALE, repaired with igdev setup.`,
	"igdev test": `pass --json for the machine contract. data.stages carries the declared-
check stage with its command and status; an undeclared stage is reported skipped, never
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
modules.`,
	"igdev verify": `pass --json for the machine contract. data.stages concatenates check, test,
and build in order — plus the Gateway stages when --gateway is set — with failed naming
the stage that stopped the run, and gateway_url carrying the Gateway that --gateway left
running (stop it with igdev gateway down). Because the --gateway half starts a Gateway,
recorded Consent is required there: without it the run stops at exit 3 with
IGDEV_E_CONSENT_REQUIRED.`,
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
require carries capabilities, one entry per argument; scan carries checked and findings
with file and line. A capability that needs a module the whitelist does not name fails
with IGDEV_E_MODULE_NOT_ENABLED and a Remediation naming igdev module enable; an enabled
module nothing stages fails with IGDEV_E_MODULE_ARTIFACT_MISSING and names igdev module
add. Both writes move the Contract Digest, so the next project command wants igdev
setup.`,
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
	"igdev module scan": `pass --json for the machine contract. data carries checked (distinct
capabilities resolved) and findings, one entry per occurrence in file, line, and
capability order. A path that does not exist is a warning on stderr, not a failure: the
scan reports what it could read. Without arguments the contract's [scan].capabilities
paths are used, which is what igdev check scans too.`,
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
	"igdev module cache-path": `pass --json for the machine contract. data carries path (the machine-wide
cache directory for this Ignition version), ignition_version, and exists. An absent
directory is normal: the cache is disposable and is created on first use, so data.exists
false is never an error.`,
	"igdev module clear": `pass --json for the machine contract. data carries modules_dir, removed
(the artifact file names deleted), count, staged (what is left), runtime_dir, and the
runtime files re-rendered. Clearing an empty staging area is a successful no-op.
Deleting the whole cache is not this verb: it stays safe to remove by hand.`,
	"igdev gateway": `pass --json for the machine contract. Every URL-producing verb reports the
same address block — instance_id, namespace, url, ports (http, https, debug) — read from
the Checkout Setup, so an agent never assumes a port. up and reset add capacity
(measured, available_mb, required_mb, headroom_mb, forced); smoke adds checks; status
adds state and services; down adds volumes_removed; logs carries the log text;
credentials carries the password. Every verb needs recorded Consent (exit 3,
IGDEV_E_CONSENT_REQUIRED) and a current Checkout Setup, and up/reset also pass the
Capacity Gate (IGDEV_E_CAPACITY, exit 3). Starting a Gateway accepts the private
modules this checkout staged, by module id (ACCEPT_MODULE_LICENSES and
ACCEPT_MODULE_CERTS); a contract with [modules] require_private_module_consent = true
instead requires the machine-global module-license and module-cert terms, and without
them the run stops at exit 3 (ADR 0006).`,
	"igdev gateway up": `pass --json for the machine contract. data carries instance_id, namespace,
url, ports, and capacity (measured, available_mb, required_mb, headroom_mb, forced). up
returns as soon as the container is started: follow it with igdev gateway wait or igdev
gateway smoke, and never assume the URL from the address block is answering yet. A
refusal is IGDEV_E_CAPACITY at exit 3 — a human frees memory or passes --force — and a
machine that was never set up is IGDEV_E_SETUP_REQUIRED, repaired with igdev setup. The
staged private modules are accepted by module id as part of starting (ADR 0006); with
[modules] require_private_module_consent the run stops at exit 3 until the
module-license and module-cert terms are recorded.`,
	"igdev gateway down": `pass --json for the machine contract. data carries instance_id, namespace,
and volumes_removed. Stopping an Instance that is already stopped succeeds. --volumes
discards the Gateway's data, which with a staged Baseline is reproduced by the next
igdev gateway reset.`,
	"igdev gateway reset": `pass --json for the machine contract. data is the up shape — instance_id,
namespace, url, ports, capacity — because reset ends with a started, waited-for Gateway.
The Capacity Gate runs before anything is discarded, so a refusal at exit 3 costs no
data; otherwise the volume is removed, the container is recreated, and the staged
Baseline is applied on the fresh launch.`,
	"igdev gateway restart": `pass --json for the machine contract. data is the address block:
instance_id, namespace, url, and ports. The named volume is kept, so nothing is restored
and no state is lost; use igdev gateway reset when a fresh Gateway is what is wanted.`,
	"igdev gateway wait": `pass --json for the machine contract. data is the address block. On timeout
the command fails with the tail of the Gateway's own log in the message, because that
log holds the reason; the Remediation names igdev gateway logs and igdev gateway status
for the follow-up. --timeout takes seconds or a duration like 3m (default 180s).`,
	"igdev gateway smoke": `pass --json for the machine contract. data carries instance_id, url, and
checks: one entry per request in the order they ran, each with path, url, status, ok,
and the error when it did not answer. A failing check is IGDEV_E_GATEWAY_UNHEALTHY with
Remediation naming igdev gateway logs --tail 50 and igdev gateway status --json. The
endpoints come from the contract's [gateway] smoke_endpoints, so the check is project-
declared.`,
	"igdev gateway status": `pass --json for the machine contract. data carries the address block plus
state (the compose project's overall verdict) and services, one entry per service with
name, service, state, and status as the container engine reports them, so an agent reads
the engine's own words instead of parsing human output.`,
	"igdev gateway logs": `pass --json for the machine contract. In human mode the log streams to
stdout; with --json the same text arrives as data.logs with instance_id and namespace,
because stdout may then carry nothing but the envelope. --tail limits how much of the
engine's buffer is read (0 is everything it holds).`,
	"igdev gateway url": `pass --json for the machine contract. data is the address block, and
data.url is the only URL to script against: the port came from the Checkout Setup, so
nothing in igdev or its callers may assume 8088.`,
	"igdev gateway credentials": `pass --json for the machine contract, and --json is the only dialect that
prints the password: human mode reports the username and where the password came from.
data carries username and password. A missing password (IGDEV_E_CONFIG_INVALID) is
repaired with igdev setup; IGDEV_GATEWAY_ADMIN_PASSWORD overrides both tiers for CI.
Never log the envelope: it is a secret.`,
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
installed SKILL.md), action (created, updated, or unchanged), and version (the CLI
Contract Version the installed frontmatter records). Installation is idempotent: identical
files are left untouched, so a re-install never moves their mtimes, and pages in
references/ the embedded skill no longer carries are removed. igdev owns SKILL.md and
references/; other files beside them are left alone, and a symlinked skill directory or
references/ is written through, never replaced. --scope repo requires a Project Root
(IGDEV_E_NOT_INITIALIZED otherwise) and writes .agents/skills/igdev/ into it.`,
	"igdev ci-local": `pass --json for the machine contract. data carries the exact invocation
(event, job, offline, command, args, workdir), act's exit code, and the tail of its
output. act's own output streams to stderr in both dialects, so a human reads the
workflow run and an agent reads the envelope. A run act cannot complete is
IGDEV_E_ACT_FAILED at act's own exit code; a host without act is IGDEV_E_ACT_MISSING
with the install commands in Remediation.`,
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
