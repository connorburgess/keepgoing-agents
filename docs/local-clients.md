# Local client discovery

The optional menu-bar companion needs an explicit list of loops to display. With v0.1.4, `keepgoing registry add|list|remove` provides that first prerequisite. The CLI contract is offline-tested; publication does not establish live acceptance. Read-only `client status` and explicit restrictive `client pause|stop` commands are implemented below. The prepared-resume flow below also supports explicit hold clearing. An unreleased [read-only native menu preview](../integrations/macos/README.md) consumes this contract. Unreleased [`keepgoing dock`](dock.md) serves a read-only page, the keepgoing panel, for every registered loop. Unreleased `keepgoing panel --allow-changes` lets that page [pause, stop and resume](dock.md#changes-from-the-page-keepgoing-panel---allow-changes) a loop through these same commands' functions, recorded as from the panel. Automatic setup registration remains planned.

Registration stores a reference only. It never enables, resumes, stops or transfers supervision; queries a provider; creates a thread; changes a scheduler; or reads a task file. Headless and process configurations need no BB installation to register. The registry is separate from the BB plugin's private setup list; neither imports or replaces the other.

## Planned run and chat navigation

The [run discovery plan](run-discovery.md) adds durable local references and CLI/JSON traversal from a routine or loop to its execution runs, provider chats/sessions and known blocking runs. In unreleased source, [`keepgoing runs`](run-discovery.md#cli-navigation-d3-unreleased) reads the run records offline, by configuration or by registry reference (`--registry-id`, `--all`). It is a separate report, not part of registry or registered status schema 1; it also shows what a loop is waiting on (roadmap D4, partial). Menu/plugin navigation remains planned. `registry add` records the reference in the loop's private `graph.json` when that graph already exists, and registered pause, stop and resume record the reference they used; `registry add` never creates a runtime file. Existing registration IDs bind a configuration reference; they do not identify an execution run or authorize navigation-based controls. The optional menu/plugin will consume the same contract that `keepgoing runs` reads.

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

The default directory is `keepgoing/registry` under the operating system's user configuration directory (Go's `os.UserConfigDir`). `--directory` selects a different private registry directory for every operation; when it is not given, `KEEPGOING_REGISTRY_DIR` names one (the tests use this to keep away from the person's own registry). `doctor` names the directory whenever the variable is set. The resolved directory must be outside Git checkouts; add also rejects destinations within the loop's configured progress repositories. No filesystem search or automatic import is performed.

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

This CLI surface is released in v0.1.4. Offline CLI/storage fixtures cover privacy, holds, missing/corrupt records, changed bindings, redacted unknown values and changes during a bounded query. A local authenticated daemon fixture verifies response versus offline behavior without a live provider. Portable client tests run in Windows CI. No installed loop, native client or live provider walkthrough was used.

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

Invalid arguments return exit 2 without a report. Invocation may create private runtime lock/control/diagnostic files for an existing registration, even when no supervisor has started; the loop stays disabled if it was disabled. Repeated explicit pause/stop calls can append diagnostic records. These are cooperative local locks and source checks, not durable file identities or protection from arbitrary external writers. Config/fallback contents changed before the invocation are loaded as current; these restrictive actions target the loop, not an earlier displayed task. If its config cannot be validated, use the existing CLI with a known correct config for recovery. Registered resume uses a separate reviewed ticket, described below.

Released in v0.1.4; portable offline fixtures only. Tests cover busy registry/control locks, operation during a held tick, pending transfers, changed bindings, corrupt records, privacy, disabled loops and confirmed saves despite history failures. No installed loop or live provider was changed.

## Registered resume

```sh
keepgoing client prepare-resume --id loop_0123456789abcdef0123456789abcdef --json
# Review the returned snapshot, then pass its ticket to an explicit resume request:
keepgoing client resume --id loop_0123456789abcdef0123456789abcdef --ticket resume_0123456789abcdef0123456789abcdef --json
```

The example ticket is a placeholder; use the freshly returned value. Both commands accept `--directory`. Preparation optionally accepts `--private` for the current goal title and next-action summary. Resume does not accept `--private`, takeover, enable or a new target. Clients must show the prepared snapshot and what resume clears before accepting the explicit action. On stale evidence, prepare and display a new snapshot for a new action; do not silently refresh the ticket and retry the old click.

`prepare-resume` leaves controls unchanged and returns the ordinary redacted snapshot with offline daemon evidence, plus a random `ticket` and `expiresAt`. It writes one private `client-resume.json` slot in the registered runtime directory, superseding any earlier preparation. This explicit preparation is a write operation; ordinary `client status` remains read-only and returns no ticket. Preparation may create lock/runtime files but never enables or starts a supervisor. Ticket issuance means local evidence was available, not that the target is running or the goal is approved/completed.

The ticket binds the registration/config/runtime reference, exact parsed config/fallback source snapshots (including paths), BB caller identity (including absence), full parsed control and state records, and validated full goal metadata (including absence). These comparisons stay inside private storage as one digest; raw paths, owner IDs, source hashes, task text and goal references are not returned. Full state binding deliberately invalidates a ticket when even a routine tick changes saved history. Refresh and review again if this occurs. Referenced task files, provider sessions, external BB metadata, environment values other than the caller, and arbitrary referenced evidence are not inspected or bound. Configured target identity is not live session verification.

Both operations acquire nonblocking locks in registry → tick → control order and revalidate sources and bindings. Busy locks return exit 75 without waiting. Owner conflict, malformed records, pending transfers or mismatched transferred targets prevent preparation/resume. As with ordinary CLI resume, an identity-free local terminal may use the existing owner claim; it cannot reuse a ticket issued to a different caller identity. Neither command takes over ownership. Pause/stop retain their independent short path and need no ticket.

Tickets expire five minutes after issuance. A clock earlier than issuance, a missing/corrupt/unsupported ticket, a superseded ID or an already-consumed ticket is refused. Resume compares fresh evidence under the locks, records consumption first, then rechecks before saving the control change. A crash or failure after consumption can leave the loop held with a spent ticket: inspect status, prepare again, review and explicitly resume. There is no automatic replay or crash recovery that clears holds. Successful resume clears paused, stop, awaiting-user, completed and the control note, exactly like ordinary resume. It preserves enabled/disabled state, owner generation, active target, goals, sessions and the loop-wide allowances (sends used, deadline, no-progress ledger, new-conversation cap); it starts no daemon or provider turn itself. In unreleased source it also [starts a new error episode](operations.md#what-resume-starts-over), as ordinary resume does, when the hold is one keepgoing raised for a spent re-prompt or nudge allowance, so a client must say that those counts then start over when it shows what resume clears. The history records the caller as `client`.

Preparation and resume return schema-1 envelopes with the same `registrationId`, `operation`, `controlSave` and `issues` fields as restrictive controls. Successful preparation has `controlSave: not_attempted`, a `ticket`, `expiresAt` and `snapshot`. Successful resume has `controlSave: confirmed` and the saved `controls`. Refused preparation has no ticket or snapshot. Additional fixed issue codes are `resume_evidence_unavailable`, `observation_changed`, `ticket_unavailable`, `ticket_unavailable_or_expired`, `ticket_consumption_unconfirmed` and, in unreleased source, `new_episode_unconfirmed`: the saved per-episode counts could not be reset, so the resume was refused with the holds intact and the ticket spent. Invalid flags return exit 2; other refusals return exit 1. Diagnostic history failures and uncertain saves use the same semantics as pause/stop. A lost response is uncertain: inspect status before acting, never replay automatically. A successful save is not evidence of subsequent agent activity.

The ticket file contains a schema version, random ID, private binding digest, issuance/expiry and consumption flag, with atomic replacement and mode 0600 on Unix. Reads reject nonregular, oversized (over 4 KiB), malformed and unsupported records; Unix also refuses group/other mode bits on the runtime directory or ticket. Windows relies on inherited access controls. A corrupt ticket must be inspected/restored privately; preparation will not silently overwrite it. One slot bounds retained state; expired/consumed records are replaced only by another explicit preparation. These are cooperative local checks, not authentication against someone who can edit files or environment variables. External file replacements, changes that revert to identical evidence, old binaries and power-loss durability remain outside the guarantee.

Released in v0.1.4. Offline fixtures cover changed config/fallbacks/target/owner/caller/control/state/goals, supersession, expiry and clock rollback, busy locks, interrupted consumption, lost output, diagnostic failure, privacy and process targets without installed BB/providers. Simulated interruption verifies the consume-before-save boundary; it is not a live crash/agent walkthrough. The read-only native preview does not yet expose these controls. Optional init registration/installation, signing/distribution, live UI and provider verification remain planned.

## Reviewed pause, stop and resume (`client review`, `client apply`)

Unreleased; fixtures and the menu's contract smoke only.

```sh
keepgoing client review stop --loop l0123456789ab --json
keepgoing client apply stop --loop l0123456789ab --expect <the review's expect value> --json
```

These are the [panel's](dock.md#changes-from-the-page-keepgoing-panel---allow-changes) pause, stop and resume for a local program that shows its own confirmation, such as the [macOS menu](../integrations/macos/README.md). Both take the loop by the page key `panel list --json` gives (`--loop`), never a registration ID or path, and act as no BB thread: `BB_THREAD_ID` is ignored.

- **`client review pause|stop|resume`** reads and changes nothing. It reads the loop the way the page does and prints the page's own review, built by the same code, as plain sentences: `preview` with `action`, `key`, `title`, `lines`, `confirm` (the confirming button's words), `expect`, `expiresAt`, and what to say when the confirmation's reply is lost (`lost`), refused (`refused`) or too late (`expired`). It reads the control record under its lock, which may create the empty lock file. A loop that cannot be reviewed now gives `result` instead, in the page's words, and exit 1.
- **`client apply pause|stop|resume --expect V`** makes the change only if the loop's control record is still the one reviewed: `expect` is a hash of that record, and a record changed since is refused as `review_again` inside the control lock. A resume prepares and uses its own ticket in the one command, as the page's does. The reply is `result` with `outcome` (`done`, `refused`, `review_again` or `uncertain`), `text` in the page's words, and `retry` when a new review may be offered. Exit 0 when done, 75 when a check holds the loop, 1 otherwise. Nothing is retried; an `uncertain` outcome means look at the loop before anything else.
- The history records these as ordinary `pause`, `stop` and `resume`, as `client pause|stop` and `client resume` do, not as from the panel.
- The page's expiry (2 minutes, 5 for a resume) is for the program to keep: the menu does not apply a review past `expiresAt`.

`panel list --json` says which changes fit each loop's control flags, as the page offers them (`can`: `pause` when not paused, `stop` when no stop is requested, `resume` when any hold is set). Today a stop stops the conversation only, at the loop's next check, and the review says so. Stopping a loop's lanes and stopping now are planned (`stop --lanes`, `--now`); when the CLI has them, `client review stop` will advertise them in a `stop` object and `client apply stop` will take them.
