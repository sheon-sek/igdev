# Ignition 开发环境自动化

[English](README.md) | **简体中文**

这是一个面向 AI Agent 驱动的 Ignition 工程开发、测试与 CI 的可复用底层环境。

默认配置针对 **Ignition 8.3.8**，并使用 **Jython 2.7.4 standalone checker**。目标是让 Claude Code、Codex CLI、OMP CLI、开发者、`act` 与 GitHub Actions 共用同一套验证协议，尽量把错误留在本地开发循环，而不是等到 push 后才在远端 CI 发现。

> 本仓库不会重新分发 Ignition、Early Access MCP module、授权模块或 Gateway backup。Ignition runtime 来自 Inductive Automation 官方 Docker image；私有 `.modl` 与 `.gwbk` 都保持在本机并由 Git 忽略。

## 这套环境提供什么

- 固定且可配置的 Ignition Docker runtime。
- 按 Ignition 版本隔离的私有 module cache，可放 `MCP-module.modl` 与其他 `.modl`。
- 使用 `org.python:jython-standalone` 的本地 Jython 兼容性检查。
- Warm Gateway：用于 Agent 高频、快速反馈。
- Clean Gateway reset：可配合固定 `.gwbk` baseline 做可重复测试。
- Git worktree 隔离：不同 worktree 自动使用不同 Compose project、volume、image tag、Gateway 名称与本地端口。
- 单一 `./devctl` 接口，供 Agent、开发者、`act` 与 GitHub Actions 共用。
- 支持本地执行 GitHub Actions 的 `act`。
- 默认仅绑定 `127.0.0.1`，避免开发 Gateway 意外暴露到局域网。

## 默认版本

| 项目 | 默认值 |
|---|---|
| Ignition | `8.3.8` |
| Ignition image | `inductiveautomation/ignition:8.3.8` |
| Jython compatibility checker | `2.7.4` |
| Gateway edition | `standard` |
| Gateway HTTP/HTTPS/debug port | 每个 checkout/worktree 自动生成确定性端口 |
| Gateway bind address | `127.0.0.1` |
| MCP module ID | `com.inductiveautomation.mcp` |

Ignition 8.3.8 将其内建 Jython 从 2.7.3 更新到 2.7.4。仓库中的 `JYTHON_VERSION` **只控制本地 standalone checker**，不能替换 Ignition image 内部自带的 Jython runtime。

## 前置条件

### 必须安装

1. **Linux、WSL2 或 macOS**，并有 Bash。
2. **Docker Engine + Docker Compose v2**，即 `docker compose`，不是旧版 Python `docker-compose`。
3. **Java JDK** 在 `PATH` 中，用于 standalone Jython checker 和 `.modl` 元数据读取（需要 `java` 与 `jar`）。Ignition 8.3 开发机建议使用 Java 17。
4. **curl 或 wget**，用于从 Maven Central 下载 Jython standalone JAR。
5. 足够的 RAM 与磁盘空间用于 Ignition image 与 Gateway。默认 Gateway 最大 heap 为 2048 MB。

### 可选

