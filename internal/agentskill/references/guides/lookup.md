# Looking things up

`igdev lookup` answers from what this machine's Ignition actually ships: the
documentation bundles inside the Gateway image's jars, the `.modl` files this checkout
stages, and the running Gateway's own OpenAPI document. Its answer is more reliable
than memory, because function signatures and endpoints change between Ignition versions.

## When

Before writing any `system.*` call in Jython, and before any `igdev gateway api` request.

## How

1. Search with a sentence: `igdev lookup "read tag values" --json`. Add
   `--kind function` or `--kind rest` to narrow it.
   Done when one of `results` is the function or endpoint the task needs.
2. Read that entry in full: `igdev lookup --name <name> --json`, where `<name>` is the
   result's `name` (a function path, or `METHOD /path` for an endpoint).
   Done when you have every parameter, its default and the return value, or the request
   body schema and responses.
3. Use what the entry says about this checkout:
   - a function's `scope` says where it runs (gateway, client, designer);
   - `module_enabled: false` means the contract does not load the module that provides
     it: enable it with `igdev module enable <id> --json` or choose another function;
   - an endpoint's `call` is a ready-to-run `igdev gateway api` line with the right
     Content-Type: start from it.

## When the answer is thin

- `indexes.rest.source` is `embedded`: no Gateway answered, so endpoints carry method
  and path only. Run `igdev gateway ensure --json`, then `igdev lookup --refresh --json`.
- A result with `source: catalog` has no documentation bundle (for example
  `system.perspective.*`): the entry names its module only, so read the Ignition manual
  page for that function.
- `IGDEV_E_DOCKER`: no Ignition image of this version is on the machine yet.
  `igdev gateway ensure --json` builds one.
- A private module's functions or endpoints are missing: stage it
  (`igdev module add` or `igdev build`) or install it, then `igdev lookup --refresh --json`.
