# Scenario: a Gateway for an AI agent

An AI agent under development needs a Gateway with known data to act on. igdev
provides the Gateway and its data; ignition-mcp turns it into MCP endpoints the agent
calls.

## Environment and data

1. **Declare the data that every fresh Gateway starts with.** Put Gateway configuration
   resources (tag providers, tags, devices, database connections) in a tracked
   directory laid out as `<module>/<type>/<name>/`, and list it in `igdev.toml`:
   `[gateway] seed = ["<dir>"]`. A seed applies to a fresh volume only.
   Done when `igdev setup --json` returns `ok:true`.
2. **Start the Gateway.** Run `igdev gateway ensure --json`; after a seed change, add
   `--fresh` once so the new seed applies (this discards the Gateway's data).
   Done when it reports `url`.
3. **Add what a seed cannot express.**
   - Projects: `igdev project import <dir or zip> --json`.
   - Live values and anything else the REST API covers: `igdev gateway api --json`, with
     the endpoint found by `igdev lookup --kind rest`.
   Done when each call returns `ok:true` and reading the data back shows it.

## Handoff to ignition-mcp

ignition-mcp's `setup` command installs the MCP Module, creates the MCP server and its
role tokens, and its `connect <role>` command writes the agent client's MCP settings.
Give it the Gateway URL from `igdev gateway ensure --json` and the `api_token` from
`igdev gateway credentials --json`. That token already holds the Read, Write and
Designer permissions setup needs.
Done when ignition-mcp's `status` reports every check passing.

## Agent run

1. Give the agent the MCP endpoint and role token ignition-mcp reported, and run it.
2. Between runs, call `igdev gateway ensure --json`: it reuses a healthy Gateway with its
   data, and the trial keeper renews an expired trial in place.
   Done when `action` is `reused`. Use `ensure --fresh` only when a run needs the seeded
   starting state again.
