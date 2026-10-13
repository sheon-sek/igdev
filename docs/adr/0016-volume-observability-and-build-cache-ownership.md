# Volume observability is read-only, and the shared build cache belongs to the operator

Status: proposed design (igdev#150, part of #140). Nothing here is implemented yet; an
implementation needs its own review, and any envelope, code, or exit-level change it brings
needs its own ADR.

Each Instance keeps Gateway data (history, internal databases, backups, installed modules)
in a named Docker volume (`compose.yaml.tmpl`). The Compose file's log rotation bounds
container stdout only, not that volume. igdev reports neither volume size nor free space
on the Docker data root. No runaway growth was observed; retention depends on the project
and its modules. Separately, `igdev cleanup` keeps the Docker build cache and reports it
`kept` (ADR 0013), but who owns its growth was never written down. A review snapshot
showed 4.454 GB of host-wide build cache, which cannot be attributed wholly to igdev nor
added to image sizes. Whether the engine's build-cache garbage collection is enabled or
effective on a given host was not verified, and this design does not assume either.

## Decision

**Volume observability, read-only.** A later change may report, per igdev Instance:

- `volume_bytes`: the data volume's size as the engine reports it (`docker system df -v`,
  the volume's `Size`). Docker computes it only for some drivers and may return `-1` or
  omit it; that is `unknown`.
- `docker_root_free_bytes`: free space on the filesystem holding the engine's data root
  (`docker info` `DockerRootDir`). On Docker Desktop, rootless, or remote engines the
  path is inside a VM or another host and is not statable from the CLI; that is `unknown`.

Every metric is either a measured number or the explicit value `unknown` with a reason
(driver does not report size, data root not reachable, engine did not answer, timed out).
**An unavailable measurement is never reported as zero.** Measurement is best effort and
bounded in time; its failure never fails the command that shows it.

Attribution limits: a volume is attributed to an Instance only through the ownership rules
ADR 0013 already uses (compose project `igdev-` plus eight hex digits, or the
`dev.igdev.instance` label). Free space is host-wide and is never attributed to igdev. The
build cache and shared base images are not added to any Instance's total.

Surfaces: `igdev doctor` (already read-only and never failing) shows the data-root free
space and each igdev volume it can attribute; `igdev gateway status` may show this
checkout's own volume size. `igdev status` stays engine-free. The inventory may be shared
with the forgotten-Instance audit (#152).

**Unused volumes are retained data, not leaks.** A volume with no running container is
classified as possibly intentional retained data (`gateway down` without `--volumes`
keeps it on purpose). No surface calls it a leak or suggests deleting it; the only
removals stay the explicit, named ones: `gateway down --volumes`, `gateway reset`, and
`igdev cleanup`.

**The shared build cache belongs to the operator.** igdev never prunes the Docker build
cache, in any scope, including `cleanup --machine`. The operator manages it with Docker's
own tools and settings (`docker builder prune`, `docker builder du`, the engine's
builder GC configuration). igdev may report its size, labelled host-wide, and makes no
claim about any host's GC policy.

## Out of scope

Quotas, automatic retention, automatic deletion of project data, a global prune, and a
build-cache prune. Each would need its own explicit proposal and review.

## Consequences

Disk growth becomes visible without igdev gaining any new way to delete data. Numbers
that cannot be measured stay visibly unknown, so a missing measurement is never mistaken
for an empty volume. The CLI Contract Version is unaffected by this note.
