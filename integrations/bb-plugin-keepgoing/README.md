# keepgoing for BB

A thin [BB](https://getbb.app) plugin for [keepgoing](../../README.md). It shows keepgoing's state on every thread a loop supervises, with Pause/Resume controls, guided setup and explicit held transfers. It also adds a `bb keepgoing` command. All decisions stay in keepgoing; the plugin only runs the `keepgoing` CLI and renders its JSON.

## Install

Use **CLI v0.1.5 + plugin v0.2.0** for goal-aware setup. Verify that the [v0.1.5 tag and release](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.1.5) are available before using this pinned installation command:

```sh
bb plugin install git:https://github.com/connorburgess/keepgoing-agents.git@v0.1.5 --plugin keepgoing
bb plugin config keepgoing set keepgoingPath /absolute/path/to/keepgoing
```

Confirm BB's displayed source/tag before installation. The repository collection selects this package; BB installs production dependencies and builds its server/app/host entries. It needs Git and network access, but not a separate global npm install. The command does not install the keepgoing CLI, configure a loop or start supervision. New UI-created loops need no manual `configs` entry. Existing managed installs use `bb plugin outdated` and `bb plugin update`; a pinned tag remains pinned, so inspect the recorded source and update policy rather than expecting it to follow new tags. Local path development installs use the path/build workflow below. No live Git installation or installed-plugin acceptance is claimed yet.

For a local source build:

```sh
cd integrations/bb-plugin-keepgoing
npm install
bb plugin build
bb plugin install .
bb plugin config keepgoing set keepgoingPath /absolute/path/to/keepgoing
bb plugin config keepgoing set configs "/absolute/path/to/keepgoing.yaml"
```

`configs` takes one existing server-local `keepgoing.yaml` path per line. Those loops and setups created through the UI are shown and controlled. UI-created setups are registered separately with their host and configuration path in private plugin storage. A path sent from the browser is checked against this list, so the plugin never runs keepgoing against an arbitrary file. The plugin needs a keepgoing build whose `status --json` includes `primaryAgent` and `targetAgent`, which is any build after 0.1.0.

## Supervise this thread

This flow is implemented on `main` and requires a keepgoing build containing `init --non-interactive --json` and stdin task input (after v0.1.1). The plugin must be rebuilt/reloaded to expose the UI and its host entry. The configured keepgoing executable and BB CLI must be available on each selected host; a bare binary name uses that host's PATH. Setup does not install tools or change logins.

1. Open **Supervise this thread** in the context banner, or its button in the context card. Enter a loop name and task with completion criteria. The **Record a goal for this task** checkbox starts selected; it uses the loop name as the initial title and shares current goal references in later prompts. Goal setup needs CLI v0.1.5 with `init --with-goal`; update the selected host binary or explicitly deselect the choice for the existing task-only flow. The plugin never silently falls back or upgrades the CLI.
2. Choose **Preview setup**. The server resolves the existing thread, provider, ready Git checkout, and connected host from BB metadata. Review the displayed project, target, configuration path, and operating limits. The initial UI uses one agent and default limits with no fallbacks; advanced setup remains available through the CLI.
3. Choose **Create disabled setup**. The host runs the shared `init` command, with task text on stdin. For goal setup, one private `task.md` snapshot is referenced by goal revision 1 while `prompt.md` holds stable wake instructions. Configuration and prompt files go into the CLI's new private setup directory outside the checkout. No agent, probe, scheduler, or account is started or created.
4. Review the shared doctor results. **Start background supervision** appears only when the saved target matches, BB identity diagnostics pass without errors, no control hold prevents start, and no responding daemon was found. Clicking it calls the existing `start --background` command on the recorded host. It enables supervision with the thread's ownership identity; it does not take over another owner or clear holds. Subsequent ticks may prompt the agent normally.

The **Setup** control / expanded banner can refresh diagnostics later. Pause/resume/stop route to the recorded host. Resume explicitly clears holds; it does not start a missing daemon. New disabled setups use the separate start action instead of a misleading Resume button. Stop/completion behavior remains in keepgoing, with no separate loop skill required.

The setup service never spawns, forks, re-parents, sends to, or changes the provider/model of a BB thread. Preview tickets expire after five minutes or plugin reload, are bound to the task hash, goal choice, target metadata, binary selection and resolved plan, and permit one creation attempt. Task text is not persisted in plugin storage, logged, or put in subprocess arguments; it remains in private `task.md` for goal setup or `prompt.md` for task-only setup after creation. Task input is limited to 256 KiB in the UI flow. Paths and target references in private plugin storage are operational metadata.

Existing configured supervision or a failed status lookup blocks creating another loop through this flow. This only covers loops known to the plugin, not arbitrary timers/configurations elsewhere. Setup does not detect or replace an external timer. Do not start a second supervisor for a loop already run by another schedule.

### Recovery and verification limits

- Failed/expired previews require a new preview. Changing task text or name clears the UI preview. Target/provider/environment changes require a new setup review; existing loops are never silently transferred.
- Creation and saving the plugin reference are not atomic. If reference persistence fails, the UI retains the created config path for recovery. Inspect that disabled setup using the CLI on its host; do not create or start another automatically. A command timeout or lost response can also leave a disabled directory; existing destinations are never overwritten.
- A failed start response can mean startup was not confirmed, not that nothing started. Refresh diagnostics before retrying. A responsive daemon prevents another start; the CLI's daemon/tick locks and ownership checks provide the final guard.
- Thread/config checks are point-in-time observations. Manual edits and external scheduling changes are not serialized by the plugin. Keep configurations unchanged while creating/starting a loop. Full model/account validation remains unimplemented; explicit transfers use the recovery protocol below.
- The plugin retains at most 32 UI-created setup references. There is no UI deletion/import operation yet; CLI-created configurations use the existing server-local setting. Identical loop names on different hosts are ambiguous to `bb keepgoing <action> <name>` and are refused; use the thread's controls instead.

Verification uses SDK fake-host/RPC tests, local subprocess fixtures, controller tests, DOM interaction tests, typechecking, and plugin bundle builds. Tests cover host routing, preview changes/replay/concurrency, holds, failed diagnostics, reload, privacy, and the separate preview/create/start clicks. No live BB thread, remote host deployment, paid request, account handoff, or end-to-end install-to-completion walkthrough was exercised. The existing conversation timer was not changed.

## Keep going from the New thread screen

Unreleased source, tested only with BB's SDK test hosts and stand-ins. It has not been run in a real BB, so nothing below is live-verified. It uses BB plugin APIs that BB marks experimental (`composer.submit`'s `experimental_data`, `bb.experimental_hooks` and its `experimental_submission`); they can change between BB versions.

