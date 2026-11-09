# Roadmap

## Milestone 1: supervise one loop (done)

- `keepgoing.yaml` with validation and defaults
- pure decision engine with table tests
- BB thread and generic process adapters
- stall detection from Git metadata, time windows, advisory lock
- control plane: enable, disable, pause, resume, stop, await-user
- `tick` for schedulers, JSON output, `runs.jsonl`

## Milestone 2: confirmed fallbacks

- `fallbacks.yaml`: ordered candidates, each a tool, account, and model
- Claude Code and Codex CLI adapters using headless JSON output and exit codes
- two probe tiers: a cheap scheduled probe with a daily cap, and an exact-target probe right before any handoff
- health registry with time-to-live; a candidate counts only when its last probe is fresh
- redundancy rules: a minimum healthy count that must span providers; below it, warn and refuse new handoffs
- handoff only for classified errors, with a generated brief that a user-supplied goal file overrides

## Milestone 3: operate it

- `daemon` mode with a PID lock and immediate stop on `SIGTERM`
- signed webhook reports using the Standard Webhooks scheme
- `ci-wait` pinned to an exact head SHA that follows a replacement run after a cancellation
- `net-retry` for network commands that should resume rather than fail
- yield to other jobs, example schedulers, first tagged release with binaries

## Later

- a thin plugin for agent IDEs that calls the CLI and renders its JSON
- optional switch-back to the primary once it is healthy again, off by default
