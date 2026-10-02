# Every Instance Gateway starts with an igdev API token already in place

igdev needs to administer the Gateway it runs: reset an expired trial (ADR 0008), and
later read and write Gateway resources for a running project. Ignition 8.3 authenticates
that over its REST API with an API token, and a token created after the Gateway started
is not honoured until the Gateway restarts. A token added by a first-run script would
therefore cost one restart per fresh Gateway, and a token added by hand is a human step
on every `gateway reset`.

We decided that setup mints one API token per checkout and that the Instance image
carries it, as its hash, in the Gateway's data directory, so the Gateway knows the token
from its first start. The token is `igdev:<base64url of 32 random bytes>`, generated
from `crypto/rand` and kept in `.igdev/local.toml` mode 0600 next to the admin password;
`gateway credentials --json` is its only readable home and every human verb keeps it out
of its output. The rendered runtime holds only the hash Ignition itself stores (SHA-256
over the key's bytes, base64url), so no rendered file is a secret; the token reaches
containers that need it through the igdev process environment, as the admin credentials
already do.

The seed is three resources in the Gateway's `external` resource collection, rendered
under `.igdev/runtime/seed/` and copied over the image's data directory, which a fresh
named volume takes as its initial content:

- `security-levels`: the default tree plus `Authenticated/IgdevAdmin`;
- `security-properties`: the commissioning defaults, with `IgdevAdmin` allowed next to
  `Authenticated/Roles/Administrator` wherever Administrator is required;
- `api-token/igdev`: a basic token at the `IgdevAdmin` level, with the token's hash and
  the Setup Stamp's `created_at` as its timestamp.

Each choice is forced by what the Gateway accepts, established on a real 8.3 Gateway:
`core` cannot be seeded, because a Gateway whose core collection exists before
commissioning faults; a token cannot hold the `Authenticated/Roles/...` levels, which
only a user's roles produce, so it holds a custom level instead; the token resource is
refused without a timestamp; and the core collection that commissioning writes would
shadow seeded general security settings, so `security-properties` is marked not
overridable.

Consequences: a fresh Gateway (`gateway up` on a new volume, or `gateway reset`) answers
the token at its first RUNNING, with no restart. A volume created before this
checkout had a token never learns it, and only `gateway reset` gives it one, at the cost
of its data. Rotating the token means `gateway reset` for the same reason. The general
security settings of an igdev Gateway cannot be edited from its web UI. That is
acceptable for a disposable development Gateway, and the Administrator role keeps every
right it had. The token is an administrator credential for a Gateway bound to loopback
ports (ADR 0003). It never leaves the checkout's 0600 file and the processes igdev
starts.
