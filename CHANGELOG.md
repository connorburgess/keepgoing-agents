# Changelog

## Unreleased

### Added

- Optional `loop.limits.deadline` and `init --deadline` hold future automatic supervision at the next check at/after an RFC3339 cutoff. The awaiting-user hold persists across restarts and is visible in status; stop and existing holds retain precedence. Child-outcome polls skip providers after the cutoff. This does not cancel running turns, cap spend, or restrict explicit manual operations. Covered by offline fixtures, not live providers.

### Fixed

- Setup now describes `--wake-every` as stall-nudge spacing; idle wakes follow the check cadence. No idle scheduling behavior changed, and per-turn nudge counts remain distinct from loop-wide work limits.

## 0.1.2 (2026-11-10)

Maintenance release collecting the changes since v0.1.1. See [release verification](docs/release-verification.md) for artifact checks and outstanding live walkthroughs. Unless explicitly recorded in [integrations](docs/integrations.md), provider and service behavior is fixture-tested rather than live-verified.

### Added

- Guided `init` creates a private disabled loop outside Git checkouts, with single-agent defaults, existing headless account directories, ordered fallbacks, operating limits and generated control instructions. Noninteractive flags, versioned JSON preview/create output and bounded stdin task input support integrations without placing task text in arguments.
- `complete --note` persists an explicit completion hold across restarts. Enable does not clear it; explicit resume reopens the task. Completion is a declaration, never inferred from transcripts, and does not yet send a completion report event.
- `doctor` provides read-only text/JSON diagnostics for setup, executable discovery, structured BB identity, headless login status, saved resume metadata, runtime access and ACL metadata on macOS/Linux/Windows. Unsupported checks stay unavailable; ordinary checks make no model calls and `--offline` skips external CLI queries. Full ACL privacy evaluation and live headless session identity remain unverified.
- Status includes configured target/account-group metadata, control holds, recorded decisions/outcomes and history freshness. Built-in daemon diagnostics use a private local challenge for responsiveness, current phase, next normal check and build-label comparison. Explicit BB automation diagnostics show structured registration and run metadata without changing timers; they do not prove scheduler liveness or script-to-loop binding.
- BB child awareness suppresses wakes, recovery and target moves while a verified direct child is active, and waits conservatively when discovery cannot be verified. The daemon checks structured child outcome notices ahead of its normal interval with persisted deduplication. Verified pending child input creates an awaiting-user hold and identifies the child in a report without prompting the parent or answering the child.
- Persistent BB ownership records and explicit `--take-over` fence conflicting owners and stale daemons. Claims remain reserved when an owner's status is unknown or idle. Identity-free schedulers preserve ownership; restrictive controls remain available. Ownership takeover does not transfer a supervised thread or create threads.
- `start` validates local setup and launches foreground or acknowledged detached supervision, preserving existing holds and refusing duplicate daemons. It does not install a service or detect/replace external timers.
- `service install|status|remove` manages macOS LaunchAgents and Linux systemd user units with private recovery records, exact-definition checks, ownership protections and unchanged loop controls. Status reports registration, health and on-disk executable comparisons; Linux adds structured execution details and daemon binding. macOS identity/startup/exit diagnostics are explicitly unsupported. Live installed-service lifecycle verification and Windows service implementation remain outstanding.
- The BB plugin's `Supervise this thread` flow separates preview, disabled creation, diagnostics and explicit background start, using the shared CLI setup contract. Covered by SDK/controller/DOM fixtures; the plugin is built and installed separately from the core binary and its live setup walkthrough remains outstanding.
- Headless `claude.env` and `codex.env` select existing account environments consistently for fresh/resumed turns, probes and Codex limit snapshots. The three-subscription example uses separate quota groups; real multi-account handoff remains unverified.
- Manual probe previews (`fallbacks check --dry-run`) describe candidates, models, timeouts and invocation counts without calling providers or changing runtime state. Expected dollar cost remains unknown; manual checks are outside the scheduled daily cap.
- Codex usage limits are classified as `rate_limit` with a reset time. `codex exec --json` reports a limit only as prose, so when a Codex probe or headless turn fails without a recognized code, keepgoing reads the Codex app-server's structured `account/rateLimits/read` snapshot.

