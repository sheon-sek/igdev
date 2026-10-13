# `cleanup --obsolete-images` reclaims old-version images and keeps the Instance

Every `[ignition].version` change builds a new `igdev-<id8>:<version>` image and pulls a
new `inductiveautomation/ignition:<version>`; the old pair stays on disk. Over several
upgrades and worktrees they accumulate, and the only ways out removed far more: a checkout
`cleanup` takes the Instance, its data volume and `.igdev/`, and `--machine` takes every
Instance, every official tag and the Consent record (ADR 0013). Independent review R7
asked for a previewable way to drop only what a version change left behind (igdev#153,
part of igdev#140).

We decided to add `igdev cleanup --obsolete-images`. It plans Docker images only, so the
Instance's containers, data volume and network, the Checkout Setup and the Consent record
are never items. It needs the Checkout Setup, which names the Instance (`IGDEV_E_SETUP_REQUIRED`, exit 1,
without it; `IGDEV_E_NOT_INITIALIZED` without `igdev.toml`), and reads
`igdev.toml` strictly for the current version: guessing a default could plan the running
Instance's image. It combines with neither `--deinit`, `--machine` nor `--uninstall`
(`IGDEV_E_USAGE`, exit 2), and acts when named without `--yes`, like a checkout cleanup:
everything it can remove belongs to this checkout or is an official image only this
checkout's obsolete images used.

Two classes are planned under separate rules, and the dry run lists them separately:

- **Instance images**: images attributed to this Instance, by Compose project label or,
  unlabelled, by `igdev-<id8>` tag, that are not tagged for the current version. Images of
  other Instances, non-igdev Compose projects and unattributed images are never planned.
  The current version's image is listed as kept. The protections of #145 apply unchanged:
  a tag outside the Instance, several tags, or a container user keeps the image.
- **Official images** carry no igdev label, and nothing records which versions igdev
  pulled (ADR 0013), so the rule is conservative: when in doubt, keep. One is planned only
  when an obsolete Instance image of this checkout is tagged for its version or starts
  with all of its layers, and it is not the current version, not the base (by layers or
  version tag) of any image the plan keeps, and used by no container. Every other tag is
  kept with the reason and, when it is merely unattributed, the command to remove it by
  hand. Unknown layers keep the image.

Every image is removed by the immutable ID inventoried at planning, never by tag and never
with `--force`, following #145: an image with several tags is kept, and execution
reinspects the ID and keeps it when its tags, its references or its container users
changed. An engine refusal, such as dependent child images under the legacy builder, is
reported as kept. Official images go first, and an Instance image built from one that
was not removed is held back, so a retry can still attribute it; one the engine refused
because of dependent child images is tried again after the Instance images built from it. Nothing prunes: no
`docker image prune`, `system prune` or build-cache removal.

Consequences: the cleanup envelope's `data` gains `obsolete_images`, and the command gains
a flag; both only add, so the CLI Contract Version stays 2. Another checkout whose Instance
image and containers are gone but whose contract names an old version cannot be seen, so
its next start may pull that version again: an official image is a re-pullable cache,
never a data store. Shared layers mean the bytes reclaimed are less than the sizes listed.
