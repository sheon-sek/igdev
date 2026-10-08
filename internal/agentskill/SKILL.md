---
name: igdev
version: "2"
description: "Ignition local development with igdev. Use when a repository has igdev.toml, when a task needs a running Ignition Gateway, when you need a system.* function or a Gateway REST endpoint, or when you build an Ignition SDK module, MCP Tools, or an AI agent against Ignition."
---

# igdev

igdev gives each checkout its own disposable Ignition Gateway. The tracked `igdev.toml`
(the Project Contract) is the only state worth keeping. `.igdev/` and the Gateway are
disposable: the next igdev command regenerates `.igdev/`, and `igdev gateway ensure`
brings the Gateway back.

Pass `--json` to every command and act on the envelope. A failure names a `code` and a
`remediation`: run the remediation. `references/errors.md` explains each code.

## Three layers

| Layer | Reach for first |
| --- | --- |
| Environment | `igdev init`, `igdev setup`, `igdev gateway ensure`, `igdev gateway down`, `igdev cleanup` |
| Operations on the Gateway | `igdev gateway api`, `igdev gateway exec`, `igdev gateway data`, `igdev module install`, `igdev restart`, `igdev project import` / `export` |
| Knowledge and checks | `igdev lookup`, `igdev check` |

## Steps

1. **Orient.** Run `igdev agent context --json`.
   Done when it returns `ok:true`. With no `igdev.toml` yet, write one with
   `igdev init --json` and the flags the task needs. When this file's `version` differs
   from the envelope's `contract`, run `igdev agent skill-install` and reload the skill.
2. **Look it up.** Before writing a `system.*` call or a REST request, run
   `igdev lookup "<what you need>" --json`, then `--name <result>` for the full entry.
   Done when every function and endpoint you write matches a looked-up entry.
3. **Check.** Run `igdev check --json`; `--all` adds the test and build stages.
   Done when it returns `ok:true`.
4. **Gateway, when behaviour depends on the Ignition runtime** (`system.*` results,
   modules, tags, OPC, REST, MCP). Run `igdev gateway ensure --json`.
   Done when it reports `url`. Read the URL, ports and `host_address` (how the Gateway
   reaches this host) from that envelope; ports differ per checkout. Then work through
   the operations layer: `igdev gateway api` presents the API token itself, and
   `igdev gateway exec` and `igdev gateway data put|get` reach inside the container.
5. **Finish.** Run `igdev gateway down --volumes`, or keep the Gateway for the next run
   and say so. Done when your report states which. When the whole project is finished,
   remove what igdev created with `igdev cleanup` instead; read its guide first.

Change the contract (versions, modules, commands) with `igdev init --json <flags>`: it
prints the diff for review, and the next command picks the change up.

## Consent

The Ignition EULA is a legal acceptance only a person can give, once per machine. When a
command exits 3 with `IGDEV_E_CONSENT_REQUIRED`, stop and hand its remediation to a
person (`igdev setup --accept-eula`, or `IGDEV_ACCEPT_EULA=Y` in the environment's
configuration). Never pass an `--accept-*` flag or set `IGDEV_ACCEPT_EULA` yourself.
Done when the person has the remediation and you have stopped.

A person working at a terminal can run `igdev init`, `igdev setup` and
`igdev module add` without arguments to get their interactive Wizards; suggest them when
a person sets up a checkout by hand.

## Scenario guides

Read the guide that matches the task before its first step:

- `references/guides/scenario-sdk-module.md`: building an Ignition module (`.modl`).
- `references/guides/scenario-mcp-tools.md`: writing Tools for the Ignition MCP Module.
- `references/guides/scenario-ai-agent.md`: preparing a Gateway and data for an AI agent.
- `references/guides/lookup.md`: when `lookup` finds nothing or reports `embedded`.
- `references/guides/cleanup.md`: before any `igdev cleanup`, to pick its scope.

A command's flags are in `references/<command path with dashes>.md`, for example
`references/gateway-api.md`; `references/README.md` lists every command.
