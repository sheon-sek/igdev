# Resource-lifecycle test matrix

The resource-lifecycle review of igdev#140 found eight scenarios (F1–F8). The
independent review added two host risks, #152 and #153. This page records where each
scenario is tested, the evidence its claim needs, the evidence it has, and what is still
unverified. Focused regression tests ship with each fix and stay there. The shared
pieces are listed after the matrix: a hermetic cycle test, the opt-in real-Gateway soak,
and the rig extensions they use.

## Evidence levels

Match the evidence to the claim (igdev#151):

| Level | Claim | Sufficient evidence |
| --- | --- | --- |
| **L1 logic** | Pure igdev logic: planning, tails, state derivation, bookkeeping | Unit tests, fake engines, the docker shim |
| **L2 engine** | Docker or Compose behavior: image labels on rebuild, dangling-image reclamation, multi-tag or in-use removal, `--remove-orphans`, service reconciliation, restart policy | A real development Docker engine or Gateway. Shim evidence alone is not enough. |
| **L3 process** | Signal and process behavior | Real processes and real signals. Add real docker or act when the claim depends on them. |
| **L4 runtime** | Behavior that depends on the Ignition runtime | A real Gateway, through `igdev gateway` verbs only (AGENTS.md escalation rules) |

A trend is an observation, not a verdict. The soak never claims a Gateway-internal leak
from a rising series alone.

## Matrix

"Owning tests" are the focused regressions that shipped with the fix.

| Scenario | Owner | Claim | Needs | Owning tests | Evidence obtained | Unverified / where the soak helps |
| --- | --- | --- | --- | --- | --- | --- |
| F1: no Gateway or keeper resource limits | #141 | Optional container limits | Design only (ADR 0014). An implementation would need L2 and L4. | none (design) | ADR 0014 proposed; no code | Nothing to verify until implementation is approved. The soak records `gateway_mem_bytes` and `keeper_mem_bytes` as the workload evidence ADR 0014 asks for. |
| F2: a failed volume removal loses retry identity | #142 | A Setup is kept while its Instance still has a Docker object | L1 | `internal/cleanup/cleanup_test.go` | Unit tests that fail on the old code (PR #155) | None for the claim. Each soak cycle runs a real checkout cleanup and requires that nothing of the Instance remains. |
| F3a: `gateway logs` retains all output; Compose diagnostics unbounded | #143 | The human log streams; Compose diagnostic tails are bounded | L1 (the stream and tail are igdev's own I/O) | `internal/docker/stream_test.go` | Unit tests (PR #161) | Behavior against a real, growing log. The soak records `cli_rss_kb_logs_stream` against `gateway_log_bytes` and `logs_stream_bytes` as a trend. |
| F3b: ci-local diagnostic tail retains huge lines | #144 | Tail bytes and unterminated lines are capped | L1 | `internal/boundedtail/tail_test.go`, `itest/cilocal_test.go` | Unit tests and a real-binary 4 MiB stream (PR #156) | None. |
| F4: an untagged rebuilt image is missing from the plan | #145 | Owned images are inventoried by ID, dangling rebuilds included; multi-tagged and in-use images are kept; nothing is forced | L2 | `internal/cleanup/images_test.go`, `internal/cleanup/cleanup_test.go` | Fake-engine fixtures. Real Docker 29.8.1 probes confirmed that non-forced ID removal refuses multi-tag and stopped-container users, and read-only inspection confirmed Compose labels on a built Gateway image (PR #158). | PR #158 could not reproduce a retained dangling rebuild: that engine dropped the old image. The soak's `rebuilds` phase rebuilds under the same tag on a real engine, records `owned_dangling_images`, and requires a dry-run cleanup to list every owned image ID. Its `cycles` require a real cleanup to leave no image. |
| F5: lookup temp containers unlabelled; removal failures silent | #146 | `dev.igdev.purpose=lookup` label; `docker rm` failures warned | L1 for argv and warnings; the label on a real create is L2 | `internal/lookup/image_test.go` | Unit tests (PR #160); real-engine lookup create and remove under signals (PR #159) | Not in the soak: lookup needs a staged private module. Whether inherited Compose labels affect `--remove-orphans` stays unverified (PR #160, #162). |
| F6: `gatewayState` says running with only the keeper up | #147 | Gateway and keeper are reconciled on reuse; `data.state` is the Gateway's own state | L2 (Compose service reconciliation, restart policy) | `internal/cli/gateway_state_test.go`, `itest/gateway_keeper_test.go`, `itest/gateway_test.go`, `itest/cleanup_test.go` | Shim tests, plus a real development Gateway (8.3.10, Compose 5.5.1) scenario table in PR #162 | Behavior over time. The soak's `long-running` phase requires both services running at every sample, every `ensure` to reuse, and the Gateway's `StartedAt` to stay unchanged. |
| F7: corrupt Jython samples and module indexes grow | #148 | Corrupt samples are bounded (N=2); module indexes expire after 90 days | L1 | `internal/jython/retention_test.go`, `internal/lookup/store_test.go` | Unit tests (PR #163); policy in `docs/cache-retention.md` | None for the claim. The soak records `cache_bytes` and `state_bytes` of its scratch HOME. |
| F8: interrupted commands leak temp state | #149 (phase 1) | SIGINT and SIGTERM clean up before exit | L3, plus real docker for the lookup container | `internal/interrupt/interrupt_test.go`, `internal/cli/interrupt_test.go`, `itest/interrupt_test.go` | Real binary and real signals in itest. A real engine with `ignition:8.3.8` showed that lookup under SIGINT and SIGTERM leaves no scratch directory and no container (PR #159). | `ci-local --with-gateway` with real act and a real Gateway: consent is human-only, so only the shim covers it. Phase 2 (signal forwarding, timeouts) is not approved. |
| Design gap: volume observability, build-cache ownership | #150 | Design (ADR 0016) | Design only | none (design) | ADR 0016 | The soak records `volume_bytes` (from `du` inside the Gateway) and `build_cache_bytes` as observations. It never prunes the build cache. |
| R2: running Instances whose checkout is missing | #152 | `doctor` inventories Instances read-only and classifies a missing checkout | L1 for classification. L2 for the remediation it suggests. | `internal/cleanup/inventory_test.go`, `itest/doctor_test.go` (goldens) | Unit tests. A real-engine probe showed `docker compose -p <ns> stop` works with the Compose file deleted and keeps the volume (PR #157). | Restart across a real daemon or host reboot. The soak's teardown requires `doctor` to list none of its Instances. |
| R7: obsolete-version images accumulate | #153 | `cleanup --obsolete-images` removes by ID only this Instance's old-version images, and the official image only they used; it keeps the Instance | L2 | `internal/cleanup/obsolete_test.go`, `itest/cleanup_test.go` | Fake-engine tests and a read-only real dry run (PR #165) | Actual removal on a real engine. The soak's `obsolete-images` phase (with `IGDEV_SOAK_OLD_VERSION`) executes it, then checks that the old images are gone and that the Gateway (same `StartedAt`), its volume, its current image and `.igdev/` stayed. |

## Shared coverage added by #151

- **`itest/lifecycle_test.go`**: `TestLifecycleCyclesLeaveNoOrphansOrLeaks` runs three
  cycles of `gateway up`, `status`, `logs`, `doctor`, `cleanup --obsolete-images --dry-run`,
  `down`, `up` and `cleanup` against the shim. After every step it asserts an empty
  TMPDIR, and after every cycle no orphan docker objects, no `.tmp` debris and no
  `.igdev/`. This is L1 evidence: igdev's own bookkeeping across repetition. It extends
  the hygiene gate, which used to cover mostly `status`, and it runs in `make all`.
- **`itest/soak_test.go`**: the opt-in real-Gateway soak described below (L2 and L4).
  Its pure helpers (trend slope, docker size parsing, the fresh-checkout claim and the
  ownership guards) are
  checked on every run by the `TestSoak*` unit tests in `itest/soak_report_test.go`.

### Rig extensions

- `testrig.Run.RSSInterval` sets how often `SampleRSS` reads `/proc`; zero keeps the
  100 µs default. The soak samples every 20 ms, because its commands run for minutes.
  `VmHWM` is a high-water mark, so a slower poll still reads the peak. The poller keeps
  a running maximum for the whole command, not a bounded queue of its first readings,
  so a peak reached minutes in is still measured (`TestRSSPollKeepsLatePeak`).
- `AssertNoDockerOrphans` and the leak assertions are reused unchanged. The orphan
  tally tracks containers, networks and volumes, not images. Image accounting is L2,
  so it lives in the fake-engine unit tests of #145 and #153 and in the soak's
  independent label and reference queries.

## The opt-in real-Gateway soak

The soak never runs by default. `go test ./...`, `make all`, `make test-quick` and CI
skip `TestSoakRealGateway` unless `IGDEV_SOAK=1` is set. Run it with:

```bash
make soak                                         # defaults below
IGDEV_SOAK_OLD_VERSION=8.3.8 IGDEV_SOAK_DURATION=30m make soak
```

### Environment it needs

- A reachable Docker engine with Compose. The soak pins igdev to the invoking user's
  current Docker context (`DOCKER_HOST`).
- A person's Consent record. The default is the machine record
  (`~/.config/igdev/accepted.toml`); `IGDEV_CONSENT_FILE` overrides it. The soak passes
  the record to igdev as `IGDEV_CONSENT_FILE`, which igdev reads but never writes, and
  under which `--accept-eula` is refused. Without consent, the soak stops at
  `IGDEV_E_CONSENT_REQUIRED` and names the person's step. It never accepts the EULA.
- Memory for one more Gateway (`IGDEV_SOAK_MEMORY_MB`, default 2048 MiB of heap) and
  disk for its images. With `IGDEV_SOAK_OLD_VERSION`, the build pulls that Ignition
  version unless it is already present. The shared Docker build cache grows with
  every build, and the soak never prunes it (ADR 0016).

### Workload

| Phase | What it does | Knob (default) |
| --- | --- | --- |
| `init` | `init` a checkout under the report directory, with a tracked seed (one tag provider) and `setup` | `IGDEV_SOAK_VERSION` (init's default) |
| `cycles` | Per cycle: `up`, `wait`, `down` (volume kept), `up` from the kept volume, `wait`, then checkout `cleanup` with a fixed seed. Each cleanup removes the Setup, so the next `setup` mints a new Instance. | `IGDEV_SOAK_CYCLES` (2) |
| `start` | Boot the Instance the remaining phases share. With an old version: boot it, `down`, move the contract to the current version and boot that from the same volume, as a person upgrades. | `IGDEV_SOAK_OLD_VERSION` (unset) |
| `rebuilds` | Per rebuild: change the seed, `setup`, `gateway ensure --fresh` (must report `reset`). This rebuilds the image under the same tag. Then a dry-run cleanup must list every owned image ID. | `IGDEV_SOAK_REBUILDS` (2) |
| `long-running` | Keep the Gateway up and sample at the interval. Each sample checks Gateway and keeper `running` and `ensure` `reused`. At the end, the Gateway's `StartedAt` must be unchanged. The phase ends 15 minutes before the `go test` deadline so the teardown has time. | `IGDEV_SOAK_DURATION` (10m), `IGDEV_SOAK_INTERVAL` (1m) |
| `obsolete-images` | With an old version: dry run, then `cleanup --obsolete-images`, then a second dry run that must plan nothing. If the plan would remove an official image that was on the engine before the soak, the soak stops before executing and records why. | `IGDEV_SOAK_OLD_VERSION` |
| teardown | Always runs, also after a failure or an interruption: diagnostics on failure, checkout `cleanup`, leftover check across every Instance the soak minted, and a `doctor` check | — |

`IGDEV_SOAK_WAIT_TIMEOUT` (10m) bounds each `gateway wait` and `ensure`.
`make soak` sets `go test -timeout 180m`. To cross one two-hour trial cycle, set
`IGDEV_SOAK_DURATION` above 2h and raise the timeout. `trial_seconds_left` then shows
the keeper's reset.

### What it records

Each sample records:

- Instance object counts (containers, volumes, networks, images, dangling images);
- Gateway and keeper memory (`docker stats`);
- data-volume bytes (`du` through `igdev gateway exec`) and Gateway log bytes (counted, never kept);
- scratch cache and state bytes, and TMPDIR entries;
- the trial's seconds left;
- the CLI's own peak RSS for `gateway logs` (streamed and `--tail 200`) and `gateway status`.

The build-cache size is read at phase boundaries. Every igdev invocation is logged with
its exit, duration and peak RSS.

Everything goes to the report directory: `IGDEV_SOAK_REPORT_DIR`, default a fresh
`$TMPDIR/igdev-soak-<UTC time>-<random>`. A named directory must not exist yet: the
soak creates it and its checkout itself and refuses an existing path or a symlink, so
it never adopts another checkout's Setup or Instance. The checkout must not lie inside
an igdev project either: `init` writes to the nearest `igdev.toml` above it, so before
`init` the soak requires that `igdev status` in the checkout finds no Project Root,
and after it that the root is the checkout. Name an absolute path outside every
project; a relative one resolves under `itest/`, inside this repository. It holds `summary.md`
(environment, workload, evidence, the trend table with first, last, min, max and
least-squares slope per hour, and pre-existing host objects that changed),
`samples.jsonl`, `commands.jsonl`,
`envelopes/` (every cleanup plan and result, in run order), and on
failure `diagnostics/` (gateway status, last 500 log lines, doctor, cleanup plan,
containers).

### What it guarantees about the host

- **Only igdev mutates.** Every change goes through an igdev verb run in the soak's
  own checkout. The soak's own docker calls are refused unless they are inspection
  verbs (`soakDockerReadOnly`).
- **Only its own Instance.** The soak creates its report directory and checkout
  exclusively, outside every igdev project, so `init` writes only the checkout's
  contract and `setup` mints a new Instance. If a Setup ever names an Instance that
  had a container, volume or image on the engine before it started, the soak fails,
  and its teardown runs neither diagnostics nor `igdev cleanup` in that checkout, so
  the foreign Instance is never touched (`TestSoakClaimForeignInstanceSkipsTeardownCleanup`).
- **Only its own objects go.** Every cleanup it runs is checkout-scoped (never
  `--machine`). Each removed item is checked against the Instances the soak minted,
  the images attributed to them just before, and the official tags that existed before
  the soak. Removing anything else fails the soak. It never prunes images, volumes or
  the build cache.
- **Official images are a cache it does not own.** An official image the soak pulled
  stays, unless igdev's own `--obsolete-images` rule removes it.
- **Interruption is scoped.** SIGINT or SIGTERM stops the soak before its next step.
  Ctrl-C also reaches the running igdev, which cleans up its own temporary state
  (#149). The teardown then removes only the soak's Instances. It never sweeps by
  age, by path or machine-wide, and never touches another Instance's data volume.
- **A failure keeps diagnostics.** The report directory always stays. The checkout
  stays when the teardown found leftovers or the run failed, so
  `cd <report>/checkout && igdev cleanup` can finish the job. `igdev doctor` lists any
  Instance a hard kill (SIGKILL) left behind.

### Results

A report is evidence for one engine and one host, never a general guarantee. The PR or
issue that ran the soak records its full results. Findings that change a row of the
matrix are kept here.

**2026-10-13, Docker 29.8.1 (overlayfs with the containerd snapshotter), Compose 5.5.1,
Ignition 8.3.10 with old version 8.3.9 (igdev#151)**:

- **#145**: Seed-changing rebuilds under the same tag left **no** dangling image (3 of 3
  and 2 of 2): this engine drops the replaced image. This confirms what PR #158 saw.
  Planning retained dangling rebuilds is still fixture-only evidence, pending an engine
  with the classic image store.
- **#153**: `cleanup --obsolete-images` removed the 8.3.9 Instance image by ID on the
  real engine. The running Gateway kept its `StartedAt`, and its volume, its 8.3.10
  image and `.igdev/` stayed. A second dry run planned nothing. The official-image
  branch had nothing to act on: the BuildKit build pulled the 8.3.9 base into the build
  cache and never tagged `inductiveautomation/ignition:8.3.9`. That branch is still
  fixture-only evidence. It applies only where an official tag exists, for example one
  that lookup or a person pulled.
- **#147**: Over 30 minutes and 15 samples, the Gateway never restarted, the keeper ran
  beside it, and every `ensure` reused it.
- Every Instance the soak minted was gone after the teardown. No pre-existing container,
  volume, network, image or tag changed. A neighbouring running Instance kept running
  throughout.
