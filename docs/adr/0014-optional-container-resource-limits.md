# Optional per-container resource limits for the Gateway and trial keeper (proposed)

**Status: proposed, design only (igdev#141, part of #140).** Nothing here is
implemented, and no default limit is approved. Implementation needs this ADR approved
and a separate, later-reviewed issue. On approval this file is edited in place: the
"(proposed)" title suffix and this status line are removed and any approver decision
(see Open questions) is recorded here; later changes follow as numbered amendments, as
in ADR 0003.

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
version, `-m`, module set and time after start with each result. All measurements use `igdev gateway *` verbs on
a disposable local Gateway only.

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
so they live in the machine-local tiers, not the tracked `igdev.toml`:
`.igdev/local.toml` and `IGDEV_*` environment, following the existing precedence
(flag > environment > `.igdev/local.toml` > `igdev.toml` > embedded defaults). A
syntax sketch, not a commitment to names; the placeholders are deliberate and **no value
is recommended**:

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

The keys are read by a bespoke machine-local reader, like `config.LoadPort`, not added to
the frozen `config.Schema`: `status --json` `config.resolved` is built from that schema,
so new schema keys would change the `status` golden.

**Applying a changed limit.** The Compose file is rendered only by `igdev setup`, and the
Setup Stamp today covers only the contract digest, seed digest, schema and CLI contract
(`internal/gate/gate.go`), so a limit edited after setup would be silently ignored by
`gateway up`. The follow-up must close that gap: setup records a digest of the resolved
limits (from `.igdev/local.toml` and `IGDEV_*`) in the Setup Stamp, and the existing
staleness gate recomputes it on every gated command, so any change, file or environment,
fails with the existing `IGDEV_E_SETUP_STALE` until `igdev setup` re-renders. Limit
environment variables are therefore setup-time inputs, unlike per-run variables such as
`IGDEV_GATEWAY_PORT`, but a change between runs is detected rather than dropped. This
reuses an existing code and exit level, so it is not a CLI Contract change.

**Validation, warning-only.** When a Gateway memory limit is set, igdev warns (does not
refuse) if it is below `-m` plus a non-heap floor, because that configuration will be
OOM-killed under load. The floor is a documented constant chosen from the measurements
above, not a guess. A PID limit should sit well above the observed thread count; CPU
limits are documented as slowing startup, module installation and builds.

**Unsupported hosts.** When the engine rejects a limit (Compose or `docker` error), the
start fails with the engine's message through the existing error path; igdev does not
silently drop a limit the person asked for. When the engine accepts but ignores it
(rootless without cgroup v2 delegation, cgroup v1 without the controller), igdev reports the effective values from
`docker inspect` in `gateway status` and warns when they differ from the configured
ones. It never retries without the limit. On Docker Desktop the limit is still enforced
by the cgroups inside the VM; the VM's own size is a separate, host-level bound that
igdev neither sets nor reports.

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

## Open questions for the approver

- **Acceptance criterion 2 (workload evidence).** igdev#141 asks for workload
  measurements with method and environment. Only idle samples exist, and they lack the
  protocol's environment fields. The approver must decide whether "a defined measurement
  protocol, and no number proposed" satisfies that criterion for this design-only ADR,
  or whether the measurements must be collected before approval. This is a human owner
  decision; the ADR does not presume it.

## Consequences

- No CLI Contract change: no envelope field, `IGDEV_E_*` code or exit level is added or
  changed by this ADR. If implementation needs one (for example an effective-limit field
  in `status --json`), it goes through CLI Contract review, `make goldens`,
  `make reference` and its own ADR.
- Users who set nothing see no change. Users who set a limit own its consequences; the
  warnings above make the likely mistakes visible.
- Follow-ups, opened only after this ADR is approved: collect the measurements above;
  implement option 2; revisit a default only with that data and separate approval.
