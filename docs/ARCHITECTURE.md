# Architecture

The repository deliberately separates the fast agent loop from runtime integration.

```text
agent change
  -> ./devctl check / test / jython-check
  -> warm local Ignition Gateway when runtime behavior matters
  -> ./devctl gateway smoke or project-specific integration assertions
  -> ./devctl ci-local (act)
  -> GitHub Actions
```

The same `devctl` command contract is intended to be used by Claude Code, Codex CLI, OMP CLI, developers, `act`, and GitHub Actions.

## State boundaries

- `.env`: local configuration and secrets; never committed.
- `.runtime/`: all disposable/generated state: downloaded Jython checker, Compose environment, optional staged baseline backup, and Docker module staging under `.runtime/docker/modules/`.
- `modules/private/`: checkout-local/private `.modl` source inputs; never committed.
- `.runtime/docker/modules/`: generated Docker build staging assembled from the global cache, `modules/private/`, and `PROJECT_MODULE_GLOB`; disposable and never committed.
- `docker/ignition/`: Docker build definitions only; no generated module copies.
- Docker named volume: warm Gateway state.

A clean runtime is obtained with `./devctl gateway reset`, which removes the worktree-specific Docker volume and recreates it. If a baseline `.gwbk` is configured, it is restored only on that fresh launch.

## Worktree isolation

`devctl` derives a Compose project name and default host ports from the absolute checkout path. Separate Git worktrees therefore receive separate Docker volumes, networks, container names, and deterministic ports unless explicitly overridden in `.env`.
