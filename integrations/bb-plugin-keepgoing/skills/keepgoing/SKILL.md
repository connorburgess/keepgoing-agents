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

Loops listed in the plugin's "Loop configs" setting and UI-created setups are shown or controlled. UI-created setups retain their host identity; existing configured paths remain server-local.

The context banner/card offers "Supervise this thread": enter a task, preview the resolved target/limits, create a disabled private setup, review doctor diagnostics, then explicitly start background supervision. This uses shared keepgoing commands on the thread's recorded host; it never creates or transfers a thread. It requires a keepgoing build with structured init and stdin task support. No separate loop skill is required.

A failed or lost creation response may leave a disabled directory. Inspect the displayed config path on its host instead of automatically retrying creation. A failed start response requires refreshed status before retrying. Other timers are not detected or changed. Task text goes through stdin and is never logged or stored in plugin settings. Existing holds and ownership remain in keepgoing; resume is explicit. Full account/model validation and thread transfer are not implemented. The UI workflow is fixture-tested; live remote-host and install-to-completion verification remain outstanding.

When you are the agent in a supervised thread and nothing on your task list can move without the person, prefer the loop's own command, `keepgoing await-user -c <config> --note "..."`. It pings the person and holds the loop until they resume it.
