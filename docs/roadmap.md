# Roadmap

## Done

**Milestone 1: supervise one loop.** Config validation, the pure decision engine, BB and process adapters, stall detection, time windows, the tick lock, control commands, and JSON tick output.

**Milestone 2: confirmed fallbacks.** `fallbacks.yaml`, headless Claude Code and Codex runners, three probe tiers with a daily cap, the health registry, redundancy across providers or groups, and handoff with an exact probe. It also covers model switching on BB threads, the generated brief with a file override, and manual retarget. BB limit detection uses structured provider-error and rate-limit events, and the loop waits for a reset when it is near.

**Milestone 3: operate it.** The daemon with immediate control handling and config reload. Signed webhook reports in the Standard Webhooks format, checked against its published test vector. `ci-wait`, `net-retry`, yield checks, scheduler examples, JSON Schemas with a drift test, Windows locking, and release automation.

**Verification pass.** The test suite was run on Ubuntu 26.04 ARM64 and Windows 11 Arm VMs. An independent audit of the docs against the code fixed these:

- Stop now wins even while the loop is paused or disabled.
- The daemon now notices a control change made during a pass.
- Relative config paths now work with headless runners and commands.
- Retarget now applies the target's model.
- A brief error no longer half-switches a candidate.
- Failed manual probes no longer delay scheduled probes.

See [integrations.md](integrations.md) for what is verified live.

**Agent chats.** BB pending interactions mean waiting on a person. `await-user` reports at once. A guide covers supervising a live chat thread.

## Next

- Supervise a real working chat thread end to end with the daemon and a webhook, as the first live proof.
- Run the live checks listed in [integrations.md](integrations.md), starting with BB send and stop against a scratch thread.
- Check the Codex adapter against a real Codex usage-limit event and map its structured fields.
- Escalate a session that stays stalled after several nudges.
- A thin BB plugin that calls the CLI and renders its JSON.
- Optional automatic switch-back to the primary once its limit resets and an exact probe passes. Off by default.
- Headless runners on Windows.
- Homebrew formula once releases are public.
