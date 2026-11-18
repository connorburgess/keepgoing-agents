# Move BB supervision

With v0.1.4, inspect a proposed move between existing BB threads:

```sh
keepgoing transfer preview -c loop.yaml --thread thr_destination
keepgoing transfer preview -c loop.yaml --thread thr_destination --json
```

The preview is read-only. The CLI also provides the explicit apply/recover commands below; the plugin also offers a [Move supervision here](../integrations/bb-plugin-keepgoing/README.md#move-supervision-here) flow for its registered setups. The preview never starts, stops, sends to, changes the model of, creates or re-parents a thread. It does not change configuration, controls, ownership, goals, cursors, allowances, history or schedules, and does not acquire locks or create a runtime directory.

The current scope is one configured BB primary without a fallback configuration. The preview shows the source/destination thread IDs, validated provider and environment metadata, configured provider/model/reasoning/quota-group labels, all current control flags, recorded owner and goal ID/revision/state. Goal titles, task text, control notes, workspace paths, raw CLI output and parser errors are withheld. IDs and configured labels are operational metadata; avoid putting secrets in labels.

## What is checked

Two read-only `bb thread show --json` calls verify each requested identity, explicit non-archived/non-deleted metadata, recognized status, and matching thread/environment IDs. The preview compares project, environment, host and working-directory metadata between the threads, and by default requires their provider IDs to match the configured provider label. Unreleased source adds the explicit provider-change plan below. Cross-environment moves, a destination equal to the source, configured fallbacks, standard routes and an active fallback remain outside this scope. Provider aliases are not guessed or rewritten.

The calls use the configured BB executable, discard stderr, bound stdout to 64 KiB and apply a per-query timeout (default five seconds, configurable with `--timeout` up to 30 seconds). These are BB metadata queries, not model requests. They do not inspect transcript text or confirm a provider login. An old or unknown response shape is unavailable rather than compatible.

`thread show` does **not** establish model/reasoning selection, account/subscription identity, shared quota or model access. The preview displays configured labels and explicitly marks that verification unavailable. Matching provider IDs does not establish equivalent accounts. No fallback probe is run.

```sh
keepgoing transfer preview -c loop.yaml --thread thr_destination --offline --json
```

Offline mode reads local metadata only and returns unavailable thread compatibility. It never executes BB or another provider CLI.

### Where each thread works (unreleased)

When both threads were verified, the preview adds `workspace.from` and `workspace.to`. Each compares the working directory BB reports for that thread with the loop's `progress.repositories` and the worktrees registered in them (`git worktree list --porcelain`, read-only, one call per repository within `--timeout`). `ok` means inside one, `warning` means outside all of them, and `unavailable` means no progress repositories are configured, the directory cannot be read on this device, or a repository's worktrees could not be listed. These checks are advisory: they never change `compatibility` or the exit code, and the paths stay withheld. Offline previews skip them. A warning means the thread's work would not be seen by progress detection, for example when a BB environment is a checkout of another repository and the real work happens in a worktree made by hand. The comparison is case-sensitive after resolving symbolic links, so on a case-insensitive disk two spellings of one folder that differ only in letter case give a false warning. The `git worktree list` output is not size-capped; it is bounded by `--timeout`.

## Reading the result

The version-1 JSON report always has `outcome: "preview"` and `applySupported: true` (this CLI has an apply command, not a promise that this preview is actionable). Its `compatibility` describes only the metadata checks:

| Value | Meaning | Exit code |
| --- | --- | --- |
| `compatible_metadata` | Checked local/BB metadata fits this limited scope; model/account verification remains unavailable; apply must revalidate | 0 |
| `blocked` | At least one definite mismatch or unsupported configuration | 1 |
| `unavailable` | Required metadata could not be verified, including offline mode | 1 |

Invalid arguments/configuration return usage exit code 2 without a report. Missing control state is disabled; missing tick state selects primary; missing goal metadata is compatible with older loops. Malformed, oversized or non-regular control/state records are unavailable. Control/state reads are capped at 1 MiB each; the existing goal history bounds apply separately. These checks are not filesystem isolation against an operator concurrently changing runtime files.

The command rechecks config/fallback source snapshots after its reads. Changes make the preview unavailable (or leave an already-blocked result blocked). Other reads remain independent: controls, owner, goal, threads and environments may change between observations. This is neither a reservation nor an authorization token. The apply command revalidates under the tick lock and uses the recovery protocol below. An existing pause, stop, awaiting-user or completion hold never authorizes automatic resume.

## Inventory what a move involves (unreleased)

Before moving a loop, or a coordinator and its lanes, list what is there:

```sh
keepgoing transfer inventory -c loop.yaml
keepgoing transfer inventory -c loop.yaml --json
keepgoing transfer inventory -c loop.yaml --base origin/main --timeout 10s
```

The inventory is read-only. It reports:

- The configured BB primary thread: provider, status, project, environment, host and the working directory BB reports for it. It also reports the loop's control flags, recorded owner and whether a transfer is pending.
- The thread's direct child threads (its lanes), found with the same listing the BB adapter uses: `thread list --parent-thread <id> --include-hidden --json`, then `thread show` for each lane, at most 200. The listing may be up to 1 MiB, enough for 200 lanes at about 1.5 KB each; other BB queries keep the 64 KiB limit. Each lane gets the same fields, plus whether it is archived.
- For each configured progress repository, every registered worktree from `git worktree list --porcelain`: path, branch or detached, HEAD, how many tracked files differ from HEAD (untracked files are not counted, as in progress detection), whether Git marks it prunable or locked, and how many commits it has that the base does not. A worktree is `merged` only when it has no commits beyond the base and no tracked changes; a fresh worktree at the base with uncommitted work shows `ahead 0`.
- For each thread, whether its working directory is `inside` a worktree (which one), `outside` all of them, or `unavailable`. A worktree that no thread's working directory is inside is `unattributed`. As in the doctor workspace check, a configured repository path counts even when Git cannot list its worktrees.

What it never does:

- It never reads events, interactions, titles or message text. Titles are left out.
- It never matches a worktree to a lane by directory or branch name. Names of hand-made worktrees often carry stale thread IDs, so such a match would be a guess.
- It never fetches, prunes, takes a lock, creates a runtime directory or writes anything. Git runs with optional locks off, so `git status` does not refresh the index on disk.
- It never checks pull requests.

The base is `--base` when given (a local reference such as `origin/main` or a branch), otherwise each repository's recorded `origin/HEAD`. The report shows its commit, whether it is remote-tracking (under `refs/remotes`, which a fetch from or a push to that remote moves) or a local branch, and when it last moved according to its own reflog. A remote-tracking base is only as fresh as the last fetch or push a person made; a local base says nothing about the remote. A base that does not resolve leaves ahead and merged unavailable and is noted.

Unlike preview, the inventory prints absolute paths: it is local operator output for this command only. Do not paste it where paths should not appear. Paths are not written to logs, reports, incidents or state.

The JSON has `schemaVersion: 1`, `outcome: "inventory"`, `readAt`, `complete`, `loop`, `controls`, `owner`, `pendingTransfer`, `target`, `lanesListed`, `lanes`, `repositories` (each with `index`, `path`, `available`, `base` with `ref`, `commit`, `remoteTracking` and `refUpdatedAt`, and `worktrees`) and `notes`. Exit code 0 means every listing and count was complete. 1 means something could not be read or counted (including one worktree's changed files or commits beyond the base), the lane listing was not a bounded list of valid IDs, there were more than 200 lanes or 500 worktrees, or the two-minute overall deadline passed. The report is still printed and says what is missing. 2 means invalid arguments or a loop whose primary is not a BB thread.

## Record a migration review (unreleased)

A separately created successor does not inherit its predecessor's lane discovery or assignments. Use an explicit private review to account for existing work before making further coordination decisions. This command writes only `migration-review.json` in the successor's runtime directory. It never moves a thread, spawns or stops a lane, changes a worktree, sends a message, resumes supervision, or edits tasks, goals or holds. Existing recording, spending and stop gates still apply. The record grants no new authority and is not evidence that migration execution succeeded.

Keep both configurations, runtime directories and the assignment file outside checkouts. Both configurations must describe distinct BB primaries with existing readable controls, no pending transfer, and a recorded primary target. The destination uses the existing ownership rules: an authorized owning BB thread or external terminal can explicitly revise the review. The command does not take over ownership. An explicitly requested inventory and review can describe a retired predecessor without changing its retirement or holds. Publication into a retired successor refuses, leaving the saved review readable through `show`. Preview is descriptive and does not promise publication eligibility. Retirement changing after preview invalidates its digest; preview again before applying.

Put declarations in a private JSON file, for example:

```json
{"assignments":[
  {"id":"website","summary":"Review the current implementation","declaredState":"active","laneThread":"thr_example_website"},
  {"id":"rebrand","summary":"Finish the remaining acceptance review","declaredState":"dormant","laneThread":"thr_example_rebrand"},
  {"id":"open-question","summary":"Clarify the unresolved acceptance criterion","declaredState":"unresolved"}
]}
```

Every observed predecessor **direct** lane needs exactly one explicit row. Descendants are excluded; this command never claims coverage of the entire lane tree. `declaredState` is `active`, `dormant`, `unresolved` or `complete`, explicitly supplied by the caller. It is separate from BB's observed `idle`, `active` and other statuses. An active assignment observed idle is not shown as running or completed. `complete` is a declaration, not verified acceptance. An optional absolute `expectedWorktree` must match the worktree containing that lane's verified BB path; names are never matched heuristically.

```sh
keepgoing transfer reconcile preview -c successor.yaml --source-config predecessor.yaml --file assignments.json --json
# Use the digest returned by this review. Revisions also require its existing ID/revision.
keepgoing transfer reconcile apply -c successor.yaml --source-config predecessor.yaml --file assignments.json --digest <reviewed-digest> --json
keepgoing transfer reconcile show -c successor.yaml
keepgoing transfer reconcile show -c successor.yaml --private
```

To revise, pass `--id <review-id> --revision <current-revision>` to both preview and apply. Existing source/successor identities cannot be rebound. A record copied to another runtime directory is unavailable; status also rejects a different loaded configuration path, even with the same loop/thread. Persisted history is checked for unfinished-ID retention on every load, so a damaged later revision cannot hide earlier unfinished work. Previous unfinished assignment IDs cannot disappear: retain them and explicitly revise their declaration, even when a lane is absent or archived. A retained row can keep its missing lane reference, marked absent from the latest listing; it is never silently treated as finished. An unrelated unobserved lane cannot be added as a new mapping. All earlier declarations and observations remain in the bounded history. Each observation has a refresh timestamp independent of declaration state. `show --private` includes summaries, paths, lane identity/status and history; do not share it publicly. Default show/status expose counts and evidence time without assignment text or paths. Status reads the saved review only, without refreshing BB or Git.

Preview and apply each take two independent typed BB/Git inventories within a two-minute deadline, compare them and bracket them with local configuration/control/state/goal snapshots. Duplicate, malformed, unknown, oversized, incomplete or changed evidence refuses publication. A bare listing reaching 200 lanes is treated as bounded/uncertain, never complete coverage; at most 500 worktrees are accepted. Apply requires the preview digest to match its new reads and rechecks local sources and ownership under both tick locks plus the destination control lock before atomically publishing one private record. Locks are nonwaiting; a healthy worker is not stopped to obtain them. No BB/Git query occurs under those publication locks. Source and successor must have the same verified project and device, but may have different environments and paths.

These are independent reads, not an atomic BB/Git snapshot or a reservation. Changes seen between reads are visible as a refusal; changes after the last external read remain possible, including an ABA change that returns to the same metadata. Even successful recording establishes only the observed checklist at that time. Failure preserves an existing record; a publication error can be ambiguous, so inspect show before retrying. Missing/invalid input or a damaged record is refused, never replaced with a fresh empty checklist. Plan and record symlinks/nonregular files are rejected. Input is capped at 100 KiB, 200 assignments, 64-character IDs and 1,024-byte summaries; records retain at most 32 revisions and 1 MiB. Reaching a limit refuses without discarding history. Recovery requires a private valid backup, not deletion of unfinished work to bypass the bounds.

This slice does not reconcile tasks/goals, resolve a recording hold, dispatch idle work, register/import a successor in BB, extend supervisor discovery to mapped lanes, or settle cross-loop retries/provider return. CLI status and the unreleased [panel](dock.md#landing-view-and-evidence-limits) expose minimized historical counts and evidence time. The panel distinguishes missing, unavailable and changed-binding records, never treating unknown work as zero; it adds no permission or migration action. BB plugin projection, installed-panel and live migration acceptance remain open. Source changes do not update an installed runtime binary. No transcript text or age-based inference determines a declaration, permission or completion.

## Apply explicitly

```sh
keepgoing transfer apply -c loop.yaml --from thr_source --thread thr_destination
```

The expected `--from` rejects stale intent. The current owner or an identity-free terminal may apply the explicit move. A caller running in the destination BB thread needs `--take-over` when another thread owns the loop; an unrelated BB caller is refused. This is cooperative local ownership, not authentication against users who can edit runtime files.

Apply supports the same single-primary/same-environment scope as the preview, with provider changes refused by default and repeats the BB metadata checks under `tick.lock`. It does not apply or verify a model, reasoning setting or account. Same-provider moves retain existing labels; matching providers do not prove matching subscriptions. Config and runtime state must be outside checkouts and configured progress repositories. Config files must be regular, at most 1 MiB, and use literal YAML without anchors, aliases, merges or multiple documents. For same-provider moves the rewrite changes only `loop.bb.thread` semantically, retaining comments and values while potentially normalizing YAML formatting; the replacement uses owner-only permissions.

The move saves an awaiting-user hold, clears primary-specific observations/cursor, replaces the configuration, then binds the target and transfers ownership. All pre-existing enabled/disabled, paused, stop, awaiting-user and completion flags and notes survive; awaiting-user is added even if the loop was previously running. The owner generation advances even if the destination already owned the loop, so an existing daemon exits rather than silently continuing under a new target. No thread is stopped, prompted, spawned or re-parented by the transfer itself. An old active turn is not cancelled. An external scheduler is not changed; a daemon that exits must be started explicitly when appropriate.

Goal identity/history, send accounting, no-progress accounting and report/incident history are retained. The reset clears last observed state/check, retry grace, primary limit/reset observations, pending-input reporting and the stop-sent marker, plus `targets/primary/bb-cursor.json`. It retains the Git fingerprint/progress time, send timing and spent nudge/reprompt/handoff counts. Ordinary subsequent checks still use their existing reset rules. Transfer is not a fresh work budget.

After a successful move, inspect status and diagnostics from the destination or terminal. Only an explicit `resume` clears the holds, using the existing control semantics. The destination is then the loop's target, so in unreleased source it cannot run that `resume` itself: a person runs it from an ordinary terminal or the panel ([why](operations.md#the-supervised-session-cannot-clear-its-own-hold)); `enable` is separately needed if the loop was disabled. Do not resume work that still requires a person or was already complete.

## Explicit provider change (unreleased source)

After a person has created the successor BB conversation, a loop with one BB primary and no fallback configuration or standard routes can move supervision to it across providers. This is a CLI-only repair path for an existing destination on the same project, environment, host and working directory. It does not create the successor or move lanes, and does not implement cross-IDE continuity.

```sh
keepgoing transfer preview -c loop.yaml --thread thr_example_destination \
  --allow-provider-change --provider codex --quota-group example-codex \
  --model example-codex-model --reasoning medium
keepgoing transfer apply -c loop.yaml --from thr_example_source \
  --thread thr_example_destination --allow-provider-change \
  --provider codex --quota-group example-codex \
  --model example-codex-model --reasoning medium
```

Both commands require all five provider-change flags. `--provider` must name the destination's exact BB provider ID, different from the source configuration; the source's live provider must still match the source configuration. Model/reasoning are operator-supplied labels, not discovered settings. Pass `--model= --reasoning=` deliberately to clear old labels and leave the destination's existing BB choices alone. Nonempty labels record the intended route for later supervision; transfer itself never applies them to BB. `thread show` does not verify these choices or account access.

The rewrite replaces `loop.provider`, `loop.group`, `loop.bb.model` and `loop.bb.reasoning`. It removes `loop.account`, `loop.probe` (including executable, model and environment), and `loop.timing`, even if timing is disabled. Loading the replacement may supply the destination provider's default probe kind; no probe runs. The old account annotation is cleared, never replaced with a guessed identity; this does not change any login or catalog entry. The explicitly supplied quota group is only a label and does not prove an independent allowance. Review destination account/probe/timing choices separately before explicitly resuming. Old usage readings remain historical records bound to their original provider/probe identity; they do not become readings for the destination.

The ordinary holds, goal/history, send ledger, progress and spent counters survive. In addition to the usual observation reset, provider changes discard old timing/route-repair intent and a provider-specific start refusal. Transfer adds awaiting-user, advances the ownership generation, and never resumes, starts a daemon/service, or changes a scheduler. Inspect status afterwards; a person must explicitly decide whether to resume and start supervision. An enabled flag alone does not establish that checks are running.

Provider-change intents use pending-record version 2, recording both providers and destination labels alongside the existing before/after config digests. Recovery uses that recorded plan with `transfer recover --id ...`; it refuses new provider-change flags. Both live providers are checked again even after config replacement. Version-1 same-provider recovery remains supported. All invokers must use a build supporting version 2; older binaries cannot recover these intents. The existing source/ownership/pending fences and filesystem limits still apply.

This source change is unreleased and fixture-tested. It does not update an installed binary or BB plugin. Plugin registry attachment is a separate [unreleased reference-only flow](../integrations/bb-plugin-keepgoing/README.md#attach-an-existing-loop-unreleased-source); installed-plugin attachment/start acceptance, automatic handoff detection, generic adapter/cross-IDE continuity and lane migration remain follow-up work; the plugin's existing transfer interface still refuses provider changes. No live Claude-to-Codex transfer or installed-service acceptance was run.

## Recover an interrupted move

```sh
keepgoing status -c loop.yaml
keepgoing transfer recover -c loop.yaml --id move_0123456789abcdef0123456789abcdef
```

Before any target/state edits, one atomic control save records `pendingTransfer` (version, generated ID, source/destination and private before/after config digests) and the awaiting-user hold. Apply holds `tick.lock` across the operation, while control writes use short `control.lock` sections. BB metadata queries never hold the control lock. A process exit releases the OS locks but retains the pending record. Resume, enable, startup, goal edits, manual probes, retargeting, ordinary ticks and child polls cannot proceed through this pending fence. Pause, stop, disable, complete and await-user controls remain writable. A stop tick may act only when the current configuration exactly matches the recorded before or after version; it stops the target in that verified version. Corrupt/unverifiable recovery metadata blocks provider work, including stop execution, until repaired.

Recovery is explicit **roll-forward**, not automatic retry or rollback. It requires the exact pending ID, repeats both BB identity checks, accepts only the recorded before/after configuration bytes, repeats the idempotent observation reset and finishes the target/ownership commit. It leaves awaiting-user set. A changed config, invalid goal, missing source/destination metadata or unsupported cursor path remains held for repair; the command never guesses a replacement target. Preserve private backups and inspect the reported condition rather than deleting the fence or hand-editing its digests. If the config can no longer load, restore a matching private copy before recovery.

The final control commit removes the pending record and saves `bbTarget`, a persistent binding checked alongside config snapshots. Stale config copies using the same state directory are refused even when their own files have not changed. Future target changes must also use transfer; manually changing `bb.thread` will conflict with the binding. Diagnostic status omits the private config digests. Begin/finish control incident records are best-effort evidence: an incident write failure preserves the already-saved hold or completed move and returns failure. If output says `applied` with a history warning, inspect status; do not retry the old transfer as if no change occurred.

This protocol covers cooperating processes and atomic file replacement, not power-loss durability or adversarial filesystem changes. All invokers must run a version supporting transfer fences; older binaries ignore the new fields, so mixed-version supervision/downgrades are unsupported after adopting transfer. Model/account verification, cross-environment moves and fallback migration remain unfinished. Cross-provider moves require the unreleased CLI opt-in above. Unreleased plugin source adds [explicit registry attachment](../integrations/bb-plugin-keepgoing/README.md#attach-an-existing-loop-unreleased-source) for an already-owned single BB primary after a CLI move; this reconciles only its plugin reference and never starts or resumes supervision.

## Verification

Offline command fixtures verify matching and incompatible metadata, missing/corrupt records, ownership/goal display, all holds, unchanged private files, query failures/timeouts, stale config and output privacy. Windows CI runs the preview, apply/recovery and config-edit fixtures; Unix CI additionally runs the process-crash journey and existing setup/recovery smoke gates. A Unix child-process fixture exits immediately after config replacement, then verifies blocked resume, pending stop, recovery, a wake only to the new fictional thread and rejection of a stale config copy. Other fixtures interrupt after hold/state writes and cover write/history failures, ownership conflicts and unchanged allowance/goal files. Inventory fixtures build a temporary repository with a recorded `origin/HEAD`, a lane worktree that is ahead and dirty, a merged one, a detached unattributed one and a stand-in for BB. They cover duplicate and over-limit child listings, malformed listings, unverifiable or deleted threads, an unlistable repository, explicit, missing and unsafe base names, no progress repositories, absent titles and messages, and an unchanged Git directory with no runtime state created, an untracked file that is not counted, a worktree whose changes cannot be counted, a configured path Git cannot list, and (on Unix, through the production runners) a listing over 64 KiB. Workspace fixtures build temporary Git repositories with a linked worktree outside the repository directory and cover inside, outside, a name-prefix sibling, a symlink, a missing directory, an unlistable repository and offline mode, each without changing compatibility or printing a path. No live transfer, model call, account change, timer change or installed-binary upgrade is part of this feature's verification.

Provider-change command fixtures cover mandatory opt-in and explicit labels, source/destination provider mismatches, project/environment/host/path mismatches, label replacement/clearing, preserved holds and budgets, every durable interruption boundary, stale-source rejection and read-only BB queries only. These native fixtures are separate from live acceptance.

## Retire an obsolete loop after a manual move (unreleased)

A saved retirement marker also refuses transfer apply/recovery before BB queries or state/configuration replacement, including damaged metadata with a valid pending provider-change intent. Stop retains its existing separate path.

A transfer keeps one loop and its state directory. Creating a successor thread
with a separate loop does not transfer that state: both loops otherwise remain
independently resumable. After explicitly identifying the successor, retire the
obsolete source loop with:

```sh
keepgoing transfer supersede -c obsolete-loop.yaml \
  --from thr_example_source --successor-thread thr_example_successor
keepgoing status -c obsolete-loop.yaml
```

This is an operator declaration, not automatic migration detection. It supports
a single BB primary without fallbacks, checks the expected source and existing
ownership, and takes the check and control locks. It records `supersededBy` in
one atomic source control update. No BB query or action runs; the successor
reference does not prove that a successor loop exists, is registered, has the
right task or is running. No authority passes to the successor.

The source becomes **Superseded** in status and the panel; the panel omits
Resume. Resume, enable, takeover, startup and same-loop transfer refuse the
marker, including from a terminal. Checks send nothing and make no probes or
ordinary observations. Stop remains available, including `stop --lanes` under
its existing explicit rules. Superseding does not cancel a running agent turn,
stop a daemon, modify a scheduler, or stop, move or recreate healthy lanes.
A check already in progress finishes before the declaration takes its lock.
Request a stop explicitly if work must be cancelled.

Every existing hold, open question, note, goal revision, task, budget and target
binding is retained. No hold is cleared because it is old, and no transcript
is read. A resolved recording question is not authorization to spend, record,
publish or lift an unrelated stop. Before activating the successor, explicitly
review its configured task and current goal, resolve only authorized holds,
inventory predecessor lanes and worktrees, and verify its registration and
scheduler. This command does none of that reconciliation.

There is no resume or unsupersede operation. Repeated declarations refuse rather
than replacing the recorded reference. If the result is uncertain, inspect
status/control before retrying. A diagnostic-history failure after the control
save reports failure with the saved declaration intact. Repair damaged control
records using the existing private-backup procedure with all controllers stopped;
do not delete the marker to resume obsolete work. A mistaken retirement requires
an explicitly reviewed fresh loop or operator recovery, not automatic rollback.

All invokers must run a build that understands supersession. Older binaries
ignore the marker and may overwrite it: mixed-version supervision and downgrades
are unsupported. This source change does not replace installed binaries or the
BB plugin. macOS fixture execution is separate from live provider acceptance,
installed-service lifecycle and native Linux/Windows verification.

Remaining acceptance gaps include atomic reconciliation across separate loops,
explicit task/goal/hold reconciliation, lane assignment mapping, dormant-work
surfacing and successor registration/attachment. Supersession alone does not
complete a migration and establishes no duplicate-execution guarantee before
its source declaration commits.
