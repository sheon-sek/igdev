---
name: igdev
version: "2"
description: "Work an Ignition local-development checkout from an agent session with igdev: orient with status, keep init and setup apart, never accept legal terms, validate before running, and speak JSON."
---

# igdev

igdev is the Ignition local-development toolchain. Each repository declares what it
needs in a tracked `igdev.toml` Project Contract; igdev materializes a disposable
runtime into the gitignored `.igdev/`. This file is the workflow. Read a reference
file below only when the task needs it; do not guess a flag or a code.

## References

- `references/README.md`: every command, one line each. Start here to find a verb.
- `references/<command>.md`: one command's flags, examples, and agent usage. The
  name is the command path with dashes, for example `references/gateway-up.md` or
  `references/module-require.md`. `igdev help <command>` prints the same text.
- `references/errors.md`: every `IGDEV_E_*` code with its exit level and next step.
  Read it when a run fails and the Remediation alone does not explain the failure.
- `references/agent-context.md`: the field-by-field shape of
  `igdev agent context --json`.

## Workflow

1. **Status first.** Orient with `igdev status --json` before doing anything else.
   It is the safe first call: it works before `init` and `setup` and never mutates.
   For a full one-call orientation — lifecycle, versions, instance, gateway,
   modules, catalog, and command availability — run `igdev agent context --json`.

2. **Keep init and setup separate.** `igdev init` writes the tracked Project
   Contract (`igdev.toml`) and is the only verb that mutates tracked files. `igdev
   setup` materializes this checkout into `.igdev/` and writes nothing tracked. A
   missing or stale Checkout Setup is repaired with `setup`, never by hand-editing
   generated files.

3. **Never accept a legal term.** Consent (the Ignition EULA, module licenses,
   module certificates) is human-only. Never run `setup --accept-eula`,
   `--accept-module-license`, or `--accept-module-certificate` on a person's
   behalf. When a command exits 3 with `IGDEV_E_CONSENT_REQUIRED`, stop and hand
   the exact Remediation command to a human.

4. **Validate before you run.** Use `igdev check` to exercise the preflight
   pipeline (module validate, capability scan, the declared check command, batched
   Jython). Fix what it reports. Do not run `test`, `build`, or a Gateway on a
   checkout that has not passed.

5. **Start a Gateway only when runtime matters.** Drive it through igdev. When
   the task needs the running Ignition runtime, call `igdev gateway ensure --json`:
   it reuses a healthy Gateway, starts a stopped one, or resets a broken one, and
   reports `action`, `url`, `container` and `trial`. Then reach the Gateway only with
   igdev verbs:
   - REST: `igdev gateway api <METHOD> <path> --json` sends the Instance API token
     for you. Never write curl with `X-Ignition-API-Token` by hand.
   - Inside the container: `igdev gateway exec -- <cmd>`, and `igdev gateway data
     put|get` for files under the data directory.
     Never `docker exec`, `docker cp` or `docker inspect` an Instance.
   - Trial: `igdev gateway trial --json`. An expired trial is reset in place by the
     trial keeper or `igdev gateway trial reset`; never rebuild a Gateway because its
     trial ran out.
   - Host services: the Gateway reaches the host at `host.docker.internal`.
   Ports are dynamic: read the URL from the JSON, never assume 8088. The Gateway is
   disposable: stop it with `igdev gateway down --volumes` when done.
   `IGDEV_E_DOCKER_DAEMON` means Docker is not running: ask a person to start it,
   and never reset the Gateway for it.

6. **Never edit `.igdev/`.** Everything under `.igdev/` is generated and
   disposable; `igdev setup` re-renders it. Delete it rather than patch it, and
   make tracked changes through the CLI so they print a reviewable diff.

7. **Never bypass a preflight fault.** A fault names an exact code and Remediation.
   Run the Remediation; do not skip the stage, silence the error, or force past it.
   `--force` is a human's risk decision, not an agent's.

8. **Speak JSON.** Pass `--json` so stdout is the single envelope
   `{ok, contract, code, message, remediation, data}` and progress stays on stderr.
   With every argument supplied igdev runs in Silent Mode: it never prompts,
   whatever the terminal is.
