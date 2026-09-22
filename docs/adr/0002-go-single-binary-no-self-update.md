# Go single binary, Releases distribution, no self-update

igdev needs embedded catalogs/templates, TOML/JSON/OpenAPI parsing, subprocess control (docker, java, gradle), fast startup, and a bounded memory footprint, and must install without dragging a language runtime onto target machines. We decided on one Go binary (cobra + huh, `//go:embed` for assets), distributed as checksummed GitHub Release tarballs installed to `~/.local/bin`, targeting Linux and macOS (WSL via the Linux binary); Windows native is unsupported. The CLI never updates itself: `version`/`status` may print an "update available" notice from a 24h-cached fetch, disabled by `IGDEV_NO_UPDATE_NOTIFIER=1`.

Considered options: shipping the existing bash bundle globally (rejected: config schema, migration, JSON contracts, and catalog resolution outgrow shell), Rust (rejected: equal fit, slower authoring for this tool's surface), Node/pyinstaller runtimes (rejected: runtime dependency violates the install-and-go rule).

Consequences: self-update's attack surface (signature checks, replacing a running binary, temp-file litter) is out of scope forever, so catalog freshness between releases depends on the Project Overlay mechanism (ADR 0005), not on shipping binaries.
