# CLI Contract 2 removes the verbs a replacement covers

Contract 1 grew verbs that duplicated another verb or had no user: `gateway smoke` was
`gateway wait` plus a check of the declared endpoints, `verify` was `check` followed by
`test` and `build`, `module scan` repeated the check pipeline's module-scan stage, and
`module cache-path` named a machine cache nothing read. Each extra verb is another
reference page, golden file and help entry to keep in step, and another path an agent
can take.

The owner reviewed the command surface item by item (issue #63) and decided:

- `gateway smoke` is removed; `gateway wait --smoke` reports the same data.
- `verify [--gateway]` is removed; `check --all [--gateway]` reports the same data.
  `check --gateway` without `--all` is a usage error.
- `module scan` is removed; `igdev check` runs the same scan over
  `[scan].capabilities` and names each failing `file:line`.
- `module cache-path` is removed; private modules are staged per checkout with
  `module add` or `[modules].artifacts`.
- `agent context --json` drops `commands.verify`.
- Kept, because people use igdev in a terminal and develop private modules with it:
  `gateway url`, `module require`, `catalog import-openapi`, `completion`, and the
  `init`, `setup` and `module add` Wizards.

The removed verbs are unregistered outright, with no deprecation release: calling one is
an unknown command, IGDEV_E_USAGE at exit 2. The CLI Contract Version is now 2, so a
checkout set up under Contract 1 reads stale until `igdev setup` runs again.

## Amendment 1: `[commands].smoke` is retired (2026-10-07)

`igdev init` asked for a `[commands].smoke` command and wrote it to `igdev.toml`, but no
verb ever ran it: the only smoke check igdev has is `gateway wait --smoke`, which reads
`[gateway].smoke_endpoints`. A field a person fills in and nothing uses is a promise the
tool does not keep, so the owner decided to remove it:

- `init` no longer has `--command-smoke`, and the `init` Wizard no longer asks for it.
  The Wizard itself stays. Passing the flag is an unknown flag, IGDEV_E_USAGE at exit 2.
- A contract that still states `[commands] smoke = …` loads: schema v1 accepts the key
  and ignores it, so no existing checkout breaks. The next `igdev init` run drops it,
  and its diff shows the removal.

The CLI Contract Version stays 2: no verb or `--json` envelope changes, and existing
contracts keep loading.
