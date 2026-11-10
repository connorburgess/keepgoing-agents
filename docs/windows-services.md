# Windows scheduling investigation

Research completed on 2026-11-10 against Microsoft documentation and the current keepgoing code. **Windows managed service support is not implemented.** No Task Scheduler calls, registration, account changes, scheduler fixtures, or live lifecycle tests were performed for this investigation. Existing Windows background-start and runner tests do not establish Task Scheduler compatibility.

The proposed first backend is a Task Scheduler 2.0 task for the current, already logged-in user. It would run the ordinary daemon, preserving keepgoing's control polling and early child-outcome checks. keepgoing would still own the loop; the scheduler would only start and restart its supervisor. Use the existing `start --background` or an independently configured scheduler until the implementation and verification gates below pass.

## Options and decision

| Option | Fit for the first backend | Decision |
| --- | --- | --- |
| Task Scheduler, interactive user token | No stored password; runs in an existing interactive session. User/task permissions still need verification. | Prototype this path with the current user and least-privilege run level. |
| Task Scheduler, password logon | Requires a password at registration. | Outside the initial scope; do not collect credentials or change logins. |
| Task Scheduler, S4U | Stores no password, but Microsoft documents no network or encrypted-file access in this mode. | Unsuitable as the default for networked agents and existing account stores. |
| Windows Service Control Manager | Creation needs SCM service-creation rights; the executable also needs a service entrypoint/control handler. | Defer; the existing console daemon is not an SCM service. |
| A periodic scheduled `tick` | Can reuse the existing single-pass command, but early checks remain limited by the external timer. | Keep as an external scheduling option, not the first managed daemon design. |

The logon distinctions come from [TASK_LOGON_TYPE](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/ne-taskschd-task_logon_type). Task creation and execution also depend on the caller's [security context](https://learn.microsoft.com/en-us/windows/win32/taskschd/security-contexts-for-running-tasks); interactive-token selection is not proof that registration succeeds on every host. SCM has separate [access rights](https://learn.microsoft.com/en-us/windows/win32/services/service-security-and-access-rights) and [ServiceMain requirements](https://learn.microsoft.com/en-us/windows/win32/services/writing-a-servicemain-function).

This design promises neither operation before login nor survival across logout, sleep, or reboot without login. It does not create users, elevate privileges, alter logon policy, or change an existing timer.

## Proposed task contract

The following settings and behavior are planned, not accepted task XML or shipped commands:

