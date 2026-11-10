# Diagnostics

`keepgoing doctor` checks a loop without starting an agent, running a probe, changing a login, or writing runtime state. It is implemented on `main`; v0.1.1 does not include it. A separate loop skill is not required.

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml
keepgoing doctor -c /private/example-loop/keepgoing.yaml --json
keepgoing doctor -c /private/example-loop/keepgoing.yaml --offline
```

The ordinary command resolves configured executable paths and runs only supported agents' `--version` commands and BB's `thread show <id> --json`. Each command has a five-second timeout and a 64 KiB stdout limit. `--timeout` changes the per-command deadline. `--offline` skips all command execution while still inspecting configuration, files, and executable availability. Custom process-adapter commands, lifecycle logs, model calls, and probe commands are never executed. Explicit diagnostics can query BB even when the loop is held; they do not change that hold.

## What it checks

- Configuration and fallback parsing/validation, task-file readability, state/control/health readability, and private-file Unix mode bits. It does not print task text or control notes, check effective write access or extended ACLs, or change permissions. Windows ACL checks are unavailable.
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

## Recent history is not scheduler health

A last check older than `--stale-after` produces a warning. The default is 30 minutes; set it above your external scheduler's interval:

```sh
keepgoing doctor -c /private/example-loop/keepgoing.yaml --stale-after 2h
```

No history means no completed tick is recorded; enabling alone does not run a supervisor. A future timestamp warns about clock differences or a test using `--now`. A recent tick, including a dry-run tick, is historical evidence only: it does not prove the daemon or scheduler is still alive.

Scheduler installation/liveness and its next scheduled check are **unavailable**. New ticks record their build label: doctor compares that historical label with its own and warns if they differ. Empty or `dev` labels cannot identify a build. A match does not prove which binary is currently running; a mismatch does not establish which is newer. Legacy tick records without a build label remain unavailable. The daemon interval in YAML does not describe an external scheduler and is not used to invent a next-run time. Inspect the daemon or scheduler directly until dedicated scheduler diagnostics are implemented.

Structured provider login checks and live headless session identity are also **not implemented**. Doctor does not invoke login commands or start an app-server to infer them. Paid probe cost previews are planned; `fallbacks check` remains a separate explicit operation that makes model calls outside the scheduled daily cap. Doctor never runs it automatically.

## Verification

Offline tests cover structured target results, account environment propagation, privacy, missing executables, corrupt records, stale/future timestamps, disabled or missing active fallbacks, and unchanged control state. Unix subprocess fixtures cover time and output limits and stderr suppression. These checks do not establish live account authentication, paid handoff, scheduler health, or Windows ACL support.
