# Local client discovery

The optional menu-bar companion needs an explicit list of loops to display. On `main` after v0.1.3, `keepgoing registry add|list|remove` provides that first prerequisite. It is unreleased and offline-tested. No GUI, client status/control endpoint or automatic setup registration is implemented yet.

Registration stores a reference only. It never enables, resumes, stops or transfers supervision; queries a provider; creates a thread; changes a scheduler; or reads a task file. Headless and process configurations need no BB installation to register. The registry is separate from the BB plugin's private setup list; neither imports or replaces the other.

## Register explicitly

```sh
keepgoing registry add -c /private/example-loop/keepgoing.yaml --json
keepgoing registry list --json
keepgoing registry list --private --json
keepgoing registry remove --id loop_0123456789abcdef0123456789abcdef --json
```

`add` requires an existing valid configuration. The CLI preserves the absolute config path supplied to ordinary commands, resolves the runtime directory through existing symlink ancestors, checks config source snapshots, and stores a random registration ID, loop name, config path and canonical runtime directory. Config-file symlinks are refused. Parent-directory aliases keep their original spelling so relative paths inside the config are not silently rebased. Registration does not establish that the config is runnable, controls are readable, the provider is installed or a supervisor is alive. It does not snapshot the task, target, goal, owner, account, credentials or executable path.

Adding the same config/name/runtime binding again returns its existing ID without rewriting the record. A changed name/runtime binding or another config pointing at an already-registered runtime directory is refused. Inspect the binding, remove the old ID and register again explicitly; a new registration gets a new ID. Duplicate loop names in different runtime directories are allowed: clients must use IDs, not names. Path comparison is not a durable filesystem identity or a defense against hard links, case aliases, mount changes or external rewrites.

`list` reads only the registry, so missing or broken configs can still be found and removed. Default output includes IDs and loop names. `--private` additionally prints config/runtime paths; keep this output private. Removing a reference does not delete configuration, state or task files, stop a daemon, revoke ownership or change any hold. A missing ID is an error; after a lost reply, list before retrying.

## Storage and version 1 output

The default directory is `keepgoing/registry` under the operating system's user configuration directory (Go's `os.UserConfigDir`). `--directory` selects a different private registry directory for every operation. The resolved directory must be outside Git checkouts; add also rejects destinations within the loop's configured progress repositories. No filesystem search or automatic import is performed.

`registry.json` contains `schemaVersion: 1` and `entries`, with each entry holding `id`, `loop`, `configPath` and `stateDir`. Writes use a short independent `registry.lock` and atomic replacement. They never acquire loop locks or edit loop state. Read-only listing does not create directories or lock files and sees either the old or new complete registry. Configuration reads and registry writes are not one transaction: the later client must revalidate each binding before relying on it.

New directories use mode 0700 and files mode 0600 on Unix. Existing registry directories/files with group/other mode bits, non-regular records and symlink records/locks are refused without changing their permissions. Existing ancestor aliases are resolved by the CLI. These mode checks are not full ACL/ownership evaluation; Windows relies on inherited access controls. Protect the surrounding private directory yourself. External writers that replace paths or ignore locks, power loss and filesystem-specific durability are outside these guarantees.

Records are limited to 128 entries and 1 MiB. Unsupported schema versions, malformed records, unknown fields, duplicate IDs/config paths/runtime paths and oversized records fail closed; no automatic repair, migration or data eviction occurs. A failed storage update preserves the old registry, although a newly created directory/lock may remain. A lost command reply can follow a successful save; inspect list before retrying. Error messages withhold raw configuration/decoder content.

Successful JSON commands return:

```json
{
  "schemaVersion": 1,
  "operation": "list",
  "entries": [
    {"id": "loop_0123456789abcdef0123456789abcdef", "loop": "example-loop"}
  ]
}
```

`add` returns one entry; `remove` returns an empty entries array plus `removedId`. Private list entries additionally include `configPath` and `stateDir`. Failure prints a diagnostic to stderr with no success JSON. Exit codes are 0 for success, 1 for unavailable/invalid records or binding conflicts, 2 for invalid flags, and 75 for registry lock contention. Consumers must check exit status and `schemaVersion`, tolerate future additive output fields, and never treat a partial/failed read as an empty list of loops.

## Next client contract (planned, not callable)

A shared versioned status/control surface is the next prerequisite. Its implementation must:

- Resolve a registration ID and revalidate config path, loop name and canonical runtime binding. Mark missing, changed or unverifiable bindings unavailable rather than follow a replacement silently. Registration is not authority to control a loop.
- Project explicit holds, actual target, goal ID/revision and optional private display metadata, recorded action/outcome and freshness, and authenticated daemon evidence. Keep historical checks, responsive supervisors and observed agent activity distinct. Do not assume an external scheduler is installed or healthy. Stage metadata remains absent until an explicit stage contract exists.
- Require explicit pause/resume/stop requests, preserving the existing CLI ownership, target, source-snapshot and hold rules. Permissive actions must be tied to the inspected target/owner/config identity to reject stale client clicks; restrictive pause/stop must remain independent of slow reads. Resume clears holds only when explicitly requested. No client may start a supervisor just by opening a window.
- Use the locally selected installed CLI through fixed arguments and structured output, without a shell, arbitrary commands from registry data, a network listener or another scheduler. Clients reject unsupported protocol versions and retain uncertainty after lost responses rather than replay mutations automatically.

The native companion, optional init registration/installation choice, goal-title display, signing/distribution and live UI verification remain planned. Offline tests cover registry reload/idempotence, binding conflicts, concurrent writers, bounds, invalid records, aliases, path privacy and unchanged loop files. Windows exercises the portable registry commands in CI; Unix additionally checks modes and symlink handling. This is not end-to-end companion or migration-readiness evidence.
