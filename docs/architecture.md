# Architecture

`keepgoing` is a supervisor, not an agent. It sits beside a running agent session and answers one question on every tick: should anything be sent, stopped, or escalated right now?

## Rules that do not bend

- **Never edit repositories.** The tool reads Git metadata to detect progress. It never writes to a checkout, commits, pushes, or merges.
- **Structured signals only.** Session state and error class come from exit codes, JSON fields, HTTP status, or a provider's status command. Transcript or log text is never parsed for decisions. Anything unclassified is `unknown`.
- **Unknown is conservative.** An unknown state means no action. An unknown error class means a bounded re-prompt and then escalation to a person, never a provider switch.
- **Stop and pause win.** They are evaluated first on every tick. Paused or disabled means the provider is not even queried.
- **Missing control means disabled.** A tick must never wake an agent nobody asked for.
- **Pure decisions.** The decision engine is a function from inputs to one action. Every rule has a table test. Side effects live in adapters and the command layer.

## Packages

```
cmd/keepgoing          CLI: tick, status, validate, and the control commands
internal/config        keepgoing.yaml loading, defaults, validation
internal/decide        the pure decision engine
internal/supervise     one tick: observe, decide, act, record
internal/adapter       the Adapter interface and shared code-to-class mapping
internal/adapter/bb    BB thread adapter over the bb CLI's JSON
internal/adapter/process  generic three-command adapter
internal/control       control.json: enabled, paused, stop, awaitingUser
internal/state         state.json, runs.jsonl, and the progress fingerprint
internal/window        weekly time windows
internal/lock          advisory lock so ticks never overlap
```

## A tick, end to end

1. Acquire the state directory lock. If another tick holds it, exit 75 and do nothing.
2. Load `control.json` and `state.json`.
3. If stop is requested and the session is running, call the adapter's stop once. Record it. Done.
4. If disabled, paused, or awaiting user, record and exit without touching the provider.
5. Ask the adapter for an observation: state, error class, queued messages, background workers, gone.
6. Compute the progress fingerprint if repositories are configured, and compare with the last tick.
7. Check the time window.
8. Run the decision engine.
9. Act: send the prompt (auto mode for wake and re-prompt, queue mode for a nudge), flip `awaitingUser`, or stop.
10. Save state, append an event to `runs.jsonl`, print the result as one JSON line.

## Adapters

An adapter implements four methods: `Name`, `Observe`, `Send`, and `Stop`. `Observe` must return a structured observation. If a provider cannot express an error class, the adapter returns `unknown`. Adapters that cannot stop a session return an unsupported error; the supervisor then records a warning and simply sends nothing more.

The shared `ClassifyCode` function maps well-known tokens and HTTP statuses to error classes. It is deliberately strict: a code with spaces in it is prose and classifies as unknown.

## State directory

Everything the tool writes lives in `state_dir`, outside any repository:

| File | Owner | Purpose |
| --- | --- | --- |
| `control.json` | operator, via the control commands | enabled, paused, stop, awaitingUser, note |
| `state.json` | the tool | fingerprint, last progress, last send, re-prompt budget |
| `runs.jsonl` | the tool | append-only events, one JSON object per line |
| `tick.lock` | the tool | advisory lock, released by the kernel on exit |

## What comes next

See [roadmap.md](roadmap.md). The next milestone adds `fallbacks.yaml`, probes, a health registry, and handoff between providers, as a separate step that only runs for classified errors against a fallback that was confirmed healthy moments before.
