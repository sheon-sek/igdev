# Troubleshooting

> This file covers the legacy bash foundation (`./devctl`), retired by ticket #19. For
> igdev, start from the generated reference's index
> ([`docs/reference/`](reference/README.md)) and
> [`IGDEV.md`](IGDEV.md); `igdev gateway logs --tail 300` and `igdev gateway status`
> are the igdev equivalents of the first two commands below.

## Gateway does not start

Run:

```bash
./devctl gateway status
./devctl gateway logs --tail 300
```

Common causes are an unaccepted EULA, a bad `.gwbk`, a module quarantine/certificate prompt, a port collision after manually overriding `auto`, or an incompatible module build.

## MCP module is missing or quarantined

Add the module locally and rebuild:

```bash
./devctl module add /path/to/MCP-module.modl
./devctl gateway reset
```

The default module certificate acceptance list includes `com.inductiveautomation.mcp`. Adjust `GATEWAY_MODULES_ENABLED`, `ACCEPT_MODULE_CERTS`, and `ACCEPT_MODULE_LICENSES` in `.env` when your module set differs.

## Jython checker fails to download

`./devctl bootstrap` downloads `org.python:jython-standalone:<JYTHON_VERSION>` from Maven Central. Check outbound HTTPS access and the configured version. The artifact URL is printed by `./devctl versions`.

## Different worktrees need fixed ports

Set explicit values in each worktree's `.env`:

```dotenv
GATEWAY_HTTP_PORT=18088
GATEWAY_HTTPS_PORT=18043
GATEWAY_DEBUG_PORT=18000
```

Using `auto` is recommended for parallel agent worktrees.

## Reset everything local

```bash
./devctl gateway down --volumes
rm -rf .runtime
```

Private source modules in `modules/private/` and the version-scoped global module cache are intentionally not removed. Docker module staging is regenerated under `.runtime/docker/modules/` on the next bootstrap/build.
