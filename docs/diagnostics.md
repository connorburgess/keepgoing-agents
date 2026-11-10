# Diagnostics

`keepgoing doctor` checks a loop without starting an agent, running a probe, changing a login, or writing runtime state. It is implemented on `main`; v0.1.1 does not include it. A separate loop skill is not required.

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml
keepgoing doctor -c /private/example-loop/keepgoing.yaml --json
keepgoing doctor -c /private/example-loop/keepgoing.yaml --offline
```

The ordinary command resolves configured executable paths and runs supported agents' `--version` commands, BB's `thread show <id> --json`, and headless providers' login-status commands described below. Each command has a five-second timeout and a 64 KiB stdout limit. `--timeout` changes the per-command deadline. `--offline` skips all command execution and local daemon queries while still inspecting configuration, files, and executable availability. Custom process-adapter commands, lifecycle logs, model calls, and probe commands are never executed. Explicit diagnostics can query BB even when the loop is held; they do not change that hold.

## What it checks

- Configuration and fallback parsing/validation, task-file readability, state/control/health readability, and private-file Unix mode bits. Separate read-only OS checks inspect runtime path access on macOS and Linux, and report Linux POSIX ACL metadata presence as described below. Doctor does not print task text or control notes or change permissions. Full ACL permission evaluation and macOS/Windows ACL inspection are unavailable.
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

## Runtime path access

`state.write_access` checks write and search permission on the runtime directory using the OS access API on macOS and Linux. If the directory does not exist, it checks the nearest existing ancestor and labels that result explicitly. It never creates directories or temporary files. An inaccessible path, read-only filesystem, non-directory ancestor, or dangling directory symlink is reported without exposing the path or raw filesystem error.

Existing regular `runs.jsonl`, `tick.lock`, and `daemon.lock` files get separate `state.<filename>.write_access` checks: history needs write access, while locks need read/write access. Append/lock symlinks and other special files are skipped with an unavailable result. Atomic JSON records are replaced through their directory; doctor does not incorrectly require direct write permission on their existing files. Their readability checks still apply.

These checks run offline too and never open files for writing, take a lock, change permissions, or alter control holds. They use the caller's current host context, which may differ from the scheduler's. The OS access API evaluates real credentials, so doctor supports only ordinary non-root callers with matching real/effective user and group IDs. Privileged or mismatched identities and other platforms, including Windows, are explicitly unavailable.

An `ok` result means the OS allowed the requested access at check time. It does not guarantee a future write, sufficient disk space, lock acquisition, file replacement, or access to every nested target directory. ACLs may affect the OS decision, but this is **not an ACL privacy audit**: owner-only mode bits alone do not prove that nobody else has access. No ACL entries or account identifiers are printed. Permission denials are errors; unclassified filesystem failures remain unavailable.

Offline fixtures cover denied/read-only/unknown results, missing ancestors, dangling links, append/lock selection, unchanged held state, and redaction. Native macOS/Linux tests exercise read-only directory and lock permissions under an ordinary user. Windows is cross-compiled; native Windows ACL inspection remains planned.

## Linux ACL metadata

Each successfully opened file or directory checked by doctor also gets a `<check-id>.acl` entry, such as `config.permissions.acl`, `task.acl`, or `state.directory.acl`. On Linux, doctor queries only the sizes of `system.posix_acl_access` and, for directories, `system.posix_acl_default`. It never loads ACL values or principal identifiers, executes an ACL command, or changes filesystem metadata. These checks work offline and preserve control holds.

Present metadata produces a warning to review ACL rules alongside mode bits. A directory's default ACL governs inheritance for new objects and can exist while that directory still has owner-only mode bits; see [Linux ACL semantics](https://man7.org/linux/man-pages/man5/acl.5.html). Presence alone does not establish that another user can access private data: doctor does not evaluate ACL entries or effective permissions for other users.

`ENODATA` is reported as **not reported (absent or hidden)**, because [the Linux API documents both possibilities](https://man7.org/linux/man-pages/man2/getxattr.2.html). Unsupported filesystems, permission errors, unexpected results, and other platforms are unavailable. No detected metadata is never treated as a privacy all-clear. If either query detects metadata, the check warns even when the other query is unavailable, with both results shown.

This inspects the configured path's target, following symlinks like the existing readability check. It does not inspect ancestor ACLs, missing paths, every nested runtime file, non-POSIX ACL formats, or macOS/Windows ACLs. Results are observations at query time, not a guarantee against subsequent filesystem changes. Mode-bit and runtime-access checks remain separate.

Fixtures cover present/default/unknown/hidden metadata, error redaction, and offline held loops without provider calls. A native Linux fixture creates a default ACL in a temporary directory, verifies detection despite `0700` mode bits, and confirms the query leaves ACL bytes and files unchanged. The native fixture skips filesystems without POSIX ACL support; CI logs that case separately so its execution can be verified. macOS and Windows report this check as unavailable.

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

New `keepgoing daemon` instances provide a loopback-only diagnostic endpoint. Status and doctor send a fresh challenge and verify the reply against the private endpoint record and the state directory identity. A stale file, copied endpoint from another loop, or reused listening port cannot by itself establish liveness. Queries never acquire the daemon lock, start an agent, or alter control state. If diagnostics cannot start, the daemon warns and continues supervising.

JSON includes `daemon` with `state` (`responsive`, `not_recorded`, `unavailable`, or `skipped`), an explanatory `message`, `binaryComparison`, `dryRun`, and `nextCheck`. A verified response also supplies `phase` (`checking` or `waiting`), `startedAt`, `observedAt`, and `nextCheckAt` while waiting. `scheduler` is `ok` only for a verified response; `supervisor.live_version` compares that daemon's build label, independently of the historical `supervisor.version` check. Raw build labels, the verification key, and endpoint address are withheld.

A responding daemon is not necessarily making progress: it can be stuck in a check while its diagnostic endpoint still responds. The next normal check is due after the current pass plus the configured interval, subject to control-poll timing. Control changes can trigger an earlier pass. These times are not a promise of execution through sleep, shutdown, or a stuck pass. Dry-run daemons report their mode explicitly.

The query is capped at one second (a shorter doctor `--timeout` applies). Endpoint records and responses have size limits; the endpoint binds only to `127.0.0.1`. The record is private runtime state and contains a random verification key, which is neither sent in requests nor printed in reports. Clean daemon shutdown removes its record. Failures remain unavailable, and `--offline` skips the query entirely.

Older daemons and external schedulers, including BB automations, launchd, systemd, cron, and Task Scheduler, are **not inspected** by this interface. No endpoint does not mean no supervisor. Service installation is also unverified. The configured daemon interval is never used to invent an external scheduler's next-run time.

Live headless session identity is **not implemented**. Doctor does not start an app-server or resume a session to infer it. Use [`fallbacks check --dry-run`](fallbacks.md#preview-manual-probes) to preview selected models, timeouts, and the scheduled cap without model calls or state changes. Dollar cost is explicitly unknown; monetary estimation and spending limits are not implemented. Removing `--dry-run` makes this a separate explicit operation outside the scheduled daily cap. Doctor never runs it automatically.

## Verification

Offline tests cover structured target results, account environment propagation, privacy, missing executables, corrupt records, stale/future timestamps, disabled or missing active fallbacks, and unchanged control state. Unix subprocess fixtures cover time and output limits and stderr suppression. Login fixtures cover logged-in/out signals, command failures, deadlines, working directories, separate account overrides, disabled fallbacks, extra-argument skips, and redaction. These checks do not establish server-side credential validity, paid handoff, external scheduler health, or Windows ACL support. Local daemon fixtures cover fresh challenges, wrong/copied/stale endpoints, deadlines, live build redaction, control changes, and state-directory reload rejection.
