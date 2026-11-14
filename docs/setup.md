# Guided setup

`keepgoing init` builds a disabled loop without editing YAML or installing a loop skill. This command requires v0.1.2 or newer; the v0.1.1 release does not include it. Follow the [installation instructions](../README.md#install), then check `keepgoing version` before using this guide. If using a separate development binary, substitute its absolute path for `keepgoing`.

## First loop

From an existing Git project with at least one commit, run:

```sh
keepgoing init
```

Published v0.1.5 detects `codex`, `claude`, and `bb` on `PATH`. v0.2.0 also discovers Cursor through the `agent` executable, preserving the existing selection order when other CLIs are installed; the desktop `cursor` launcher is not a headless CLI. Choose a task file or enter a task with clear completion criteria, choose an agent, and accept or adjust the operating limits. The default is one agent with no fallbacks. A BB target requires an existing thread ID and its provider label; headless targets start a new session on their first wake and resume that session afterward. Setup does not attach to existing headless or IDE conversations.

The wizard shows the target, project, account directory when explicitly selected, quota groups, fallback order, operating limits, and future probe behavior. It does not run providers, test authentication, create accounts, alter permissions, enable supervision, or install a scheduler.

It writes `looptender.yaml` (releases up to v0.2.0 wrote `keepgoing.yaml`, which is still read), `prompt.md`, and, if needed, `fallbacks.yaml` into a **new** directory beneath the OS user configuration directory:

- macOS: `~/Library/Application Support/looptender/loops/<name>`
- Linux: `$XDG_CONFIG_HOME/looptender/loops/<name>` or `~/.config/looptender/loops/<name>`
- Windows: `%AppData%\looptender\loops\<name>`

On a computer where the tool already keeps its files in a `keepgoing` directory from before the rename, the directory is `keepgoing/loops/<name>` in the same place; see [per-user directories](configuration.md#per-user-directories).

Runtime state will live in that directory's `state` subdirectory. `--directory` selects a different new directory. Setup refuses paths inside the project or any Git checkout, including paths reached through existing symlinks, and refuses existing destinations even if empty. New directories use mode `0700` and files use `0600` on Unix; existing parent permissions are preserved. On Windows, filesystem ACLs govern access. Tasks and account configuration paths stay outside the checkout; account files are never copied.

**Task files are imported once.** Setup copies the entered task, stdin, or `--task-file` contents into the generated `prompt.md`. Later edits to the original task file do not update that snapshot. Supervision rereads the saved `loop.prompt_file` (generated as `prompt.md`) when preparing task prompts; edit that saved file to revise future prompts while preserving its control instructions. Already-sent messages are unchanged. keepgoing does not follow referenced documents or detect whether the task wording has become stale.

Review the generated task and configuration, then run the exact `start --foreground` command printed by setup. Commands include the resolved config path; Windows output uses PowerShell syntax. Start validates local files and executable availability, enables the loop, and runs the daemon in the terminal. Choose `--background` to detach it instead; the command waits for a startup acknowledgement and prints the daemon PID. Existing holds remain in effect until `resume`. See [starting supervision](operations.md#starting-supervision) for validation limits, controls, and background lifecycle. `enable` alone still does not start a supervisor. Background mode installs no service and does not restart after a crash or reboot. On macOS and Linux, a [service](services.md#the-recommended-way-to-keep-checks-running) is the recommended way to keep checks running across restarts, and `init` prints its install command; external schedulers remain available on every platform. Whatever runs the checks, `status` says when they have [stopped](operations.md#external-schedulers-can-stop-on-their-own).

The generated prompt tells the agent to honor newer user instructions, finish and verify the entire task before calling `complete`, and call `await-user` when only human-dependent work remains. The agent needs permission to run these control commands and write the private state directory. Setup preserves the provider's current sandbox and permission settings; it does not bypass a permission request. Completion holds across supervisor restarts until an explicit `resume`.

<a id="optional-goal-during-setup-unreleased"></a>

## Optional goal during setup

In v0.1.5, `keepgoing init --with-goal` records a durable goal while creating the disabled setup. The interactive wizard asks whether to record and share a goal, suggesting yes; answer no or pass `--with-goal=false` to retain the existing prompt-only setup. Noninteractive callers must explicitly pass `--with-goal`; existing scripts retain their previous behavior. The rebuilt BB plugin exposes an explicit goal checkbox using the same flag; older plugins retain task-only setup. Published v0.1.4 does not support this flag.

The task and completion criteria are copied once into private `task.md`. The goal title starts as the loop name; both objective and acceptance references point to that absolute snapshot path. `state/goal.json` holds the generated identity and revision 1, using the existing goal store. `prompt.md` contains stable operating instructions, and `loop.prompt_goal` explicitly shares the current goal specification in future automatic messages. Task text is not repeated in that scaffold or ordinary setup output. The agent must be able to read the private snapshot; setup does not grant filesystem access or change provider permissions. The supervisor does not read the goal's references or validate completion criteria.

Review the printed `doctor` command, then explicitly start. Goal creation never enables supervision, claims an owner, starts a daemon, or makes a provider call. Preview creates no files and allocates no goal ID. A normal write failure attempts to remove only this new setup's files; a crash or failed cleanup can leave a partial disabled directory. Inspect it privately before retrying; existing destinations are never overwritten.

Use [goal revise](goals.md#revise-explicitly) to change the title or references with the expected ID/revision. Editing the original task file does not update the snapshot. Editing a referenced file does not increment the goal revision or track its contents; preserve new specifications in separate files and revise their references when history matters. Goal metadata does not bind completion to a revision or prove useful progress. The rebuilt BB setup flow now uses this contract; its live deployment, the timed first-loop walkthrough and revision-bound results remain unfinished.

Goal task input is bounded to 1 MiB of UTF-8 text; file input must be a regular file. Offline fixtures cover the released BB/Claude Code/Codex setup paths and the v0.2.0 Cursor path, preview/create consistency, source-copy behavior, fresh composition after goal revision, preserved holds, private output and cleanup. No live provider or installed loop was changed.

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

In v0.1.3 or newer, optional `--no-progress-timeout 2h` holds after two hours without verified Git metadata changes. The baseline survives turns, restart and resume. Downtime counts through v0.1.5; v0.2.0 does not count time in which no check ran or the network was down. Choose it only when Git activity is a suitable progress signal. See [no-progress timeout](operations.md#no-progress-timeout).

In v0.1.3 or newer, optional `--max-sends 20` caps automatic send attempts with a persistent counter; failed attempts count and resume does not reset it. Zero or omission leaves accounting disabled. See [send allowance](operations.md#total-send-allowance).

In v0.1.3 or newer, optional `--deadline "2030-01-01T18:00:00Z"` sets an absolute cutoff, shown in both text and JSON previews. It requires a timezone. The next ordinary check at or after the deadline holds future supervision; it does not stop a running agent. Changing the cutoff does not clear an existing hold. See [deadline behavior](operations.md#loop-deadline). `--wake-every` controls stall-nudge spacing; idle wakes follow the check interval and other gates. Neither it nor `--max-nudges` is a loop-wide turn budget.

For an existing BB thread:

```sh
keepgoing init --non-interactive \
  --name example-chat --agent bb \
  --thread thr_example --provider codex \
  --project /projects/example-app --task-file /private/example-task.md
```

This records the supplied target; it does not verify the thread's identity, switch its model/login, or transfer another supervisor. Use [doctor](diagnostics.md) for target diagnostics. Existing loops can use the separate [BB transfer commands](transfer.md), released in v0.1.4 with fixture tests; live transfer verification remains open.

<a id="cursor-setup-unreleased-source"></a>

## Cursor setup

With v0.2.0 or newer, choose `cursor` in the wizard or pass `--agent cursor`. Published v0.1.5 does not support this choice.

```sh
keepgoing-dev init --non-interactive --agent cursor \
  --name example-cursor --project /projects/example-app \
  --task-file /private/example-task.md --with-goal --max-sends 10
```

Setup resolves `agent` on `PATH` and saves that executable, the project directory and optional `--model`. It never runs the provider or checks/changes its login; run `keepgoing doctor` afterwards to check the CLI version and stored login. Existing Cursor credentials and permissions are inherited, and Cursor must already trust the project directory or the first turn fails before it starts. `--account-dir` and `--fallback` are rejected for Cursor without writing a setup; the wizard skips those questions. Cursor is also unavailable as a fallback for other agents. `--dry-run` and the private stdin/JSON path below work as for other targets.

This starts a separate CLI conversation on the first wake. Existing IDE chats, interactive permission bridging, quota/reset detection and automatic fallback recovery are unavailable. Text/JSON previews and doctor disclose these limits; reading a version string does not establish a supported version range. Review [Cursor evidence and remaining live checks](cursor.md) before starting. Creation stays disabled, including with an optional goal.

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
| `startJson` | v0.2.0. `true` when this build's `start` accepts `--background --json`, which reports a [second loop on one session](operations.md#one-loop-per-session) as a structured refusal. Absent in older results: do not pass the flag. |
| `loop`, `project`, `configPath`, `stateDir`, `promptPath` | Resolved setup identity and paths. These paths are private metadata; handle the response accordingly. |
| `taskPath`, `goal` | Additive fields in v0.1.5, present only with `--with-goal`. `taskPath` identifies the private snapshot; `goal` describes `promptContext: true`, `titleSource: "loop_name"`, `specificationSource: "task_snapshot"`, and, after creation only, `record` with generated ID/revision/state. Preview has no saved record. Private task text and goal references are omitted. |
| `taskBinding` | `snapshot`: task input is imported once into `promptPath` (or `taskPath` for goal setup in v0.1.5); the original input is not tracked. Added in v0.1.4; absent in older results means this metadata is unavailable. |
| `taskSource` | `inline`, `file`, or `stdin`; never the task text or source filename. |
| `primary` | An `agent` reference (adapter, provider, group, and applicable thread/workdir/model fields) and `account` selection (`inherited` or `explicit_directory`). BB inherits the existing thread's login/model. |
| `fallbacks` | Ordered array of `{id, target}`, each target with the same shape as `primary`; empty array for a single agent. |
| `limits` | `wakeEvery`, `stallAfter`, `interval`, `backoff` as Go duration strings; `maxNudges`, `maxReprompts`, and `failoverOn` (empty or rate-limit-only); optional `deadline`, `maxSends` and `noProgressTimeout` (v0.1.3+). |
| `probes` | Present only with fallbacks: `every`, `ttl`, `dailyCapPerCandidate`, `exactOutsideCap`, and `manualOutsideCap`. The cap does not include exact/manual calls. No probe runs during setup. |
| `next` | `doctor`, `startForeground`, and `startBackground` argv arrays, including the executable and exact config path. Execute as argument arrays, never concatenate into a shell command. These are suggestions, not actions performed by init. |
| `limitations` | Human-readable scope and review reminders; do not parse these strings to make decisions. |

**Refused (v0.2.0).** When another enabled loop already supervises the same session (see [one loop per session](operations.md#one-loop-per-session)), `init --json` writes nothing, exits 1, and prints exactly `{"schemaVersion":1,"outcome":"refused","refusal":{"code":"second_loop_on_session","otherLoop":"example-loop"}}` on stdout: `outcome: "refused"`, a `refusal.code`, and the other loop's name in `refusal.otherLoop`, never its path. The sentence for people, which does name the other loop's configuration, is on stderr. Show `otherLoop`; do not parse stderr. `--allow-second-loop` sets the loop up anyway. Other codes may be added; treat an unknown code as a refusal without a reason.

The result omits task/prompt text, raw YAML, environment maps, and account-directory values. It does not verify the supplied BB identity, provider/model availability, authenticated subscription, independent quota, or scheduler state. After creation, an integration can use the existing `doctor --json` flow to display diagnostics, then offer an explicit start choice. Review the resolved target and limits before starting; do not automatically run `next` commands when parsing a result. Preview paths do not yet exist, and a preview does not reserve them or guarantee later creation will succeed.

Validation, input, and setup-write failures exit nonzero with stderr diagnostics and no success JSON. A stdout failure may occur after successful file creation and may leave partial JSON: require both exit 0 and a complete supported result, inspect the destination before retrying, and never overwrite it automatically. Help retains the normal CLI help behavior.

Offline fixtures cover preview/create consistency, BB identity fields, ordered account selections, omitted private input, bounded/cancelled stdin, failed writes, and unchanged existing setups. The separately distributed [BB setup UI](../integrations/bb-plugin-keepgoing/README.md#supervise-this-thread) uses this shared CLI contract and is fixture-tested; live deployment and install-to-completion verification remain open. CLI setup alone does not install the plugin or attach/transfer a thread.

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

**Following your standard route list instead.** If you keep a [standard route list](routes.md), the wizard asks `Follow your standard route list for handoffs? N of its M routes apply to this loop. (yes/no) [yes]` for a `bb`, `claude` or `codex` loop, before the fallback questions, and only when at least one route applies; yes skips them, and the loop gets `routes: standard` with handoff on for rate limits. A script follows the list only with `--routes standard`, never because a list exists; `--routes standard` and `--fallback` exclude each other, and `--routes standard` is refused while no usable list is written or the list has no routes (`routes: []`). The JSON `limits.failoverOn` is what is written: `[rate_limit]` for a following loop, even while no route applies yet. The JSON result says `routes: "standard"` and lists the routes that apply to the loop now as its `fallbacks`. Probes follow the list's policy, not `--probe-daily-cap`.

To add a fallback to a loop that already exists, v0.2.0 provides the guided [`keepgoing fallbacks add`](fallbacks.md#add-a-fallback-with-guided-setup).

Generated fallbacks use redundancy `min_healthy: 1`, `span: group`. After you enable and run supervision, scheduled probes run at most every 12 hours per candidate, with a 24-hour health TTL and a default cap of two scheduled calls per candidate per UTC day. Active-group candidates are skipped. Exact handoff probes and manually requested probes are additional calls outside that cap. No calls occur during setup. Handoffs are allowed only for structured `rate_limit` errors and require an exact target probe. Switch-back stays off.

The generated setup and account routing are covered by tests with fake provider discovery and temporary Git repositories. Real login verification, paid multi-account handoff, and a fresh install-to-completion walkthrough remain separate acceptance checks; setup success does not claim those passed.
