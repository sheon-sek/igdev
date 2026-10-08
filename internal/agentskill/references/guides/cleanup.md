# Cleaning up after a finished project

`igdev cleanup` removes what igdev created. Use it when a project is finished, when the
machine is being handed back, or when the environment is so tangled that starting from
nothing is simpler. To start the Gateway over during ongoing work, use
`igdev gateway reset` (or `igdev gateway ensure --fresh`) instead: it keeps the checkout
and everything else.

## Pick the scope

Each scope removes everything the one before it removes, plus more:

| Command | Removes | Keeps |
| --- | --- | --- |
| `igdev cleanup` | this checkout's Gateway and trial-keeper containers, data volume, network, built image, and `.igdev/` | the repository and everything outside this checkout |
| `igdev cleanup --deinit` | also `igdev.toml`, the managed block in `AGENTS.md`, and a repository-scope skill | `.gitignore`, as it is |
| `igdev cleanup --machine --yes` | also every igdev Instance on this machine, every official Ignition image no container uses, igdev's cache, state and config (the Consent record among them), and the global skills | the igdev binary |
| `igdev cleanup --uninstall --yes` | also the igdev binary and the PATH entry `install.sh` added | nothing of igdev's |

The Docker build cache, act's images and cache, a file `IGDEV_CONSENT_FILE` names, and
the tracked content the contract points at (overlays, the seed) are not igdev's and are
always kept.

`cleanup` and `--deinit` stay within this checkout, so run them when the task calls for
them. `--machine` and `--uninstall` reach other checkouts and the whole machine: run them
only when a person asked for that scope, and pass `--yes` then.

## Steps

1. **Keep what matters.** The Gateway's projects and data go with it. Export each project
   to keep with `igdev project export <name> --output <dir> --json`.
   Done when every project to keep is in the repository or the person said none is.
2. **Preview.** Run the chosen command with `--dry-run --json`. When the person did not
   name the scope, show them `data.items` (each with `kind`, `ref` and `action`) and let
   them confirm.
   Done when the person agrees, or the scope is plain `cleanup` or `--deinit` named by
   the task.
3. **Clean up.** Run the same command without `--dry-run`.
   Done when it returns `ok:true`. `IGDEV_E_CLEANUP_PARTIAL` means some items failed:
   fix the `reason` of each item with `action: failed` and run the same command again,
   which only retries what is left.
4. **Confirm.** Run the command again with `--dry-run --json`.
   Done when `data.would_remove` is 0.

## Afterwards

- `--deinit` leaves its changes in the working tree. Commit them, or tell the person
  they are there to commit.
- After `--machine` the Consent record is gone: the next Gateway start exits 3 with
  `IGDEV_E_CONSENT_REQUIRED` until a person accepts the EULA again (see `SKILL.md`,
  Consent).
