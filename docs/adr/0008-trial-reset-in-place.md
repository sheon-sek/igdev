# An expired trial is reset in place, by default automatically

An igdev Gateway runs on Ignition's two-hour trial. When the trial expires the Gateway
stops executing, and until now the only way back was to rebuild it. Restoring a `.gwbk`
does not start a new trial either. A rebuild costs minutes and loses everything the
Gateway gathered since its start: tag values, history, live connections, and the state
of a scenario that was half run. That loss is exactly what a long development or test
session cannot afford.

Ignition 8.3 reports the trial at `GET /data/api/v1/trial`, which needs no credential,
and starts a new trial at `POST /data/api/v1/trial`, which needs an API token. The
Gateway accepts the POST only once the trial has expired. Before that it answers 403
whatever the token, so a reset cannot be done ahead of time. It can only follow
expiry.

We decided that igdev resets an expired trial in place, with the Instance's own API
token (ADR 0007), and never restarts or rebuilds the Gateway to do it:

- `igdev gateway trial` reports the license mode, the time left, and whether the trial
  expired. `gateway status` and `agent context` carry the same `trial` object when the
  Gateway answers.
- `igdev gateway trial reset` resets an expired trial on demand. On a trial that has
  time left it is a successful no-op that reports `reset: false`. A refusal is
  `IGDEV_E_TRIAL_RESET`, and its remediation names what the status means: 401 is a
  volume that predates the token, and 403 is a trial that is no longer expired.
- With the contract's `[gateway] trial_reset = "auto"` (the default), `gateway up`
  also starts a `trial-keeper` service in the Instance's Compose project. It runs from
  the Instance image as the Gateway's user, with no published port. It reads the trial
  every 60 s, every 5 s in the last two minutes, and posts the reset the moment the
  trial reports expired. The token reaches it through the igdev process environment,
  never a rendered file. `trial_reset = "off"` renders no keeper, for a project that
  wants to see an expired Gateway.

The request carries the `Origin`, `Referer` and `Accept` headers the Gateway's own web UI
sends, because the Gateway checks where a state-changing request came from.

Consequences: a Gateway under `auto` runs indefinitely on trials, with at most a few
seconds of expired state each time. A Gateway whose volume predates the token can only be
reset by `gateway reset`, which says so. The keeper is one more container per Instance,
but it is the same image, so it costs no pull and almost no memory. It is the only
process outside igdev that holds the token. `trial_reset` is a contract key, so a
repository decides it once for every checkout.
