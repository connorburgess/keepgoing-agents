![Keepgoing Agents - supervision for long-running coding agents](docs/assets/keepgoing-banner.jpg)

# keepgoing-agents

`keepgoing` supervises a coding agent: it wakes an idle session, handles structured failures with bounded retries, and can hand off to a confirmed fallback when configured. The agent does the work; keepgoing never edits, commits to, or merges a repository.

The core is a single binary for macOS, Linux and Windows. Use its daemon or an existing scheduler to check the loop. No separate loop skill is required. The host and supervisor must be running for checks to happen.

## Current availability

**Latest release: [v0.1.4](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.1.4).** Homebrew and release archives include the CLI features below. Installing a release does not upgrade a separately deployed supervisor executable or install the BB plugin or macOS companion.

| Surface | What you can use | Verification boundary |
| --- | --- | --- |
| Core CLI | Setup, start, controls, bounds, diagnostics, goals and BB transfer | Released in v0.1.4; broad offline tests, with partial live BB evidence |
| Headless Claude Code / Codex | New sessions, saved-session resume and configured account environments | Implemented; full live multi-turn, cancellation and separate-account handoff checks remain open |
| BB plugin | Setup, status, controls and transfer for plugin-created loops | Separate installation; newer UI flows are fixture-tested, with live deployment verification open |
| macOS menu preview | Read-only local loop and optional goal display | Developer build only; interactive controls, packaging and accessibility verification remain open |
| Cursor / Antigravity | Integration plans | No built-in adapters or existing IDE-chat attachment today |

Recent changes on `main` are **not in v0.1.4**: opt-in goal creation during setup, successful goal-subcommand help exit codes, retention of BB's generic interrupted-turn diagnostic code, and [recorded automatic prompt goal revisions](docs/goals.md#recorded-send-provenance). See the [changelog](CHANGELOG.md) and [release evidence](docs/release-verification.md) for exact scope.

The complete live setup → multiple turns → hold → restart → recovery → completion walkthrough is still unfinished. Existing tests and package installation do not establish readiness to migrate other threads. The [roadmap](docs/roadmap.md) tracks those acceptance checks separately from implementation.

## Install

With Homebrew on macOS or Linux:

```sh
brew install connorburgess/tap/keepgoing
keepgoing version
# For an existing Homebrew installation:
# brew update && brew upgrade keepgoing
```

