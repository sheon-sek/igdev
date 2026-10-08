# Scenario: Tools for the Ignition MCP Module

The Ignition MCP Module serves Tools, Resources and Prompts from a Designer project
(the runtime bundle): each Tool is a `resource.json` plus a Jython `onToolCalled.py`.
igdev runs the Module on a disposable Gateway, compiles the Jython, and imports the
bundle as a project.

## Set up once

1. **Load the MCP Module.** The `.modl` is a private artifact, so it stays out of git.
   Before the first Gateway, stage it with `igdev setup --json --module <path to .modl>`;
   on a Gateway that already runs, use `igdev module install <path to .modl> --json`,
   which keeps the Gateway's data.
   Done when `igdev restart --json` lists the Module under `modules.healthy`.
2. **Point the checks at the bundle.** Run
   `igdev init --json --scan-jython <bundle dir> --scan-capabilities <bundle dir>`.
   Done when the envelope's diff shows both `[scan]` paths.

## Loop

1. Edit a Tool. Look up every `system.*` call first (`references/guides/lookup.md`).
2. Run `igdev check --json`. The Jython stage compiles every script under the pinned
   Jython; the scan stage checks that the modules the scripts' `system.*` calls need are
   enabled.
   Done when it returns `ok:true`.
3. Run `igdev gateway ensure --json`, then
   `igdev project import <bundle dir> --overwrite --json`.
   Done when `data.changes` names the bundle's project.
4. Call the Tools through an MCP client connected to the Module's endpoint on this
   Gateway (`url` from `ensure`). A Jython error that only shows at runtime is in
   `igdev gateway logs --json`.
   Done when each changed Tool returns the expected result or error.

`igdev check` proves the Jython compiles, not that `system.*` behaves as expected: step 4
is where runtime behaviour is shown.

## Handoff

Connecting an MCP client to the Module (server configuration, role tokens, the client's
MCP settings) belongs to ignition-mcp: its `setup` and `connect <role>` commands take
the Gateway URL from `igdev gateway ensure --json` and the token from
`igdev gateway credentials --json`.

To keep a project change made in the Designer, export it back into the repository:
`igdev project export <name> --output <bundle dir> --json`. Done when `git status` shows
the expected changes.
