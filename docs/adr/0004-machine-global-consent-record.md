# Consent is a machine-global, human-only record

Ignition EULA, module licenses, and module certificate acceptances are legal statements made by a person, not facts about a checkout. A per-checkout `accepted = true` (the original plan) forced a human acceptance ritual in every new git worktree, which, under heavy parallel agent use, pressures people to script the gate away. We decided to record acceptance once per user+machine in `~/.config/igdev/accepted.toml` (which legal term, when, which CLI version), written only by human-invoked commands; agents that hit missing Consent receive `IGDEV_E_CONSENT_REQUIRED` as a human-required exit with the exact command to run. This stays inside the no-global-project-state rule: Consent is user state, not project state.

Consequences: a fresh worktree of an already consented project can run unattended `igdev setup`; an unauthorized machine still stops at the same gate; per-checkout overrides remain possible but are not the default location.

## Amendment 1: an unattended runner reads a person's exported record (igdev#59)

A GitHub runner, or an act job, has no person and no machine record, so the gate above
stopped every CI run that needed a Gateway. Accepting from flags in CI would be an agent
accepting on a person's behalf, which this ADR forbids.

We decided that a person may carry their own acceptance to a runner, and nothing else:

- `igdev consent export [--output FILE]` writes the record a person recorded on their
  machine (mode 0600 with `--output`). It refuses when the EULA is not accepted there.
- `IGDEV_CONSENT_FILE` names such a file. When it is set, every command reads Consent
  from it instead of `~/.config/igdev/accepted.toml`. igdev never writes it and never
  writes a machine record in that mode, and `setup --accept-*` is a usage error.
- The file is validated entry by entry: a term counts only with a known term id, an
  RFC 3339 `accepted_at` that is not in the future, and the `cli_version` that recorded
  it. A missing or invalid term is still `IGDEV_E_CONSENT_REQUIRED` at exit 3; it names
  the file, and its remediation is `igdev consent export`, a person's step.

Consequences: the person who exported the file stays the one who accepted; the file is a
CI secret they own. Rotating or revoking acceptance is deleting the secret. A runner
never gains consent it was not handed.

## Amendment 2: the EULA gates starting a Gateway, and an environment can carry it (2026-10-08, igdev#101)

Rendering `.igdev/` is not running Ignition, yet `setup` wrote nothing until the EULA was
accepted, so `check`, `test` and `build` failed on every fresh machine. Every
`gateway` verb also re-checked the EULA, even `url` and `down`.

- `setup` always materializes the Checkout Setup and says on stderr when the EULA is
  missing. Only the verbs that start a Gateway — `gateway up`, `reset`, `ensure`,
  `check --gateway`, `ci-local --with-gateway` — stop with `IGDEV_E_CONSENT_REQUIRED` at
  exit 3. A verb that addresses a running Gateway needs no check: it could not be
  running otherwise.
- `IGDEV_ACCEPT_EULA=Y` (also `yes`, `true`, `1`) counts as the EULA acceptance. A person
  sets it once where an environment is defined — a cloud environment's variables, a CI
  secret, a shell profile — the same way the Ignition image reads
  `ACCEPT_IGNITION_EULA=Y`. igdev never writes a record for it, and agents never set it.
  The consent fault names it as the second Remediation.
- The setup Wizard's Consent step reports the record and walks on; it never accepts.
