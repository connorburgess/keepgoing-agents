# Diagnostics

`keepgoing doctor` checks a loop without starting an agent, running a probe, changing a login, or writing runtime state. It is implemented on `main`; v0.1.1 does not include it. A separate loop skill is not required.

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml
keepgoing doctor -c /private/example-loop/keepgoing.yaml --json
keepgoing doctor -c /private/example-loop/keepgoing.yaml --offline
```

The ordinary command resolves configured executable paths and runs supported agents' `--version` commands, BB's `thread show <id> --json`, and headless providers' login-status commands described below. In v0.2.0 it also runs the loop's [connectivity check](operations.md#network-outages) once, reported as `network.check`: one request to the configured target, or one run of `network.command`. Each command has a five-second timeout and a 64 KiB stdout limit. `--timeout` changes the per-command deadline. `--offline` skips all command execution and local daemon queries while still inspecting configuration, files, and executable availability. Custom process-adapter commands, lifecycle logs, model calls, and probe commands are never executed. Explicit diagnostics can query BB even when the loop is held; they do not change that hold.

## What it checks

- Configuration and fallback parsing/validation, task-file readability, state/control/health readability, and private-file Unix mode bits. Separate read-only OS checks inspect runtime path access on macOS/Linux/Windows and ACL metadata on Linux/macOS/Windows as described below. Doctor does not print task text or control notes or change permissions. Full ACL privacy evaluation is unavailable.
- Executable availability and recognized version output for the primary and enabled fallback agents. It displays only a narrowly recognized provider version, never arbitrary stdout or stderr. A recognized version does not establish compatibility with every required provider feature.
- BB thread identity from structured JSON, including explicit not-found, archived, and deleted results. A missing ID, mismatched ID, malformed JSON, or failed query is unverified. The configured primary differing from the caller's `BB_THREAD_ID` produces a warning; doctor never transfers the loop.
- Explicit headless account-directory overrides for existence and absolute paths. Account directory values and all other environment values are withheld. Directory existence does not verify credentials, subscription identity, or independent quota. Inherited account selection is reported as unverified.
- The recorded active target and quota group, current control state, and last completed tick. A missing or disabled active fallback is an error. Status history is not read from agent transcripts.

Checks have stable IDs and one of four statuses:

| Status | Meaning |
| --- | --- |
| `ok` | The specific check passed; this is not a blanket claim that the loop is ready. |
| `warning` | Something needs review, such as stale history or broad file permissions. |
| `error` | A concrete failure, such as invalid configuration, missing CLI, or missing BB thread. |
| `unavailable` | The check is unsupported, deliberately skipped, not yet applicable, or could not establish a result. |

Exit status is **1** if any check is `error`, **0** otherwise, and **2** for invalid flags. Exit 0 may include warnings and unavailable checks; scripts must inspect the report for their own readiness requirements. Invalid configuration also produces a JSON report when `--json` is requested. Parser errors and raw command output are withheld because they can contain private values.

JSON has `schemaVersion: 1`, `version` (this doctor's binary), `checkedAt`, `staleAfter`, `nextCheck`, and `checks`. With valid configuration it also identifies the loop, recorded target, configured target agent and quota group, readable control state, and last recorded check when available. Each check has `id`, `status`, `message`, and an optional `fix`. Treat IDs and statuses as machine fields; messages are explanatory text. No credentials, environment maps, prompts, or control notes are included.

<a id="cursor-diagnostics-unreleased-source"></a>

## Cursor diagnostics

For a Cursor loop, doctor resolves the configured `agent` executable and may read `--version`; `--offline` skips that call. It recognizes the date/hash version shape without treating it as a verified compatibility range. Account identity, login health and quota state remain unavailable. The `agent.<target>.capabilities` check also describes the missing IDE attachment, interactive permission bridge and quota/reset/fallback support. Doctor never runs Cursor login, model-list or prompt commands. These source-only checks do not establish real-provider readiness; see [Cursor evidence](cursor.md).

## BB automation diagnostics

Select an existing script automation explicitly:

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml \
  --bb-automation auto_example --bb-project proj_example --json
```

The two selection flags are required together. `--bb-cli /path/to/bb` optionally selects the BB executable for this query, independently of the loop's provider CLI. The selection is used only for this diagnostic invocation and is not written into configuration. No automation is discovered by matching prompt or script text.

Doctor calls only `bb automation show <id> --project <id> --json`, under the ordinary command timeout and output limit. It verifies both returned IDs, requires a script automation and an explicit enabled field, and parses timestamps as Unix milliseconds. `scheduler.bb` reports disabled automation, missing or overdue next-run metadata, and a reported failed last run as warnings. Missing CLIs, query errors, unexpected shapes or timestamps, and agent-mode automations remain unavailable. Unknown last-run outcomes are shown as `unknown`, never raw text.

The optional `bbAutomation` JSON object contains `state` (`registered`, `unavailable`, or `offline`), the selected `id` and `projectId`, `enabled`, UTC `nextRunAt`/`lastRunAt` when present, a recognized `lastRunStatus`, `loopVerified: false`, and an explanatory message. No automation name, script, prompt, environment, script path, error text, or run output is displayed or used for a decision. `--offline` skips the query even if the selected CLI is missing.

This is registration and historical run metadata, **not verified loop scheduling**. An enabled record does not prove that the scheduler process is alive, its binary is current, or its script invokes this loop. A skipped run may be a normal silent tick; a successful script does not establish task progress. The selected automation's next-run time stays separate from the report's `nextCheck`, which still requires a responding built-in daemon. Doctor never runs, pauses, resumes, edits, or creates an automation, and never changes loop control holds.

Offline fixtures cover the read-only command and deadline, identity mismatches, unsupported modes, redaction, stale/disabled/failed metadata, invalid selection, and unchanged held state. A read-only macOS smoke check against an existing BB script automation verifies the live response shape; it does not run the script or verify its loop binding. These automation checks do not inspect launchd, systemd, cron, or Windows Task Scheduler. Managed launchd/systemd diagnostics are available separately through [service status](services.md).

## Runtime path access

`state.write_access` checks write and search permission on the runtime directory using the OS access API on macOS and Linux. If the directory does not exist, it checks the nearest existing ancestor and labels that result explicitly. It never creates directories or temporary files. An inaccessible path, read-only filesystem, non-directory ancestor, or dangling directory symlink is reported without exposing the path or raw filesystem error.

Existing regular `runs.jsonl`, `tick.lock`, and `daemon.lock` files get separate `state.<filename>.write_access` checks: history needs write access, while locks need read/write access. Append/lock symlinks and other special files are skipped with an unavailable result. Atomic JSON records are replaced through their directory; doctor does not incorrectly require direct write permission on their existing files. Their readability checks still apply.

These checks run offline too and never write data, take a lock, change permissions, or alter control holds. They use the caller's current host context, which may differ from the scheduler's. On macOS/Linux, the OS access API evaluates real credentials, so doctor supports only ordinary non-root callers with matching real/effective user and group IDs. Unix checks never open files for writing. Privileged or mismatched Unix identities and unsupported platforms are explicitly unavailable.

On Windows, doctor uses [CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew) with `OPEN_EXISTING`, requests the needed rights, and immediately closes the handle without reading or writing data. It permits read/write/delete sharing and never creates or truncates a file. Directory checks request file creation, subdirectory creation, and traversal rights; history checks match Go's append-open rights, while lock checks request read/write rights. Directory handles require `FILE_FLAG_BACKUP_SEMANTICS`, so doctor first excludes thread impersonation and enabled backup/restore privileges to avoid a permission bypass. Unknown token information remains unavailable; no privileges are enabled or changed. Sharing conflicts are unavailable, not classified as permission denials.

