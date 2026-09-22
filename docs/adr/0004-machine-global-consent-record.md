# Consent is a machine-global, human-only record

Ignition EULA, module licenses, and module certificate acceptances are legal statements made by a person, not facts about a checkout. A per-checkout `accepted = true` (the original plan) forced a human acceptance ritual in every new git worktree, which, under heavy parallel agent use, pressures people to script the gate away. We decided to record acceptance once per user+machine in `~/.config/igdev/accepted.toml` (which legal term, when, which CLI version), written only by human-invoked commands; agents that hit missing Consent receive `IGDEV_E_CONSENT_REQUIRED` as a human-required exit with the exact command to run. This stays inside the no-global-project-state rule: Consent is user state, not project state.

Consequences: a fresh worktree of an already consented project can run unattended `igdev setup`; an unauthorized machine still stops at the same gate; per-checkout overrides remain possible but are not the default location.
