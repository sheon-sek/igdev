# Private module licenses and certificates are accepted per checkout by default

A private module staged in a checkout is the checkout's own artifact — in a module
repository the developer is the module's author — so a machine-global acceptance ritual
for that module's license and signing certificate protects nobody and adds a human step
to every fresh worktree. That is the friction ADR 0004 rejected for the EULA, and it
matters more here: `igdev gateway up` is exactly the command a module developer runs
after every build.

We decided that igdev passes the module id of every private artifact staged in the
checkout to the Gateway as `ACCEPT_MODULE_LICENSES` and `ACCEPT_MODULE_CERTS` at
`gateway up` and `gateway reset` time, so an unsigned or self-signed `.modl` that
declares a license loads unattended. The ids come from the `module.xml` files the
staging directory already holds; built-in modules are never passed, because only staged
private artifacts are read. `agent context` reports which ids would be passed, and
whether the opt-in below is in force, so an unattended run can tell which behaviour it
gets without starting a Gateway.

`[modules] require_private_module_consent = true` is the opt-out that keeps the strict
behaviour: the machine-global `module-license` and `module-cert` terms (ADR 0004) are
then required before any id is passed, and a checkout that has not recorded them stops
with `IGDEV_E_CONSENT_REQUIRED` at exit level 3, naming
`igdev setup --accept-module-license` and `igdev setup --accept-module-certificate`. The
terms stay machine-global, human-only, and never written by an automated run; a checkout
that stages a third-party module it did not build states the opt-in and pays the human
step deliberately.

Consequences: the default is permissive only about artifacts the checkout itself staged,
so a checkout that stages nothing behaves exactly as before; a Gateway started from a
checkout with a staged private module no longer needs a human for the module's license or
certificate; the module write verbs still do not verify signatures or licenses, because a
`.modl` is never a source of knowledge (ADR 0005) — the acceptance is about letting the
Gateway load what the developer staged, not about trusting it; and the Consent record
keeps a term per legal statement, so an operator who needs the strict gate has one
contract key to set.
