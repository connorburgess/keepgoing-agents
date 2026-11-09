# Changelog

## Unreleased

### Added

- Supervision of one loop through BB threads, generic processes, and headless Claude Code or Codex sessions.
- A decision engine that wakes idle sessions, nudges stalled ones, and re-prompts errors with backoff, then waits for a person.
- `fallbacks.yaml` with scheduled, exact, and manual probes, a health registry, redundancy rules, and handoff to confirmed fallbacks.
- Structured BB limit detection from provider-error and rate-limit events, including waiting out a reset that is near.
- The daemon mode, the control commands, `retarget`, and `status`.
- Signed webhook reports in Standard Webhooks or bearer format.
- The `ci-wait` and `net-retry` helpers.
- JSON Schemas, examples, and scheduler setups.