- Bind a deterministic task identity to the current user's SID and canonical loop state directory. Resolve Windows path aliases before hashing; do not reuse the Unix UID field. Refuse another principal, unrecorded task, changed action, or conflicting owner.
- Register a single direct executable action for the state-bound daemon, with absolute binary/config/state paths and a working directory. Never launch `start`, which would enable the loop, or a shell containing task text. Preserve every control flag, note, target, and ownership generation.
- Use an explicitly enabled task with a logon trigger limited to that user and an explicit initial run after verification. Create without overwrite, initially disabled, then verify the registered definition before enabling. Do not add a registration trigger. Microsoft distinguishes create-only, update, and disabled registration in [TASK_CREATION](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/ne-taskschd-task_creation).
- Set `MultipleInstancesPolicy=IgnoreNew`, retaining the daemon and tick locks as the final concurrency guards. Microsoft defines this as skipping new instances while one is running; it is not a replacement for loop identity checks. See [instance policies](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/ne-taskschd-task_instances_policy).
- Set `ExecutionTimeLimit=PT0S` explicitly: the documented default stops a task after 72 hours. Use bounded restart settings and report exhausted retries; do not imply indefinite recovery. See [execution time limits](https://learn.microsoft.com/en-us/windows/win32/taskschd/tasksettings-executiontimelimit).
- Make battery, idle, network, and wake policies explicit in the preview. Proposed defaults allow battery operation, impose no idle/network prerequisite, and do not wake a sleeping computer. Validate those choices on the target OS; do not inherit unspecified scheduler defaults or replay missed agent prompts.

Task Scheduler's [executable action](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/nn-taskschd-iexecaction) separates executable, arguments, and working directory, but arguments are still a Windows command-line string. Environment substitutions can use values cached when the task engine started. Native tests must establish literal spaces, quotes, backslashes, Unicode, and percent handling; reject unsupported paths before registration. Do not assume XML escaping alone preserves argv.

A private service entrypoint/profile must set the selected PATH and clear BB_THREAD_ID before entering the daemon. The existing `__service-daemon` checks state-directory binding but relies on the OS service definition for environment setup. Do not copy the installer's full environment, credentials, webhook secrets, or prompts into task XML. Runtime files stay outside checkouts; Windows DACL handling must protect recovery records and any IPC credentials. Go mode 0600 alone is not evidence of a private Windows DACL.

## Structured status and recovery

Use the Task Scheduler API's typed properties and HRESULTs behind a bounded adapter. A fixed PowerShell bridge may be investigated, but is not selected or implemented: it would need to preserve numeric HRESULTs, avoid interpolation of user data, and emit only an allowlisted JSON schema. Never interpret localized `schtasks` tables, console messages, or event prose.

Status must distinguish registration, enabled state, task state, historical results, and the live daemon challenge:

| Signal | Planned interpretation |
| --- | --- |
| Task path, principal, actions, triggers, settings | Verify against the private record before managing it. Compare a strict semantic definition; scheduler XML serialization need not be byte-identical. Unknown extra actions/settings require review. |
| `State` | Use the documented enum: unknown, disabled, queued, ready, running. Running does not prove this loop's daemon is healthy. |
| `LastRunTime`, `LastTaskResult` | Historical scheduler evidence, not provider health. Preserve numeric result codes; distinguish never-run and running statuses from failures. |
| `NextRunTime` | Scheduler metadata only. It can reflect disabled triggers and does not describe the daemon's next loop check. A logon trigger need not have a predictable next timestamp. |
| Manager failure or unfamiliar schema/code | Unavailable/unknown, never absent and never a reason for provider failover. |

See the [task state enum](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/ne-taskschd-task_state), [result constants](https://learn.microsoft.com/en-us/windows/win32/taskschd/task-scheduler-error-and-success-constants), [last result](https://learn.microsoft.com/en-us/windows/win32/taskschd/registeredtask-lasttaskresult), and [next-run caveat](https://learn.microsoft.com/en-us/windows/win32/taskschd/registeredtask-nextruntime). Numeric nonzero values are not automatically failures; absence needs a tested exact-task lookup contract with an accessible scheduler/folder.

Install/remove should share `service.lock`, the local preflight, and private recovery records with the existing backends. Recovery via `--state-dir` must work without a valid config. Removal should verify identity, disable future starts, stop only the matching supervisor, verify no queued/running task instances and a free daemon lock, delete the exact registration, verify absence, and finally remove recovery files. Retain the record on any failure; never delete a task merely because its name resembles keepgoing.

## Shutdown is a delivery gate

Microsoft's [AllowHardTerminate](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/nf-taskschd-itasksettings-put_allowhardterminate) describes WM_CLOSE followed by optional TerminateProcess. The current console daemon handles interrupt/termination signals in [main.go](../cmd/keepgoing/main.go), and its [diagnostics endpoint](../internal/daemoninfo/daemoninfo.go) only answers status challenges. Neither establishes a cooperative Task Scheduler shutdown path. Setting AllowHardTerminate=false alone is insufficient evidence of graceful removal.

[IRegisteredTask::Stop](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/nf-taskschd-iregisteredtask-stop) can return S_FALSE when instances cannot be stopped. It must not be treated as verified shutdown just because the HRESULT is nonnegative. [DeleteTask](https://learn.microsoft.com/en-us/windows/win32/api/taskschd/nf-taskschd-itaskfolder-deletetask) removes registration; deletion alone is not evidence that the daemon exited.

The Windows [runner](../internal/adapter/runner/spawn_windows.go) and [background launcher](../cmd/keepgoing/start_windows.go) set detached/process-group flags, without requesting job breakaway. Windows normally associates child processes with their parent's [job object](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects). Therefore, current console tests do not prove that detached agent turns survive a scheduled supervisor stopping. Whether and how Task Scheduler groups these processes must be measured; do not infer it from the general job-object documentation.

Before exposing installation, provide a bounded, authenticated, service-instance-specific supervisor shutdown mechanism, or establish an equally reliable native mechanism. It must leave loop controls unchanged and never stop a manual daemon or an agent turn. Do not repurpose `keepgoing stop`: that command intentionally stops the agent. No PID-only kill fallback or automatic process-tree termination belongs in service removal. If shutdown cannot be verified, retain recovery state and return incomplete removal.

## Implementation and verification gates

Deliver follow-up changes as separate focused PRs:

1. Establish supervisor-only shutdown and private Windows state/identity handling. Offline tests must cover wrong/stale instance identity, copied credentials, manual daemons, concurrent ticks, held loops, and inaccessible or permissive DACLs.
2. Implement the typed scheduler adapter and install/status/remove using fake manager responses. Cover create-only collisions, concurrent operations, partial registration/removal, changed definitions, missing configs, unknown HRESULTs, S_FALSE stop, malformed/oversized output, timeouts, unchanged controls, and no provider calls. Dry runs make no scheduler calls or writes.
3. Validate task definitions through `TASK_VALIDATE_ONLY` on Windows without registration, then run an isolated native lifecycle fixture under an existing standard user's interactive session. Use a local process adapter with no model calls. Verify exact argv/environment, control precedence, duplicate refusal, daemon liveness, restart behavior, detached-child survival, and removal across multiple trigger opportunities.
4. Separately verify logout/login, reboot/login, sleep, battery transition, network loss, binary/config relocation, and a prolonged soak on each supported Windows architecture. Record versions, evidence, and limitations before claiming support. An unavailable interactive test host blocks this verification only; continue other roadmap work.

The next research-to-code step is the shutdown/identity fixture, not a live installation. The existing Windows CI background-process and access-diagnostic tests remain useful prerequisites, but none of these Task Scheduler gates has passed yet.
