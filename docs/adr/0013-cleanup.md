# `igdev cleanup` removes what igdev created, with a confirmation only past the checkout

Nothing in igdev undid it. `gateway down --volumes` left the image igdev builds for each
Instance (`igdev-<id>:<version>`) and the Checkout Setup; nothing removed `init`'s tracked
files, the machine-level directories, the global Agent Skills, or the binary; and an
Instance whose checkout was deleted could only be found by hand. The owner asked for one
command that clears everything igdev produced, Docker included, once a project is finished
(2026-10-08, igdev#110).

We decided to add `igdev cleanup` with four scopes, each a superset of the one before:

- `cleanup`: this checkout's Instance (containers, data volume, network, the image igdev
  built) and `.igdev/`. A container's `com.docker.compose.project.working_dir` label
  finds an Instance whose `.igdev/` is already gone.
- `--deinit`: also `igdev.toml`, the managed `AGENTS.md` block, and a repository-scope
  Agent Skill. `.gitignore` is left as it is (owner, 2026-10-08). Changes stay in the
  working tree, each with its diff.
- `--machine`: every igdev Instance on the engine, the `.igdev/` of each checkout its
  containers name (only when that record names the same Instance), the official
  `inductiveautomation/ignition` images (every tag: once a checkout cleanup removed the
  image igdev built, nothing records which versions igdev pulled, and the confirmation
  lists each one), igdev's XDG cache, state and config directories, and the global Agent
  Skills.
- `--uninstall`: also the binary, when it sits under an install.sh prefix, and the PATH
  entry install.sh added between its marker.

An igdev object is one whose compose project is `igdev-` plus eight hex digits, or whose
container carries the new `dev.igdev.instance` label the Compose file now sets; a user's
own `igdev-foo` project never matches. What igdev uses but does not own stays and is
reported `kept`: the Docker build cache, act's images, cache and `.actrc`, the file
`IGDEV_CONSENT_FILE` names, `IGDEV_ACCEPT_EULA`, a base image another container uses, and
tracked content the contract points at (overlays, the seed).

Confirmation is tiered (owner, 2026-10-08). `cleanup` and `--deinit` act when named, as
`reset` does (ADR 0012): everything they remove belongs to this checkout. `--machine` and
`--uninstall` reach other checkouts' Gateways, the Consent record and the binary, so they
need `--yes`; in a terminal without it, cleanup prints the plan and asks y/N once, and any
other run without it is `IGDEV_E_USAGE` at exit 2 carrying the plan in `data`.

Cleanup never runs the Gate's Setup refresh, and runs with no `igdev.toml`. It reads the
engine before it changes anything: a daemon that does not answer is
`IGDEV_E_DOCKER_DAEMON` and nothing is removed, so the record that finds the Instance
survives; no docker CLI at all means there is nothing Docker-side to clean. Docker objects
go before files. A failed item does not stop the others; the run then ends with the new
code `IGDEV_E_CLEANUP_PARTIAL` at exit 1, every item's outcome in `data`. A second run has
nothing left to remove.

Consequences: the y/N prompt is the one prompt outside the Wizards (CONTEXT.md). The CLI
Contract Version stays 2: the verb, its flags, the label and the code only add.
