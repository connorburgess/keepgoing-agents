# Supervising an agent chat

A chat thread in an agent workspace such as BB is a loop that a person also talks to. keepgoing can supervise one. It has to tell three situations apart, and it does so from structured status only:

| The thread is | keepgoing |
| --- | --- |
| working: status active and progress is visible | leaves it alone |
| stopped with nothing pending: status idle, no pending interaction, loop not marked awaiting user | wakes it with the loop prompt |
| waiting on a person: a pending permission prompt, question, or form, or the loop marked awaiting user | sends nothing, and reports `await_user` once |

The second row is the common failure. An agent ends its turn asking the person to run a command, and the thread sits idle until the person comes back. keepgoing turns that into one of two outcomes:

- **The agent keeps going with other work.** A loop almost always has a roadmap. The wake prompt points the agent at it, so a task needing the person blocks only itself while the agent moves to the next item.
- **The person is pinged.** When nothing else can move, the agent runs `keepgoing await-user --note "..."`. That stops the wakes and sends an `await_user` report with the note, so the person hears about it right away instead of finding out later.

## Example

```yaml
# keepgoing.yaml, kept outside any repository
version: 1
loop:
  name: example-chat
  state_dir: ~/.local/state/keepgoing/example-chat
  adapter: bb
  provider: claude-code
  group: claude-example-account
  bb: {thread: thr_example123}
  prompt_file: chat-loop-prompt.md
  progress:
    repositories: [~/src/example-app]
    stall_after: 45m
  fallbacks: fallbacks.yaml
  report:
    secret_file: ~/.config/keepgoing/example-chat.env
    events: [handoff, degraded, recovered, await_user, stop, error]
```

[examples/chat-loop-prompt.md](../examples/chat-loop-prompt.md) is a starting prompt. It asks the agent to:

- honor stop and pause;
- start from the roadmap and work the highest-priority item that does not need the person;
- treat an item that needs the person as blocking only itself;
- run `keepgoing await-user --note` only when no roadmap item can move.

## Where to run it

Run keepgoing outside the thread it supervises: a scheduler, a BB automation, or `keepgoing daemon`. A thread that runs `keepgoing tick` on itself is always active while it does so, so it never sees itself idle.

The agent inside the thread may still call the control commands, such as `keepgoing await-user` or `keepgoing pause`. Put the config path in the prompt so it knows where they are.

## What counts as waiting on a person

- **BB.** The adapter reads `bb thread interactions list --json`. An item whose status is `pending` means waiting. If that call fails, it reads the newest `system/interaction/lifecycle` events and uses the last status for each interaction id. The interaction's text is never read.
- **The process adapter.** The status command prints `"awaitingInput": true`.
- **Any adapter.** The loop was marked with `keepgoing await-user`, and only `keepgoing resume` clears it.

A pending interaction is reported once. When it is answered, the next idle tick wakes the thread as usual, and a later interaction is reported again.
