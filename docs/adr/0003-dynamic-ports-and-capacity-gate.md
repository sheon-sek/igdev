# Dynamic port allocation with a machine capacity gate

The old vendored foundation derived instance identity and ports from `cksum(absolute path) % 700`, mapping a 3-port triplet into 700 slots: collisions across parallel worktrees were a matter of time, and moving a checkout changed its identity. We decided each Checkout Setup mints a random UUID `instance_id` at first setup, ports are allocated dynamically and recorded in `.igdev/setup.json`, all URL-producing output reads the recorded ports, and a machine-level Capacity Gate refuses to start another Gateway when `MemAvailable` falls below requested heap plus 512 MB (exit code 3, human action possible via `--force` or by stopping instances).

Explicit port pinning stays machine-local (`.igdev/local.toml`, `IGDEV_*` env); the tracked `igdev.toml` has no port fields, because ports are a property of the machine, not the repository.

Consequences: nothing can assume port 8088, so agents must read `igdev status --json` / `igdev gateway url`; parallel agent worktrees become the design center rather than an edge case.
