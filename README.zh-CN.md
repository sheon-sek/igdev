# igdev — Ignition 本地开发工具链（上手指南）

**简体中文** | [English](README.md)

`igdev` 是一个全局安装的 Go 单文件 CLI：引擎与 Ignition 领域知识都在它内部，仓库只保留
一份被跟踪的 `igdev.toml`（Project Contract）和一行 `.gitignore`，运行时状态放在随时可删的
`.igdev/` 里。对 agent 它说一种方言（JSON envelope + `IGDEV_E_*` 错误码），对人它说另一种
（详细 help 与 Wizard）。

本文件是**入门指南**，刻意不包含任何命令表：完整的命令参考（每个命令一页）是英文的，并且
由命令定义自动生成 —— 见 [`docs/reference/`](docs/reference/README.md)。任何手工维护的命令
清单都会与 CLI 行为漂移，所以本文件不复制它们。

## 安装

```bash
curl -fsSL https://github.com/sheon-sek/igdev/releases/latest/download/install.sh | bash

igdev doctor               # 只读地审计本机环境，永不失败
igdev agent skill-install  # 安装与本二进制版本匹配的 Agent Skill
```

安装脚本会校验 release 的 sha256，并把二进制放进 `~/.local/bin`。运行 Gateway 需要 Docker
与 Compose 插件，Jython 兼容性检查需要 JVM；Gradle 与 `act` 是可选的。igdev 不会自我更新，
只会在有新版本时提示一次。

## 生命周期：先 init，再 setup

`igdev init` 写入 Project Contract：Ignition/Jython 版本、模块白名单、扫描路径、Gateway 堆
大小与时区，以及项目自己的 `check`/`test`/`build`/`smoke` 命令。它**唯一**会改动被 git 跟踪
的文件，并且每次写入都会打印 unified diff。在一个还没有 contract 的仓库里，终端会得到
Wizard；`--yes` 静默采用同样的默认值，`--json` 永不提问。

`igdev setup` 物化这个 checkout：生成随机 UUID 的 Instance 身份（绝不从路径推导，因此多个
worktree 不会撞车）、动态分配的 loopback 端口、由内嵌模板渲染出的 Compose 文件与构建上下文，
以及 Gateway 管理员密码。这些全部写在 gitignore 的 `.igdev/` 下，删掉后重跑 `setup` 即可。

其余所有项目命令都先经过 Gate（发现 Project Root、校验 contract schema、校验 Setup Stamp）。
缺少或过期的 setup 会返回 `IGDEV_E_SETUP_REQUIRED` / `IGDEV_E_SETUP_STALE`，修复方式就是重跑
`igdev setup`；`igdev status` 是唯一永远可用的命令，因为它只报告状态、不强制状态。

运行 Gateway 受 Ignition EULA 约束：Consent 由**人**在本机记录一次，自动化的运行如果发现它
不存在，会以退出码 3 加上 `IGDEV_E_CONSENT_REQUIRED` 停下，并在 Remediation 里给出需要由人
执行的准确命令。

## 日常怎么用

```bash
igdev status                     # 我在哪、配置是什么（任何状态下都能跑）
igdev check                      # 固定顺序的预检查流水线
igdev test / igdev build         # 派发 contract 里声明的项目命令
igdev verify --gateway           # 全套，外加运行时冒烟

igdev module require system.report.executeReport   # 能力预检查
igdev jython check src/main/python                 # 单次 JVM 批量编译

igdev gateway up && igdev gateway wait && igdev gateway smoke
igdev gateway url                # 唯一可以直接脚本化的 URL
igdev gateway down --volumes
```

`igdev check` 的顺序是契约：模块校验 → 能力扫描 → 项目声明的 check → 一次性 JVM 里批量
Jython 编译。**不要假设 8088 端口**：端口是 setup 时分配的，`igdev gateway url` 或任何 JSON
envelope 里的 `ports` 才是答案。

配置按层级解析，先命中者胜出：命令行 flag > `IGDEV_*` 环境变量 > `.igdev/local.toml` >
被跟踪的 `igdev.toml` > 内嵌默认值。配置文件永远不会被执行。

## 与 Agent 协作

加 `--json`，stdout 就是唯一的 JSON envelope（进度与提示走 stderr）：

```json
{"ok": false, "contract": "1", "code": "IGDEV_E_CONSENT_REQUIRED", "message": "…",
 "remediation": [{"command": "igdev setup --accept-eula", "why": "human-only consent"}], "data": {}}
```

退出码是契约的一部分：**0** 成功，**1** 命令失败（看 `code`），**2** 用法错误（缺少参数时会
点名 flag），**3** 需要人来操作 —— 此时应当交回给人，而不是重试。所有参数都给齐时，igdev 进入
Silent Mode，无论 stdin 是不是终端都绝不提问。

开局先调用 `igdev agent context --json`：一次调用返回生命周期状态与 Consent、各版本号、
Instance 与端口、Gateway 是否在运行、已暂存的模块、catalog 摘要，以及当前可用的项目命令。
它的字段说明见 [`docs/reference/agent-context.md`](docs/reference/agent-context.md)。
要本地复现某个 CI job，用 `igdev ci-local --job <name>`。

## 其它文档

- [`docs/reference/`](docs/reference/README.md)：完整命令参考，由命令定义自动生成，**请勿手改**。
- [`docs/IGDEV.md`](docs/IGDEV.md)：本仓库的设计与开发说明。
- [`CONTEXT.md`](CONTEXT.md)：规范术语；[`docs/adr/`](docs/adr)：不可逆决策记录。
- [`AGENTS.md`](AGENTS.md)：仓库内的 Agent 指令（安全命令、升级规则、版本变更路径）。
- 本仓库同时是 igdev 自身的代码仓库，并以此工具链自举：项目契约见 `igdev.toml`，
  本地状态见 `.igdev/`；开发用命令见 [README.md](README.md) 的 “Developing this repository”。
