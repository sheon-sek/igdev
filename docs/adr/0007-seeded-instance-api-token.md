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

## Amendment 1: the seed names the login provider the volume really has (igdev#69)

The first version of the seed copied the commissioning defaults verbatim, including
`systemAuthProfile` and `systemIdentityProvider` set to `default`. On a real 8.3 Gateway
that broke two things:

- **Browser login on a fresh volume.** Ignition commissions the admin from
  `GATEWAY_ADMIN_USERNAME/PASSWORD` in `initSecurityProperties`, which returns early when
  security settings already exist. The seed's settings exist, so commissioning falls
  through to Ignition's password-reset path, which creates the admin in a user source and
  identity provider named `temp`. No `default` provider exists, and because the seeded
  settings are authoritative, `/data/app/login` answered 500
  (`Identity provider not found: default`). The token kept working, which is why the
  smoke check stayed green.
- **Restoring a Baseline.** On a restored volume the password-reset path writes its
  `security-properties` as a CREATE into `core`, where the backup already has one, and
  the Gateway faults (`PushConflictException: CREATE conflict`).

We decided that the seeded settings carry placeholders for the two names, and a small
wrapper the image carries (`igdev-gateway-entrypoint.sh`) fills them in on a volume's
first start, before handing over to the image's own `docker-entrypoint.sh`. igdev
passes the names at `gateway up` time in `IGDEV_SYSTEM_USER_SOURCE` and
`IGDEV_SYSTEM_IDENTITY_PROVIDER`:

- a fresh volume gets `temp`, the names Ignition's own password-reset path gives the
  commissioned admin;
- a volume restored from the staged Baseline gets the names the backup's own
  `security-properties` records, read from the `.gwbk`, falling back to `default`. It is
  started without the admin credentials (the wrapper removes the empty variables), so it
  is not commissioned at all and keeps the backup's users.

Consequences: on a fresh Gateway the admin logs in through a provider named `temp`; the
name is cosmetic and the provider is an ordinary internal one. On a Gateway restored from
a Baseline the admin password is the backup's, not the one `gateway credentials` reports;
the Instance token works either way. The rendered files still hold no secret and are
still identical whether or not a Baseline is staged. The real-Gateway e2e workflow checks
that `/data/app/login` redirects to the identity provider.
