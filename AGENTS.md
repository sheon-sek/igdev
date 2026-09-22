<!-- igdev:start -->
<!-- managed by igdev; edit outside these markers only -->
## igdev
`igdev.toml` declares this repository's Ignition toolchain; run `igdev status` first.
<!-- igdev:end -->

# Agent Instructions

This repository is `igdev` itself: the standalone, globally installed Ignition
development toolchain (Go, `cmd/` + `internal/`). It dogfoods its own Project Contract
(`igdev.toml`), so the commands below are the same ones it ships.

## Safe commands

Read-only; may run automatically after relevant changes:

- `igdev status` / `igdev status --json` — the only command that works before init and setup
- `igdev check` — module validate → capability scan → `make fmt-check vet` → batched Jython compile
- `igdev module list`, `igdev module validate`, `igdev module require <capability>`
- `igdev catalog status --json`
- `igdev doctor` — read-only host audit; it never fails
- `igdev gateway status`, `igdev gateway logs --tail 200`, `igdev gateway url`

Mutating local state; run when the task calls for it:

- `igdev test` (`make test`) and `igdev build` (`make build`)
- `igdev setup` — re-materializes `.igdev/`; run it after any `igdev.toml` change
- `igdev gateway up|wait|smoke|down --volumes` — a disposable local Gateway only
- `igdev gateway reset` only when the task explicitly requires a clean Gateway

Repository tooling (the Go half of this tree):

- `make all` — `fmt-check vet test`; `make test-quick` skips the timing, packaging, and installer gates
- `make gates` — the resource and hygiene gates alone
- `make reference-check` — fails when `docs/reference/` drifts from the command definitions
- `make package` — release tarballs and checksums under `dist/`

## Escalation rules

Validate cheapest first: `igdev check`, then `igdev test`, then the real Gateway.
Escalate to a real Gateway (Docker) only when behavior depends on Ignition runtime APIs,
modules, tags, OPC, Gateway configuration, or MCP — and then only through `igdev gateway
*`, never ad-hoc `docker run` or `docker compose` commands.

A standalone `igdev jython check` is a syntax and bytecode compatibility gate: it does
not prove `system.*` behavior. Runtime-dependent behavior must be checked against the
real Ignition container. The development Gateway is never a production or staging
target, and nothing writes to external systems unless the task explicitly authorizes it.

Consent is human-only. When a command exits 3 with `IGDEV_E_CONSENT_REQUIRED`, stop and
hand `igdev setup --accept-eula` to a person; never accept the Ignition EULA, a module
license, or a module certificate on a user's behalf.

## Version changes

Runtime versions live in `igdev.toml`, never in Docker files: change
`[ignition].version` or `[ignition].jython_version` (by hand or with
`igdev init --ignition-version …`) and re-run `igdev setup`. Moving the contract moves
the Contract Digest and makes the Checkout Setup stale on purpose
(`IGDEV_E_SETUP_STALE`); `igdev setup` is the repair. Everything under `.igdev/` is
generated and disposable — never hand-edit it and never commit it.

## Private modules

Never commit EA, licensed, or private `.modl` files. Add them with
`igdev module add <file.modl>` (staged under the gitignored `.igdev/modules/`), or place
them in the version-scoped machine cache reported by `igdev module cache-path`. Module
metadata comes from the archive's embedded `module.xml`; use `igdev module validate`
before runtime and `igdev module require <capability>` for explicit capability checks
before a Gateway starts.

`[modules].enabled` in `igdev.toml` may only whitelist a module the selected Ignition
image ships built-in or one whose `.modl` the checkout stages: an enabled entry nothing
can load fails `igdev check` with `IGDEV_E_MODULE_ARTIFACT_MISSING` on purpose. A private
module (here `com.inductiveautomation.mcp`) is therefore staged per checkout, never
whitelisted from a committed contract.

## The frozen contract

`itest/testdata/golden/` and `docs/reference/` are the frozen agent-facing contract: the
JSON envelope, the `IGDEV_E_*` codes, the exit levels, and the help text. After an
intentional behavior change, run `make goldens` and read the diff, run `make reference`
to regenerate the public reference, and record any envelope, code, or exit-level change
as an ADR — that is a CLI Contract Version break, not a normal release.