The New thread composer gets a **Keep going** switch in its action row, and a **Keep going…** row in the menu beside Send for the compact layout, where BB draws no action row. Turning it on shows a banner that says what will happen and holds one button, **Send and keep going**. That button is the send that asks for supervision. A plain Enter sends the message without supervision; the new thread then shows the ordinary **Supervise this thread** offer.

What happens after **Send and keep going**:

1. The plugin asks its server for a single-use request (valid for two minutes, one per draft) and sends the message through BB's own composer with that request attached. BB creates the thread and delivers the message as usual.
2. BB hands the request back to the plugin's `message.dispatch` hook together with the thread it created. That is the only link the plugin accepts. It never guesses from project, time or text. If the request is unknown, expired, already used, or does not describe a person's first message on a thread created after the switch was turned on, nothing is created, and the thread says so.
3. Once the thread's environment and host are ready, the plugin runs the same setup as **Supervise this thread**: preview, then create. The first message is the task (recorded as the loop's goal, on stdin), and the loop is named after the thread's title, or `bb-<thread id>` when the title gives no usable name or that name is taken.
4. Supervision starts, once, when setup's own diagnostics allow it. Pressing **Send and keep going** is the consent to that start. The thread's banner or card then shows `keepgoing: keeping this going`, the loop's limit, and **Pause** and **Stop**.

If anything about the start is uncertain, the setup is left switched off and the thread shows `keepgoing: ready, not started`, the reason, and one button, **Start keeping this going**. That covers diagnostics that report an error, a start that could not be confirmed, and a hold or a responding supervisor already present. A start is never tried a second time by the plugin.

