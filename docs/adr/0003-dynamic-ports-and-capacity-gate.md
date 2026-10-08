# Dynamic port allocation with a machine capacity gate

The old vendored foundation derived instance identity and ports from `cksum(absolute path) % 700`, mapping a 3-port triplet into 700 slots: collisions across parallel worktrees were a matter of time, and moving a checkout changed its identity. We decided each Checkout Setup mints a random UUID `instance_id` at first setup, ports are allocated dynamically and recorded in `.igdev/setup.json`, all URL-producing output reads the recorded ports, and a machine-level Capacity Gate refuses to start another Gateway when `MemAvailable` falls below requested heap plus 512 MB (exit code 3, human action possible via `--force` or by stopping instances).

Explicit port pinning stays machine-local (`.igdev/local.toml`, `IGDEV_*` env); the tracked `igdev.toml` has no port fields, because ports are a property of the machine, not the repository.

Consequences: nothing can assume port 8088, so agents must read `igdev status --json` / `igdev gateway url`; parallel agent worktrees become the design center rather than an edge case.

## Amendment 1: low memory warns instead of refusing (2026-10-08, igdev#105)

The Capacity Gate's exit-3 refusal made a person decide something the machine already
reports: an agent stopped and waited, although the risk is one disposable container
running slower or being killed. `up`, `reset`, `ensure` and `check --gateway` still
measure `MemAvailable` and report `capacity` exactly as before, and print a warning on
stderr naming the numbers and the running igdev instances, but they start the Gateway.
`--force` is kept, and only changes the warning's wording. `IGDEV_E_CAPACITY` is retired
and stays reserved in the code table (ADR 0012).
