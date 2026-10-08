# The development Gateway defaults to open: no refusal that protects only itself

An igdev Gateway is a disposable Docker container built for one checkout, bound to
loopback, holding nothing but development data. Several guards in igdev protected that
container from the person or agent working in it: `setup` wrote nothing until the EULA
was accepted, a stale Checkout Setup stopped every command, low memory stopped a start
at exit 3 as a human action, `gateway data` refused any path outside the data directory,
the seed refused resource types and secret fields, `gateway credentials` hid the
password from a human, and the AGENTS block igdev writes forbade docker and curl. Each
one turned a routine step into a failed run or a hand-off to a person (owner, 2026-10-08,
igdev#100).

We decided that a guard stays only when it protects something outside the disposable
Gateway, or records a legal statement a person makes:

- **Stays:** the Ignition EULA is a person's acceptance, once per machine (ADR 0004).
  The API token is sent only to this Instance. Destructive verbs (`reset`,
  `down --volumes`, `ensure --fresh`, `project import --overwrite`) act only when named.
  Bounds that keep an envelope readable (`--json` inline caps) stay, and are reported,
  never silent. The interactive Wizards stay.
- **Goes:** the EULA gates only the verbs that start a Gateway (ADR 0004, amendment 2).
  A missing or stale Checkout Setup is refreshed by the Gate itself. Low memory is a
  warning (ADR 0003, amendment 1). `gateway data` reaches any container path, as the
  Gateway's user or root. `gateway api` streams whole bodies in human mode and takes
  request bodies up to 1 GiB. The seed takes any resource type but igdev's own, and a
  secret in it is a warning (ADR 0009, amendment 1). The `.modl` reader's total bound is
  1 GiB. `gateway credentials` prints the credentials in human mode. Docker errors name
  the socket-permission fix and let an agent start the engine. `ci-local` fetches a
  pinned act when none is installed. The managed AGENTS block prefers igdev verbs
  without forbidding docker or curl.

Consequences: an agent on a fresh machine can init, set up, check, test and build with
no person involved; the only exit-3 stop left is the EULA, before a Gateway starts, and
a person can give it once per environment with `IGDEV_ACCEPT_EULA=Y`. `IGDEV_E_CAPACITY`
stays in the frozen code table, retired. The CLI Contract Version stays 2: no verb,
flag or `--json` key is removed, and the new flags (`gateway data --user`) only add.

## Amendment 1: `cleanup` confirms only what reaches past the checkout (2026-10-08, igdev#110)

`igdev cleanup` joins the destructive verbs that act when named, for this checkout and
`--deinit`. `--machine` and `--uninstall` remove other checkouts' Gateways, the Consent
record and the binary, which are outside this disposable Gateway, so they keep a guard:
`--yes`, or one y/N in a terminal (ADR 0013).