Every loop created this way has a loop-wide limit: it stops for a person after 40 automatic messages, or after 24 hours without progress, which keepgoing measures as a Git change. The numbers are defined once, in `new-thread-contract.ts`, and both can be changed under **Details** in the banner before sending (1 to 1000 messages; 2, 6, 12, 24 or 72 hours). The plugin refuses a setup whose CLI report does not carry the requested limits.

The setting **Keep going from the New thread screen** has two values. *Start automatically* is the default and is described above. *Review first; I start it* always creates the setup switched off and leaves the start to the button on the thread; the banner says which applies before you send. Automatic start happens only if that mode was shown when the switch was turned on and is still set when the thread is ready.

When it cannot go ahead, it says why and does nothing else:

- The keepgoing program does not run on the BB server: the switch stays off and the banner explains. This check runs on the BB server, not on the host a new thread will use; a host without keepgoing is reported on the thread, after creation.
- Another loop already supervises the thread, or that could not be checked: nothing is created or started.
- The thread does not work in a Git repository, or its environment is not ready within ten minutes: nothing is created.
- The message has no text, or more than 256 KiB.

Limits of this flow:

- The plugin never creates, sends to, re-parents or changes a thread. BB creates the thread; the plugin only learns of it.
- The task text is held in the plugin server's memory from the send until the setup is created (at most ten minutes), and is never written to plugin storage, logged, or put in subprocess arguments. Reloading the plugin in that window drops the request; the thread then shows the ordinary offer.
- The plugin's dispatch hook runs for every message BB dispatches and always answers "proceed". It does not delay, queue or reject a send.
- There is no keyboard shortcut for **Send and keep going**.
- The limits and the cadence shown on the thread after creation are kept in memory; after a plugin reload the thread shows the ordinary status row and **Setup** panel instead.

## Move supervision here

Implemented on `main` after v0.1.3 and fixture-tested. The transfer CLI is released in v0.1.4; plugin v0.2.0 is distributed through the separate tagged Git installation above. Both the plugin and the host CLI need the transfer build; rebuild the plugin’s app/host bundles before installing an update. Older CLIs are rejected when the preview does not advertise apply support.

1. Open the destination thread’s keepgoing banner or **Supervise this thread** / **Setup** card panel. Under **Move supervision here**, choose an existing plugin-created loop in the same project, environment, host, workspace and provider.
2. Click **Preview move**. Review the old/new threads, provider, current owner, all control flags and any goal ID/revision. Model/account identity remains unverified. Five-minute tickets are single-use and expire on reload; changed target metadata, controls, goal or configured labels require a new preview.
3. Click **Move and keep held**. This explicitly authorizes destination ownership takeover. The CLI preserves existing holds, goals and work accounting, adds awaiting-user, resets thread observations and fences stale configs/daemons. It does not cancel the old thread’s running turn, send work, create/re-parent a thread or change a timer.
4. Once status verifies the held destination, the plugin updates its saved reference. Review **Setup** diagnostics. **Resume** is a separate explicit action that clears holds; it does not start a missing daemon. A Resume click from a stale view of the source thread is refused. A disabled loop uses its separate start action after holds are resolved. Never resume a completed task or a task still waiting on a person.

Only plugin-created setups appear. Manually listed configurations still use `keepgoing transfer preview|apply|recover` on their host; importing them into this UI remains planned. A duplicate manual reference to the selected setup blocks the UI move. Known destination supervision or unverified status blocks another loop; arbitrary external timers/configurations are not discovered.

### Interrupted moves

A private saved request is written before calling apply. It retains the original registration, destination metadata and binary selection, with no prompt, control note, credentials or raw command output. While it exists, the banner says **move requires inspection** and plugin start/resume are refused. Pause/stop remain available through the commands and are not queued behind a slow transfer.

After a timeout, lost reply, plugin reload or persistence failure, open the destination, select the pending loop and choose **Check move status**. If the CLI already completed and still verifies the expected held destination/owner, this repairs the plugin reference without replaying apply. Otherwise **Recover move** is an explicit action: it resumes the observed CLI pending ID, or revalidates and retries the saved source/destination request if the CLI had not begun. It always leaves holds intact. There is no automatic retry or recovery timer.

