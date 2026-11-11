# Move BB supervision

With v0.1.4, inspect a proposed move between existing BB threads:

```sh
keepgoing transfer preview -c loop.yaml --thread thr_destination
keepgoing transfer preview -c loop.yaml --thread thr_destination --json
```

The preview is read-only. The CLI also provides the explicit apply/recover commands below; the plugin also offers a [Move supervision here](../integrations/bb-plugin-keepgoing/README.md#move-supervision-here) flow for its registered setups. The preview never starts, stops, sends to, changes the model of, creates or re-parents a thread. It does not change configuration, controls, ownership, goals, cursors, allowances, history or schedules, and does not acquire locks or create a runtime directory.

The current scope is one configured BB primary without a fallback configuration. The preview shows the source/destination thread IDs, validated provider and environment metadata, configured provider/model/reasoning/quota-group labels, all current control flags, recorded owner and goal ID/revision/state. Goal titles, task text, control notes, workspace paths, raw CLI output and parser errors are withheld. IDs and configured labels are operational metadata; avoid putting secrets in labels.

## What is checked

Two read-only `bb thread show --json` calls verify each requested identity, explicit non-archived/non-deleted metadata, recognized status, and matching thread/environment IDs. The preview compares project, environment, host and working-directory metadata between the threads, and requires their provider IDs to match the configured provider label. Cross-environment moves, provider changes, a destination equal to the source, configured fallbacks and an active fallback are outside this first scope. Provider aliases are not guessed or rewritten.

The calls use the configured BB executable, discard stderr, bound stdout to 64 KiB and apply a per-query timeout (default five seconds, configurable with `--timeout` up to 30 seconds). These are BB metadata queries, not model requests. They do not inspect transcript text or confirm a provider login. An old or unknown response shape is unavailable rather than compatible.

`thread show` does **not** establish model/reasoning selection, account/subscription identity, shared quota or model access. The preview displays configured labels and explicitly marks that verification unavailable. Matching provider IDs does not establish equivalent accounts. No fallback probe is run.

```sh
keepgoing transfer preview -c loop.yaml --thread thr_destination --offline --json
```

Offline mode reads local metadata only and returns unavailable thread compatibility. It never executes BB or another provider CLI.

## Reading the result

The version-1 JSON report always has `outcome: "preview"` and `applySupported: true` (this CLI has an apply command, not a promise that this preview is actionable). Its `compatibility` describes only the metadata checks:

| Value | Meaning | Exit code |
| --- | --- | --- |
| `compatible_metadata` | Checked local/BB metadata fits this limited scope; model/account verification remains unavailable; apply must revalidate | 0 |
| `blocked` | At least one definite mismatch or unsupported configuration | 1 |
| `unavailable` | Required metadata could not be verified, including offline mode | 1 |

Invalid arguments/configuration return usage exit code 2 without a report. Missing control state is disabled; missing tick state selects primary; missing goal metadata is compatible with older loops. Malformed, oversized or non-regular control/state records are unavailable. Control/state reads are capped at 1 MiB each; the existing goal history bounds apply separately. These checks are not filesystem isolation against an operator concurrently changing runtime files.

The command rechecks config/fallback source snapshots after its reads. Changes make the preview unavailable (or leave an already-blocked result blocked). Other reads remain independent: controls, owner, goal, threads and environments may change between observations. This is neither a reservation nor an authorization token. The apply command revalidates under the tick lock and uses the recovery protocol below. An existing pause, stop, awaiting-user or completion hold never authorizes automatic resume.

## Apply explicitly

```sh
keepgoing transfer apply -c loop.yaml --from thr_source --thread thr_destination
```

The expected `--from` rejects stale intent. The current owner or an identity-free terminal may apply the explicit move. A caller running in the destination BB thread needs `--take-over` when another thread owns the loop; an unrelated BB caller is refused. This is cooperative local ownership, not authentication against users who can edit runtime files.

Apply supports the same single-primary/same-environment/provider scope as the preview and repeats the BB metadata checks under `tick.lock`. It does not apply or verify a model, reasoning setting or account. Existing labels are retained; matching providers do not prove matching subscriptions. Config and runtime state must be outside checkouts and configured progress repositories. Config files must be regular, at most 1 MiB, and use literal YAML without anchors, aliases, merges or multiple documents. The rewrite changes only `loop.bb.thread` semantically, retaining comments and values while potentially normalizing YAML formatting; the replacement uses owner-only permissions.

The move saves an awaiting-user hold, clears primary-specific observations/cursor, replaces the configuration, then binds the target and transfers ownership. All pre-existing enabled/disabled, paused, stop, awaiting-user and completion flags and notes survive; awaiting-user is added even if the loop was previously running. The owner generation advances even if the destination already owned the loop, so an existing daemon exits rather than silently continuing under a new target. No thread is stopped, prompted, spawned or re-parented by the transfer itself. An old active turn is not cancelled. An external scheduler is not changed; a daemon that exits must be started explicitly when appropriate.

Goal identity/history, send accounting, no-progress accounting and report/incident history are retained. The reset clears last observed state/check, retry grace, primary limit/reset observations, pending-input reporting and the stop-sent marker, plus `targets/primary/bb-cursor.json`. It retains the Git fingerprint/progress time, send timing and spent nudge/reprompt/handoff counts. Ordinary subsequent checks still use their existing reset rules. Transfer is not a fresh work budget.

After a successful move, inspect status and diagnostics from the destination or terminal. Only an explicit `resume` clears the holds, using the existing control semantics; `enable` is separately needed if the loop was disabled. Do not resume work that still requires a person or was already complete.

## Recover an interrupted move

```sh
keepgoing status -c loop.yaml
keepgoing transfer recover -c loop.yaml --id move_0123456789abcdef0123456789abcdef
```

Before any target/state edits, one atomic control save records `pendingTransfer` (version, generated ID, source/destination and private before/after config digests) and the awaiting-user hold. Apply holds `tick.lock` across the operation, while control writes use short `control.lock` sections. BB metadata queries never hold the control lock. A process exit releases the OS locks but retains the pending record. Resume, enable, startup, goal edits, manual probes, retargeting, ordinary ticks and child polls cannot proceed through this pending fence. Pause, stop, disable, complete and await-user controls remain writable. A stop tick may act only when the current configuration exactly matches the recorded before or after version; it stops the target in that verified version. Corrupt/unverifiable recovery metadata blocks provider work, including stop execution, until repaired.

Recovery is explicit **roll-forward**, not automatic retry or rollback. It requires the exact pending ID, repeats both BB identity checks, accepts only the recorded before/after configuration bytes, repeats the idempotent observation reset and finishes the target/ownership commit. It leaves awaiting-user set. A changed config, invalid goal, missing source/destination metadata or unsupported cursor path remains held for repair; the command never guesses a replacement target. Preserve private backups and inspect the reported condition rather than deleting the fence or hand-editing its digests. If the config can no longer load, restore a matching private copy before recovery.

The final control commit removes the pending record and saves `bbTarget`, a persistent binding checked alongside config snapshots. Stale config copies using the same state directory are refused even when their own files have not changed. Future target changes must also use transfer; manually changing `bb.thread` will conflict with the binding. Diagnostic status omits the private config digests. Begin/finish control incident records are best-effort evidence: an incident write failure preserves the already-saved hold or completed move and returns failure. If output says `applied` with a history warning, inspect status; do not retry the old transfer as if no change occurred.

This protocol covers cooperating processes and atomic file replacement, not power-loss durability or adversarial filesystem changes. All invokers must run a version supporting transfer fences; older binaries ignore the new fields, so mixed-version supervision/downgrades are unsupported after adopting transfer. Model/account verification, cross-environment/provider moves, fallback migration and plugin import of manually configured loops remain unfinished.

## Verification

Offline command fixtures verify matching and incompatible metadata, missing/corrupt records, ownership/goal display, all holds, unchanged private files, query failures/timeouts, stale config and output privacy. Windows CI runs the preview, apply/recovery and config-edit fixtures; Unix CI additionally runs the process-crash journey and existing setup/recovery smoke gates. A Unix child-process fixture exits immediately after config replacement, then verifies blocked resume, pending stop, recovery, a wake only to the new fictional thread and rejection of a stale config copy. Other fixtures interrupt after hold/state writes and cover write/history failures, ownership conflicts and unchanged allowance/goal files. No live transfer, model call, account change, timer change or installed-binary upgrade is part of this feature's verification.
