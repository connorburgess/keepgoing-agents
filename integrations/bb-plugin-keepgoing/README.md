# looptender for BB

**keepgoing is being renamed to looptender.** From plugin v0.3.1 (on `main`, not yet in a release) BB shows this plugin as looptender, and its labels and messages say looptender. Everything you type is unchanged for now: the plugin id (`keepgoing`), the `bb keepgoing` command, the `keepgoingPath` setting, the `keepgoing` executable and `keepgoing.yaml`. Updating keeps the plugin's settings, secrets and schedules, because BB keeps them under the plugin id. The rest of this page still says keepgoing where it means the tool or something you type.

A thin [BB](https://getbb.app) plugin for [keepgoing](../../README.md). It shows keepgoing's state on every thread a loop supervises, with Pause/Resume controls, guided setup and explicit held transfers. It also adds a `bb keepgoing` command. All decisions stay in keepgoing; the plugin only runs the `keepgoing` CLI and renders its JSON.

The plugin reports its version (`--client bb-plugin@VERSION`, from `package.json`) when it reads a loop's status, so the keepgoing panel's Versions view can say which plugin and which keepgoing build it runs. A keepgoing too old to know the flag is asked again without it.

## Install

Use **CLI v0.2.0 + plugin v0.3.0** (its labels say keepgoing). Verify that the [v0.2.0 tag and release](https://github.com/connorburgess/keepgoing-agents/releases/tag/v0.2.0) are available before using this pinned installation command:

```sh
bb plugin install git:https://github.com/connorburgess/keepgoing-agents.git@v0.2.0 --plugin keepgoing
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

## Move supervision here

Implemented on `main` after v0.1.3 and fixture-tested. The transfer CLI is released in v0.1.4; plugin v0.2.0 and later are distributed through the separate tagged Git installation above. Both the plugin and the host CLI need the transfer build; rebuild the plugin’s app/host bundles before installing an update. Older CLIs are rejected when the preview does not advertise apply support.

1. Open the destination thread’s looptender banner or **Supervise this thread** / **Setup** card panel. Under **Move supervision here**, choose an existing plugin-created loop in the same project, environment, host, workspace and provider.
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

- **The context banner above the message box.** This is the default. A `looptender: …` item sits at the right of the banner. Click it to expand the loop diagnostics and a Pause or Resume button.
- **The thread context card** at the top right of a thread page. This shows when BB's thread context card experiment is on. The looptender row sits in the card's lower group.

The rebuilt plugin also shows recorded goal identity/revision in setup diagnostics and expanded status. Goal titles, task text and references are withheld; metadata is not verified completion. Missing fields say “not reported” (no record or an older CLI), malformed records are unavailable, and unavailable goal metadata prevents a new start through setup. Editing the goal choice invalidates its preview. The original task source and edits to referenced files are not revision-tracked; see [goal setup](../../docs/setup.md#optional-goal-during-setup). This flow and display are fixture-tested; live deployment and first-loop verification remain open.

Both read:

- `looptender: enabled`: the loop is enabled and this thread is its current target. This label alone does not prove a supervisor is running.
- `looptender: watching <thread or target>`: the current target is elsewhere.
- `looptender: paused`, `stop requested`, `awaiting user`, `completed`, or `disabled`: the loop's control state.

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

## License

This plugin is part of keepgoing and is source-available under the [Sustainable Use License 1.0](../../LICENSE); see the [licensing guide](../../LICENSING.md). The plugin at the `v0.1.5` tag, like every release through v0.1.5, remains under Apache-2.0.
