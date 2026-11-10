# Guided setup

`keepgoing init` builds a disabled loop without editing YAML or installing a loop skill. This command is implemented on `main`; the v0.1.1 release does not include it. Install a build containing this change before following these instructions.

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

Every wizard choice has a flag. `--non-interactive` never reads input; it requires a name, agent, and task or task file. Project defaults to the current directory. Prefer `--task-file` to avoid putting private task text in shell history or process arguments.

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

This records the supplied target; it does not verify the thread's identity, switch its model/login, or transfer another supervisor. Target diagnostics and BB transfer are separate roadmap work.

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
