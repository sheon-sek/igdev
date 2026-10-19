# Optional per-container resource limits for the Gateway and trial keeper

**Status: accepted, design only (igdev#141, part of #140). Approved by the owner
(sheon-sek) on 2026-10-19.** Nothing here is implemented, and no default limit is
approved. Implementation needs a separate, later-reviewed issue. Later changes follow as
numbered amendments, as in ADR 0003.

## Context

Neither the `gateway` nor the `trial-keeper` service in the generated Compose file sets
`mem_limit`, `memswap_limit`, `cpus` or `pids_limit`
(`internal/runtimeassets/templates/compose.yaml.tmpl`). The Gateway's `-m <MemoryMB>`
bounds the JVM heap, its dominant consumer, but not metaspace, code cache, thread stacks,
direct buffers, other native memory or child processes. Several worktrees running
Gateways at once can pressure the host with nothing in igdev bounding each container.

Admission is warning-only by decision (ADR 0003 amendment 1, igdev#105; ADR 0012): `up`,
`reset`, `ensure` and `check --gateway` measure `MemAvailable`, warn on stderr, and start
anyway, including when free memory cannot be measured (`internal/cli/gateway.go`,
`admit`). This is a hardening gap against the long-term host-safety goal, not a
regression.

## Evidence gathered so far

Read-only `docker inspect` / `docker stats` at 281e25d on one Linux host (Docker Engine,
cgroup v2), reported in igdev#141. Docker Engine version, Ignition version, the staged
module set and time-after-start were not recorded for these samples, so they do not
meet the protocol below:

| Container | Config | Memory | PIDs | HostConfig |
| --- | --- | --- | --- | --- |
| gateway, idle | `-m 2048` | ~652 MiB | 145 | `Memory=0`, `MemorySwap=0`, `NanoCpus=0`, no `PidsLimit` |
| trial-keeper, idle | — | ~12 MiB | 2 | same |

These are idle samples. They are **not** workload evidence: no modules under load, no tag
load, no Designer session, no project import. No host failure was measured. They are
not enough to propose any number, and this ADR proposes none.

### Measurement required before any number is proposed

Per scenario, sample once a second for the run's duration and record the peak and p95:

- container RSS and cgroup `memory.current` / `memory.peak` (`docker stats`, or the
  cgroup files through `igdev gateway exec`);
- JVM heap and non-heap via `jcmd <pid> GC.heap_info` through `igdev gateway exec`, if
  the image ships `jcmd` (unverified); otherwise cgroup `memory.stat` (`anon`, `file`)
  only. Native Memory Tracking is out of scope: igdev has no JVM-argument surface and
  `.igdev/` must not be hand-edited, so it cannot be enabled within these constraints;
- `pids.current` and JVM thread count (`jcmd <pid> Thread.print | grep -c '^"'`);
- CPU seconds and wall time of `igdev gateway up` → `wait --smoke`.

Scenarios: idle after start; first start with every staged module (including a private
module such as `com.inductiveautomation.mcp`); a seeded project import; a tag provider
with representative tag count and scan rate; one and three concurrent Designer sessions;
a trial reset by the keeper. Record host, engine and version, cgroup version, Ignition
version, `-m`, module set and time after start with each result. All measurements run
against a disposable local Gateway only, through `igdev gateway *` verbs where one exists
and read-only `docker stats` otherwise. `igdev gateway exec` reaches only the Gateway
container (`Compose.Exec` targets `GatewayServiceName`), so the keeper's figures come
from `docker stats` alone.

## Options

1. **No limits (status quo).** Docker's default. Nothing to configure, nothing to break;
   no bound on one runaway container.
2. **Opt-in limits, unset by default (proposed).** igdev renders a limit only when the
   person or agent configures one. Today's behavior stays the baseline.
3. **Default limits derived from `-m`** (for example heap plus a fixed headroom). Rejected
   for now: no workload evidence supports a headroom, and a too-tight cap turns host
   pressure into OOM kills of the Gateway inside its cgroup, a new failure that today's
   users never see. A default change needs its own approval after option 2's data.
4. **Enforcing admission budget** (refuse or queue a start past a machine budget, with
   concurrent-start reservation). Rejected: it reverses ADR 0003 amendment 1 and ADR 0012,
   and would change exit levels and codes, a CLI Contract break.

## Proposed decision

Option 2.

**Configuration surface.** Limits are a property of the machine, like ports (ADR 0003),
so they do not go in the tracked `igdev.toml`. They are read from `.igdev/local.toml`
only: no `IGDEV_*` environment variable and no flag sets a limit. An environment tier
would make the variable's presence an input to staleness, so an invocation without it
(another shell, an agent subprocess) would refresh setup, render the Compose file without
the limit, and the next start would run uncapped. The Gateway port pin avoids this because
setup records it into `.igdev/local.toml` (`localconfig.WritePort`) and the Setup Stamp's
`ports`; recording environment limits the same way is possible later but is not proposed
here. A syntax sketch, not a commitment to names; the placeholders are deliberate and
**no value is recommended**:

```toml
# .igdev/local.toml
[limits.gateway]
memory = "<size>"      # mem_limit; absent = unlimited
memory_swap = "<size>" # memswap_limit; absent = not rendered (Docker's own default)
cpus = <n>             # cpus
pids = <n>             # pids_limit

[limits.trial_keeper]
memory = "<size>"
pids = <n>
```

Each key renders only when set; `memory_swap` is never derived from `memory`, so igdev
introduces no swap default. Absent keys render nothing, so the Compose file is
byte-identical to today's when no limit is set.

The keys are read by a bespoke machine-local reader, like `localconfig.LoadPort`, not
added to the frozen `config.Schema`: `status --json` `config.resolved` is built from that
schema, so new schema keys would change the `status` golden.

**Applying a changed limit.** Two steps are needed, and the second is the one a person
can miss.

*Rendering.* The Compose file is rendered only by `igdev setup`, and the Setup Stamp
today compares only the stamp schema, contract digest, seed digest, contract schema and
CLI contract (`stampState`, `internal/gate/gate.go`), so a limit edited after setup would
never reach the Compose file. The follow-up adds a digest of the limits read from
`.igdev/local.toml` to the Setup Stamp and to that comparison. A changed digest makes the
Checkout Setup stale (`IGDEV_E_SETUP_STALE`), and the Gate repairs that as it does today
(ADR 0012; `App.requireSetup`, `repairableBySetup` in `internal/cli`): setup re-runs,
re-renders the Compose file, and the command continues. Only a failed refresh fails the
command, through the existing setup error path. The field is a tolerant addition, not a
`StampSchema` bump: it is omitted when no limit is set, and `Decode` (`json.Unmarshal`)
reads a stamp without it as empty, so existing checkouts with no limits stay current
instead of refreshing once for nothing.

*Reaching a running container.* A re-rendered Compose file does not change a container
that is already running. Only `docker compose up` applies it: Compose recreates a service
whose configuration changed since the container was created (Compose's documented `up`
behavior, not reproduced with igdev for this ADR), which restarts the JVM and keeps the
named volume. In igdev that is `igdev gateway up`, `igdev gateway reset`,
`igdev check --all --gateway`, and `igdev gateway ensure` only when it starts or resets
the Gateway (`compose.Up` in `internal/cli`). `gateway ensure` reusing a healthy Gateway
(`ensureReused`) runs no `compose up`, and `igdev gateway restart` and `igdev restart`
run `docker compose restart gateway` (`Compose.Restart`), which restarts the same
container with its old HostConfig. So after a limit change, a Gateway left running, or
reused or restarted, keeps its old limits until one of the `compose up` verbs above runs.
The follow-up does not change `ensure` or `restart` to recreate (that would add an
`ensure` action or reason, a contract item); instead `gateway status` warns when the
running container's HostConfig differs from the rendered limits and names
`igdev gateway up` as the repair, the same repair igdev already gives for another
rendered setting ("recreate the Gateway container with it, keeping the volume",
`internal/cli/gateway_rest.go`).
The same applies to the trial keeper, which `compose up` also recreates.

This adds one input to the existing staleness check and reuses existing codes, exit levels
and refresh behavior. How the new `gateway status` warnings (here and below) are printed
is left to the follow-up; any new `--json` field for them goes through CLI Contract
review (see Consequences).

**Validation, warning-only.** When a Gateway memory limit is set, igdev warns (does not
refuse) if it is below `-m` plus a non-heap floor, because that configuration will be
OOM-killed under load. The floor is a documented constant chosen from the measurements
above, not a guess. A PID limit should sit well above the observed thread count; CPU
limits are documented as slowing startup, module installation and builds.

**Unsupported hosts.** When the engine rejects a limit (Compose or `docker` error), the
start fails with the engine's message through the existing error path; igdev does not
silently drop a limit the person asked for. When the engine accepts but ignores it
(for example rootless without cgroup v2 delegation, or cgroup v1 without the controller),
`docker inspect` HostConfig still shows the requested values: Docker's rootless mode
drops the resources from the OCI runtime spec without touching HostConfig.
`gateway status` therefore keeps three things apart: the *rendered* limit (the Compose
file), the container's *configured* limit (its HostConfig, compared with the rendered one
as above), and *enforcement*, which HostConfig never proves.

For the Gateway, enforcement is read from the running container's own cgroup through the
same path `igdev gateway exec` uses (cgroup v2 `memory.max`, `memory.swap.max`,
`cpu.max`, `pids.max`, or the v1 equivalents), optionally preceded by an engine
capability check (`docker info` cgroup version, driver and rootless security option).
When the cgroup value matches the request the limit is reported as enforced; when it
differs or is `max`, `gateway status` warns that the limit is not enforced on this host;
when the file cannot be read, it warns that enforcement is unverified. A Gateway that is
not running has no cgroup to read: status says the container is not running and reports
no enforcement verdict, rather than the not-enforced warning. igdev never retries a start
without the limit.

For the trial keeper this ADR proposes the configured limit only, reported as
unverified. `igdev gateway exec` reaches only the Gateway (`Compose.Exec` hard-codes
`GatewayServiceName`, and its help says "inside this Instance's Gateway container"), and
giving it a service selector would change its help text and `docs/reference/`, a CLI
Contract change that is not proposed. Verifying the keeper would need either that change,
through contract review, or a separate internal read that this ADR does not design.

On Docker Desktop the limit is still enforced by the cgroups inside the VM; the VM's own
size is a separate, host-level bound that igdev neither sets nor reports.

**OOM kills and restart.** Both services use `restart: unless-stopped`, so a capped
Gateway that is OOM-killed restarts, possibly in a loop. The follow-up surfaces
`docker inspect` `State.OOMKilled` and `RestartCount` as a `gateway status` warning; it
does not change the restart policy.

**Admission position.** Admission stays warning-only. The Capacity Gate's numbers and
warnings are unchanged. Concurrent-start reservation is out of scope while admission
only warns; it becomes meaningful only if a separately reviewed policy enforces a budget.

**Host protection is partial.** A hard per-container cap protects the host from one
runaway Gateway. It does **not** remove pressure from many legitimately running Gateways
(see igdev#152, forgotten Instances) or from image builds and pulls, which run outside
the Instance's containers.

## Approver decision (2026-10-19)

- **Acceptance criterion 2 (workload evidence).** The owner accepted "a defined
  measurement protocol, and no number proposed" as sufficient for this design-only ADR.
  Workload measurements under the protocol above are collected in the implementation
  follow-up, before any limit value or default is proposed.

## Consequences

- No CLI Contract change: no envelope field, `IGDEV_E_*` code or exit level is added or
  changed by this ADR. If implementation needs one (for example an effective-limit field
  in `status --json`), it goes through CLI Contract review, `make goldens`,
  `make reference` and its own ADR.
- Users who set nothing see no change. Users who set a limit own its consequences; the
  warnings above make the likely mistakes visible.
- Follow-ups, opened only after this ADR is approved: collect the measurements above;
  implement option 2; revisit a default only with that data and separate approval.