An `ok` result means the OS allowed the requested access at check time. It does not guarantee a future write, sufficient disk space, lock acquisition, file replacement, or access to every nested target directory. ACLs may affect the OS decision, but this is **not an ACL privacy audit**: owner-only mode bits alone do not prove that nobody else has access. No ACL entries or account identifiers are printed. Permission denials are errors; unclassified filesystem failures remain unavailable.

Offline fixtures cover denied/read-only/unknown results, missing ancestors, dangling links, append/lock selection, unchanged held state, and redaction. Native macOS/Linux tests exercise read-only directory and lock permissions under an ordinary user. Native Windows CI fixtures exercise allowed and denied directory access, missing ancestors without creation, append-only DACLs, read-only files, sharing conflicts, unchanged contents, released handles, and offline held state. Test DACL changes are confined to temporary fixtures; production doctor never changes permissions. Pure fixtures cover enabled/disabled privilege filtering and malformed token metadata. These tests do not verify remote shares, every reparse-point type, impersonation behavior, or every Windows architecture; DACL privacy inspection is separate below.

## Linux ACL metadata

Each successfully opened file or directory checked by doctor also gets a `<check-id>.acl` entry, such as `config.permissions.acl`, `task.acl`, or `state.directory.acl`. On Linux, doctor queries only the sizes of `system.posix_acl_access` and, for directories, `system.posix_acl_default`. It never loads ACL values or principal identifiers, executes an ACL command, or changes filesystem metadata. These checks work offline and preserve control holds.

Present metadata produces a warning to review ACL rules alongside mode bits. A directory's default ACL governs inheritance for new objects and can exist while that directory still has owner-only mode bits; see [Linux ACL semantics](https://man7.org/linux/man-pages/man5/acl.5.html). Presence alone does not establish that another user can access private data: doctor does not evaluate ACL entries or effective permissions for other users.

