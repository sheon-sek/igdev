# Optional per-container resource limits for the Gateway and trial keeper (proposed)

**Status: proposed, design only (igdev#141, part of #140).** Nothing here is
implemented, and no default limit is approved. Implementation needs this ADR approved
and a separate, later-reviewed issue.

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
cgroup v2), reported in igdev#141:

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
- JVM heap and non-heap (`jcmd <pid> GC.heap_info`, `VM.native_memory summary` when NMT
  is enabled through the Gateway's JVM args);
- `pids.current` and JVM thread count (`jcmd <pid> Thread.print | grep -c '^"'`);
- CPU seconds and wall time of `igdev gateway up` → `wait --smoke`.

Scenarios: idle after start; first start with every staged module (including a private
module such as `com.inductiveautomation.mcp`); a seeded project import; a tag provider
with representative tag count and scan rate; one and three concurrent Designer sessions;
a trial reset by the keeper. Record host, engine, cgroup version, Ignition version,
`-m`, and module set with each result. All measurements use `igdev gateway *` verbs on
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
sketch, not a commitment to names:

```toml
# .igdev/local.toml
[limits.gateway]
memory = "3g"      # mem_limit; empty/absent = unlimited
memory_swap = "3g" # memswap_limit; defaults to memory when memory is set
cpus = 2.0         # cpus
pids = 2048        # pids_limit

[limits.trial_keeper]
memory = "128m"
pids = 64
```

Absent keys render nothing, so the Compose file is byte-identical to today's when no
limit is set. A limit changes the rendered Compose file, so it is applied by
`igdev setup` like any other local configuration change.

**Validation, warning-only.** When a Gateway memory limit is set, igdev warns (does not
refuse) if it is below `-m` plus a non-heap floor, because that configuration will be
OOM-killed under load. The floor is a documented constant chosen from the measurements
above, not a guess. A PID limit should sit well above the observed thread count; CPU
limits are documented as slowing startup, module installation and builds.

**Unsupported hosts.** When the engine rejects a limit (Compose or `docker` error), the
start fails with the engine's message through the existing error path; igdev does not
silently drop a limit the person asked for. When the engine accepts but ignores it
(rootless without cgroup v2 delegation, cgroup v1 without the controller, some Docker
Desktop VMs where the limit applies to the VM), igdev reports the effective values from
`docker inspect` in `gateway status` and warns when they differ from the configured
ones. It never retries without the limit.

**Admission position.** Admission stays warning-only. The Capacity Gate's numbers and
warnings are unchanged. Concurrent-start reservation is out of scope while admission
only warns; it becomes meaningful only if a separately reviewed policy enforces a budget.

**Host protection is partial.** A hard per-container cap protects the host from one
runaway Gateway. It does **not** remove pressure from many legitimately running Gateways
(see igdev#152, forgotten Instances) or from image builds and pulls, which run outside
the Instance's containers.

## Consequences

- No CLI Contract change: no envelope field, `IGDEV_E_*` code or exit level is added or
  changed by this ADR. If implementation needs one (for example an effective-limit field
  in `status --json`), it goes through CLI Contract review, `make goldens`,
  `make reference` and its own ADR.
- Users who set nothing see no change. Users who set a limit own its consequences; the
  warnings above make the likely mistakes visible.
- Follow-ups, opened only after this ADR is approved: collect the measurements above;
  implement option 2; revisit a default only with that data and separate approval.
