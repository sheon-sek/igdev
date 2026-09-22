# Ignition Development Environment Automation

**English** | [简体中文](README.zh-CN.md)

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
3. **Java JDK** available on `PATH` for the standalone Jython checker and `.modl` metadata inspection (`java` + `jar`). Java 17 is a practical default for an Ignition 8.3 development workstation.
4. **curl or wget** to download the configured Jython standalone JAR from Maven Central.
5. Enough RAM/disk for the Ignition image and Gateway. The default Gateway max heap is 2048 MB.

### Optional

- [`act`](https://github.com/nektos/act) for `./devctl ci-local`.
- A deterministic development Gateway backup (`.gwbk`).
- Private/EA `.modl` files, especially `MCP-module.modl`.

### Ubuntu 26.04 LTS quick install

The commands below are a minimal setup for a fresh Ubuntu 26.04/WSL2 Ubuntu 26.04 development machine.

#### Docker Engine + Compose v2

Use Docker's official `apt` repository:

```bash
sudo apt update
sudo apt install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# Allow the current user to run Docker without sudo.
sudo usermod -aG docker "$USER"
newgrp docker

docker run hello-world
docker compose version
```

#### Java 17

Ubuntu 26.04 provides `openjdk-17-jdk`:

```bash
sudo apt update
sudo apt install -y openjdk-17-jdk
java -version
```

#### `act`

The upstream installer can be used directly:

```bash
curl -s https://raw.githubusercontent.com/nektos/act/master/install.sh | bash
```

Depending on how/where the installer is invoked, the binary may be left at `./bin/act`. If so, make it globally available:

```bash
sudo mv ./bin/act /usr/local/bin/act
sudo chmod +x /usr/local/bin/act
act --version
```

Alternatively, the upstream installer also documents running the script with `sudo bash` for a system-wide installation.

On the **first** `./devctl ci-local`, `act` asks which runner image size to use. Choose **Medium**. It is the default option, is roughly 500 MB, and includes the tools needed to bootstrap most Actions. Large is much heavier; Micro is intentionally incomplete.

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

This creates the git-ignored `.env`, downloads the configured Jython checker once, creates the runtime directories, and stages private modules into the generated `.runtime/docker/modules/` Docker build input. `modules/private/` remains the checkout-local source of truth; `.runtime/` is disposable generated state.

If you do not want the command to record EULA acceptance, use:

```bash
cp .env.example .env
# edit .env manually
./devctl bootstrap
```

### Private module source vs Docker staging

Private modules intentionally have a single user-facing checkout location:

```text
modules/private/                 # user-provided checkout-local source
~/.cache/ignition-devenv/modules/<version>/   # optional reusable global source
        ↓ ./devctl bootstrap / gateway up
.runtime/docker/modules/         # generated disposable Docker build staging
        ↓ docker build
/usr/local/bin/ignition/user-lib/modules/
```

`docker/ignition/` contains only Docker build definitions. It no longer stores generated `.modl` copies. The root Docker build context is tightly constrained by `.dockerignore`, so only the Dockerfile and generated `.runtime/docker/modules/` staging are sent to the build.
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

Inspect the effective module set for this environment. Private `.modl` files are read from their embedded `module.xml`, so the configured module ID and its artifact appear as one logical row with name, version, source and status:

```bash
# Effective built-in + private/third-party modules
./devctl module list

# Only effective built-in modules
./devctl module list --built-in

# Only effective private/third-party modules
./devctl module list --private

# Full Ignition 8.3 built-in module catalog available to GATEWAY_MODULES_ENABLED
./devctl module catalog
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

## Module capability preflight

A large class of Ignition failures can be rejected **before starting a Gateway**. Native scripting checks are now exact rather than namespace-only:

- [`config/native-system-functions.tsv`](config/native-system-functions.tsv) is the Ignition **8.3 Gateway-scope** source of truth used by `devctl`. It contains 392 exact native functions across all 37 documented `system.*` namespaces, expanded from the official Ignition 8.3 System Functions reference.
- Every catalog entry is classified as `platform`, `module`, or `conditional`, with required module IDs recorded when applicable.
- A detected `system.*` call that is not in the Gateway catalog is an error. It is **not silently ignored**; this catches misspellings, non-Gateway functions, and version mismatches early.
- `/data/api/v1/resources/...` paths still resolve module IDs dynamically from the `com.*` segment.
- [`config/capability-modules.tsv`](config/capability-modules.tsv) is reserved for optional project-specific, non-`system.*` aliases so native dependencies cannot drift between two catalogs.

Manual checks:

```bash
# Platform function: valid Gateway API, no optional module required
./devctl module require system.tag.readBlocking

# Module-owned functions
./devctl module require system.report.executeReport
./devctl module require system.security.validateUser
./devctl module require system.roster.getRosters

# Conditional device API: OPC UA base is checked; the concrete device type may
# additionally require its driver module, which cannot be inferred from the call name
./devctl module require system.device.addDevice

# Ignition resource API path: module ID is embedded in the path
./devctl module require '/data/api/v1/resources/names/com.inductiveautomation.opcua/device'

# Explicit module ID escape hatch for a project-specific requirement
./devctl module require module:com.inductiveautomation.reporting
```

A typo or non-Gateway function now fails explicitly:

```text
[module-preflight] ERROR: unknown or non-Gateway Ignition 8.3 native function: system.tag.readBlokcing
[module-preflight] Check the function name, Gateway scope, and target Ignition version.
```

When a required module is missing from `GATEWAY_MODULES_ENABLED`, the check fails with a remediation command:

```text
[module-preflight] ERROR: system.report.executeReport requires com.inductiveautomation.reporting (not enabled by GATEWAY_MODULES_ENABLED)
[module-preflight] Fix: ./devctl module enable com.inductiveautomation.reporting
```

For private/optional modules that are not part of the built-in Docker image catalog, preflight also requires a matching `.modl` artifact. This applies to cataloged APIs such as OPC HDA (`com.inductiveautomation.opccom`), Twilio (`com.inductiveautomation.twilio`), and SECS/GEM (`com.inductiveautomation.secsgem`). The artifact ID is read from the root `module.xml`.

Validate the whole module configuration:

```bash
./devctl module validate
```

Scan source trees for native `system.*` calls and `/data/api/v1/resources/...` references:

```bash
./devctl module scan src/ignition src/fastmcp
```

The scanner recognizes nested native functions such as `system.historian.types.dataPoint`, validates every detected native call against the exact Gateway catalog, and then checks its module requirements.

To make this automatic, configure colon-separated paths in `.env`:

```dotenv
MODULE_REQUIREMENT_PATHS=src/ignition:src/fastmcp
JYTHON_SOURCE_PATHS=scripts/mcp
```

`./devctl check` runs `module validate`, then scans `MODULE_REQUIREMENT_PATHS` plus `JYTHON_SOURCE_PATHS`, and only then enters project checks/Jython compilation. The native catalog itself is also verified by `./devctl self-test` for expected function count, all 37 namespaces, duplicate entries, classification validity, representative module mappings, nested-call scanning, and unknown-function rejection.

This remains a **static pre-Gateway check**. It validates the target Ignition 8.3 Gateway API inventory and configured module/artifact requirements; it does not prove that a running Gateway successfully loaded the module or that a call will succeed with a particular runtime argument. Runtime verification remains `./devctl gateway smoke` and project-specific Gateway/OpenAPI assertions.

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

The defaults in `.env.example` are intentionally a **subset** oriented toward engineering/MCP work:

```dotenv
GATEWAY_MODULES_ENABLED=com.inductiveautomation.perspective,com.inductiveautomation.historian,com.inductiveautomation.opcua,com.inductiveautomation.webdev,com.inductiveautomation.mcp
ACCEPT_MODULE_CERTS=com.inductiveautomation.mcp
ACCEPT_MODULE_LICENSES=
```

`GATEWAY_MODULES_ENABLED` is a comma-delimited whitelist of fully-qualified built-in and third-party module identifiers. When it is non-empty, `./devctl module list --built-in` reports only the built-in modules selected by that environment. Private/third-party identifiers and local `.modl` files are shown by `./devctl module list --private`. With no filter, `module list` shows both groups.

If `GATEWAY_MODULES_ENABLED` is empty, the environment does not apply a module whitelist, so `module list --built-in` treats the full built-in image catalog as effective.

### Ignition 8.3 built-in module identifiers

The following catalog is sourced from the Ignition 8.3 Docker image documentation and is also stored machine-readably in [`config/builtin-modules.tsv`](config/builtin-modules.tsv). Use `./devctl module catalog` to print the same catalog from the CLI.

| Module identifier | Image module file |
|---|---|
| `com.inductiveautomation.alarm-notification` | `Alarm Notification-module.modl` |
| `com.inductiveautomation.opcua.drivers.ablegacy` | `Allen-Bradley Drivers-module.modl` |
| `com.inductiveautomation.opcua.drivers.bacnet` | `BACnet Driver-module.modl` |
| `com.inductiveautomation.opcua.drivers.dnp3` | `DNP3-Driver.modl` |
| `com.inductiveautomation.opcua.drivers.dnp3v2` | `DNP3-Driver-v2.modl` |
| `com.inductiveautomation.eam` | `Enterprise Administration-module.modl` |
| `com.inductiveautomation.eventstream` | `EventStream-module.modl` |
| `com.inductiveautomation.historian` | `Historian-module.modl` |
| `com.inductiveautomation.opcua.drivers.iec61850` | `IEC 61850 Driver-module.modl` |
| `com.inductiveautomation.connectors.kafka` | `Kafka Connector-module.modl` |
| `com.inductiveautomation.opcua.drivers.logix` | `Logix Driver-module.modl` |
| `com.inductiveautomation.opcua.drivers.micro800` | `Micro800 Driver-module.modl` |
| `com.inductiveautomation.opcua.drivers.mitsubishi` | `Mitsubishi-Driver.modl` |
| `com.inductiveautomation.opcua.drivers.modbus` | `Modbus Driver v2-module.modl` |
| `com.inductiveautomation.connectors.mongodb` | `MongoDB Connector-module.modl` |
| `com.inductiveautomation.opcua.drivers.omron` | `Omron-Driver.modl` |
| `com.inductiveautomation.opcua` | `OPC-UA-module.modl` |
| `com.inductiveautomation.perspective` | `Perspective-module.modl` |
| `com.inductiveautomation.reporting` | `Reporting-module.modl` |
| `com.inductiveautomation.sfc` | `SFC-module.modl` |
| `com.inductiveautomation.opcua.drivers.siemens` | `Siemens Drivers-module.modl` |
| `com.inductiveautomation.opcua.drivers.siemens-symbolic` | `Siemens Enhanced Driver-module.modl` |
| `com.inductiveautomation.sms-notification` | `SMS Notification-module.modl` |
| `com.inductiveautomation.sqlbridge` | `SQL Bridge-module.modl` |
| `com.inductiveautomation.historian.sql` | `SQL Historian-module.modl` |
| `com.inductiveautomation.symbol-factory` | `Symbol Factory-module.modl` |
| `com.inductiveautomation.opcua.drivers.tcpudp` | `UDP and TCP Drivers-module.modl` |
| `com.inductiveautomation.vision` | `Vision-module.modl` |
| `com.inductiveautomation.phone-notification` | `Voice Notification-module.modl` |
| `com.inductiveautomation.webdev` | `Web Developer Module.modl` |
| `com.inductiveautomation.jdbc.postgresql` | `PostgreSQL JDBC Driver Module.modl` |
| `com.inductiveautomation.jdbc.mariadb` | `MariaDB JDBC Driver Module.modl` |
| `com.inductiveautomation.jdbc.mssql` | `MSSQL JDBC Driver Module.modl` |

Solution Suite identifiers documented by Ignition are separate selectors rather than individual module IDs:

| Suite | Identifier |
|---|---|
| Application Building Suite | `com.inductiveautomation.suite.application` |
| Industrial Historian Suite | `com.inductiveautomation.suite.historian` |
| DataOps Suite | `com.inductiveautomation.suite.dataops` |
| Enterprise Integration Suite | `com.inductiveautomation.suite.enterprise` |
| Alarm Management Suite | `com.inductiveautomation.suite.alarms` |

Modify the effective module set per project. For a private/third-party `.modl`, keep the file out of Git and add its fully-qualified identifier to `GATEWAY_MODULES_ENABLED` when you want it explicitly enabled/non-quarantined at Gateway launch. Use `ACCEPT_MODULE_CERTS` and `ACCEPT_MODULE_LICENSES` only for modules whose certificate/license terms you have reviewed and accepted.

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
MODULE_REQUIREMENT_PATHS=src/ignition:src/fastmcp
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

The default first run is online so `act` can fetch its runner image/actions. On the first interactive prompt, choose the **Medium** runner image.

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
./devctl module list [--private|--built-in]
./devctl module catalog
./devctl module require <capability> [...]
./devctl module scan <file|dir> [...]
./devctl module validate
./devctl module enable <module-id> [...]
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