`ENODATA` is reported as **not reported (absent or hidden)**, because [the Linux API documents both possibilities](https://man7.org/linux/man-pages/man2/getxattr.2.html). Unsupported filesystems, permission errors, unexpected results, and other platforms are unavailable. No detected metadata is never treated as a privacy all-clear. If either query detects metadata, the check warns even when the other query is unavailable, with both results shown.

This Linux check inspects the configured path's target, following symlinks like the existing readability check. It does not inspect ancestor ACLs, missing paths, every nested runtime file, or non-POSIX ACL formats. macOS and Windows metadata have separate implementations below. Results are observations at query time, not a guarantee against subsequent filesystem changes. Mode-bit and runtime-access checks remain separate.

Fixtures cover present/default/unknown/hidden metadata, error redaction, and offline held loops without provider calls. A native Linux fixture creates a default ACL in a temporary directory, verifies detection despite `0700` mode bits, and confirms the query leaves ACL bytes and files unchanged. The native fixture skips filesystems without POSIX ACL support; CI logs that case separately so its execution can be verified.

## macOS ACL metadata

The same `<check-id>.acl` entries inspect macOS extended ACL metadata through `getattrlist`, requesting only `ATTR_CMN_EXTENDED_SECURITY`. A fixed 12-byte buffer receives the length and attribute reference. `FSOPT_REPORT_FULLSIZE` supplies the full required size while leaving the variable security payload outside that buffer. Doctor validates the header layout and length arithmetic, then reports metadata presence without loading ACL entries, owner/group identifiers, or principal UUIDs. The interface is described by [Apple's metadata API](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/getattrlist.2.html) and the current [SDK definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/attr.h).

Present metadata warns even when Unix mode bits are owner-only. A zero-length payload is **not reported (absent or unsupported)**; failed queries and malformed headers are unavailable. None is a privacy all-clear. An empty ACL structure can still count as present metadata: doctor does not inspect entry counts, allow/deny rules, or inheritance flags, and does not apply Linux default-ACL semantics to macOS.

The query follows the configured symlink target and runs offline without commands, writes, or locks. It checks the same successfully opened paths as the other file diagnostics, not ancestors or every nested runtime file. Native temporary-file and directory tests add an ACL while retaining `0600`/`0700` mode bits, verify the warning, exercise symlink targets and removal, and check that inspection leaves file contents, modes, modification times, and ACL presence unchanged. Separate fixtures cover malformed/truncated headers, offset and length overflow, failed queries, private output, and held state. No real account changes or provider calls are used.

## Windows DACL metadata

Windows uses the same `<check-id>.acl` entries. Doctor resolves the configured symlink path and calls [GetNamedSecurityInfoW](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-getnamedsecurityinfow) for DACL information only. Windows allocates a security descriptor; doctor asks native APIs for DACL presence and entry count, then frees that allocation. Unlike the Unix size-only queries, this API obtains the descriptor in native memory, but doctor never parses ACEs, resolves SIDs, requests owner/group/SACL information, or prints descriptor contents.

The result distinguishes four cases:

| DACL metadata | Status | Meaning |
| --- | --- | --- |
| Null or absent | `error` | The DACL allows unrestricted access; other security mechanisms are not evaluated. |
| Empty | `warning` | No access is granted through DACL entries. Review whether the supervisor can operate. |
| Contains entries | `warning` | Rules exist, but their effective grants, denies, and inheritance were not evaluated. |
| Query failed or unavailable | `unavailable` | No conclusion; raw errors and descriptor contents are withheld. |

[Microsoft distinguishes null and empty DACLs](https://learn.microsoft.com/en-us/windows/win32/secauthz/null-dacls-and-empty-dacls). A populated or empty DACL is not reported as a privacy all-clear. These checks do not evaluate the caller's token, privileges, integrity level, share permissions, effective write access, ancestor ACLs, or every nested runtime file. They inspect only paths that passed the existing readability/type checks, and cannot guarantee that permissions remain unchanged after the query.

Inspection works offline without commands, permission changes, temporary writes, or locks. Dedicated Windows CI runs native in-memory null/absent/empty/populated descriptor fixtures and temporary-file/directory queries, plus shared redaction and held-loop fixtures. No descriptor is installed onto a real file by these tests. They verify local native APIs, not every filesystem, remote share, reparse-point type, or Windows architecture.

## Saved headless sessions

For each configured primary or enabled fallback using the Claude Code or Codex headless adapter (and the primary-only Cursor adapter in v0.2.0), doctor inspects `targets/<target>/session` under the loop's state directory. The `agent.<target>.session` check and the JSON `sessions` map identify the saved resume ID. Text output labels it as saved and unverified live. BB and custom process targets do not have these fields.

Each map entry has `state` (`saved`, `missing`, or `unavailable`), an optional `id`, `source: "runner_session_file"`, `liveVerified: false`, and an explanatory `message`. Missing records are normal before a resume ID is saved. An unreadable or unrecognized record produces a warning without displaying its content. Only UUID-shaped IDs from regular files of at most 128 bytes are displayed; symlink records are not followed. Other ID formats still work as before in the runner, but diagnostics withhold them.

This inspection works in offline mode, does not modify holds or session files, and never reads prompt, stdout, stderr, or transcript files. A saved ID may belong to a previous turn; it does not prove that a process is running, that provider history still exists, or that a session can resume under the current account/settings. A first turn can be active before any ID has been saved. Live identity checks remain planned.

## Headless login status

For primary and enabled fallback headless agents, doctor runs the provider's status command with the target's environment overrides and working directory:

- Claude Code: `claude auth status --json`. A boolean `loggedIn: true` with exit 0 is `ok`; `loggedIn: false` with exit 0 or 1 is a `warning`. Missing/malformed JSON, contradictory success with a failed command, and other failures are `unavailable`. The command and its exit codes are described in the [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference).
- Codex: `codex login status`. Exit 0 is `ok`, meaning credentials are present according to the [Codex CLI reference](https://developers.openai.com/codex/cli/reference/). Any failure is `unavailable`: doctor cannot distinguish missing credentials from an unsupported command or operational error using a nonzero exit alone. It never parses the prose output.

These checks report local provider status, not server validation, subscription identity, available quota, or model access. They do not sign in, sign out, or make model requests. Account identifiers, emails, auth methods, paths returned by providers, and raw output are withheld. Doctor does not open credential files itself. The provider may perform its own ordinary CLI housekeeping.

`--offline` skips these commands. BB-managed provider authentication remains unavailable. A headless target with `extra_args` also has an unavailable login check: arbitrary turn arguments can override authentication settings, and doctor does not replay them. Disabled fallback agents are not queried. The doctor's inherited environment may differ from an external scheduler's; run diagnostics in that scheduler's context when checking its account selection.

## Recent history is not scheduler health

A last check older than `--stale-after` produces a warning. The default is 30 minutes; set it above your external scheduler's interval:

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml --stale-after 2h
```

No history means no completed tick is recorded; enabling alone does not run a supervisor. A future timestamp warns about clock differences or a test using `--now`. A recent tick, including a dry-run tick, is historical evidence only: it does not prove the daemon or scheduler is still alive.

New ticks record their build label: doctor compares that historical label with its own and warns if they differ. Empty or `dev` labels cannot identify a build. A historical match does not prove which binary is currently running; a mismatch does not establish which is newer. Legacy tick records without a build label remain unavailable.

## Live daemon diagnostics

New `keepgoing daemon` instances provide a loopback-only diagnostic endpoint. Status and doctor send a fresh challenge and verify the reply against the private endpoint record and the state directory identity. A stale file, copied endpoint from another loop, or reused listening port cannot by itself establish liveness. Queries never acquire the daemon lock, start an agent, or alter control state. New snapshots also carry optional process/invocation metadata for Linux [service binding](services.md#linux-service-to-daemon-binding); public status and doctor reports omit the IDs, and responsiveness alone does not verify a service relationship. If diagnostics cannot start, the daemon warns and continues supervising.

JSON includes `daemon` with `state` (`responsive`, `not_recorded`, `unavailable`, or `skipped`), an explanatory `message`, `binaryComparison`, `dryRun`, and `nextCheck`. A verified response also supplies `phase` (`checking` or `waiting`), `startedAt`, `observedAt`, and `nextCheckAt` while waiting. `scheduler` is `ok` only for a verified response; `supervisor.live_version` compares that daemon's build label, independently of the historical `supervisor.version` check. Raw build labels, the verification key, and endpoint address are withheld.

A responding daemon is not necessarily making progress: it can be stuck in a check while its diagnostic endpoint still responds. The next normal check is due after the current pass plus the configured interval, subject to control-poll timing. Control changes can trigger an earlier pass. These times are not a promise of execution through sleep, shutdown, or a stuck pass. Dry-run daemons report their mode explicitly.

The query is capped at one second (a shorter doctor `--timeout` applies). Endpoint records and responses have size limits; the endpoint binds only to `127.0.0.1`. The record is private runtime state and contains a random verification key, which is neither sent in requests nor printed in reports. Clean daemon shutdown removes its record. Failures remain unavailable, and `--offline` skips the query entirely.

Older daemons and external schedulers, including BB automations, launchd, systemd, cron, and Task Scheduler, are **not inspected** by this interface. No endpoint does not mean no supervisor. Service installation is also unverified. The configured daemon interval is never used to invent an external scheduler's next-run time.

Live headless session identity is **not implemented**. Doctor does not start an app-server or resume a session to infer it. Use [`fallbacks check --dry-run`](fallbacks.md#preview-manual-probes) to preview selected models, timeouts, and the scheduled cap without model calls or state changes. Dollar cost is explicitly unknown; monetary estimation and spending limits are not implemented. Removing `--dry-run` makes this a separate explicit operation outside the scheduled daily cap. Doctor never runs it automatically.

## Verification

Offline tests cover structured target results, account environment propagation, privacy, missing executables, corrupt records, stale/future timestamps, disabled or missing active fallbacks, and unchanged control state. Unix subprocess fixtures cover time and output limits and stderr suppression. Login fixtures cover logged-in/out signals, command failures, deadlines, working directories, separate account overrides, disabled fallbacks, extra-argument skips, and redaction. These checks do not establish server-side credential validity, paid handoff, external scheduler health, or full ACL/effective-access evaluation. Local daemon fixtures cover fresh challenges, wrong/copied/stale endpoints, deadlines, live build redaction, control changes, and state-directory reload rejection.

## Incident investigation: current coverage and priority gaps

Today keepgoing owns `control.json`, the last recorded check in `state.json`, and the decision history in `runs.jsonl`. These can distinguish an explicit hold from a supervisor that has stopped checking. Tick output can include report-delivery warnings; a scheduler wrapper may save that output separately or discard it, so an automation's successful/silent-skip status alone does not prove delivery. Inspect the configured wrapper's output destination when investigating existing installations. Do not assume a background daemon preserves raw stdout/stderr.

The [bounded tick history](#bounded-tick-incident-history) preserves normalized observations, supported structured provider codes/categories, retry intent, decision inputs and saved control changes. The separate [delivery ledger](#notification-delivery-evidence) records notification outcomes. [`timeline`](#offline-incident-timeline) combines these records offline; provider-host logs may still be needed for details outside this sampled contract, including root-cause investigation. No raw transcript or stderr is interpreted.

The [priority reliability checkpoints](roadmap.md#priority-reliability-checkpoints) cover active retry classification, keepgoing-owned structured evidence across adapters, failed-alert visibility, a read-only timeline and recovery verification. Active BB retry handling is available in v0.1.3 and fixture-tested; see [retry handling and recorded reasons](operations.md#bb-retry-and-stall-handling). Common evidence, decision inputs, control-save records and combined chronology acceptance are implemented below with offline fixtures; delivery diagnostics and the offline timeline are also implemented below with fixtures; existing-loop deployment and live recovery verification remain separate; see [release evidence](release-verification.md#v013-reliability-release-evidence). Records must exclude task/transcript text and secrets; unsupported provider details remain unavailable. Connectivity recovery alone must never resume an explicit hold.

### Interrupted BB turns and host availability

In v0.1.5, diagnostic records retain the finite code `thread_command_failed` from structured BB failures. The code remains an **unknown** decision error: it does not prove a host disconnect, network outage, quota limit or authentication problem. Error message/detail text is neither classified nor saved. Older redacted records cannot be recovered. This diagnostic correction is released in v0.1.5.

`TestIncidentJourneyBBInterruptedCommandRecovery` verifies the following offline through fresh CLI harness invocations and fictional BB responses:

- A failing status command or invalid JSON records unavailable observation, sends nothing and spends no recovery attempt.
- A readable `error` status with an interrupted terminal event and generic failure code stays unknown. Queued work prevents another prompt. Once the queue is empty, the existing policy permits one recovery attempt in this fixture: a continuation prompt through v0.1.5, and in v0.2.0 one retry of the failed turn with no prompt.
- If the error persists after the configured attempt cap, supervision enters an awaiting-user hold. A new active turn cannot clear that hold; explicit resume permits observation again and clears stale error evidence on the new turn.
- An isolated copy of keepgoing's journal reconstructs the sequence through `timeline`, including the generic code, unavailable observations and hold transitions, without provider logs or private error text.

This tests existing bounded re-prompt behavior. Through v0.1.5 the adapter recovers with `bb thread tell`. In v0.2.0 the same re-prompt decision retries the failed turn by reference with `bb thread retry` and sends no prompt; see [recovering a failed BB turn](operations.md#recovering-a-failed-bb-turn-by-retry). It does not restart BB's host daemon or distinguish the host cause from a generic failure. Checks cannot run while their own host or scheduler is unavailable. A transient failure that starts and ends between checks may leave no failure snapshot. Live unattended recovery from a host disconnect and host-specific structured classification remain unverified.

## Bounded tick incident history

Available in v0.1.3 or newer; **offline fixtures only**. Every invocation that reaches the supervisor tick attempts to write `incidents.json` in the configured private state directory, including held checks, failed observations and dry runs. Supported control commands also append records as described below. No new provider call is made for diagnostics. A hold records `not_observed`; a failed adapter call records `unavailable`. Successful adapter results retain their normalized state/error class and queue/child counts. BB retry intent is `retrying`, `not_retrying`, `unverified`, `expired` (a [stale retry notice](operations.md#stale-retry-notices) that was no longer honored), `declined` (v0.2.0: a notice dropped because [BB had already declined the retry](operations.md#a-notice-bb-has-already-declined)) or `unavailable`; headless/process retry intent is explicitly unavailable. These are check-time observations, not continuous event capture.

Recognized build labels include numeric release versions, `dev`, commit hashes (with optional `smoke-` prefix and `-dirty` suffix), and, after v0.1.3, numeric versions followed by `-dev.<7–40 lowercase hex characters>` or `-rc.<digits>`. Other suffixes remain `unavailable` rather than exposing arbitrary build text. Earlier records already redacted as `unavailable` cannot be reconstructed. A label is caller-supplied historical metadata, not a verified binary fingerprint.

Schema version 4 contains a monotonic sequence within retained history, check-start time, recognized build label, adapter kind, hashed configured-target alias, normalized decision action/cause/outcome, nudge/re-prompt counts after the tick, control flags before/after, and the last persisted active-recovery timestamp for that target. A missing control snapshot is null; `countersAvailable: false` means the recorded zero counters are placeholders because state could not be loaded. The target key correlates an alias; it neither identifies a provider session nor anonymizes guessable aliases. Only release-style numeric versions, `dev` and recognized commit/smoke hashes are retained as build labels; other labels are unavailable. Provider event time remains unavailable. The source/code fields below describe structured inputs and never infer a network root cause. Counts and recovery metadata come from supervisor state; they are not independent proof of progress or of the agent's current liveness.

The rolling JSON file retains at most **256 records and 512 KiB**. Through v0.1.5 it prunes the oldest record first, which at ten-minute intervals covers roughly 42 hours. v0.2.0 keeps the same total bound but splits it into [retention tiers and collapses repeated checks](#retention-tiers-and-collapsed-checks), so moves and holds cannot be evicted by routine checks or probe noise. `dropped` counts retention pruning, not failed writes or missed checks. Each write uses a 0600 temporary file, syncs it and replaces the previous snapshot under `incident.lock`. Readers can use the complete old or new file without locking. Runtime filesystem modes follow existing platform guarantees; Windows ACL evaluation is separate. Corrupt/truncated, oversized, future-schema and symlinked journals are refused without automatic reset. Unknown string values normalize to unavailable. This is not tamper-proof storage; deletion/rollback, external writers, disk failure, a crash during a tick and power-loss durability of directory metadata are outside its guarantees.

The history excludes task text, raw provider output/errors, child/provider thread IDs, account/environment values, paths and webhook URLs. Read-only consumers should use the typed loader rather than treating legacy `runs.jsonl` or headless stdout as this contract. Existing history/artifact formats retain their previous behavior and are not covered by these new content guarantees.

A journal write failure produces a fixed warning and a non-success tick result **after** normal tick work; it cannot retract a prompt or undo a saved hold/stop. Existing `state.json`/`runs.jsonl` may already record the action, and they are not rewritten to include this later failure. Inspect the tick result/scheduler output for it. No notification is sent or replayed automatically. Checks refused before the tick (including lock/ownership/config guards), early notification polls and external file edits are not recorded here. Supported standalone control saves are captured below. The before/after control snapshots are not a complete control audit. Decision inputs are captured as described below. Combined R2 chronology acceptance is verified below; R3 adds durable delivery diagnostics; R4 adds the read-only incident view.

Offline tests reconstruct a synthetic BB retry, unavailable scan, recovery, child-input hold and continued held checks using only this journal. Other fixtures execute a local process status command and read synthetic Claude/Codex result files through the actual adapters, exercise restart/retention, reject malformed/aliased history, exclude secret markers, and verify that journal contention does not block stop or undo holds. They do not verify a live provider outage or installed loop.

### Structured source metadata

Schema 2 adds `source`, `category`, `httpStatus` and `exitCode` and fills `providerCode` when a token is recognized. Unrecognized/missing strings are `unavailable`, unsupported numbers are null, and `sourceTime` remains `unavailable`. A numeric zero exit is distinct from a missing exit. The vocabulary is finite; raw unknown tokens, error messages and transcript text are never stored. These diagnostic fields are ignored by the decision engine and cannot authorize failover.

| Source | Evidence represented |
| --- | --- |
| `bb_status` | Structured thread status; no retained provider-error details. |
| `bb_provider_error`, `bb_system_error`, `bb_failure_event` | Recognized fields from the retained error summary. Complete log scans refresh that summary; it is not a timestamped stream of every error. |
| `bb_error_summary` | Legacy cursor classification with no detailed metadata; no log replay is added solely to reconstruct it. |
| `bb_rate_limits` | A blocked structured rate-limit window supplies the effective error classification. |
| `process_json`, `process_exit` | Parsed status JSON or the status command's exit; a process exit is not the supervised agent's own exit unless that command defines it so. |
| `runner_state`, `runner_exit` | Headless wrapper state or its recorded exit, without a recognized result/event. |
| `claude_result`, `codex_events` | Structured result/event fields already parsed by the adapter, plus a valid wrapper exit when available. |
| `codex_rate_limits` | The existing structured app-server limit check confirmed a blocked window. |

BB error metadata survives restart with the cursor and clears on recognized recovery/new turn. Incomplete log scans withhold details. Codex event metadata follows its existing accumulated classification; a later unknown token does not make an earlier classified error fresh. Retained details can therefore describe an older failure within the turn. Check-start time is the supervisor's observation time, not the provider error's occurrence time. This remains a sampled summary, not a complete incident event stream.

Loading a version-1 journal is read-only and keeps its version. The next successful append upgrades it to the current schema, preserving sequence numbers, retained records and pruning counts; old records keep their new fields unavailable. An older binary refuses the newer journal rather than overwriting it, which can make its tick return a history-write failure after normal actions. Keep tested and installed build labels explicit when deploying or rolling back. Offline fixtures cover upgrade/restart/recovery, partial scans, malformed numeric statuses, source distinctions, secret-marker rejection and metadata's independence from decision policy. No installed loop or live outage was used for these checks.

### Decision evaluations

Schema 3 records the ordered calls to the pure decision engine within each tick. `decisions.availability: captured` with an empty `evaluations` array means the engine was not called, for example because a saved hold or failed observation ended the check early. Legacy records have `availability: unavailable`; that is not evidence of an early hold. Loading versions 1 and 2 leaves the file unchanged; the next append upgrades to the current schema without fabricating old inputs. Older writers refuse the newer journal.

Each evaluation records a fixed stage, proposed action, normalized cause and a typed projection of policy inputs: control flags, state/error class, queue/child counts, retry flags, window eligibility, reached bounds, stall/wake/backoff durations, pre-action counters and thresholds, failover eligibility/availability and switch-back readiness. Durations are integer nanoseconds (fields ending in `Ns`), including negative clock deltas. Child IDs, provider metadata and free-text reasons are excluded. Missing/unknown enum values are unavailable.

These are **values passed to the engine**, not proof that every condition was independently checked or every branch evaluated. `normal`, `switch_back_failed` and `handoff_failed` supply the ordinary policy inputs; early `deadline`, `progress_limit`, `send_limit`, `child_input` and `stop` evaluations supply only their applicable gate/observation/bound fields. Other zero/false values in those early evaluations are defaults, not verified clearance. Decision flags record that a hard bound was reached, not its configured deadline, total send usage or Git metadata. Observation availability remains in the tick snapshot.

A failed handoff or switch-back probe can produce a second evaluation with changed eligibility, and send reservation can produce a later bound evaluation. Proposed actions may differ from the tick's final action (for example an already-sent stop is suppressed) or may fail during execution. The record's outcome and final action remain separate; this is not proof of successful delivery. Pre-action decision counters can differ from the post-tick counters. Keep at most eight evaluations per tick, retaining the newest and recording omitted evaluations in `decisions.dropped` (separate from journal retention pruning). Current paths need fewer; the cap bounds future additions.

Offline fixtures cover stall/nudge/hold reconstruction, all early engine paths, failed handoff/switch-back re-evaluation, stop deduplication, dry-run adapter checks, exact negative duration preservation, privacy, retention and legacy migration. This adds no provider work, changes no decision policy and does not deploy the new binary to existing loops.

### Saved control changes

Schema 4 distinguishes `kind: tick` from `kind: control`. The CLI records successful saves by `enable`, `disable`, `pause`, `resume`, `stop`, `complete`, `await-user`, `start`, and `daemon --take-over`. Repeated commands are recorded even if their gate flags do not change. A control record contains the operation, build, before/after gate flags, owner-presence flags and whether the owner identity/generation changed. Private notes, owner/thread IDs, owner generations and previous-owner details are excluded. No adapter, target, decision inputs, progress counters or recovery metadata are inferred for a control save.

The flags are copied inside the serialized control update, so an intervening writer cannot corrupt the before/after pair. `checkStartedAt` is the start of that locked update callback; `controlChange.saveObservedAt` is when the caller observed a successful save after the control lock was released. Neither is claimed as the exact filesystem commit time. `outcome: control_saved` says only that the save succeeded, not that a stop reached the agent, a daemon started, a report arrived or the overall CLI command succeeded. Rejected updates and failed saves produce no success record.

The journal append follows the control save and runs outside the control lock. Its sequence orders appends, not concurrent control mutations or overlapping ticks. Another control update can finish before the earlier record is appended; a crash can leave a saved change with no record. Clock changes also limit timestamp ordering. Tick snapshots include their own before/after observations and can overlap command records; they are not additional control mutations. This is bounded diagnostic evidence, not a complete or tamper-proof audit. Manual edits, setup initialization, low-level control writers and changes made by older binaries are not backfilled. Automatic holds retain their existing tick snapshots.

If the journal is locked, malformed or unwritable, the save stays in place. Standalone control commands emit a fixed warning, continue their existing log/report processing and return failure. A new completion declaration still gets its existing single delivery attempt, and repeating the command cannot replay it; no new notification retry is introduced. Existing control-log failures retain their prior report-skipping behavior. For `start` or `daemon --take-over`, a journal failure stops startup with an explicit saved-control error; the enabled flag or owner claim may already have changed, and all holds remain. Inspect status and repair history storage before retrying startup. An already-running supervisor can still observe those saved flags.

Schema-1/2/3 records load read-only as ticks with no control-change details. The next append upgrades to the current schema, preserving existing sequence/pruning counts and available observations/decision evidence. Older binaries refuse the newer journal. Tick and control records share the same retention budget. Offline fixtures cover command/held-tick chronology, no-op commands, owner privacy, startup/takeover, rejected/failed saves, history contention/corruption, migration, and preserved completion/report semantics. No installed loop was changed and no live provider was called.

### Target moves, probes and limit waits

Released in v0.2.0; **offline fixtures only**. Schema 5 adds the evidence needed to see how a loop was passed between targets:

- `targetKeyAfter` on tick records: the hashed alias of the active target after the check. It differs from `targetKey` only when that check handed off or switched back, so a move records both ends.
- `probes` on tick records: up to eight results per check, each with a hashed candidate alias, the tier (`scheduled` or `exact`), pass/fail and a normalized error class. `captured` with no results means the check ran no probe. Probe output, models, latency and reset times stay in the existing private health records.
- `kind: retarget`: a successful manual `retarget`, with the hashed source and destination aliases and `outcome: target_saved`. The `--note` text stays in `runs.jsonl` and never enters this history. A retarget observes, probes and sends nothing, and the record claims none of that. Retargeting to the already-active target moves nothing and appends no record.
- Causes `limit_wait`, `limit_no_fallback` and `limit_failover_disabled` replace `other` when a check sent nothing because a provider limit has a known reset. The cause is the decision engine's typed rule, not a match on reason text, so it also covers a wait decided after every handoff candidate failed its probe or after a failed switch-back probe. The reset interval remains in the recorded decision input (`resetInNs`).

These are hashes of configured aliases, exactly as `targetKey` already was. They name no thread, session, account or model, and they do not anonymize a guessable alias. Loading schema 1–4 is read-only and leaves these fields `unavailable`; the next append upgrades the file. Older binaries refuse a schema-5 journal, as with earlier upgrades. The 256-record and 512 KiB caps are unchanged; the new fields add roughly 50 bytes to an ordinary check and about 130 bytes per probe result, so probe-heavy history reaches the byte cap slightly sooner. A retarget whose history append fails keeps the saved target, prints a fixed warning and exits 1; it is not rolled back.

Not retained: manual `fallbacks check` probes, an automatic return to the primary when the active fallback is removed or disabled, target changes written by older binaries, and anything done outside keepgoing. In particular, work continued by hand in a different conversation leaves no record here. Run and chat identity belongs to the [run and conversation discovery](run-discovery.md) graph (D1–D3), which v0.2.0 records and `keepgoing runs` shows. This history deliberately defines no run, chat or conversation identity of its own, so the two cannot disagree; its events do not yet reference that graph's node IDs (roadmap R7c).

### Retention tiers and collapsed checks

Released in v0.2.0; **offline fixtures only**. Schema 6 keeps the total bound of 256 records and 512 KiB and reserves part of it for each of four tiers:

| Tier | Reserved | Records |
| --- | --- | --- |
| `key` | 64 | Who had the loop and whether it was held: a check that moved the target (handoff or switch-back), a retarget, an ownership change, daemon takeover or transfer step, and any check or control save that changed a hold flag (entered or cleared). |
| `condition` | 48 | Evidence of an ongoing problem: limit-wait checks, checks with a failed probe, and handoff or switch-back attempts that did not move the target. |
| `activity` | 80 | Wakes, nudges, re-prompts and stops; control commands that changed no flag; failed checks; records of unknown kind. |
| `routine` | 64 | Checks that executed nothing and changed nothing, including held checks and dry runs. |

**Reservations.** The reservations add up to 256. A tier may hold more than its reservation while the journal has room to spare. Once the journal is full, each append prunes the oldest record of a tier that holds more than its reservation, taking routine first, then activity, then condition, and key last. A tier at or under its reservation never loses a record to another tier's volume, and a higher tier's borrowed records are not taken while a lower tier is also over its reservation: a stream of wakes prunes older wakes, not moves. Probe failures and failed attempts are in `condition` precisely so that they cannot consume the room reserved for moves and holds. A tier still prunes its own oldest records once it alone exceeds what is available.

**Collapsed runs.** A check is repeatable when it executed no action, left the controls and the target unchanged, and attempted no move: no exact-tier probe ran and the engine was not re-evaluated after a failed handoff or switch-back. A dry run is always repeatable. Consecutive repeatable checks that are identical apart from their time, decision inputs and scheduled probes form a run. A run keeps its first check and its latest as full records; every check between them is folded into `collapsed` on the latest: `count`, `firstAt` and `lastAt` (the earliest and latest folded check times, even if the host clock moved backwards between them), `passedProbes` and `failedProbes`.

Scheduled probe results are folded only when they repeat that candidate's last result in the run's retained records. A candidate's first result in a run, a first failure, a failure with a different error class and a recovery each stay as their own record. Nothing is assumed: if the record that showed a candidate failing has since been pruned, its next result is retained whatever it is. Folded results are counted in `passedProbes` and `failedProbes`, and `probeTargetKeys` lists up to eight of the candidates they probed. Folded checks lose their own decision inputs (for example each check's remaining reset interval) and which result belonged to which candidate; retained records keep theirs. Any other record ends the run: a send, a control save, a different observation or counter, a new build label, an attempted move.

Examples, at ten-minute checks: a limit wait in which no probe runs is two `condition` records however long it lasts. With one fallback probed on a schedule, passing or failing every time, it is four: both ends of the wait before the first probe result, that first result, and the latest check carrying the counts. A handoff attempt whose candidates pass their exact probes but cannot be activated is its own record between the waits on either side, although its final action and cause match theirs. Failed switch-back probes after a handoff are attempted moves, usually followed by a wake of the fallback, and are never folded; they fill and then prune the `condition` tier and leave the handoff in `key`. Repeated failed checks collapse the same way in the `activity` tier.

**Accounting.** Sequence numbers are no longer contiguous. `nextSequence - 1` always equals retained records plus `dropped` (pruned) plus `collapsed` (folded), and a journal that does not add up is refused, as is a summary on a record that is not repeatable or whose span is inverted. `pruned` gives each tier's pruned count and `newestAt`, the local time of the newest record pruned from that tier: that tier's history is incomplete up to that time. When a record carrying a summary is pruned, its folded checks stay in the journal's `collapsed` total and no longer appear on any record.

**Limits of the design.** The reservations are record counts, not time guarantees, and the 512 KiB cap usually binds before 256 records do. Measured sizes: a retarget is about 1.0 KiB, a control save or a held check 1.2 KiB, a check or wake with one decision evaluation 1.9 KiB, and a failed switch-back wake (two evaluations and a probe) 2.8 KiB. Only a journal made of records at or under 2 KiB reaches 256; one made of failed switch-back wakes holds about 187, and a mix of the two lands in between. When the byte cap forces pruning while some tier is over its reservation, that tier is pruned as above. When no tier is over, the same order still applies and takes tiers below their reservations: routine is emptied first, then activity, then condition, and key last. So under sustained byte pressure the newest routine and activity history is what is given up to keep moves, holds and conditions; the newest record always survives. In time: a loop that wakes every ten minutes produces six activity records an hour, so the whole journal is at most about 40 hours of wakes, and about 13 hours once the other tiers hold their reservations. More than 64 moves, retargets, ownership changes and hold changes will prune the oldest of them once the journal is full. The tier and the collapse rule are derived from typed fields when a record is written or loaded, never from stored text, and nothing in the journal is read back by a decision.

**Upgrade.** Schema 1–5 journals load read-only and unchanged. The next append replays the retained legacy records through the same rules: runs of identical checks are collapsed, and since a legacy journal holds at most 256 records the replay itself prunes nothing. Sequence numbers and the legacy `dropped` count are preserved. From then on a full journal prunes one record per append from the lowest tier over its reservation, which for a legacy journal of checks that cannot collapse is the routine tier. Records an older build dropped have no tier or time. Older binaries refuse a schema-6 journal, as with earlier upgrades.

Offline fixtures cover the tier and repeatability of each record shape; a 720-check limit wait surviving 1,000 later checks; ten-day waits with a fallback failing scheduled probes at three intervals; 1,000 failed switch-back attempts not evicting a handoff; a stream of wakes not evicting borrowed key records; probe news (first result, first failure, changed class, recovery, and a recovery after its failure record was pruned) staying retained; filters by probe and by target matching a summary; a failed handoff attempt driven through the supervisor staying out of the surrounding wait; run boundaries; borrowed and reserved room; byte-pressure pruning; clock rollback; a pruned first record; legacy upgrades, including 256 checks that cannot collapse; refused inconsistent accounting; and caller-supplied summaries being ignored. No installed loop was upgraded.

### Combined incident acceptance

R2 acceptance is verified by `cmd/keepgoing/incident_journey_test.go` with offline fixtures. Each command invocation reloads config and persisted state through the CLI harness. Checks use an explicit fictional timestamp to avoid waiting through backoff; local headless subprocesses actually run, emit structured output and terminate. This is not a live provider walkthrough or a separate supervisor-process restart test.

| Fixture | Sequence verified from common history |
| --- | --- |
| BB stand-in CLI | Active network retry persists across checks; retry ends with a structured failure, one re-prompt exhausts the configured allowance, and the next check saves a hold. Provider recovery while held does not cause observation or clear the hold. Explicit resume permits a fresh observation of matching provider activity, clearing old error metadata. |
| Local process commands | Structured network error and exhausted retry allowance produce a hold. Changing the status fixture to active does not clear it; explicit resume exposes the healthy status. Unsupported retry intent and HTTP/event-time details remain unavailable. |
| Claude/Codex stand-in processes | Failed local turns emit network-error results/events and exit 1. Re-prompt exhaustion persists a hold. A repaired stand-in and increased configured allowance do not reopen it until resume. The resume of that hold starts a new error episode (v0.2.0; through v0.1.5 the spent retries stayed recorded); the next resumed process succeeds, exits 0 and is observed as idle before another ordinary wake. Session-resume arguments reach the stand-ins; this proves fixture behavior only. |

The reconstruction assertions copy **only `incidents.json`** into a fresh directory and read it with the typed loader. No BB logs/cursor, config, control file, runner stdout, session artifacts or transcripts are available to that reader. Assertions check actions, normalized source/error/retry evidence, decision thresholds, before/after controls, unobserved held checks, recovered observations, sequence/build/check metadata and secret-marker exclusion; reading leaves the copy unchanged. Existing tests separately cover bounded retention, schema migration, malformed storage and control/report behavior under journal failure.

An observation timestamp records when keepgoing sampled a source, not the source event's occurrence or freshness. No source-event timestamp is fabricated, and unsupported headless/process retry intent remains unavailable. Retention, concurrent appends, crashes and external edits retain the gaps documented above. These passing fixtures complete R2's common-evidence acceptance, not R3 delivery tracking, the R4 user-facing timeline, R5 live verification or the BB-pilot-readiness gate. The code is available in v0.1.3; fixture verification does not install it into an existing loop.


## Notification delivery evidence

In v0.1.3 or newer, `status`, `status --json`, and `doctor --offline --json` read private `deliveries.json`. Failed or unconfirmed notifications remain visible even when subsequent held checks succeed or an external scheduler labels them silent skips. Reading status/diagnostics never sends a report, changes a hold, repairs storage or replays a completion. This is delivery evidence, not a connectivity diagnosis, current endpoint-health check, or the combined incident timeline.

The version-1 file holds up to **64 deliveries / 128 KiB**, with monotonically allocated sequence numbers and a count of evicted records. Each retained delivery has a fixed event label, local start/update times, outcome, `receipt: unverified`, and at most two request-attempt records with start/finish times and available numeric HTTP status. Times come from the host clock, not the provider or receiver. Sequence order reflects recording at preparation, not response or control-save order. An ordinary attempt is one `client.Do` call and may include its existing redirects; per-redirect hops and wire-byte counts are unavailable.

| Outcome | Evidence |
| --- | --- |
| `prepared` | Reporting began; no request attempt has yet been recorded. A crash can leave this state. |
| `setup_failed`, `body_failed`, `sign_failed`, `request_failed` | Local preparation failed. Setup/body/sign failures have no request attempts; request construction can fail before a retry. |
| `attempt_started` | Intent was recorded immediately before calling the HTTP client. It does not prove bytes were sent. A live in-flight request, crash or failed final write can leave this state. |
| `transport_failed` | The client returned an error without a usable response; delivery may already have occurred. Raw errors and URLs are withheld. |
| `http_rejected` | The final response was outside 2xx. The numeric status is retained; downstream processing/receipt remains unknown. |
| `not_sent` | The check had no time left for this report, so no request was made (v0.2.0). A build older than this outcome treats a history containing it as unavailable. |
| `http_accepted` | The client observed 2xx. This proves HTTP acceptance only; destination application and human receipt remain unverified. |

Text status prints historical warnings, their times, request counts and final HTTP statuses. JSON `notifications` contains `state` (`not_recorded`, `recorded`, `unavailable`), `latest` (most recently started retained delivery), `warnings` and `dropped`. Every retained delivery without final HTTP acceptance appears in `warnings`. Later acceptance of another event does not erase an older failure; these warnings age out only with bounded retention. A retry that receives 2xx leaves earlier attempt evidence within that delivery, without a final-failure warning. Doctor adds the same typed summary and a `notifications` check; missing evidence is unavailable, never an all-clear. Reporting disabled/unsubscribed, no invocation, older builds, deletion and crash gaps can all produce no evidence. Do not infer receipt from an empty warning list.

Records exclude endpoint URLs, secrets, signing IDs, headers, event text/data, notes, account/thread/loop identities and raw errors. Unknown event names become `unknown`; readers reject unknown fields, unsupported schemas, invalid enum values, oversized/non-regular files and symlinks. Status itself still contains its existing private fields such as control notes; only the new delivery metadata is redacted. Store the state directory outside repositories.

Writes use a separate nonblocking `delivery.lock`, a synced owner-private temporary file and atomic replacement. No delivery lock is held during HTTP; a stop/pause can still update control while a report is in flight. A write failure warns through the existing report-error path after the original send policy runs. It does not roll back a hold, suppress a send, add retries, or refund completion deduplication. Missing/corrupt history is never silently repaired by the reader; failed writes can leave missing or stale evidence. Lock contention, process crashes, power loss, external writers and retention prevent this from being a complete audit. There is no automatic resend or failure-acknowledgement command.

Completion still submits once without redirects on its persisted false-to-true declaration; repeated commands, restarts and held ticks do not replay. Ordinary reports still retry once for transport errors/5xx. `report test` also records evidence and says HTTP accepted rather than delivered; its dry run records nothing. A crash or control-log failure before reporting begins leaves no delivery record. Diagnostic history is never a send authorization ledger. Inspect setup and the receiver before any manual resend; do not resume a completed loop merely to retry an alert.

Offline acceptance uses local HTTP 2xx/4xx/5xx/redirect fixtures, transport failures, fresh CLI harness invocations and isolated ledger readers. It verifies automatic child escalation and explicit await-user/completion holds, warnings across later held checks, no provider work or report replay while held, setup failure, retention, privacy, corrupted/contended storage, and unconfirmed intent after a lost final write. Existing completion concurrency/deduplication tests remain in force. These are local fixtures, not an installed-loop restart, live webhook receipt, real outage or migration-readiness certification.


## Offline incident timeline

Available in v0.1.3 or newer; **offline fixtures only**:

```sh
keepgoing timeline -c keepgoing.yaml
keepgoing timeline -c keepgoing.yaml --json --limit 100 --stale-after 1h
# v0.2.0: who had the loop, and when it changed hands.
keepgoing timeline -c keepgoing.yaml --kind moves --since 168h
keepgoing timeline -c keepgoing.yaml --kind limit_wait,probe --target primary --json
# Inspect copied/private records even when configuration cannot be loaded:
keepgoing timeline --state-dir /private/example-loop --json
```

`timeline` is always offline. It reads only `incidents.json`, `deliveries.json` and a bounded projection of `control.json` from the selected directory. With `-c` (default `keepgoing.yaml`), normal config loading locates `state_dir`; `--state-dir` skips config/task/fallback loading entirely. Do not supply both. It never queries providers, scheduler APIs, daemon endpoints, CI, subprocesses or Git, acquires writer locks, creates state directories, or changes controls/history. There is no `--now`, resend or repair mode.

The text view first shows current saved flags, then retained evidence summaries and chronological events. It answers what was last **recorded**: a check, an active observation (which may include retries), a send-related action, a transition into a hold, held checks, and failed/unconfirmed notifications. An action/outcome is not proof that a message was sent or work succeeded. A last historical hold is deliberately not attributed as the cause of current flags: intervening changes, missing records, retargeting or reused directories can break that link. The hold event includes recorded cause and decision inputs where available, such as spent re-prompts and configured thresholds. It does not rerun the decision engine or invent missing reasons.

JSON schema 5 (v0.2.0; v0.1.3–v0.1.5 print schema 1 without `filter`, `retention`, event `kinds`, the `target_moves` layer or the target-move and limit-wait summaries) includes:

- `control`: current flag availability/state, flags and available save timestamp, excluding notes and ownership identities. Missing control is explicitly `not_recorded`, with the supervisor's disabled default. Unreadable, non-regular, symlinked, oversized (over 64 KiB) or invalid control is `unavailable`.
- `retention`: for `incidents.json`, the total record and byte bounds, each tier's reservation, retained count (which can exceed the reservation while the journal has spare room), pruned count and `prunedThrough` time, `collapsedChecks` (folded into retained records), `collapsedPruned` (folded into records since pruned) and `untieredPruned` (dropped by an older build). See [what the view shows about missing history](#collapsed-and-pruned-history).
- `sources`: per-file availability, loaded schema version, retained and dropped counts. Empty/missing journals are `not_recorded`; a schema version on an empty source is the loader's default, not proof that the file existed. Invalid or unsupported history is `unavailable` and is not repaired.
- `summary`: source/sequence references for latest check, last non-dry-run active observation, last non-dry-run send-related action and last retained hold entry, plus counts of retained checks, non-dry-run checks with pre-existing holds, and notification warnings. These summaries use all retained evidence, even if the display limit hides their referenced rows. They use source sequence order, which can differ from clock order.
- `events`: normalized typed incident/delivery snapshots, descriptions, source/sequence, timestamp basis and freshness. Incident fields retain available adapter/build/target-key/source evidence and ordered decision inputs. Delivery fields retain per-attempt observations and explicitly unverified receipt. Unknown incident strings remain normalized/unavailable; arbitrary payloads and raw errors are never echoed.
- `events[].kinds`: fixed labels derived from the typed record, listed under [navigating events](#navigating-events).
- `filter`: the applied kinds, target key and time bounds, with `matched` and `excluded` event counts.
- `layers`, `limitations`, `nextSteps`: distinguish historical supervisor checks, provider connectivity and notification evidence from unavailable scheduler/liveness/CI/progress evidence, with actions for inspecting incomplete storage, stale checks or alerts. Suggestions never authorize resuming or sending anything.

The display includes the newest 40 events by local timestamp by default; `--limit` accepts 1–320, the combined retention cap. Rows are presented oldest first, ties ordered by source then sequence. `omitted` counts rows hidden by the display limit, separately from records dropped by storage retention. Tick rows use check-start time, control rows use post-save observation time, and delivery rows use their latest recorded update. A delivery is one snapshot with its attempt list, not a separate row for each attempt. Source sequences remain available to reconstruct each file's ordering. Local clocks, explicit test tick timestamps, late webhook results and concurrent file reads prevent cross-source ordering from establishing causality.

### Navigating events

Released in v0.2.0; **offline fixtures only**. Every event carries one or more kinds, and four flags narrow the displayed events. Filters apply before `--limit`. Summaries, sources and layers always describe all retained evidence, filtered or not.

| Kind | Event |
| --- | --- |
| `check` | Any recorded check. `dry_run` marks one that executed no action; a dry run is never labelled as a send or a move. |
| `wake`, `nudge`, `reprompt`, `stop` | The check's final recorded action. An action is not proof that a message was delivered. |
| `handoff`, `switch_back` | A check that moved the loop to a fallback or back to the primary. From schema 5 the row shows both targets, and the kind requires them to differ: a handoff or switch-back that failed without changing the target is only a `check`. Older records lack the second target, so the kind requires a succeeded outcome. |
| `retarget` | A saved manual retarget. |
| `ownership` | A control save that changed the loop owner, a daemon takeover, or a transfer step. Owner identities are withheld. |
| `probe` | A check that ran at least one scheduled or exact probe, or that summarizes folded checks which did. |
| `limit_wait` | A check that sent nothing because a provider limit has a known reset. |
| `limit_wait_report` | The check that detected a long limit wait with no handoff possible and requested its one report (or recorded that none was sent). |
| `turn_retry` | A re-prompt that asked the provider to re-run the failed turn by reference. The row says what the provider answered and whether a prompt was sent instead. |
| `child_retry` | A check that did [lane recovery](operations.md#lane-recovery) work: lanes whose failed turn BB was asked to retry, lanes given up, lanes put off for lack of time. Counts only. |
| `route_list` | A check of a loop that follows the [standard route list](routes.md) that read it changed since the loop's previous check. Recorded once per change. |
| `route_change` | A saved change to the loop's own route list (`routes --loop`, `fallbacks move` or `remove`): the operation, the route's hashed key, and who made it as a role (`owner_thread` or `terminal`). |
| `hold` | A check or control save that entered a stop, pause, disable, awaiting-user or completed state. |
| `control` | Any saved control command. |
| `collapsed` | A check that also summarizes identical earlier checks. |
| `notification` | A delivery record. |

- `--kind` takes a comma-separated list; an event matches when it has any listed kind. `moves` is shorthand for `handoff,switch_back,retarget,ownership`: every recorded change of who has the loop.
- `--target` takes `primary`, a fallback id, or a 64-character target key copied from JSON. An alias is hashed locally and never echoed; the report shows only the key. It matches a record's target before or after the check, any candidate it probed, and any candidate whose probes were folded into its summary. Control saves and deliveries record no target, so a target filter hides them, including `hold`, `control` and `ownership` events that came from a control command; combine `--target` with check-based kinds. Text rows name `primary` and shorten any other key to `fallback <first 8 characters>`; a hash cannot be turned back into a fallback id.
- `--since` and `--until` take an RFC 3339 time or a positive duration meaning that long before the read time (`--since 72h`). Bounds are inclusive and compare the local timestamps used for ordering. A record that summarizes collapsed checks matches when any part of its span falls in the range: from the earliest of its folded checks and its own time to the latest of them.

Invalid filters exit 2 with a fixed message that does not repeat the rejected value. A filter that matches nothing is a successful, empty view, not evidence that nothing happened.

When the latest retained check is a limit wait, `nextSteps` says so. keepgoing sends no prompt during that wait. The check that first finds the reset further away than `errors.report_wait_after`, with no handoff possible, is marked `limit_wait_report` and says whether its one report was requested, failed or not subscribed; `summary.lastLimitWaitReport` references it. See [a long limit wait](operations.md#a-long-limit-wait-with-no-way-out). The marker is incident schema 7 (`limitWaitReport` on tick records: `none`, `requested`, `delivery_failed` or `not_subscribed`); a marked check is never collapsed and is kept in the `condition` tier. Timeline JSON is schema 4 with this addition.

Incident schema 8 adds `turnRetry` on tick records: `none`, `retried`, `queued`, `nothing_to_retry`, `not_in_error`, `already_queued`, `queue_unconfirmed`, `refused` or `unsupported`. The observation's `retry` value gains `declined` beside `expired`, for a notice dropped because BB's retry plugin had declined the failure; no schema change is needed, and an older build reads it as `unavailable`. `summary.lastTurnRetry` references the newest marked check, and timeline JSON is schema 5 with it. Older binaries refuse a schema-8 journal, as with earlier upgrades.

Incident schema 9 adds two fields to a control record's `controlChange`: `caller`, the role that saved the control (`owner_thread`, `other_thread`, `terminal`, `client`, or `unavailable` where it is not recorded; never an identity), and `newEpisode`, true when a `resume` cleared a hold keepgoing had raised for a spent per-episode allowance and so [started a new error episode](operations.md#what-resume-starts-over). `timeline` prints both. Older binaries do not read a schema 9 journal.

Incident schema 10 adds `lanes` on tick records: the counts `retried`, `givenUp` and `deferred` of that check's [lane recovery](operations.md#lane-recovery) work, all zero for a loop without `bb.children.retry_errored` and for records loaded from an older journal. A check that did lane work is never folded into its neighbours and is kept in the `condition` tier. The timeline JSON is schema 6: it adds the `child_retry` kind and `summary.lastChildRetry`. A build older than this change refuses a schema 10 journal.

Incident schema 11 (route-list notes) adds `routeListChanged` on tick records, set when a loop that follows the standard route list read it changed since its previous check; such a check is never folded into its neighbours. It adds `route_change` records, kept in the `key` tier, with `routeChange`: `operation` (`move`, `remove`, `off` or `on`), `targetKey` (the route's hashed key) and `caller` (`owner_thread` or `terminal`). The timeline JSON is schema 7 (route-list notes): it adds the `route_list` and `route_change` kinds. A build older than this change refuses a schema 11 journal; move `incidents.json` aside to run one.

Offline fixtures cover each kind, combined filters, relative and absolute bounds, alias hashing, filter-independent summaries, limit ordering, refused arguments, legacy records without target evidence, dry runs and unchanged runtime files. They use fictional journals; no live handoff, switch-back or limit was observed for this change.

### Collapsed and pruned history

Released in v0.2.0; **offline fixtures only**. The view never leaves a gap unexplained:

- A record that summarizes a run says so in its description (`COLLAPSED: 298 identical earlier checks from … to … are summarized here`, with counts of folded passing and failed probe results) and carries `incident.collapsed`. `summary.heldChecks` and `summary.limitWaitChecks` count folded checks too; `summary.retainedChecks` counts records, and the text view prints the folded total beside it.
- The `retention` line shows, per tier, how many records are retained, its reservation, how many were pruned and through what time, plus the collapsed totals.
- `filter.reachesPrunedHistory` is true, and the text view prints a `NOTE`, when the selected range extends to or before a time through which some tier was pruned, or when an older build dropped records without recording when. With no `--since`, any pruning sets it. It concerns the time range only: it is set even when the kinds you selected were not the ones pruned, so read the per-tier line for which history is incomplete.

An empty result is therefore one of three things: nothing recorded in a fully retained range, checks folded into a summary outside the selected kinds, or pruned history. The view distinguishes the third; it cannot recover it.

Freshness is `recent`, `stale` (older than the positive `--stale-after`, default 30 minutes) or `future` (timestamp after the view's read time). It describes the local record's recency, not provider-event freshness or current liveness. Current control flags remain authoritative for the view even if their save timestamp is old. Source event times, internet root cause, installed/running build identity, useful task progress and delivery receipt are unavailable. Build labels describe recorded commands only; target keys correlate aliases, not verified session identity. Use `status` for a separate current daemon check, or `doctor` for setup and explicitly selected scheduler diagnostics.

Exit 0 means the view was produced, including historical failures, holds or missing evidence; it is not a health certification. A corrupt/unreadable source yields a partial report from other sources and exit 1. Invalid arguments or config selection yield exit 2 with redacted errors; `--state-dir` remains available when config is broken. Do not discard records or clear holds to obtain exit 0.

Acceptance tests run the command on the existing BB/process/Claude/Codex fixture journeys, including isolated journal copies with no config or adapter logs. A combined local process/HTTP fixture verifies failure, bounded re-prompt, persistent hold, rejected escalation, continued held checks and explicit recovery. It checks the text/JSON explanation and unchanged runtime files/provider/report activity. Additional fixtures cover control precedence, dry runs, privacy, missing/corrupt/oversized/legacy evidence, partial source failures, retention, display limiting and clock reorderings. This is not a live outage, installed-loop restart or migration-readiness certification; R5 remains pending.
