# igdev — the Ignition local-development toolchain

**English** | [简体中文](README.zh-CN.md)

`igdev` is one globally installed Go binary that owns the Ignition local-development
engine, the Ignition domain knowledge, and the agent-facing contract. A repository
carries exactly two tracked things — an `igdev.toml` **Project Contract** and one
`.gitignore` line — plus disposable, gitignored state in `.igdev/`. Nothing about the
toolchain is vendored, so a tool fix is one binary upgrade instead of a rewrite in every
repository.

Agents drive it through one JSON envelope and named `IGDEV_E_*` codes; people get
detailed per-command help and a Wizard where an argument is genuinely hard to invent.
Both dialects come from the same command definitions, which is also what the generated
reference in [`docs/reference/`](docs/reference/README.md) is rendered from.

## Install

```bash
curl -fsSL https://github.com/sheon-sek/igdev/releases/latest/download/install.sh | bash
```

The installer detects the OS and architecture (`linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64`; WSL uses the Linux binary, Windows native is
unsupported), verifies the release checksum, and puts `igdev` in `~/.local/bin`.
`--version`, `--prefix`, `--repo-url`, and `--base-url` override the defaults. igdev
never updates itself: it only notices a newer release once a day.

Check the machine before anything else, and install the Agent Skill:

```bash
igdev doctor              # read-only host audit; never fails
igdev agent skill-install # the workflow document this binary ships
```

Docker with the Compose plugin is required to run a Gateway; a JVM is required for the
Jython compatibility check; Gradle and `act` are optional, because only a project that
declares a Gradle command or runs its workflows locally needs them.

> igdev does not redistribute Ignition, modules, or backups. The Gateway is built from
> Inductive Automation's official Docker image, and private `.modl`/`.gwbk` files stay
> local and gitignored.

## The lifecycle

Only the first verb touches tracked files, and only the second creates the state a
Gateway runs from. Keeping them apart is what makes a fresh clone reproducible.

### `igdev init` writes the Project Contract

`igdev.toml` declares what this repository needs: the Ignition and Jython versions, the
enabled module whitelist, the paths to scan, the Gateway heap and timezone, and the
project's own `check`/`test`/`build`/`smoke` commands. The first run writes schema v1
defaults; every later run is an edit that preserves what the file already holds. Each
write prints a unified diff — review it, because a Contract change moves the Contract
Digest and makes the checkout's setup stale on purpose.

In a terminal, a repository with no contract gets a Wizard that reads the layout and
pre-fills `[commands]` and `[scan]`. `--yes` takes the same defaults silently, and
`--json` never prompts.

### `igdev setup` materializes the checkout

`setup` writes `.igdev/`: a random UUID Instance identity (never derived from the
checkout path, so parallel worktrees cannot collide), dynamically allocated loopback
ports, the Compose file and Docker build context rendered from embedded templates, the
generated Gateway admin password, and the Setup Stamp that ties the state to the
contract's digest. Nothing here is tracked, and deleting `.igdev/` only means running
`setup` again.

Running a Gateway is governed by the Ignition EULA, so Consent is recorded once per
machine by a human-invoked command. An automated run that finds it missing stops at exit
level 3 with `IGDEV_E_CONSENT_REQUIRED` and the exact command to hand to a person.

### Every other command passes the Gate

A project command discovers the Project Root, validates the contract schema, validates
the Setup Stamp, and checks its own prerequisites. A missing or stale setup is
`IGDEV_E_SETUP_REQUIRED` or `IGDEV_E_SETUP_STALE`, and `igdev setup` is the repair.
`igdev status` is the exception that always works: it reports the state instead of
enforcing it.

## The daily loop

```bash
igdev status                          # where am I, what is configured
igdev check                           # the fixed preflight pipeline
igdev test
igdev build                           # re-stages the modules the Gateway mounts
igdev verify --gateway                # everything, plus the runtime half

igdev module require system.report.executeReport   # capability preflight
igdev module scan ignition/script-python
igdev jython check src/main/python

igdev gateway up && igdev gateway wait && igdev gateway smoke
igdev gateway url                     # the only URL to script against
igdev gateway logs --tail 200
igdev gateway down --volumes
```

`igdev check` always runs the same stages in the same order: module validate, capability
scan, the project's declared check command, then a batched Jython compile in one JVM.
`test`, `build`, and `verify` dispatch the `[commands]` stages the repository declares,
so project tooling stays the source of truth and igdev stays an orchestrator.

Never assume port 8088: `igdev gateway url` (or `ports` in any JSON envelope) reads the
port this Instance actually owns.

Configuration resolves from the highest tier that names a key: command-line flags, then
`IGDEV_*` environment variables, then `.igdev/local.toml`, then the tracked
`igdev.toml`, then embedded defaults. No config file is ever executed, and `--config
key=value` overrides one key for a single run.

## Working with an agent

Pass `--json` and stdout is a single envelope, with progress and notices on stderr:

```json
{"ok": false, "contract": "1", "code": "IGDEV_E_CONSENT_REQUIRED",
 "message": "…", "remediation": [{"command": "igdev setup --accept-eula", "why": "human-only consent"}],
 "data": {}}
```

Exit levels are part of the contract: **0** success, **1** command failure (the code says
which), **2** usage error (a missing argument names the flag), **3** human action
required — stop and hand off rather than retrying. When every argument is supplied, igdev
runs in Silent Mode and never prompts, whatever stdin is.

The orientation call is `igdev agent context --json`: one call returns the lifecycle
state and Consent, the versions in play, the Instance and its ports, whether the Gateway
is running, the staged modules, the catalog digests, and which project verbs are
available. It works before `init` and `setup`. Its fields are documented in
[`docs/reference/agent-context.md`](docs/reference/agent-context.md).

`igdev agent skill-install` writes the workflow skill this binary ships (globally by
default, `--scope repo` into the repository), with the CLI Contract Version in its
frontmatter so the guidance can never contradict the tool.

To reproduce a CI job locally, `igdev ci-local --job <name>` runs it through `act`.

## Developing this repository

This repository is igdev's home, and it dogfoods the toolchain it ships: `igdev.toml` is
the Project Contract the installed binary reads, and `.igdev/` is its disposable
Checkout Setup. The contract marks `check` as `make fmt-check vet`, so the CLI gate and
CI run the same commands.

```bash
make all           # gofmt check, go vet, the whole test suite (resource gates included)
make test-quick    # the suite without the timing, packaging, and installer gates
make gates         # the resource and hygiene gates alone
make package       # release tarballs and checksums under dist/
make goldens       # rewrite the frozen agent contract after an intentional change
make reference     # regenerate docs/reference from the command definitions
```

Agent instructions for working in this tree live in [`AGENTS.md`](AGENTS.md).

## Documentation

- [`docs/reference/`](docs/reference/README.md) — the public command reference, one page
  per command, generated from the command definitions by `go run ./cmd/igdev-docs`
  (`make reference`). Do not hand-edit it; a drift check fails the build.
- [`docs/IGDEV.md`](docs/IGDEV.md) — the design and development notes for this tree.
- [`CONTEXT.md`](CONTEXT.md) — the normative vocabulary; [`docs/adr/`](docs/adr) — the
  hard-to-reverse decisions.
- [`README.zh-CN.md`](README.zh-CN.md) — the Chinese onboarding guide (it points here
