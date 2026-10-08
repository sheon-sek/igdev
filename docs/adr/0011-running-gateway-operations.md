# Running-Gateway operations keep the Gateway's data

Before this, the only way to load a rebuilt module was `module add` followed by
`gateway reset`, and a reset discards every project and every configuration change. A
project could only be imported through `gateway api` with a hand-built zip and the right
Content-Type. The SDK, MCP Tools and AI Agent scenarios all iterate against one Gateway,
so each iteration cost the data the iteration was testing. All of the facts below were
measured on 8.3.8 on 2026-10-07 (issue #89).

We decided to add four verbs that work on the running Gateway through its REST API with
the Instance token (ADR 0007):

- `igdev restart` restarts the container in place, waits for RUNNING, and reports the
  modules from `/data/api/v1/modules/healthy` and `/quarantined`, with pending upgrades
  before and after (`shouldUpgrade`). `gateway restart` stays the bare restart.
- `igdev module install <file.modl>` validates and stages the file (as `module add`), then
  uploads, accepts the module's own certificate and EULA, and installs it. It restarts
  in place when the module waits as a pending upgrade, did not start, or was quarantined
  only because the Gateway had not yet read an acceptance.
- `igdev build --install` runs `module install` for every staged artifact whose sha256
  changed since the last install (recorded in `.igdev/modules/.installed.json`).
- `igdev project import|export` zips a project directory deterministically, posts it as
  `application/zip`, and unpacks an export so the directory holds exactly its files.

A module this checkout stages that ends quarantined is the new code
`IGDEV_E_MODULE_QUARANTINED` at exit level 1, carrying the Gateway's reason; an unsigned
build names `allow_unsigned_modules`.

ADR 0006 extends to these verbs. The module's own terms are accepted unattended because
it is the checkout's artifact, behind the machine-global `module-license` and
`module-cert` terms when `require_private_module_consent = true`. 8.3 sometimes restarts
into its commissioning app's modules step for an unsigned module installed over REST;
`restart` finishes that step only when every module it lists is staged by this checkout,
and any other step is a person's, at exit level 3.

Consequences: the CLI Contract Version stays 2, because every change is additive (new
verbs, a new flag, a new code). A `[modules].enabled` whitelist is fixed when the
container starts, so `module install` reports a new id `inactive` under one rather than
recreating the container. `module install` and `build --install` need a running Gateway;
they never start one.

## Amendment 1 (2026-10-08): restart accepts every module's terms

Under ADR 0012, `restart` finishes the commissioning modules step for every module it
lists, staged or not, unless `require_private_module_consent = true`, in which case only
staged modules are accepted (ADR 0006 amendment 3).