- [`act`](https://github.com/nektos/act)，用于 `./devctl ci-local`。
- 一份确定性的开发 Gateway backup（`.gwbk`）。
- 私有/EA `.modl`，尤其是 `MCP-module.modl`。

## Ubuntu 26.04 LTS 基础安装

以下适用于干净的 Ubuntu 26.04 LTS，也适合 WSL2 中的 Ubuntu 26.04。

### 安装 Docker Engine + Docker Compose v2

使用 Docker 官方 `apt` repository：

```bash
sudo apt update
sudo apt install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF_DOCKER
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF_DOCKER

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

让当前用户可以不使用 `sudo` 调用 Docker：

```bash
sudo usermod -aG docker "$USER"
newgrp docker
```

验证：

```bash
docker run hello-world
docker compose version
```

### 安装 Java 17

Ubuntu 26.04 官方仓库提供 `openjdk-17-jdk`：

```bash
sudo apt update
sudo apt install -y openjdk-17-jdk
java -version
```

### 安装 `act`

可使用 upstream installer：

```bash
curl -s https://raw.githubusercontent.com/nektos/act/master/install.sh | bash
```

在某些调用方式下，installer 会把二进制放到当前目录的 `./bin/act`。如果出现这种情况，需要手动移到全局 PATH：

```bash
sudo mv ./bin/act /usr/local/bin/act
sudo chmod +x /usr/local/bin/act
act --version
```

upstream 也提供直接通过 `sudo bash` 做 system-wide install 的方式。

**第一次执行：**

```bash
./devctl ci-local
```

`act` 会询问默认 runner image 大小：**请选择 `Medium`**。Medium 是默认项，约 500 MB，并包含启动多数 Actions 所需的工具。Large 非常大；Micro 刻意只保留极少工具，兼容性不足。

## Quick Start

### 1. Clone

```bash
git clone https://github.com/sheon-sek/ignition-devenv-automation.git
cd ignition-devenv-automation
```

### 2. 检查环境

```bash
./devctl doctor
```

### 3. Bootstrap，并明确接受 Ignition EULA

阅读 Inductive Automation 的 license 后执行：

```bash
./devctl bootstrap --accept-eula
```

它会：

- 从 `.env.example` 生成 Git ignored 的 `.env`。
- 下载当前配置的 Jython standalone checker。
- 创建 `.runtime/`。
- 把当前 Ignition 版本对应的私有 modules staging 到生成态的 `.runtime/docker/modules/`。
- 生成 worktree-specific Compose runtime config。

`modules/private/` 是 checkout-local 的私有 module source of truth；`.runtime/` 则全部属于可删除、可重建的生成态。

如果不想让命令自动记录 EULA acceptance：

```bash
cp .env.example .env
# 手动编辑 .env
./devctl bootstrap
```

### Private module source 与 Docker staging

私有 module 在 repo 内只有一个用户需要直接管理的位置：

```text
modules/private/                 # 用户提供的 checkout-local source
~/.cache/ignition-devenv/modules/<version>/   # 可选的全局复用 source
        ↓ ./devctl bootstrap / gateway up
.runtime/docker/modules/         # 自动生成、可删除的 Docker build staging
        ↓ docker build
/usr/local/bin/ignition/user-lib/modules/
```

`docker/ignition/` 现在只保存 Docker build definition，不再保存生成出来的 `.modl` 副本。Docker build context 虽然改为 repo root，但 `.dockerignore` 会严格限制实际送入 build 的内容，只保留 Dockerfile 与 `.runtime/docker/modules/`。
### 4. 添加 MCP EA module

MCP `.modl` 不提交 Git：

```bash
./devctl module add /path/to/MCP-module.modl
```

也可以放入全局、按 Ignition 版本隔离的 cache：

```bash
./devctl module cache-path
# 例如：
# ~/.cache/ignition-devenv/modules/8.3.8

cp /path/to/MCP-module.modl "$(./devctl module cache-path)/"
./devctl bootstrap
```

查看当前环境 module。Private `.modl` 会直接读取内部根目录的 `module.xml`，因此配置里的 module ID 与磁盘上的 artifact 会合并成同一条记录，并显示名称、版本、来源和状态：

```bash
# 当前环境所有有效 built-in + private/third-party
./devctl module list

# 只看当前有效 built-in
./devctl module list --built-in

# 只看当前有效 private/third-party
./devctl module list --private

# 查看 Ignition 8.3 Docker image 支持的完整 built-in catalog
./devctl module catalog
```

### 5. 启动真实 Ignition Gateway

```bash
./devctl gateway up
./devctl gateway wait
./devctl gateway status
```

只输出 URL：

```bash
./devctl gateway url
```

默认使用 `auto` port，所以多个 Git worktree 可以同时启动各自 Gateway，而不需要争抢宿主机 `8088`。

### 6. Runtime smoke test

```bash
./devctl gateway smoke
```

失败时先看日志：

```bash
./devctl gateway logs --tail 300
./devctl gateway logs -f
```

## 日常 Agent / Developer Loop

优先运行成本最低的验证：

```bash
./devctl check
./devctl test

# Jython syntax/bytecode compatibility
./devctl jython-check path/to/script.py
./devctl jython-check path/to/ignition/scripts/

# 需要真实 Ignition runtime 时
./devctl gateway up
./devctl gateway smoke

# 聚合验证
./devctl verify

# 在本机复现 GitHub Actions foundation job
./devctl ci-local
```

Standalone Jython checker 通过，**不代表** `system.tag.*`、`system.opc.*`、Gateway scope、Java object、Ignition MCP tools 或安装模块在真实 Ignition 中一定正确。涉及这些能力时必须对真实 Gateway 做验证。

## Module 能力预检查

大量 Ignition 错误应该在 **Gateway 启动之前**就被挡住。现在 native scripting 检查不再只是 namespace/prefix 推测，而是 exact function 检查：

- [`config/native-system-functions.tsv`](config/native-system-functions.tsv) 是 `devctl` 使用的 Ignition **8.3 Gateway scope** 原生函数 source of truth。当前收录官方 8.3 System Functions 文档中的 **37 个 `system.*` namespace、392 个展开后的 exact native function**。
- 每个函数明确分类为 `platform`、`module` 或 `conditional`；需要额外 module 时同时记录完整 module ID。
- 扫描到的 `system.*` 如果不在 Gateway catalog 中会直接报错，**不会再静默跳过**。因此拼写错误、非 Gateway scope 函数、目标版本不匹配都会更早暴露。
- `/data/api/v1/resources/...` 仍可从路径中的 `com.*` segment 动态识别 module ID。
- [`config/capability-modules.tsv`](config/capability-modules.tsv) 只保留给项目自定义的非 `system.*` capability alias，避免 native dependency 出现两份 source of truth。

手动检查示例：

```bash
# Platform function：函数有效，不需要额外可选 module
./devctl module require system.tag.readBlocking

# 由 module 提供的 native functions
./devctl module require system.report.executeReport
./devctl module require system.security.validateUser
./devctl module require system.roster.getRosters

# Conditional device API：先检查 OPC UA 基础 module；
# 实际 device type 可能还需要具体 Driver module，单凭函数名无法静态推断
./devctl module require system.device.addDevice

# Resource API：module ID 直接嵌在路径中
./devctl module require '/data/api/v1/resources/names/com.inductiveautomation.opcua/device'

# 项目特殊 requirement 的显式 escape hatch
./devctl module require module:com.inductiveautomation.reporting
```

拼错函数名或使用非 Gateway scope 函数现在会明确失败：

```text
[module-preflight] ERROR: unknown or non-Gateway Ignition 8.3 native function: system.tag.readBlokcing
[module-preflight] Check the function name, Gateway scope, and target Ignition version.
```

如果所需 module 没有出现在 `GATEWAY_MODULES_ENABLED`，仍会给出直接修复命令：

```text
[module-preflight] ERROR: system.report.executeReport requires com.inductiveautomation.reporting (not enabled by GATEWAY_MODULES_ENABLED)
[module-preflight] Fix: ./devctl module enable com.inductiveautomation.reporting
```

对于不属于 built-in Docker image catalog 的私有/可选 module，preflight 还会要求本地存在匹配的 `.modl` artifact。例如 OPC HDA (`com.inductiveautomation.opccom`)、Twilio (`com.inductiveautomation.twilio`) 与 SECS/GEM (`com.inductiveautomation.secsgem`)。Module ID 从 `.modl` 根目录的 `module.xml` 读取。

检查整个 module 配置：

```bash
./devctl module validate
```

扫描源码中的 native `system.*` 与 `/data/api/v1/resources/...`：

```bash
./devctl module scan src/ignition src/fastmcp
```

scanner 现在也能识别 `system.historian.types.dataPoint` 这种多层 native function；每一个扫描到的 native call 都先做 exact Gateway catalog validation，再做 module requirement validation。

要自动接入日常检查，在 `.env` 配置：

```dotenv
MODULE_REQUIREMENT_PATHS=src/ignition:src/fastmcp
JYTHON_SOURCE_PATHS=scripts/mcp
```

`./devctl check` 会先执行 `module validate`，再扫描 `MODULE_REQUIREMENT_PATHS` + `JYTHON_SOURCE_PATHS`，然后才进入项目检查与 Jython compilation。`./devctl self-test` 还会验证 catalog 的函数数量、37 个 namespace、重复项、classification、代表性 module mapping、nested-call scanner，以及 unknown-function rejection。

这仍然属于 **pre-Gateway 静态检查**：它证明目标 Ignition 8.3 Gateway native API inventory 与当前 module/artifact 配置相符，但不证明运行中的 Gateway 已成功加载 module，也无法仅从函数名判断所有 runtime 参数（例如具体 device driver）。最终 runtime verification 仍由 `./devctl gateway smoke` 和项目自己的 Gateway/OpenAPI assertions 完成。

## 确定性的 `.gwbk` baseline

准备一份包含测试 tags、projects、security、device/OPC config 等固定状态的开发 backup：

```bash
./devctl baseline set /path/to/dev-baseline.gwbk
```

然后从全新 Docker volume 重建：

```bash
./devctl gateway reset
```

Ignition Docker 的 `-r` restore 只在 **fresh Gateway launch** 应用，因此 `gateway reset` 会先删除该 worktree 的 Gateway data volume。

也可以在 `.env` 固定路径：

```dotenv
GATEWAY_BACKUP=/absolute/path/to/dev-baseline.gwbk
```

查看或清除：

```bash
./devctl baseline status
./devctl baseline clear
```

## 修改 Ignition 版本

**不要修改 Dockerfile。** 修改 Git ignored 的 `.env`：

```dotenv
IGNITION_VERSION=8.3.9
JYTHON_VERSION=2.7.4
```

然后：

```bash
./devctl bootstrap
./devctl versions
./devctl gateway reset
```

`IGNITION_VERSION` 同时决定：

- `inductiveautomation/ignition:<version>` runtime image。
- 该 Ignition 版本对应的 global private module cache 目录。

### Module compatibility

切换 Ignition 版本时，EA/third-party module 必须确认与目标 Ignition 版本兼容。不同版本的 module 应放在不同 cache：

```text
~/.cache/ignition-devenv/modules/
├── 8.3.8/
│   └── MCP-module.modl
└── 8.3.9/
    └── MCP-module.modl
```

## 修改 Jython checker 版本

只改 `.env`：

```dotenv
JYTHON_VERSION=2.7.3
```

然后：

```bash
./devctl bootstrap
./devctl versions
```

需要的 JAR 会按需下载到 `.runtime/tools/jython/`。

再次强调：这只改变**本地兼容性 checker**。真实 Jython runtime 仍由目标 Ignition release 决定。

## Built-in / Third-party Module 配置

`.env.example` 默认只启用工程/MCP 场景需要的一个**子集**：

```dotenv
GATEWAY_MODULES_ENABLED=com.inductiveautomation.perspective,com.inductiveautomation.historian,com.inductiveautomation.opcua,com.inductiveautomation.webdev,com.inductiveautomation.mcp
ACCEPT_MODULE_CERTS=com.inductiveautomation.mcp
ACCEPT_MODULE_LICENSES=
```

`GATEWAY_MODULES_ENABLED` 是由 fully-qualified module ID 组成的逗号分隔 whitelist，可同时包含 built-in 与 third-party module。

行为定义：

- `./devctl module list`：当前环境有效 built-in + private/third-party。
- `./devctl module list --built-in`：当前有效 built-in。
- `./devctl module list --private`：当前 private/third-party module；按 module ID 合并，并显示 artifact 元数据、来源与状态。
- `./devctl module catalog`：Ignition 8.3 Docker image 内建 module 的完整可选清单。

如果 `GATEWAY_MODULES_ENABLED` 为空，则环境不施加 module whitelist，因此 `module list --built-in` 会把完整 built-in image catalog 视为有效集合。

### Ignition 8.3 Docker image built-in module 完整清单

该清单同步保存在 [`config/builtin-modules.tsv`](config/builtin-modules.tsv)，供 `devctl` 本身使用。

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

Ignition 8.3 还定义以下 Solution Suite identifier；这些不是单个 module ID：

| Suite | Identifier |
|---|---|
| Application Building Suite | `com.inductiveautomation.suite.application` |
| Industrial Historian Suite | `com.inductiveautomation.suite.historian` |
| DataOps Suite | `com.inductiveautomation.suite.dataops` |
| Enterprise Integration Suite | `com.inductiveautomation.suite.enterprise` |
| Alarm Management Suite | `com.inductiveautomation.suite.alarms` |

Private/third-party `.modl` 仍保持在 Git 之外。需要它在 Gateway launch 时被明确启用/解除 quarantine 时，把其 fully-qualified module ID 加入 `GATEWAY_MODULES_ENABLED`；只有在已经阅读并接受相应条款/证书后，才把 ID 放入 `ACCEPT_MODULE_CERTS` / `ACCEPT_MODULE_LICENSES`。

仅用于本地未签名开发模块时，可显式开启：

```dotenv
IGNITION_ALLOW_UNSIGNED_MODULES=true
```

不要把它当生产默认值。

## 接入真实项目

仓库提供 hooks 与 `.env` command adapters，让不同 Agent 不需要各自发明 build/test 命令。

### Hooks

- `hooks/check.sh`：快速、非破坏性验证。
- `hooks/test.sh`：更完整的项目测试。
- `hooks/gateway-smoke.sh`：对真实运行 Gateway 做断言；`GATEWAY_URL` 会自动注入。

### `.env` command adapters

例如：

```dotenv
PROJECT_CHECK_CMD=./gradlew :gateway:test
PROJECT_TEST_CMD=python -m pytest -q
JYTHON_SOURCE_PATHS=src/ignition:scripts/mcp
MODULE_REQUIREMENT_PATHS=src/ignition:src/fastmcp
```

`JYTHON_SOURCE_PATHS` 使用冒号分隔。

统一开发协议：

```text
Agent implementation
    -> ./devctl check
    -> ./devctl test
    -> ./devctl jython-check ...
    -> ./devctl gateway smoke     # 需要 runtime 时
    -> ./devctl ci-local
    -> push / GitHub Actions
```

## Warm Gateway 与 Clean Gateway

### Warm loop

```bash
./devctl gateway up
```

Docker named volume 会保留 Gateway state，适合高频 Agent feedback。

### Clean loop

```bash
./devctl gateway reset
```

会删除当前 worktree 的 Gateway data volume，重建/启动 Gateway，在首次启动时应用 baseline，并等待 Gateway 可访问。

## 多 Git worktree / 多 subagent

每个 checkout 会根据绝对路径生成 deterministic instance ID。默认会隔离：

- Docker Compose project。
- Gateway volume/network namespace。
- 本地 image tag。
- HTTP/HTTPS/debug host ports。
- Gateway name。

因此不同 subagent 在各自 worktree 中不会静默共享同一个 Gateway state。

详见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 本地 GitHub Actions：`act`

安装 `act` 后：

```bash
./devctl ci-local
```

第一次需要联网拉 runner image/actions，并在 size 询问时选择 **Medium**。

完成第一次下载后，可在 `.env` 设置：

```dotenv
ACT_OFFLINE=1
```

之后 `devctl` 会追加：

```text
--pull=false --action-offline-mode
```

`act` 是 GitHub Actions emulator，不是 100% GitHub-hosted runner replacement；最终 CI authority 仍然是 GitHub Actions。

## GitHub Actions

包含两个 workflow：

- `foundation-ci`：push/PR 时执行快速 foundation validation，不启动 Ignition。
- `gateway-smoke`：手动触发，对指定 Ignition 版本启动 clean base Gateway smoke test，不依赖私有 MCP module。

## `devctl` 命令速查

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

## 安全与授权边界

- Gateway 默认只绑定 loopback。
- `.env`、`.gwbk`、private `.modl`、下载工具和 runtime state 全部 Git ignored。
- 不要把生产凭证放进 `.env`。
- 除非任务明确要求并授权，不要让测试 hooks 指向 production/staging。
- `ACCEPT_IGNITION_EULA=Y` 表示你明确接受 Inductive Automation license；仓库不会静默帮你接受。
- Third-party module 的 certificate/license 由使用者自行确认。

## Troubleshooting

详见 [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md)。通常先运行：

```bash
./devctl doctor
./devctl versions
./devctl module list
./devctl gateway status
./devctl gateway logs --tail 300
```

## 主要资料来源

- Ignition 8.3 Docker image / built-in module identifiers: https://docs.inductiveautomation.com/docs/8.3/platform/advanced-deployments/docker-image
- Ignition 8.3 environment variables: https://docs.inductiveautomation.com/docs/8.3/appendix/reference-pages/platform-environment-variables
- Docker Engine on Ubuntu: https://docs.docker.com/engine/install/ubuntu/
- Ubuntu 26.04 `openjdk-17-jdk`: https://packages.ubuntu.com/resolute/openjdk-17-jdk
- `act`: https://github.com/nektos/act
