# Preview a BB supervision move

On `main` after v0.1.3, inspect a proposed move between existing BB threads:

```sh
keepgoing transfer preview -c loop.yaml --thread thr_destination
keepgoing transfer preview -c loop.yaml --thread thr_destination --json
```

This is a read-only planning command. Applying a move and the plugin's "Move supervision here" action are not implemented. The preview never starts, stops, sends to, changes the model of, creates or re-parents a thread. It does not change configuration, controls, ownership, goals, cursors, allowances, history or schedules, and does not acquire locks or create a runtime directory.

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

The version-1 JSON report always has `outcome: "preview"` and `applySupported: false`. Its `compatibility` describes only the metadata checks:

| Value | Meaning | Exit code |
| --- | --- | --- |
| `compatible_metadata` | Checked local/BB metadata fits this limited scope; model/account verification and applying the move remain unavailable | 0 |
| `blocked` | At least one definite mismatch or unsupported configuration | 1 |
| `unavailable` | Required metadata could not be verified, including offline mode | 1 |

Invalid arguments/configuration return usage exit code 2 without a report. Missing control state is disabled; missing tick state selects primary; missing goal metadata is compatible with older loops. Malformed, oversized or non-regular control/state records are unavailable. Control/state reads are capped at 1 MiB each; the existing goal history bounds apply separately. These checks are not filesystem isolation against an operator concurrently changing runtime files.

The command rechecks config/fallback source snapshots after its reads. Changes make the preview unavailable (or leave an already-blocked result blocked). Other reads remain independent: controls, owner, goal, threads and environments may change between observations. This is neither a reservation nor an authorization token. A future apply command must revalidate under locks, preserve holds and goal identity, fence stale owners/configurations, reset thread-specific observations without refunding loop-wide limits, and define recoverable behavior for crashes between writes. An existing pause, stop, awaiting-user or completion hold never authorizes automatic resume.

## Verification

Offline command fixtures verify matching and incompatible metadata, missing/corrupt records, ownership/goal display, all holds, unchanged private files, query failures/timeouts, stale config and output privacy. Windows CI runs the same preview tests; Unix CI also runs the existing setup/recovery smoke gates. No live transfer, provider call, account change, timer change or installed-binary upgrade is part of this feature's verification.
