# Local client discovery

The optional menu-bar companion needs an explicit list of loops to display. On `main` after v0.1.3, `keepgoing registry add|list|remove` provides that first prerequisite. It is unreleased and offline-tested. Read-only `client status` and explicit restrictive `client pause|stop` commands are implemented below. The GUI, guarded client resume and automatic setup registration remain planned.

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

## Registered status (schema 1)

```sh
keepgoing client status --id loop_0123456789abcdef0123456789abcdef --json
keepgoing client status --id loop_0123456789abcdef0123456789abcdef --json --offline
keepgoing client status --id loop_0123456789abcdef0123456789abcdef --json --private
```

Use `--directory` for a non-default registry, as with registry commands. One request inspects one registered ID. It revalidates the stored loop name and canonical runtime directory against the current regular config file, preserving config-relative path semantics. Missing IDs, invalid configs, changed runtime/name bindings, a pending transfer or a mismatched transferred BB target return unavailable status. The registry and config sources are rechecked after the daemon query; a change discards the snapshot. These checks do not establish a durable file identity or authorize later controls.

Successful reads and evidence failures both produce a JSON envelope with `schemaVersion: 1`, `registrationId`, `readAt`, `outcome`, `issues` and an optional `snapshot`. Outcomes describe evidence availability, **not loop or agent health**:

- `available` (exit 0): the required binding and readable local records supplied a snapshot. A stopped/disabled loop, missing daemon endpoint or no check history may still have available evidence.
- `partial` (exit 1): independent fields are usable, but control, state, target, window or goal evidence is unavailable. Render the usable fields alongside the issue codes; never supply a healthy default for omitted fields.
- `unavailable` (exit 1): the binding is unverifiable, changed during the read, has a pending/mismatched transfer, or the request was cancelled. There is no snapshot. Registry failures are also unavailable, never an empty successful result.

Invalid arguments return exit 2 with no report. `issues` contains fixed codes such as `registration_unavailable`, `binding_unavailable`, `binding_changed`, `registration_changed`, `transfer_pending`, `target_binding_mismatch`, `control_unavailable`, `state_unavailable`, `target_unavailable`, `window_unavailable`, `goal_unavailable` and `cancelled`. Clients must reject unsupported schema versions, tolerate additional fields/codes, and preserve uncertainty on nonzero exit or lost output.

The snapshot projects the loop name; explicit control flags and summary; `completionEvidence` (`declaration_only` or `unavailable`); the configured active target alias/adapter/BB thread with `identityEvidence: configuration_only`; recorded runtime state, source, freshness, threshold, timestamp and build comparison; and the historical last action/target/outcome/dry-run flag. Unknown action/outcome values become `unknown`; missing zero timestamps are omitted. A missing control file means disabled, while an unreadable/malformed one omits controls. Missing history has `freshness: never`; corrupt history omits target/runtime rather than assuming primary. `--stale-after` changes the positive freshness threshold (default 30 minutes) without changing supervision policy.

Goal output is limited to ID, revision and metadata state. `--private` adds only the current goal title and next-action summary, from the same revision; it still withholds objective/acceptance/scope/evidence references and history. A goal owned by another loop is unavailable. No task file, transcript, provider output, session artifact, account path, control note, owner identity or config/runtime path is emitted. Private text output is quoted to preserve control characters safely. These metadata fields are descriptions, not execution instructions or acceptance evidence.

By default the command challenges the existing local daemon endpoint with a one-second timeout. It reports the authenticated response's timestamp, phase, expected next check, dry-run flag and build comparison; it never starts a responder. `--offline` skips this query. `externalScheduler: not_inspected` remains explicit; a responsive daemon proves neither current agent activity nor future execution. There are no adapter/provider/BB/scheduler/Git commands, controls, notifications, locks or writes. The command does not scan all registry entries or execute a command specified by registry data.

`readAt` is the start of independent reads, not an atomic snapshot timestamp. Controls, state, goals and daemon evidence can change independently or immediately afterward. Endpoint unavailability alone does not make all other evidence partial. There is no observation token or permission to reuse this snapshot to resume a loop. Out-of-band filesystem changes and changes that revert during a read are not fully detectable.

This surface is implemented after v0.1.3 and unreleased. Offline CLI/storage fixtures cover privacy, holds, missing/corrupt records, changed bindings, redacted unknown values and changes during a bounded query. A local authenticated daemon fixture verifies response versus offline behavior without a live provider. Portable client tests run in Windows CI. No installed loop, native client or live provider walkthrough was used.

## Registered pause and stop

