# Ignition Development Environment Automation

Reusable local development and CI foundation for AI-agent-driven Ignition engineering.

The default profile targets **Ignition 8.3.8** and uses a **Jython 2.7.4 standalone checker** so Claude Code, Codex CLI, OMP CLI, developers, `act`, and GitHub Actions can share one validation contract instead of discovering failures only after a remote CI push.

> This repository does **not** redistribute Ignition, the Early Access MCP module, licensed modules, or Gateway backups. It builds from Inductive Automation's official Docker image and keeps private `.modl`/`.gwbk` files local and git-ignored.

## What this gives you

- Pinned, configurable Ignition Docker runtime.
- Version-scoped private module cache for `MCP-module.modl` and other `.modl` files.
- Local Jython compatibility check using `org.python:jython-standalone`.
- Warm Gateway for fast agent feedback.
- Clean Gateway reset with optional deterministic `.gwbk` baseline restore.
- Worktree-safe Docker project names, volumes, image tags, and automatic local ports.
- A single `./devctl` interface for agents, developers, local CI, and GitHub Actions.
- `act` support for running the same GitHub Actions foundation job locally.
- Safe default bind address (`127.0.0.1`) so the development Gateway is not exposed to the LAN.

## Default runtime

| Component | Default |
|---|---|
| Ignition | `8.3.8` |
| Ignition image | `inductiveautomation/ignition:8.3.8` |
| Jython compatibility checker | `2.7.4` |
| Gateway edition | `standard` |
| Gateway HTTP/HTTPS/debug ports | deterministic `auto` ports per checkout/worktree |
| Gateway bind address | `127.0.0.1` |
| MCP module ID | `com.inductiveautomation.mcp` |

Ignition 8.3.8 updated its embedded Jython runtime from 2.7.3 to 2.7.4. The `JYTHON_VERSION` setting in this repository controls only the **standalone local checker**; it cannot replace the Jython runtime bundled inside an Ignition image.

## Prerequisites

### Required

1. **Linux, WSL2, or macOS** with Bash.
2. **Docker Engine + Docker Compose v2** (`docker compose`, not the legacy Python `docker-compose`).
3. **Java** available on `PATH` for the standalone Jython checker. Java 17 is a practical default for an Ignition 8.3 development workstation.
4. **curl or wget** to download the configured Jython standalone JAR from Maven Central.
5. Enough RAM/disk for the Ignition image and Gateway. The default Gateway max heap is 2048 MB.

### Optional

