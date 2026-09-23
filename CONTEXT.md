# igdev

The domain language for igdev, a toolchain that manages Ignition local development environments. Repositories declare what they need in a tracked contract; igdev materializes disposable runtime per checkout. igdev was previously the repo-vendored `devctl` in this foundation.

## Language

### Environment lifecycle

**Project Contract**:
A tracked, per-repository declaration of how that repository uses igdev. Lives in `igdev.toml` at the project root.
_Avoid_: manifest, project config, `.env`

**Checkout Setup**:
A gitignored, per-checkout-or-worktree record that the Project Contract has been materialized locally, plus its generated runtime state. Lives in `.igdev/`.
_Avoid_: runtime dir, bootstrap state

**Instance**:
The pairing of one Project Contract with one Checkout Setup. Owns exactly one runtime namespace: compose project name, gateway name, published ports, gateway volume.
_Avoid_: environment, sandbox, workspace

**Contract Digest**:
The content hash of the Project Contract bytes. Decides whether a Checkout Setup is current or stale.
_Avoid_: project fingerprint (ambiguous; it was used for both the digest and the instance identity)

**Setup Stamp**:
What a Checkout Setup records to prove currency: the Contract Digest, the contract schema version, and the CLI contract version.
_Avoid_: lock file

**Consent**:
A human-created record that a legal term (Ignition EULA, module license, module certificate) was accepted. Bound to the user and machine, not to a checkout. An agent may read Consent state; only a human may create it.
_Avoid_: acceptance flag, license env var

**Project Root**:
The directory containing `igdev.toml`. The stop point of the upward search every project command begins with.
_Avoid_: tool dir (the old `ROOT_DIR` conflated program location with project location)

### Capability knowledge

**Capability**:
Something project code intends to use through the Ignition runtime: a `system.*` function or a REST endpoint.
_Avoid_: feature, API call

**Core Catalog**:
The capability knowledge bundled into the installed igdev binary, keyed by Ignition version. Covers the native function map, the capability rules, and the REST catalog.
_Avoid_: built-in config, repo catalogs

**Project Overlay**:
Tracked, project-held capability knowledge that extends the Core Catalog, typically contributed by private or third-party modules.
_Avoid_: custom catalog, patch

**Effective Catalog**:
The resolution of Core Catalog plus Project Overlay. What preflight checks consult.
_Avoid_: merged config

**Private Module**:
A licensed or EA `.modl` that is never committed. Lives in the Checkout Setup, or in the global cache only when explicitly opted into.
_Avoid_: custom module, vendor module

**Baseline**:
A `.gwbk` backup used to seed a disposable Gateway.
_Avoid_: snapshot, restore file

**Capacity Gate**:
A machine-level limit that refuses to start a new Instance when free system memory falls below that Instance's requested heap plus headroom.
_Avoid_: OOM guard, resource quota

**Preflight**:
Read-only capability and module verification that runs before a Gateway starts or code is checked.
_Avoid_: validation (overloaded with manifest validation)

### Agent contract

**Gate**:
The mandatory first-stage checks every project command passes before doing work: discovery, contract schema, Setup Stamp, then command prerequisites.
_Avoid_: middleware, boot check

**Wizard**:
An interactive flow that triggers only when a required value is missing, stdin is a TTY, and `--json` is absent. Exists for `init`, `setup`, and `module add` only.
_Avoid_: TUI, prompt mode

**Silent Mode**:
A fully specified invocation with no prompts, which is what agents must always use.
_Avoid_: non-interactive (that is the enabling property, not the mode itself)

**Remediation**:
A machine-readable next step inside an error result: the exact command that clears the failure.
_Avoid_: hint, suggestion

**Agent Skill**:
The workflow document bundled with igdev and installed by `igdev agent skill-install`, never by setup. Its entry teaches ordering, boundaries, and error handling. Beside the entry sit the command reference and the error-code reference, which an agent reads only when a task needs them. It carries no API catalogs.
_Avoid_: README block, cheat sheet

**CLI Contract Version**:
The stability epoch of the machine-facing interface: JSON envelope, error codes, exit codes. Bumps when any of those break, independent of release semver.
_Avoid_: schema version (that names the Project Contract schema)