- Headless Claude Code and Codex runners on Windows. Each turn runs through a detached copy of the keepgoing binary acting as the wrapper. Stop ends the turn's process tree with `taskkill`.

- `switch_back`, off by default, returns the loop to its primary between the fallback's turns. It waits until the primary's reported reset time, or `min_after` when none was reported, and until the primary passes an exact probe. It sends a `switch_back` report. `loop.probe` sets how the primary is probed.
- A failed probe saves its raw output to `probe-failures/<candidate>/` in the state directory, keeping the newest five, so unrecognised error shapes such as a provider's usage-limit event can be mapped later. It is never read for a decision.
- A native-binary smoke check for macOS/Linux verifies guided setup, offline diagnostics, persisted holds/completion and owner conflicts using fictional loops with provider invocation traps. The release guide separates this evidence from real two-turn/resume, account handoff and installed-service acceptance.
- Publication verifies all archive checksums and runs that smoke check against the unpacked Linux amd64 archive before uploading release assets.

### Fixed

- Loaded configuration and fallback sources are checked under the tick lock before provider work. Stale, missing or unreadable snapshots withhold ticks, probes, retargeting and provider stop execution until reload/repair; diagnostic status remains available. External writers that ignore the lock and changes to referenced files are outside this guard.
- Child-input precedence fixtures no longer require a real subprocess inspection chain to finish within a 500 ms poll under race instrumentation. Separate timing and cancellation tests retain coverage without changing production deadlines or at-most-once event consumption.
- Installation instructions now distinguish v0.1.1 artifacts and matching examples from the v0.1.2 quick start, with an explicit revision-labeled development build path.

## 0.1.1 (2026-11-10)

### Added

- `keepgoing status --json` includes `primaryAgent` and `targetAgent`, with the adapter, provider, model, and BB thread or working directory, so tools can match a loop to the session it supervises.
- A thin BB plugin in `integrations/bb-plugin-keepgoing`. It shows keepgoing's state in a supervised thread's context banner or context card, with Pause and Resume, and adds `bb keepgoing status`, `pause`, `resume`, and `stop`.

## 0.1.0 (2026-11-10)

### Fixed

- The BB adapter read the oldest 60 log events instead of the newest, because `bb thread log --limit` returns events oldest first. On a long thread it missed current errors, rate limits, and interactions. It now keeps a cursor and reads only new events after a one-time full pass.
- A stop request is now honored while the loop is paused, disabled, or awaiting a person, and when state files cannot be read.
- The daemon now acts on a stop or pause written during a pass, instead of waiting a full interval.
- Relative `state_dir` and command paths now resolve against the config file, so headless runners and `./script.sh` commands work from any directory.
- `retarget` now applies the target agent's model and reasoning level.
- A handoff brief that cannot be written no longer leaves a candidate's model switched.
- A failed manual or exact probe no longer delays the next scheduled probe. The active target's quota group is no longer probed on schedule.
- A gone target now marks the loop awaiting user instead of staying silent.
- Time zones now load on Windows, and the network probe command works there.

### Added

- BB threads with a pending permission prompt, question, or form count as waiting on a person. keepgoing sends nothing to them and reports `await_user` once.
- `keepgoing await-user` sends an `await_user` report at once, with the note.
- The process adapter's status object accepts `awaitingInput`.
- `progress.max_nudges`, default 3: a session that stays stalled through that many nudges marks the loop awaiting user instead of being nudged forever.
- `docs/agent-chats.md` and `examples/chat-loop-prompt.md` for supervising a chat thread.
- `probe.env` for probing a candidate that uses a different account.
- `docs/integrations.md` and `docs/testing.md`, and a test bundle for machines without Go.
- Supervision of one loop through BB threads, generic processes, and headless Claude Code or Codex sessions.
- A decision engine that wakes idle sessions, nudges stalled ones, and re-prompts errors with backoff, then waits for a person.
- `fallbacks.yaml` with scheduled, exact, and manual probes, a health registry, redundancy rules, and handoff to confirmed fallbacks.
- Structured BB limit detection from provider-error and rate-limit events, including waiting out a reset that is near.
- The daemon mode, the control commands, `retarget`, and `status`.
- Signed webhook reports in Standard Webhooks or bearer format.
- The `ci-wait` and `net-retry` helpers.
- JSON Schemas, examples, and scheduler setups.
