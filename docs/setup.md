# Guided setup

`keepgoing init` builds a disabled loop without editing YAML or installing a loop skill. This command is implemented on `main`; the v0.1.1 release does not include it. Follow the [development build instructions](../README.md#development-build-for-the-current-quick-start) before using this guide, and substitute that binary's absolute path for `keepgoing`.

## First loop

From an existing Git project with at least one commit, run:

```sh
keepgoing init
```

The wizard detects `codex`, `claude`, and `bb` on `PATH`. Choose a task file or enter a task with clear completion criteria, choose an agent, and accept or adjust the operating limits. The default is one agent with no fallbacks. A BB target requires an existing thread ID and its provider label; headless targets start a new session on their first wake and resume that session afterward. Setup does not attach to existing headless or IDE conversations.

The wizard shows the target, project, account directory when explicitly selected, quota groups, fallback order, operating limits, and future probe behavior. It does not run providers, test authentication, create accounts, alter permissions, enable supervision, or install a scheduler.

It writes `keepgoing.yaml`, `prompt.md`, and, if needed, `fallbacks.yaml` into a **new** directory beneath the OS user configuration directory:

- macOS: `~/Library/Application Support/keepgoing/loops/<name>`
- Linux: `$XDG_CONFIG_HOME/keepgoing/loops/<name>` or `~/.config/keepgoing/loops/<name>`
- Windows: `%AppData%\keepgoing\loops\<name>`

Runtime state will live in that directory's `state` subdirectory. `--directory` selects a different new directory. Setup refuses paths inside the project or any Git checkout, including paths reached through existing symlinks, and refuses existing destinations even if empty. New directories use mode `0700` and files use `0600` on Unix; existing parent permissions are preserved. On Windows, filesystem ACLs govern access. Tasks and account configuration paths stay outside the checkout; account files are never copied.

Review the generated task and configuration, then run the exact `start --foreground` command printed by setup. Commands include the resolved config path; Windows output uses PowerShell syntax. Start validates local files and executable availability, enables the loop, and runs the daemon in the terminal. Choose `--background` to detach it instead; the command waits for a startup acknowledgement and prints the daemon PID. Existing holds remain in effect until `resume`. See [starting supervision](operations.md#starting-supervision) for validation limits, controls, and background lifecycle. `enable` alone still does not start a supervisor. Background mode installs no service and does not restart after a crash or reboot. On macOS, [LaunchAgent management](services.md) is available separately; external schedulers remain available on every platform.

The generated prompt tells the agent to honor newer user instructions, finish and verify the entire task before calling `complete`, and call `await-user` when only human-dependent work remains. The agent needs permission to run these control commands and write the private state directory. Setup preserves the provider's current sandbox and permission settings; it does not bypass a permission request. Completion holds across supervisor restarts until an explicit `resume`.

## Scripted setup

Every wizard choice has a flag. `--non-interactive` never asks questions; it requires a name, agent, and task or task file. It reads stdin only when `--task-file -` is explicit. Project defaults to the current directory. Prefer `--task-file` to avoid putting private task text in shell history or process arguments.

```sh
keepgoing init --non-interactive \
  --name example-loop --agent codex \
  --project /projects/example-app \
  --task-file /private/example-task.md \
  --directory /private/example-loop \
  --wake-every 1h --stall-after 30m --max-nudges 3 \
  --max-reprompts 3 --backoff 10m --interval 5m
```

Add `--dry-run` to preview without writing files. `keepgoing init --help` lists all flags. Input ending or cancellation while answering the wizard writes no setup. Existing setups are never overwritten; edit their private configuration or choose a new name/directory.

For an existing BB thread:

```sh
keepgoing init --non-interactive \
  --name example-chat --agent bb \
  --thread thr_example --provider codex \
  --project /projects/example-app --task-file /private/example-task.md
```

This records the supplied target; it does not verify the thread's identity, switch its model/login, or transfer another supervisor. Use [doctor](diagnostics.md) for target diagnostics. BB transfer remains separate roadmap work.

## Structured setup for integrations

`init --non-interactive --json` prints one versioned JSON result on success. Add `--dry-run` for a preview; omit it to create the private setup. Both use the same validation and configuration generation as the wizard. The ordinary text output remains the default. JSON requires noninteractive mode so wizard questions cannot corrupt stdout.

For task entry from an integration, pass `--task-file -`, write UTF-8 task text to the child's stdin, and close the pipe. This keeps task text out of command arguments and avoids a temporary task file. Input is limited to 1 MiB before whitespace trimming; empty, oversized, invalid UTF-8, and failed reads are rejected before any setup files are written. Signal cancellation interrupts a blocked stdin read. `--task-file -` also works with ordinary text output, but always requires `--non-interactive`. Use `./-` to read a file literally named `-`.

For example, in a POSIX shell:

```sh
keepgoing init --non-interactive --json --dry-run \
  --name example-chat --agent bb \
  --thread thr_example --provider codex \
  --project /projects/example-app --task-file - < /private/example-task.md
```

Schema version 1 has these fields:

| Field | Meaning |
| --- | --- |
| `schemaVersion`, `version` | Contract version and CLI build label. Reject unsupported contract versions. |
| `outcome` | `preview` means no files written; `created` means private setup files were written. Neither starts supervision. |
| `loop`, `project`, `configPath`, `stateDir`, `promptPath` | Resolved setup identity and paths. These paths are private metadata; handle the response accordingly. |
| `taskSource` | `inline`, `file`, or `stdin`; never the task text or source filename. |
| `primary` | An `agent` reference (adapter, provider, group, and applicable thread/workdir/model fields) and `account` selection (`inherited` or `explicit_directory`). BB inherits the existing thread's login/model. |
| `fallbacks` | Ordered array of `{id, target}`, each target with the same shape as `primary`; empty array for a single agent. |
| `limits` | `wakeEvery`, `stallAfter`, `interval`, `backoff` as Go duration strings; `maxNudges`, `maxReprompts`, and `failoverOn` (empty or rate-limit-only). |
| `probes` | Present only with fallbacks: `every`, `ttl`, `dailyCapPerCandidate`, `exactOutsideCap`, and `manualOutsideCap`. The cap does not include exact/manual calls. No probe runs during setup. |
| `next` | `doctor`, `startForeground`, and `startBackground` argv arrays, including the executable and exact config path. Execute as argument arrays, never concatenate into a shell command. These are suggestions, not actions performed by init. |
| `limitations` | Human-readable scope and review reminders; do not parse these strings to make decisions. |

The result omits task/prompt text, raw YAML, environment maps, and account-directory values. It does not verify the supplied BB identity, provider/model availability, authenticated subscription, independent quota, or scheduler state. After creation, an integration can use the existing `doctor --json` flow to display diagnostics, then offer an explicit start choice. Review the resolved target and limits before starting; do not automatically run `next` commands when parsing a result. Preview paths do not yet exist, and a preview does not reserve them or guarantee later creation will succeed.

Validation, input, and setup-write failures exit nonzero with stderr diagnostics and no success JSON. A stdout failure may occur after successful file creation and may leave partial JSON: require both exit 0 and a complete supported result, inspect the destination before retrying, and never overwrite it automatically. Help retains the normal CLI help behavior.

Offline fixtures cover preview/create consistency, BB identity fields, ordered account selections, omitted private input, bounded/cancelled stdin, failed writes, and unchanged existing setups. This is the shared CLI contract for the planned BB setup UI; it does not add a plugin setup button or attach/transfer a thread.

## Existing accounts and ordered fallbacks

`--account-dir` selects an existing headless account configuration directory using `CODEX_HOME` or `CLAUDE_CONFIG_DIR`. Omitting it inherits the launching environment and the provider's normal login. `--model` chooses a primary headless model; fallback models inherit their respective CLI configurations. Advanced model/probe settings remain available in [configuration](configuration.md) and [fallbacks](fallbacks.md).

Give each independent quota pool a label using letters, digits, hyphens, underscores, or dots. Accounts sharing limits must share a label: changing a label or using a different directory does not create independent quota. Setup cannot verify which subscription a directory authenticates. It refuses fallback labels already used by an earlier target, since those targets cannot provide independent recovery.

Repeat `--fallback ADAPTER[:GROUP][=ACCOUNT_DIR]` in preference order. Without an explicit group, the provider label is used (`codex` or `claude-code`). The wizard asks these fields separately. For three already authenticated independent accounts:

```sh
keepgoing init --non-interactive \
  --name example-accounts --agent claude \
  --account-dir /private/accounts/claude-a --group pool-a \
  --fallback codex:pool-b=/private/accounts/codex-b \
  --fallback claude:pool-c=/private/accounts/claude-c \
  --probe-daily-cap 2 \
  --project /projects/example-app --task-file /private/example-task.md
```

Generated fallbacks use redundancy `min_healthy: 1`, `span: group`. After you enable and run supervision, scheduled probes run at most every 12 hours per candidate, with a 24-hour health TTL and a default cap of two scheduled calls per candidate per UTC day. Active-group candidates are skipped. Exact handoff probes and manually requested probes are additional calls outside that cap. No calls occur during setup. Handoffs are allowed only for structured `rate_limit` errors and require an exact target probe. Switch-back stays off.

The generated setup and account routing are covered by tests with fake provider discovery and temporary Git repositories. Real login verification, paid multi-account handoff, and a fresh install-to-completion walkthrough remain separate acceptance checks; setup success does not claim those passed.
