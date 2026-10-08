# A project seeds its Gateway configuration from tracked files, within an allowlist

A project's e2e work needs the same Gateway configuration every time: a tag provider for
the simulation, an OPC UA connection to the simulator, a database connection for the
historian. Until now each harness rebuilt it after `gateway up` by calling the Gateway's
REST API or by restoring a `.gwbk`. That costs time on every fresh volume, and a backup is
an opaque binary that no review can read.

The Instance image already carries a seed: igdev's own security level, security settings
and API token, in the `external` resource collection, which a fresh named volume takes as
its initial content (ADR 0007).

We decided that a Project Contract may name tracked seed directories, and that igdev
merges them into that same seed:

- `[gateway] seed = ["tests/ignition/seed", …]` lists repository-relative directories,
  each laid out like a resource collection: `<module>/<type>/<name>/…`. `setup` copies
  their files into `.igdev/runtime/seed/config/resources/external/`.
- Only an allowlist of resource types is accepted, as `<module>/<type>`:
  `ignition/tag-provider`, `ignition/tag-group`, `ignition/tag-definition`,
  `ignition/tag-type-definition`, `ignition/opc-connection`,
  `ignition/database-connection`, `ignition/schedule`, `ignition/holiday`,
  `com.inductiveautomation.opcua/device`, and
  `com.inductiveautomation.historian/historian-provider`. Each is configuration whose
  credentials, when it has any, live in a separate field.
- igdev's own resources take precedence and cannot be overridden: a seed file under
  `ignition/security-levels`, `ignition/security-properties` or `ignition/api-token` is a
  collision. So is the same file written by two seed directories.
- A tracked seed holds no secrets. A JSON key whose name ends in `password`, `passwd`,
  `secret`, `privatekey`, `apikey`, `token`, or `credential(s)` (ignoring case, `_`, `-`
  and `.`) must be null or empty. That also refuses a Gateway-encrypted value, which is
  tied to the Gateway that encrypted it and is useless anywhere else.
- Symlinks, files over 1 MiB, and seeds over 16 MiB in total are refused.
- Every refusal is `IGDEV_E_CONFIG_INVALID` and names the offending path, and for a
  secret the JSON path of the field, never its value.
- The Setup Stamp records a digest of the seed's file names and bytes. A change in a
  seed directory therefore makes the checkout stale, and `setup` re-renders the build
  context. A file dropped from the seed is removed from it.

Consequences: a seed takes effect only on a fresh volume, because the Gateway's data
volume is initialized from the image once. `gateway reset` and `gateway ensure --fresh`
pick a seed change up, and the docs say so. Resources the Gateway creates itself on
commissioning (the `default` tag provider, for example) live in the `core` collection,
which overrides `external`. A seed should therefore add resources under its own names
rather than redefine built-in ones. A type outside the allowlist needs a new decision
that amends this ADR. Connecting to a system that needs a credential is a step a harness
takes after start, with the credential from its own secret store.

## Amendment 1: any resource type, and a secret is a warning (2026-10-08, igdev#104)

The allowlist sent every new resource type through a new decision, and the secret rule
refused a development database password the project chose to track. Both protected only
the disposable Gateway the seed builds.

- A seed may carry any `<module>/<type>/<name>/` resource except igdev's reserved ones
  (`api-token`, `security-levels`, `security-properties`), which stay a collision.
- A secret-named field with a value is a warning on stderr naming the file and the JSON
  path, never the value. A file that is not JSON is still refused.
- The bounds are 64 MiB per file and 512 MiB in total, so a large tag export fits.
