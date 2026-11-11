# Diagnostics

`keepgoing doctor` checks a loop without starting an agent, running a probe, changing a login, or writing runtime state. It is implemented on `main`; v0.1.1 does not include it. A separate loop skill is not required.

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml
keepgoing doctor -c /private/example-loop/keepgoing.yaml --json
keepgoing doctor -c /private/example-loop/keepgoing.yaml --offline
```

The ordinary command resolves configured executable paths and runs supported agents' `--version` commands, BB's `thread show <id> --json`, and headless providers' login-status commands described below. Each command has a five-second timeout and a 64 KiB stdout limit. `--timeout` changes the per-command deadline. `--offline` skips all command execution and local daemon queries while still inspecting configuration, files, and executable availability. Custom process-adapter commands, lifecycle logs, model calls, and probe commands are never executed. Explicit diagnostics can query BB even when the loop is held; they do not change that hold.

## What it checks

- Configuration and fallback parsing/validation, task-file readability, state/control/health readability, and private-file Unix mode bits. Separate read-only OS checks inspect runtime path access on macOS/Linux/Windows and ACL metadata on Linux/macOS/Windows as described below. Doctor does not print task text or control notes or change permissions. Full ACL privacy evaluation is unavailable.
- Executable availability and recognized version output for the primary and enabled fallback agents. It displays only a narrowly recognized numeric version, never arbitrary stdout or stderr. A recognized version does not establish compatibility with every required provider feature.
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

For each configured primary or enabled fallback using the Claude Code or Codex headless adapter, doctor inspects `targets/<target>/session` under the loop's state directory. The `agent.<target>.session` check and the JSON `sessions` map identify the saved resume ID. Text output labels it as saved and unverified live. BB and custom process targets do not have these fields.

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

The [bounded tick history](#bounded-tick-incident-history) now preserves normalized observation/retry snapshots. Detailed provider connection events are not yet preserved in a common keepgoing incident history. An investigation can therefore require the adapter host's logs, such as BB's structured provider events. Headless process/session artifacts also do not constitute a unified diagnostic timeline. `status` and `doctor` do not currently produce the proposed incident bundle or a durable report-delivery ledger.

The [priority reliability checkpoints](roadmap.md#priority-reliability-checkpoints) cover active retry classification, keepgoing-owned structured evidence across adapters, failed-alert visibility, a read-only timeline and recovery verification. Active BB retry handling is implemented on `main` and fixture-tested, but unreleased; see [retry handling and recorded reasons](operations.md#bb-retry-and-stall-handling). The first bounded tick-history slice is implemented below; detailed common evidence, delivery ledger, timeline and full recovery journey remain unfinished. Records must exclude task/transcript text and secrets; unsupported provider details remain unavailable. Connectivity recovery alone must never resume an explicit hold.

## Bounded tick incident history

Implemented on `main`, **unreleased; offline fixtures only**. Every invocation that reaches the supervisor tick attempts to write `incidents.json` in the configured private state directory, including held checks, failed observations and dry runs. No new provider call is made for diagnostics. A hold records `not_observed`; a failed adapter call records `unavailable`. Successful adapter results retain their normalized state/error class and queue/child counts. BB retry intent is `retrying`, `not_retrying`, `unverified` or `unavailable`; headless/process retry intent is explicitly unavailable. These are check-time observations, not continuous event capture.

Schema version 2 contains a monotonic sequence within retained history, check-start time, recognized build label, adapter kind, hashed configured-target alias, normalized decision action/cause/outcome, nudge/re-prompt counts after the tick, control flags before/after, and the last persisted active-recovery timestamp for that target. A missing control snapshot is null; `countersAvailable: false` means the recorded zero counters are placeholders because state could not be loaded. The target key correlates an alias; it neither identifies a provider session nor anonymizes guessable aliases. Only release-style numeric versions, `dev` and recognized commit/smoke hashes are retained as build labels; other labels are unavailable. Provider event time remains unavailable. The source/code fields below describe structured inputs and never infer a network root cause. Counts and recovery metadata come from supervisor state; they are not independent proof of progress or of the agent's current liveness.

The rolling JSON file retains at most **256 records and 512 KiB**, pruning oldest records as needed. `dropped` counts retention pruning, not failed writes or missed checks. At ten-minute intervals the record cap covers roughly 42 hours; faster checks shorten that window. Each write uses a 0600 temporary file, syncs it and replaces the previous snapshot under `incident.lock`. Readers can use the complete old or new file without locking. Runtime filesystem modes follow existing platform guarantees; Windows ACL evaluation is separate. Corrupt/truncated, oversized, future-schema and symlinked journals are refused without automatic reset. Unknown string values normalize to unavailable. This is not tamper-proof storage; deletion/rollback, external writers, disk failure, a crash during a tick and power-loss durability of directory metadata are outside its guarantees.

The history excludes task text, raw provider output/errors, child/provider thread IDs, account/environment values, paths and webhook URLs. Read-only consumers should use the typed loader rather than treating legacy `runs.jsonl` or headless stdout as this contract. Existing history/artifact formats retain their previous behavior and are not covered by these new content guarantees.

A journal write failure produces a fixed warning and a non-success tick result **after** normal tick work; it cannot retract a prompt or undo a saved hold/stop. Existing `state.json`/`runs.jsonl` may already record the action, and they are not rewritten to include this later failure. Inspect the tick result/scheduler output for it. No notification is sent or replayed automatically. Checks refused before the tick (including lock/ownership/config guards), standalone control commands, early notification polls and changes between ticks are not recorded here yet. The before/after control snapshots are not a complete control audit. The remainder of R2 adds complete decision inputs and control transitions; R3 adds durable delivery diagnostics; R4 adds the read-only incident view.

Offline tests reconstruct a synthetic BB retry, unavailable scan, recovery, child-input hold and continued held checks using only this journal. Other fixtures execute a local process status command and read synthetic Claude/Codex result files through the actual adapters, exercise restart/retention, reject malformed/aliased history, exclude secret markers, and verify that journal contention does not block stop or undo holds. They do not verify a live provider outage or installed loop.

### Structured source metadata

Schema 2 adds `source`, `category`, `httpStatus` and `exitCode` and fills `providerCode` when a token is recognized. Unrecognized/missing strings are `unavailable`, unsupported numbers are null, and `sourceTime` remains `unavailable`. A numeric zero exit is distinct from a missing exit. The vocabulary is finite; raw unknown tokens, error messages and transcript text are never stored. These diagnostic fields are ignored by the decision engine and cannot authorize failover.

| Source | Evidence represented |
| --- | --- |
| `bb_status` | Structured thread status; no retained provider-error details. |
| `bb_provider_error`, `bb_system_error` | Recognized fields from the retained error summary. Complete log scans refresh that summary; it is not a timestamped stream of every error. |
| `bb_error_summary` | Legacy cursor classification with no detailed metadata; no log replay is added solely to reconstruct it. |
| `bb_rate_limits` | A blocked structured rate-limit window supplies the effective error classification. |
| `process_json`, `process_exit` | Parsed status JSON or the status command's exit; a process exit is not the supervised agent's own exit unless that command defines it so. |
| `runner_state`, `runner_exit` | Headless wrapper state or its recorded exit, without a recognized result/event. |
| `claude_result`, `codex_events` | Structured result/event fields already parsed by the adapter, plus a valid wrapper exit when available. |
| `codex_rate_limits` | The existing structured app-server limit check confirmed a blocked window. |

BB error metadata survives restart with the cursor and clears on recognized recovery/new turn. Incomplete log scans withhold details. Codex event metadata follows its existing accumulated classification; a later unknown token does not make an earlier classified error fresh. Retained details can therefore describe an older failure within the turn. Check-start time is the supervisor's observation time, not the provider error's occurrence time. This remains a sampled summary, not a complete incident event stream.

Loading a version-1 journal is read-only and keeps its version. The next successful append upgrades it to version 2, preserving sequence numbers, retained records and pruning counts; old records keep their new fields unavailable. An older binary refuses the newer journal rather than overwriting it, which can make its tick return a history-write failure after normal actions. Keep tested and installed build labels explicit when deploying or rolling back. Offline fixtures cover upgrade/restart/recovery, partial scans, malformed numeric statuses, source distinctions, secret-marker rejection and metadata's independence from decision policy. No installed loop or live outage was used for these checks.
