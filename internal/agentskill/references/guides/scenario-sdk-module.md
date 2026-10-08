# Scenario: an Ignition SDK module

The repository builds a `.modl` with Gradle or Maven. The inner loop rebuilds the module
and hot-installs it into a running Gateway, so projects, tags and configuration survive
each iteration. Unsigned development builds load by default.

## Set up once

1. **Declare the build.** Run
   `igdev init --json --command-build "<build command>" --modules-artifacts '<glob of the built .modl>'`,
   for example `./gradlew build` and `build/libs/*.modl`. Add `--command-check` and
   `--command-test` when the build tool has those stages.
   Done when the envelope's `data.contract.diff` shows `[commands].build` and
   `[modules].artifacts`.
2. **Build and stage.** Run `igdev build --json`.
   Done when the `module-restage` stage lists your module id in `artifacts`.
3. **Start the Gateway.** Run `igdev gateway ensure --json`. A module staged before the
   first start loads on the fresh volume.
   Done when it reports `url` and `igdev restart --json` lists your id under
   `modules.healthy`.

## Loop

1. Edit the code.
2. Run `igdev check --json`. Done when it returns `ok:true`.
3. Run `igdev build --install --json`. It builds, re-stages, uploads only artifacts whose
   bytes changed, and restarts the Gateway when an install replaced a running build.
   Done when every entry in the `module-install` stage's `installs` has
   `status: healthy`.
4. Verify the behaviour against the running Gateway: `igdev gateway logs --json` for
   the module's log lines, `igdev gateway api --json` for its endpoints (find them with
   `igdev lookup --kind rest "<what the module serves>"`).
   Done when the behaviour you changed shows up there.

## When an install is not healthy

- `IGDEV_E_MODULE_QUARANTINED`: the envelope carries the Gateway's reason. "unsigned"
  means the contract turned unsigned modules off; `igdev init --json --allow-unsigned-modules`
  turns them back on. Any other reason is in the module itself: fix it and loop again.
- `status: inactive`: the contract's `[modules].enabled` whitelist does not name the
  module, so the Gateway leaves it stopped. Clear the whitelist with
  `igdev init --json --modules ""`, or add the id with `igdev module enable <id> --json`;
  either takes effect on a fresh Gateway, `igdev gateway ensure --fresh --json`, which
  discards the Gateway's data.

## Compile against the Gateway's own jars

The Gateway image holds the Ignition SDK jars the module runs against. Pack them in the
container and copy the archive out:

```text
igdev gateway exec --json -- tar -czf /tmp/ignition-lib.tgz -C /usr/local/bin/ignition lib
igdev gateway data get /tmp/ignition-lib.tgz ./ignition-lib.tgz --json
```

Done when `data.bytes` is non-zero and the build resolves its compile-only dependencies
from the unpacked `lib/`.

## Handoff

None: the scenario ends with the module healthy on the Gateway. Finish as `SKILL.md`
step 5 says.
