# `igdev doctor` reports igdev Instances whose checkout is missing, read-only

A worktree deleted or moved without `igdev gateway down` leaves its Gateway and keeper
running, and both use `restart: unless-stopped`, so they also come back after a daemon or
host restart. Nothing in igdev said which running Instances no longer have a checkout;
the only recovery was `igdev cleanup --machine`, which removes everything (igdev#152,
part of igdev#140).

We decided to add an additive `data.instances` object to `igdev doctor`:

- `engine` is `available` or `unknown`; `error` says why it is unknown. An engine that
  cannot be asked never reports zero Instances: `items` is then `null`.
- `items` lists every container-holding Instance whose Compose project is an Instance
  namespace (`igdev-` plus eight hex digits); a user's `igdev-foo` project and other
  projects are ignored. Each carries `namespace`, `instance`, `checkout` (from the
  `working_dir` label), `path_state` (`present`, `missing`, `unknown`), `classification`
  (`active`, `needs_review`, `checkout_missing`, `unknown`) and per-container `services`.
- A present checkout is active only when its Setup identity matches the container's
  full Instance UUID and namespace. Different UUIDs sharing a namespace prefix need
  review. Conflicting service identity labels also need review, and `instance` is
  empty because no single UUID describes them. For legacy containers with no identity
  labels, matching the Setup's namespace remains the explicit fallback.
- A container's `restart` is `running`, `restartable` (engine state `restarting`), or
  `unknown`. `docker inspect` on Docker 29.8.1 exposes no field that says whether an
  operator stopped an exited container, so igdev does not guess between "stays stopped"
  and "comes back".
- A non-active Instance carries `suggestion`: `docker compose -p <namespace> stop`.
  doctor never runs it.

Why `doctor` and a documented Docker command: doctor is already read-only, works
outside a checkout, and never fails (exit 0), so no new verb, flag, error code or exit
level is needed, and a field addition is the smallest CLI Contract change. A new igdev
stop verb would be a mutating surface the Epic did not authorize. `stop` rather than
`down` keeps the containers, network and data volume, and needs no Compose file.

Verified on a real engine (Docker 29.8.1, Compose v5.5.1, 2026-10-13): a probe project
with two `unless-stopped` services and a named volume was started, its Compose file
deleted, then `docker compose -p <project> stop` exited 0, left both containers `exited`,
kept the volume, and left another running Instance untouched. Not verified: the
behaviour across a real daemon or host restart. Docker documents that `unless-stopped`
does not restart a container that was explicitly stopped, so the stopped Instance is
expected to stay stopped after a reboot until `igdev gateway up` in its checkout or
`docker start`.

## Consequences

- The `doctor` envelope gains `data.instances`; existing fields are unchanged and the
  exit level stays 0. `ready` does not depend on the inventory. CLI Contract version is
  not bumped: the change is additive.
- doctor now makes one `docker ps --all` call when docker is on PATH.
- No automatic stop, down or volume removal; a missing path is an observation, never a
  deletion criterion. Volume size reporting is left to igdev#150.
