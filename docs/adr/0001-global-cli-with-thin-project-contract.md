# Global CLI with a thin per-project contract

Each Ignition repository used to vendor a full dev-foundation (`devctl`, `scripts/`, `config/`, `docker/`, `.env`, `.runtime/`), so every tool fix had to be propagated to every copy. We decided instead to ship one globally installed CLI (`igdev`) that holds all domain knowledge, and to keep only a thin tracked Project Contract (`igdev.toml`) plus gitignored per-checkout state (`.igdev/`) inside each repository. The CLI holds no project semantic state; the repository holds no tool implementation.

Considered options: continued vendoring (rejected: per-repo drift, every repo pays the maintenance of the toolchain), devcontainer-style full isolation (rejected: loses direct host worktree/agent workflows), a global daemon with per-project registry (rejected: recreates the "works on my machine" gap the contract file closes).

Consequences: every project command must pass the Gate (discover, contract schema, Setup Stamp) before doing work; `init` and `setup` become two distinct lifecycle verbs; the repo-vendored bash layout is retired without a compatibility shim (v0.x, single consumer).
