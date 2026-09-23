package contract

// CodeDoc documents one error code for agents: the exit level it is emitted at
// and what to do about it. The generated error reference (docs/reference and
// the Agent Skill) renders this table, and a test in this package fails when a
// Code constant is missing from it.
type CodeDoc struct {
	Code Code
	// Exit is the exit level as the reference prints it: a number, or a note
	// for the codes that pass a child process's exit code through.
	Exit string
	// Meaning says what went wrong.
	Meaning string
	// Next says what an agent does about it.
	Next string
}

// CodeDocs lists every non-empty Code in the order contract.go declares them.
var CodeDocs = []CodeDoc{
	{CodeUsage, "2", "Unknown command or flag, an unusable argument, or a `--config` key igdev does not know.",
		"Read `igdev help <command>` and fix the invocation."},
	{CodeMissingArgument, "2", "A required argument was not supplied.",
		"Supply it. Agents always pass every argument, so no prompt appears."},
	{CodeConfigInvalid, "1", "A config tier exists but cannot be interpreted: environment, `igdev.toml`, or checkout-local config.",
		"Fix the file or variable the message names."},
	{CodeInternal, "1", "An unexpected failure with no more specific code.",
		"Report it with the full envelope. Do not retry blindly."},
	{CodeNotInitialized, "1", "No `igdev.toml` at or above the working directory.",
		"Change into the repository, or run `igdev init` if the task is to adopt igdev."},
	{CodeSetupRequired, "1", "The Project Contract exists but this checkout has no `.igdev/`.",
		"Run `igdev setup`."},
	{CodeSetupStale, "1", "`.igdev/` no longer matches `igdev.toml` (Contract Digest, schema, or CLI Contract Version).",
		"Run `igdev setup`. Never hand-edit `.igdev/`."},
	{CodeConsentRequired, "3", "A legal term (Ignition EULA, module license, module certificate) is not accepted on this machine.",
		"Stop. Hand the Remediation command to a person. Never accept it yourself."},
	{CodePortAlloc, "1", "The host refused three loopback binds, so no ports could be recorded.",
		"Retry `igdev setup`. If it repeats, check what holds loopback ports."},
	{CodeContractSchemaUnsupported, "1", "`igdev.toml` declares a schema version this binary does not speak.",
		"Install the igdev version the contract needs. Do not edit the schema field."},
	{CodeVersionUnsupported, "1", "`[tool].min_version` in `igdev.toml` is newer than this binary.",
		"Ask a person to upgrade igdev."},
	{CodeCapacity, "3", "The Capacity Gate refused to start a Gateway: free memory is below the heap plus headroom.",
		"Stop. A person frees memory or decides to pass `--force`."},
	{CodeGatewayUnhealthy, "1", "The Gateway missed its health deadline or failed a smoke check.",
		"Read `igdev gateway logs --tail 200`, then fix the cause."},
	{CodeDocker, "1", "A container-engine call failed, or the engine is missing or not compose v2.",
		"Run `igdev doctor` and report what it finds."},
	{CodeBaselineMissing, "1", "The `baseline set` source path does not exist.",
		"Pass the path of an existing `.gwbk`."},
	{CodeBaselineInvalid, "1", "The `baseline set` source is a directory or cannot be read.",
		"Pass a readable `.gwbk` file."},
	{CodeUnknownCapability, "1", "Nothing in the Effective Catalog owns a `system.*` function, REST request, or capability name.",
		"Check the spelling and the Ignition version. A private module's capability needs a Project Overlay."},
	{CodeModuleNotEnabled, "1", "Project code needs a module outside `[modules].enabled`.",
		"Run the `igdev module enable` command in the Remediation."},
	{CodeModuleArtifactMissing, "1", "An enabled module is neither built into the image nor staged as a `.modl`.",
		"Stage it with `igdev module add <file.modl>`, or remove it from the whitelist."},
	{CodeModuleArchiveInvalid, "1", "A `.modl` is unreadable, has no usable `module.xml`, or trips the zip-bomb guard.",
		"Get a valid archive. Do not repackage it."},
	{CodeModuleUnknown, "1", "`module enable` got an id that is neither built in nor declared by a staged `.modl`.",
		"Use one of the close ids the message suggests, or stage the `.modl` first."},
	{CodeOverlayInvalid, "1", "A Project Overlay file cannot be read or does not follow the overlay format.",
		"Fix the overlay file the message names."},
	{CodeOverlayConflict, "1", "A Project Overlay row has the same key as a Core Catalog row.",
		"Remove or rename the overlay row. The overlay may add rows, not shadow them."},
	{CodeCatalogVersionMissing, "1", "This binary carries no Core Catalog for the contract's Ignition version.",
		"Pick a supported `[ignition].version` or upgrade igdev."},
	{CodeCapabilityAmbiguous, "1", "One capability resolves to more than one owner.",
		"Pick one of the candidates the message names and make the reference explicit."},
	{CodeJavaMissing, "1", "The Jython check found no JVM on PATH.",
		"Ask a person to install a JDK, or run `igdev doctor`."},
	{CodeJythonVersionUnsupported, "1", "`[ignition].jython_version` has no pinned artifact digest in this binary.",
		"Use a supported Jython version."},
	{CodeJythonFetch, "1", "The pinned Jython artifact could not be downloaded or cached.",
		"Check network access and cache permissions, then retry."},
	{CodeChecksumMismatch, "1", "A downloaded artifact failed its sha256 pin after one retry.",
		"Stop and report it. The artifact is never used."},
	{CodeJythonPathMissing, "1", "A `jython check` path does not exist.",
		"Pass paths that exist."},
	{CodeJythonSyntax, "1", "The batched Jython compile rejected files. The message names each path and line.",
		"Fix those lines, then rerun `igdev check`."},
	{CodeCommandFailed, "the stage's exit code (1 if bash is missing)", "A declared project stage (`[commands].check`, `.test`, `.build`) exited non-zero.",
		"Read the stage output and fix the project code."},
	{CodeActMissing, "1", "`ci-local` found no `act` on PATH.",
		"Ask a person to install act, as the Remediation says."},
	{CodeActFailed, "act's exit code", "An act run exited non-zero or could not start. `data` carries the invocation and output tail.",
		"Read the output tail in `data` and fix the failing workflow step."},
}