CLI mutation, plugin reference update and saved-request removal are separate writes. Failures retain a recovery path; a completed move can coexist with an old plugin reference until reconciled. Changed metadata/binary, conflicting registrations, an externally cleared hold, malformed records or an unrelated CLI move require private inspection on the host. Do not delete recovery records or replay moves blindly. There is no cancel/import UI in this first scope, and the plugin queue does not serialize external writers or other plugin instances. Point-in-time preview reads do not replace the CLI’s final checks.

Verification uses SDK fake-host/host-entry tests, controller failure fixtures and DOM interactions, plus typechecking and bundle builds. A local check also exercised the actual built CLI through the plugin controller/host wrapper with fake BB metadata, verifying destination registration and preserved holds. The CLI’s own process-crash and new-target-only wake fixtures cover runtime transfer behavior. Live BB transfer, remote deployment, model/account identity and end-to-end migration readiness remain unverified. No existing timer, installed plugin or supervised thread was changed during these tests.

## What it shows

BB shows thread context in one of two places. The plugin uses whichever one is showing:

- **The context banner above the message box.** This is the default. A `keepgoing: …` item sits at the right of the banner. Click it to expand the loop diagnostics and a Pause or Resume button.
- **The thread context card** at the top right of a thread page. This shows when BB's thread context card experiment is on. The keepgoing row sits in the card's lower group.

The rebuilt plugin also shows recorded goal identity/revision in setup diagnostics and expanded status. Goal titles, task text and references are withheld; metadata is not verified completion. Missing fields say “not reported” (no record or an older CLI), malformed records are unavailable, and unavailable goal metadata prevents a new start through setup. Editing the goal choice invalidates its preview. The original task source and edits to referenced files are not revision-tracked; see [goal setup](../../docs/setup.md#optional-goal-during-setup-unreleased). This flow and display are fixture-tested; live deployment and first-loop verification remain open.

Both read:

- `keepgoing: enabled`: the loop is enabled and this thread is its current target. This label alone does not prove a supervisor is running.
- `keepgoing: watching <thread or target>`: the current target is elsewhere.
- `keepgoing: paused`, `stop requested`, `awaiting user`, `completed`, or `disabled`: the loop's control state.

The expanded banner and card hover show the actual target thread (or adapter for non-BB targets), whether this is that thread, declared completion even under another hold, runtime evidence source/freshness, last recorded action/outcome/target/time, and the existing note/send/fallback summary. A completion declaration is not independently verified task success. Failed and dry-run actions remain labeled; missing history and zero timestamps are unavailable, never proof that nothing was sent.

A responsive daemon reports its challenge timestamp, phase, expected next check, dry-run flag and build comparison. These are snapshots at the displayed status-read time, refreshed roughly every 30 seconds while mounted and on plugin change events. Neither a response nor an enabled label proves current agent health or future execution. External schedulers are not inspected. No recorded/unavailable daemon endpoint does not prove there is no external scheduler or older daemon. Older CLI builds can still supply basic status; missing diagnostic fields are explicitly unavailable.

RPC failures retain any previous snapshot with a warning; returned lookup errors also show the warning. Controls and setup are hidden until a successful refresh; **Refresh status** retries the read without applying controls. Open setup/move panels remain mounted while hidden so an in-flight result is not discarded. Command-line pause/stop remain available for private recovery. A failure for any registered loop makes the view incomplete, including errors in a manually listed loop that could not be matched to a thread.

These richer diagnostics are implemented after v0.1.3 and require rebuilt plugin bundles. SDK status-projection and DOM fixtures cover target changes, completion under holds, failed/dry-run/stale/missing evidence, legacy builds, rejected refreshes and recovery. Typechecking and bundle builds pass. Live banner/card layout, installed-plugin refresh and remote deployment remain unverified.

## Commands

```sh
bb keepgoing status
bb keepgoing pause <loop>
bb keepgoing resume <loop>
bb keepgoing stop <loop>
```
