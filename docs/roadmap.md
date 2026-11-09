# Roadmap

## Done

**Milestone 1: supervise one loop.** Config validation, the pure decision engine, BB and process adapters, stall detection, time windows, the tick lock, control commands, and JSON tick output.

**Milestone 2: confirmed fallbacks.** `fallbacks.yaml`, headless Claude Code and Codex runners, three probe tiers with a daily cap, the health registry, redundancy across providers or groups, and handoff with an exact probe. It also covers model switching on BB threads, the generated brief with a file override, and manual retarget. BB limit detection uses structured provider-error and rate-limit events, and the loop waits for a reset when it is near.

**Milestone 3: operate it.** The daemon with immediate control handling and config reload. Signed webhook reports in the Standard Webhooks format, checked against its published test vector. `ci-wait`, `net-retry`, yield checks, scheduler examples, JSON Schemas with a drift test, Windows locking, and release automation.

## Next

- Check the Codex adapter against a real Codex usage-limit event and map its structured fields.
- A thin BB plugin that calls the CLI and renders its JSON.
- Optional automatic switch-back to the primary once its limit resets and an exact probe passes. Off by default.
- Headless runners on Windows.
- Homebrew formula once releases are public.
