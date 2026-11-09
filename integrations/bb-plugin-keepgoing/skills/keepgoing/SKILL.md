---
name: keepgoing
description: Show or control the keepgoing loops that supervise BB threads, with bb keepgoing status, pause, resume, and stop.
---

# keepgoing in BB

keepgoing supervises agent loops: it wakes an idle thread, nudges a stalled one, and hands off to a confirmed fallback on a usage limit. This plugin shows its state in each supervised thread's context card and exposes these commands:

```sh
bb keepgoing status           # every configured loop: control state, target, last state
bb keepgoing pause <loop>     # stop waking the thread until resumed
bb keepgoing resume <loop>    # clear pause, stop, and awaiting-user
bb keepgoing stop <loop>      # stop the running turn, then send nothing more
```

Only loops listed in the plugin's "Loop configs" setting are shown or controlled.

When you are the agent in a supervised thread and nothing on your task list can move without the person, prefer the loop's own command, `keepgoing await-user -c <config> --note "..."`. It pings the person and holds the loop until they resume it.