- [`act`](https://github.com/nektos/act) for `./devctl ci-local`.
- A deterministic development Gateway backup (`.gwbk`).
- Private/EA `.modl` files, especially `MCP-module.modl`.

Run the prerequisite check:

```bash
./devctl doctor
```

`act` is reported as optional; missing Docker/Compose/Java/downloader prerequisites are reported as failures.

## Quick start

### 1. Clone

```bash
git clone https://github.com/sheon-sek/ignition-devenv-automation.git
cd ignition-devenv-automation
```

### 2. Bootstrap and explicitly accept the Ignition EULA

Review Inductive Automation's license, then run:

```bash
./devctl bootstrap --accept-eula
```

This creates the git-ignored `.env`, downloads the configured Jython checker once, creates the runtime directories, and stages any private modules from the version-scoped cache.

If you do not want the command to record EULA acceptance, use:

```bash
cp .env.example .env
# edit .env manually
./devctl bootstrap
```

### 3. Add the MCP EA module (when needed)

The MCP `.modl` is intentionally not stored in Git. Add your local file:

```bash
./devctl module add /path/to/MCP-module.modl
```

Or place reusable modules in the global, version-scoped cache:

```bash
./devctl module cache-path
# example output:
# ~/.cache/ignition-devenv/modules/8.3.8

cp /path/to/MCP-module.modl "$(./devctl module cache-path)/"
./devctl bootstrap
```

List effective private modules:

```bash
./devctl module list
```

### 4. Start the real Gateway

```bash
./devctl gateway up
./devctl gateway wait
./devctl gateway status
```

Print only the URL:

```bash
./devctl gateway url
```

The default `.env` uses `auto` ports, so independent Git worktrees can run separate Gateways without all fighting for `8088`.

### 5. Run a runtime smoke check

```bash
./devctl gateway smoke
```

Inspect logs when something fails:

```bash
./devctl gateway logs --tail 300
./devctl gateway logs -f
```

## Daily agent/developer loop

Use the cheapest relevant layer first:

```bash
# foundation + fast project checks
./devctl check
./devctl test

# Jython syntax/bytecode compatibility, without pretending system.* exists locally
./devctl jython-check path/to/script.py
./devctl jython-check path/to/ignition/scripts/

# real Ignition behavior
./devctl gateway up
./devctl gateway smoke

# aggregate local verification
./devctl verify

# emulate the GitHub Actions foundation job locally
./devctl ci-local
```

A standalone Jython pass does **not** prove that `system.tag.*`, `system.opc.*`, Gateway scope, Java objects, Ignition MCP tools, or installed modules behave correctly. Those paths must be verified against the real Gateway.

## Using a deterministic Gateway baseline

A baseline `.gwbk` lets every clean integration run begin from the same tags, projects, security profiles, device/OPC configuration, and other Gateway state.

Stage a backup locally:

```bash
./devctl baseline set /path/to/dev-baseline.gwbk
```

Then recreate the Gateway from a fresh Docker volume:

```bash
./devctl gateway reset
```

Ignition's Docker `-r` restore is only applied on a **fresh Gateway launch**, which is why `gateway reset` removes the named data volume first.

You can instead make a persistent path part of local configuration:

```dotenv
GATEWAY_BACKUP=/absolute/path/to/dev-baseline.gwbk
```

Check or clear the staged backup:

```bash
./devctl baseline status
./devctl baseline clear
```

## Changing the Ignition version

Do **not** edit the Dockerfile. Edit the git-ignored `.env`:

```dotenv
IGNITION_VERSION=8.3.9
JYTHON_VERSION=2.7.4
```

Then rebuild from clean Gateway state:

```bash
./devctl bootstrap
./devctl versions
./devctl gateway reset
```

`IGNITION_VERSION` selects the official `inductiveautomation/ignition:<version>` image and also selects the version-specific global private-module cache.

### Important: module compatibility

When changing Ignition versions, verify that every third-party/EA module you stage is compatible with that exact Ignition version. Keep different module builds in different cache folders:

```text
~/.cache/ignition-devenv/modules/
├── 8.3.8/
│   └── MCP-module.modl
└── 8.3.9/
    └── MCP-module.modl
```

## Changing the Jython checker version

Edit only `.env`:

```dotenv
JYTHON_VERSION=2.7.3
```

Then:

```bash
./devctl bootstrap
./devctl versions
```

The required JAR is downloaded on demand from Maven Central into `.runtime/tools/jython/`.

Again, this changes the **local compatibility checker only**. The real Jython runtime remains whatever the selected Ignition release embeds.

The repository emits compatibility warnings for the known Ignition 8.3.8/8.3.9 -> Jython 2.7.4 relationship and for earlier 8.3.x targets when the configured checker is suspicious. Treat the official release notes as the source of truth when targeting another version.

## Built-in/third-party module configuration

The defaults in `.env.example` are oriented toward the engineering/MCP environment:

```dotenv
GATEWAY_MODULES_ENABLED=com.inductiveautomation.perspective,com.inductiveautomation.historian,com.inductiveautomation.opcua,com.inductiveautomation.webdev,com.inductiveautomation.mcp
ACCEPT_MODULE_CERTS=com.inductiveautomation.mcp
ACCEPT_MODULE_LICENSES=
```

Modify these for each project's required module set. Ignition 8.3 supports `GATEWAY_MODULES_ENABLED`, `ACCEPT_MODULE_CERTS`, and `ACCEPT_MODULE_LICENSES` for containerized module lifecycle/acceptance.

For an unsigned development module only, explicitly opt in locally:

```dotenv
IGNITION_ALLOW_UNSIGNED_MODULES=true
```

Do not make that a production default.

## Integrating this foundation into a real project

The repository contains two lightweight hooks and optional command adapters so a project can keep one stable interface regardless of whether the active agent is Claude Code, Codex CLI, OMP CLI, a developer, `act`, or GitHub Actions.

### Hook files

- `hooks/check.sh` — fast, non-destructive checks.
- `hooks/test.sh` — broader project tests.
- `hooks/gateway-smoke.sh` — assertions against the running real Gateway. `GATEWAY_URL` is injected automatically.

### Command adapters in `.env`

Examples:

```dotenv
PROJECT_CHECK_CMD=./gradlew :gateway:test
PROJECT_TEST_CMD=python -m pytest -q
JYTHON_SOURCE_PATHS=src/ignition:scripts/mcp
```

`JYTHON_SOURCE_PATHS` is colon-separated.

For an Ignition module project, you can make the existing build command part of the hook instead of teaching each agent a different Gradle invocation.

The intended contract is:

```text
Agent implementation
    -> ./devctl check
    -> ./devctl test
    -> ./devctl jython-check ... (when relevant)
    -> ./devctl gateway smoke (when runtime behavior matters)
    -> ./devctl ci-local
    -> push / GitHub Actions
```

## Warm vs clean Gateway

### Warm loop

Use:

```bash
./devctl gateway up
```

The named Docker volume persists Gateway state. This is the fast feedback environment.

### Clean loop

Use:

```bash
./devctl gateway reset
```

This removes the worktree's Gateway data volume, rebuilds/starts the Gateway, applies the configured baseline backup on first launch, and waits for the Gateway to answer.

This is the pre-commit/pre-PR integration environment when clean state matters.

## Parallel Git worktrees / subagents

Every checkout derives a deterministic instance ID from its absolute path. Unless you override the values in `.env`, that ID controls:

- Docker Compose project name.
- Gateway named volume/network namespace.
- local image tag.
- automatic HTTP/HTTPS/debug host ports.
- default Gateway name.

This prevents two subagents working in separate worktrees from silently sharing Gateway state.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Local GitHub Actions with `act`

Install `act`, then:

```bash
./devctl ci-local
```

The default first run is online so `act` can fetch its runner image/actions.

After the required images/actions are already cached, set:

```dotenv
ACT_OFFLINE=1
```

Then `devctl` adds:

```text
--pull=false --action-offline-mode
```

This makes local CI reproduction faster and reduces repeated downloads. `act` is still an emulator rather than a perfect GitHub-hosted runner replacement, so GitHub Actions remains the final CI authority.

## GitHub Actions

Two workflows are included:

- `foundation-ci` — fast validation on push/PR; runs `./devctl self-test` and does **not** download/start Ignition.
- `gateway-smoke` — manual workflow for explicitly testing a selected Ignition version against a clean base Gateway without private MCP modules.

This avoids spending GitHub Actions time pulling the full Ignition runtime for every small change while preserving an explicit remote smoke-test path.

## `devctl` command reference

```text
./devctl doctor
./devctl bootstrap [--accept-eula]
./devctl versions
./devctl self-test
./devctl check
./devctl test
./devctl verify
./devctl ci-local

./devctl jython-check <file|dir> [...]

./devctl module add <file.modl>
./devctl module list
./devctl module cache-path
./devctl module clear

./devctl baseline set <file.gwbk>
./devctl baseline clear
./devctl baseline status

./devctl gateway up
./devctl gateway down [--volumes]
./devctl gateway reset
./devctl gateway restart
./devctl gateway wait [--timeout SEC]
./devctl gateway smoke
./devctl gateway status
./devctl gateway logs [docker-compose-log-options]
./devctl gateway url
```

## Security and licensing boundaries

- The default Gateway binds to loopback only.
- `.env`, `.gwbk`, private `.modl`, downloaded tools, and runtime state are git-ignored.
- Do not place production credentials in `.env`.
- Do not point test hooks at production/staging systems unless a task explicitly requires and authorizes it.
- Passing `ACCEPT_IGNITION_EULA=Y` means you are accepting Inductive Automation's license terms; this repository does not do so silently.
- Third-party module certificates/licenses remain the user's responsibility.

## Troubleshooting

See [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md). The first diagnostic commands are usually:

```bash
./devctl doctor
./devctl versions
./devctl gateway status
./devctl gateway logs --tail 300
```
