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
