# Changelog

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
