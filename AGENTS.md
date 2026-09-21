# Agent Instructions

This repository is the reusable local-development foundation for Ignition engineering work.
The default runtime target is Ignition 8.3.8 with a Jython 2.7.4 compatibility checker.

## Command contract

Agents should use `./devctl` rather than inventing one-off Docker commands.

Safe commands that may run automatically after relevant changes:

- `./devctl self-test`
- `./devctl versions`
- `./devctl jython-check <path>`
- `./devctl module validate`
- `./devctl module require <capability>`
- `./devctl module scan <path>`
- `./devctl check`
- `./devctl test`
- `./devctl gateway status`
- `./devctl gateway logs --tail 200`
- `./devctl gateway up`
- `./devctl gateway wait`
- `./devctl gateway smoke`
- `./devctl gateway reset` when the task explicitly requires a clean disposable Gateway
- `./devctl verify`

Do not use this development Gateway as a production or staging target. Do not perform writes to external systems unless the task explicitly authorizes them.

## Validation behavior

After changing code, run the cheapest relevant validation first. `./devctl check` performs module configuration preflight before project checks and should not be bypassed. When code calls a module-owned `system.*` function or Ignition resource API, use `./devctl module require` or configure `MODULE_REQUIREMENT_PATHS` so `module scan` can reject missing modules before the Gateway starts. Escalate to the real Gateway only when behavior depends on Ignition runtime APIs, modules, tags, OPC, Gateway configuration, or MCP.

For Jython scripts, a standalone Jython check is only a syntax/bytecode compatibility gate. It does not prove `system.*` behavior. Runtime-dependent behavior must be checked against the real Ignition container.

## Version changes

Do not edit the Dockerfile to change runtime versions. Change `IGNITION_VERSION` and, when appropriate, `JYTHON_VERSION` in `.env`, then run `./devctl bootstrap` and `./devctl gateway reset`.

`JYTHON_VERSION` controls only the local standalone checker. The Jython version embedded in Ignition is determined by the selected Ignition image.

## Private modules

Never commit EA, licensed, or private `.modl` files. Add them with `./devctl module add <file.modl>` or place them in the version-scoped global cache reported by `./devctl module cache-path`.

Use `./devctl module list` for the full effective environment, `--built-in` or `--private` to filter it, and `./devctl module catalog` when choosing from the complete Ignition 8.3 built-in image catalog.


Private `.modl` metadata is read from its embedded `module.xml`, so configured module IDs and artifacts are represented as one record. Use `./devctl module validate` before runtime and `./devctl module require <capability>` for explicit capability checks.