Or download a [release archive](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.1.4) for macOS, Linux or Windows and verify it against the published SHA-256 checksums. The [package verification record](docs/release-verification.md#v014-release-and-package-evidence) identifies the paths tested; not every OS/architecture has had a native installation walkthrough.

With Go 1.24 or newer:

```sh
go install github.com/connorburgess/keepgoing-agents/cmd/keepgoing@latest
```

`go install` selects a tagged release, not `main`. These builds report `dev` because they lack the release build label; inspect `go version -m /path/to/keepgoing` for the module version. Prebuilt archives carry an embedded version label.

### Development build

On macOS/Linux, keep a source build separate from the installed release:

```sh
git clone https://github.com/connorburgess/keepgoing-agents.git
cd keepgoing-agents
revision=$(git rev-parse HEAD)
mkdir -p "$HOME/.local/bin"
go build -trimpath -ldflags "-X main.version=dev-$revision" \
  -o "$HOME/.local/bin/keepgoing-dev" ./cmd/keepgoing
"$HOME/.local/bin/keepgoing-dev" version
```

Substitute that absolute binary path for `keepgoing` below. Record its revision when testing; building does not replace an existing loop's executable or timer. See [binary smoke checks](docs/release-verification.md#check-an-actual-binary-without-provider-calls).

## Quick start

Install and sign in to a supported agent CLI first, or use an existing BB thread. From an existing Git project with at least one commit:

```sh
keepgoing init
```

1. Choose a task, an agent and operating limits. The default is one agent with no fallbacks.
2. Review the plan. Creation writes a **disabled** private setup outside the checkout; it does not run agents, probes, logins or a scheduler.
3. Run the exact `doctor` command printed by setup. Review its warnings and unavailable checks; credential presence does not establish usable quota.
4. Run the printed `start --foreground` command to enable and supervise the loop. Use `--background` to detach instead. Background mode installs no service and does not restart after a crash or reboot.

Use the generated config path for subsequent commands. For example, replacing this fictional path with yours:

```sh
keepgoing status -c /private/example-loop/keepgoing.yaml
keepgoing pause -c /private/example-loop/keepgoing.yaml --note "reviewing by hand"
keepgoing resume -c /private/example-loop/keepgoing.yaml
keepgoing stop -c /private/example-loop/keepgoing.yaml
keepgoing complete -c /private/example-loop/keepgoing.yaml --note "Task verified"
```

`complete` saves a completion hold; it is a declaration by the agent or operator, not independent verification. `resume` explicitly clears pause, stop, awaiting-user and completion holds. Existing holds remain intact when starting or restarting a supervisor.

Unreleased safety correction: repeated control writes advance the loaded timestamp even when the system clock repeats or moves backwards, so an older registered-resume ticket cannot overlook a repeated pause. This is covered by offline tests; published v0.1.4 does not include the correction.

The quick start needs v0.1.2 or newer. For v0.1.1 use its [tagged documentation](https://github.com/connorburgess/keepgoing-agents/tree/v0.1.1). Follow the [setup guide](docs/setup.md) for scripted setup, accounts, bounds, Windows command syntax and private paths. Advanced configurations are in [examples](examples/); release users should use examples from their matching tag.

## How it connects to models

keepgoing uses the agent host's existing credentials and permissions:

- **BB:** select an existing thread. BB runs its provider/model; keepgoing observes structured state and sends control/messages through the BB CLI. It does not switch the thread's login or create/re-parent threads.
- **Claude Code or Codex:** the installed CLI runs a new headless session on the first wake and resumes its saved session afterward. By default it inherits the launching environment and provider configuration. `init --account-dir` can select an existing account directory; `--model` selects a headless model. Setup does not attach to existing IDE or headless chats.
- **Process adapter:** supply structured status and resume commands for another system. This is an integration contract, not verified support for every IDE.

Multiple headless accounts and fallback order are configurable; quota-group labels must describe actual shared limits. [Account setup](docs/setup.md#existing-accounts-and-ordered-fallbacks) and [fallback probes](docs/fallbacks.md) explain the model calls and account boundaries. Probe previews with `fallbacks check --dry-run` spend no model quota; actual probes do.

Automatic model catalogue discovery and retirement/replacement policy remain [planned](docs/roadmap.md#11-useful-supervision-features-from-product-analysis). Updating a provider CLI, changing its selected model and upgrading keepgoing are separate operations. keepgoing does not silently upgrade model selections.

## Looping and setup

Task input is **copied once** into the generated `prompt.md`; changes to the original task file are not tracked. Future prompts reread the saved prompt file. Keep operating instructions stable and have the agent inspect current authorized sources before choosing work.

For a revisioned task, [`goal create|revise|show`](docs/goals.md) stores private goal identity, title, references and revisions. Opt-in `loop.prompt_goal` includes the current specification in automatic prompts and handoffs. The goal commands and prompt transport are available in v0.1.4. On `main` (unreleased), `init --with-goal` and an explicit wizard choice create a goal referencing one private task snapshot, with stable wake policy and current-goal transport enabled. The BB setup UI does not yet offer this choice. It does not update old queued messages, infer roadmap progress or verify acceptance.

The [BB plugin](integrations/bb-plugin-keepgoing/README.md) offers a separate setup flow with preview, disabled creation, diagnostics and explicit start. It also supports held transfer for plugin-created loops. The CLI's [`transfer preview|apply|recover`](docs/transfer.md) preserves goals and holds while fencing stale targets. Both transfer paths remain fixture-tested rather than live-verified.

### Optional macOS menu preview

The [native preview](integrations/macos/README.md) reads explicitly registered local loops on menu open or refresh. It can show goal metadata privately and distinguish current holds from historical activity. It starts no supervisor or timer. It is an unreleased, read-only developer build; interactive controls and optional installation remain planned.

## Why

An agent can finish a turn before its task is complete, hang without progress, or encounter a usage limit. keepgoing applies explicit continuation and recovery rules so a person does not have to watch every turn.

Decisions use structured status: exit codes, JSON fields, HTTP status and provider events. Transcript prose is never a decision signal. Unknown errors get bounded re-prompts and then a human hold; they never trigger failover.

## What it does

- **Wakes, nudges and retries.** Idle sessions can wake at each ordinary check. Stall nudges and error re-prompts have separate caps/backoff. Active BB children and pending provider retries suppress duplicate work; pending human input requires attention.
- **Bounds automatic work.** Optional deadlines, total send allowances and Git-based no-progress timeouts persist across turns and restarts. Failed sends count; resume does not refund them. These are not dollar budgets or proof of useful work. See [operations](docs/operations.md).
- **Confirms fallbacks before handoff.** Eligible configured fallbacks need health evidence and an exact-model probe. Known reset times can hold recovery while ordinary checks continue. Switch-back is opt-in; no configured fallbacks means no automatic handoff. See [fallbacks](docs/fallbacks.md).
- **Preserves control and ownership.** Stop and holds take precedence on checks. A different BB thread needs explicit takeover; transfer keeps the loop held until explicitly resumed. Invalid or stale configuration can withhold provider operations, including stop, until repaired/reloaded.
- **Explains recorded failures.** `doctor` checks supported setup/access/status signals; `status` separates holds, historical checks and daemon evidence; `timeline` reads keepgoing-owned incident and notification records. Missing evidence stays unavailable. See [diagnostics](docs/diagnostics.md).
- **Reports attention and completion.** Signed webhooks support escalation and completion declarations. Delivery history distinguishes transport/HTTP results from unverified human receipt. Reports require configuration; they are not an implicit notification service.
- **Runs with your host.** Use `start`, a daemon, an existing scheduler, or managed macOS/Linux [services](docs/services.md). `ci-wait` and `net-retry` are separate command helpers. Windows service management remains planned.

## Limitations

- A disconnected host cannot run checks. keepgoing does not restart the BB host daemon. Generic errors do not establish an internet or host-disconnect cause; see [interrupted-turn evidence](docs/diagnostics.md#interrupted-bb-turns-and-host-availability).
- BB error classification needs structured provider events. Codex headless limit classification may query its app-server's structured rate-limit snapshot when a failed turn has no recognized code; unavailable evidence remains unknown. See [integration evidence](docs/integrations.md).
- Headless stop requests go to an owned wrapper, never a PID trusted from a file. Legacy turns without a stop key are refused. Acceptance is not proof of exit; abrupt wrapper death can leave descendants requiring inspection. [Cancellation details](docs/operations.md#owned-headless-cancellation).
- A BB fallback probe uses the local Claude/Codex CLI account, not the BB thread's login. Configure the probe account explicitly when needed; keepgoing cannot establish account equivalence from a label.
- Setup, transfer, multi-account recovery, installed-service lifecycle and menu interaction still have live verification gaps. Do not equate a compiled feature or passing fixture with a successful real-provider walkthrough.

## Documentation

- [Setup](docs/setup.md) and [configuration](docs/configuration.md): wizard, scripts, existing accounts and advanced settings.
- [Operations](docs/operations.md), [diagnostics](docs/diagnostics.md) and [services](docs/services.md): controls, failure evidence and background execution.
- [Goals](docs/goals.md), [transfer](docs/transfer.md) and [local clients](docs/local-clients.md): task metadata and optional integrations.
- [Agent chats](docs/agent-chats.md) and [BB plugin](integrations/bb-plugin-keepgoing/README.md): conversation supervision.
- [Integration evidence](docs/integrations.md), [testing](docs/testing.md) and [release verification](docs/release-verification.md): what was actually checked and what remains open.
- [Roadmap](docs/roadmap.md) and [feature analysis](docs/supervision-feature-analysis.md): priorities and checkable acceptance criteria.
- [Architecture](docs/architecture.md), [examples](examples/) and [JSON Schemas](schema/): implementation and integration contracts.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Use fictional data everywhere. Roadmap completion includes a [capability and documentation review](docs/testing.md#capability-and-documentation-review); documentation must distinguish implemented, released and live-verified behavior.

## License

[Apache-2.0](LICENSE).
