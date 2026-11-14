---
name: keepgoing
description: Show or control the keepgoing loops that supervise BB threads, with bb keepgoing status, pause, resume, and stop.
---

# keepgoing in BB

keepgoing supervises agent loops: it wakes an idle thread, nudges a stalled one, and hands off to a confirmed fallback on a usage limit. This plugin shows its state in each supervised thread's context card and exposes these commands:

```sh
bb keepgoing status           # every configured loop: control state, target, last state
bb keepgoing pause <loop>     # stop waking the thread until resumed
bb keepgoing resume <loop>    # clear pause, stop, and awaiting-user; on a loop held for spent re-prompts or nudges those counts start over
bb keepgoing stop <loop>      # stop the running turn, then send nothing more
```

Loops listed in the plugin's "Loop configs" setting and UI-created setups are shown or controlled. UI-created setups retain their host identity; existing configured paths remain server-local.

The context banner/card offers "Supervise this thread": enter a task, preview the resolved target/limits, create a disabled private setup, review doctor diagnostics, then explicitly start background supervision. This uses shared keepgoing commands on the thread's recorded host; it never creates or transfers a thread. It requires a keepgoing build with structured init and stdin task support. No separate loop skill is required. The explicit **Record a goal for this task** choice requires a post-v0.1.4 CLI with `init --with-goal`; task-only setup remains available by deselecting it. Previews bind this choice and never silently omit a requested goal. Goal setup saves one private snapshot, records a goal using the loop name as its initial title, and shares current goal references in later prompts. The agent must have permission to read the snapshot; setup does not grant access.

A failed or lost creation response may leave a disabled directory. Inspect the displayed config path on its host instead of automatically retrying creation. A failed start response requires refreshed status before retrying. Other timers are not detected or changed. Task text goes through stdin and is never logged or stored in plugin settings. Existing holds and ownership remain in keepgoing; resume is explicit. Full account/model validation is not implemented. The UI workflow is fixture-tested; live remote-host and install-to-completion verification remain outstanding.

When you are the agent in a supervised thread and nothing on your task list can move without the person, prefer the loop's own command, `keepgoing await-user -c <config> --note "..."`. It pings the person and holds the loop until they resume it.

On BB's New thread screen (unreleased, not live-verified) the composer has a **Keep going** switch. With it on, **Send and keep going** sends the first message as the task and, once the thread exists, creates a setup for that thread with a loop-wide limit (40 automatic messages or 24 hours without a Git change unless changed under Details) and starts supervision once. If the start is uncertain the setup stays switched off and the thread shows the reason and a **Start keeping this going** button; the plugin never retries a start. The loop is named after the project with a random suffix, never after the message. A plain Enter sends without supervision and turns the switch off. A send BB rejects never leads to supervision. The plugin holds at most 32 setups and cannot remove one yet. The plugin setting "Keep going from the New thread screen" can be set to review first, which always leaves the start to that button. The plugin never creates or messages a thread, and attaches supervision only to the thread BB reports for that exact send.

The context panel also offers **Move supervision here** for plugin-created loops in the same BB environment/provider. Select a loop, preview both targets/holds/owner, then explicitly choose **Move and keep held**. This authorizes ownership transfer through the host CLI; it never resumes automatically or changes a timer. It requires a post-v0.1.3 transfer build and updated plugin bundles. Manually configured loops use the CLI transfer commands; import remains unavailable.

A saved move request blocks start/resume until reconciled. After a lost reply or reload, choose **Check move status** from the destination: a verified held destination repairs only the plugin reference. **Recover move** explicitly continues a pending CLI ID, or revalidates/retries the saved request if no CLI move began. Pause/stop remain independent. Unknown or changed host/config metadata requires private CLI inspection; never delete recovery records or repeat apply blindly. Only fixture verification is claimed.

The banner/card diagnostics show recorded goal ID/revision (not private title/references), declared completion, current target thread, timestamped daemon/check evidence and the last recorded action/outcome. Treat these as snapshots, not proof of current agent health or future execution. External schedulers are not inspected; older builds may omit diagnostic fields. Failed refreshes show a warning and hide UI actions until status is refreshed, retaining a prior snapshot only as historical evidence. Use CLI pause/stop for recovery when UI status is unavailable. These diagnostics require rebuilt plugin bundles and are fixture-tested, not live deployment verification.