```sh
keepgoing client pause --id loop_0123456789abcdef0123456789abcdef --json
keepgoing client stop --id loop_0123456789abcdef0123456789abcdef --json
```

These are explicit requests to add a hold to the registered loop, independent of whichever target it currently supervises. They do not resume, enable, take over ownership, clear any hold, stop a process directly or start a supervisor. `pause` sets only paused; `stop` sets only stop. Both preserve notes, disabled/completed/awaiting-user state, owner generation and pending transfer metadata. As with ordinary CLI pause/stop, a different BB caller can add these restrictive holds without taking over. They intentionally need no earlier status token, live target lookup or healthy daemon response. A stop is executed by a later authorized supervisor check; its save does not confirm provider termination.

Use `--directory` for a non-default private registry. The command pins an existing registration under the short registry lock, loads the current config, then revalidates registration, loop name, canonical runtime binding and config/fallback source bytes inside the control update. Registry removal and other control writers cannot overlap those locks. Missing/broken/rebound configs or malformed/non-object/oversized control records are refused rather than repaired. Pending transfers remain held and may receive additional restrictive flags. The command takes no tick lock and performs no provider, BB, daemon, scheduler or notification calls. Lock acquisition is nonblocking; it never waits for a running tick.

JSON responses have `schemaVersion: 1`, `registrationId`, `operation`, `controlSave`, `issues`, and `controls` only after a confirmed save. All issues are fixed codes; paths, notes, task text, owner identity and raw errors are withheld. Save evidence is explicit:

- `confirmed`: the control save succeeded. Exit 0 means control and diagnostic writes succeeded; exit 1 with `incident_history_unavailable` or `run_history_unavailable` means the hold is saved but diagnostic history is incomplete. Neither means the provider acted.
- `not_attempted`: a precondition, cancellation or busy lock prevented a save. Exit 75 with `busy` means contention; other refusals return exit 1. Other issue codes include `registry_unavailable`, `binding_unavailable`, `registration_changed`, `binding_changed`, `control_unavailable`, `cancelled` and `control_or_registration_unavailable`.
- `unconfirmed`: the command reached the save but could not confirm its result (exit 1). Inspect status before another action. A missing or lost response is also uncertain, regardless of the subprocess exit status; clients must never infer failure-to-save from lost output or automatically replay mutations.

Invalid arguments return exit 2 without a report. Invocation may create private runtime lock/control/diagnostic files for an existing registration, even when no supervisor has started; the loop stays disabled if it was disabled. Repeated explicit pause/stop calls can append diagnostic records. These are cooperative local locks and source checks, not durable file identities or protection from arbitrary external writers. Config/fallback contents changed before the invocation are loaded as current; these restrictive actions target the loop, not an earlier displayed task. If its config cannot be validated, use the existing CLI with a known correct config for recovery. Registered client resume remains unavailable.

Implemented after v0.1.3, unreleased; portable offline fixtures only. Tests cover busy registry/control locks, operation during a held tick, pending transfers, changed bindings, corrupt records, privacy, disabled loops and confirmed saves despite history failures. No installed loop or live provider was changed.

## Next client resume (planned, not callable)

Registered pause/stop do not implement permissive controls. The next resume surface must:

- Resolve a registration ID and revalidate config path, loop name and canonical runtime binding. Mark missing, changed or unverifiable bindings unavailable rather than follow a replacement silently. Registration is not authority to control a loop.
- Project explicit holds, actual target, goal ID/revision and optional private display metadata, recorded action/outcome and freshness, and authenticated daemon evidence. Keep historical checks, responsive supervisors and observed agent activity distinct. Do not assume an external scheduler is installed or healthy. Stage metadata remains absent until an explicit stage contract exists.
- Require explicit resume requests, preserving the existing CLI ownership, target, source-snapshot and hold rules. Permissive actions must be tied to the inspected target/owner/config identity to reject stale client clicks; restrictive pause/stop must remain independent of slow reads. Resume clears holds only when explicitly requested. No client may start a supervisor just by opening a window.
- Use the locally selected installed CLI through fixed arguments and structured output, without a shell, arbitrary commands from registry data, a network listener or another scheduler. Clients reject unsupported protocol versions and retain uncertainty after lost responses rather than replay mutations automatically.

The native companion, optional init registration/installation choice, guarded client resume, signing/distribution and live UI verification remain planned. Offline tests cover registry reload/idempotence, binding conflicts, concurrent writers, bounds, invalid records, aliases, path privacy and unchanged loop files. Windows exercises the portable registry commands in CI; Unix additionally checks modes and symlink handling. This is not end-to-end companion or migration-readiness evidence.
