# keepgoing for BB

A thin [BB](https://getbb.app) plugin for [keepgoing](../../README.md). It shows keepgoing's state on every thread a loop supervises, with Pause/Resume controls and a guided setup flow. It also adds a `bb keepgoing` command. All decisions stay in keepgoing; the plugin only runs the `keepgoing` CLI and renders its JSON.

## Install

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

1. Open **Supervise this thread** in the context banner, or its button in the context card. Enter a loop name and task with completion criteria.
2. Choose **Preview setup**. The server resolves the existing thread, provider, ready Git checkout, and connected host from BB metadata. Review the displayed project, target, configuration path, and operating limits. The initial UI uses one agent and default limits with no fallbacks; advanced setup remains available through the CLI.
3. Choose **Create disabled setup**. The host runs the shared `init` command, with task text on stdin. Configuration and prompt files go into the CLI's new private setup directory outside the checkout. No agent, probe, scheduler, or account is started or created.
4. Review the shared doctor results. **Start background supervision** appears only when the saved target matches, BB identity diagnostics pass without errors, no control hold prevents start, and no responding daemon was found. Clicking it calls the existing `start --background` command on the recorded host. It enables supervision with the thread's ownership identity; it does not take over another owner or clear holds. Subsequent ticks may prompt the agent normally.

The **Setup** control / expanded banner can refresh diagnostics later. Pause/resume/stop route to the recorded host. Resume explicitly clears holds; it does not start a missing daemon. New disabled setups use the separate start action instead of a misleading Resume button. Stop/completion behavior remains in keepgoing, with no separate loop skill required.

The setup service never spawns, forks, re-parents, sends to, or changes the provider/model of a BB thread. Preview tickets expire after five minutes or plugin reload, are bound to the task hash, target metadata, binary selection and resolved plan, and permit one creation attempt. Task text is not persisted in plugin storage, logged, or put in subprocess arguments; it remains in the private prompt file after creation. Task input is limited to 256 KiB in the UI flow. Paths and target references in private plugin storage are operational metadata.

Existing configured supervision or a failed status lookup blocks creating another loop through this flow. This only covers loops known to the plugin, not arbitrary timers/configurations elsewhere. Setup does not detect or replace an external timer. Do not start a second supervisor for a loop already run by another schedule.

### Recovery and verification limits

- Failed/expired previews require a new preview. Changing task text or name clears the UI preview. Target/provider/environment changes require a new setup review; existing loops are never silently transferred.
- Creation and saving the plugin reference are not atomic. If reference persistence fails, the UI retains the created config path for recovery. Inspect that disabled setup using the CLI on its host; do not create or start another automatically. A command timeout or lost response can also leave a disabled directory; existing destinations are never overwritten.
- A failed start response can mean startup was not confirmed, not that nothing started. Refresh diagnostics before retrying. A responsive daemon prevents another start; the CLI's daemon/tick locks and ownership checks provide the final guard.
- Thread/config checks are point-in-time observations. Manual edits and external scheduling changes are not serialized by the plugin. Keep configurations unchanged while creating/starting a loop. Full model/account validation and atomic thread transfer remain separate roadmap items.
- The plugin retains at most 32 UI-created setup references. There is no UI deletion/import/transfer operation yet; CLI-created configurations use the existing server-local setting. Identical loop names on different hosts are ambiguous to `bb keepgoing <action> <name>` and are refused; use the thread's controls instead.

Verification uses SDK fake-host/RPC tests, local subprocess fixtures, controller tests, DOM interaction tests, typechecking, and plugin bundle builds. Tests cover host routing, preview changes/replay/concurrency, holds, failed diagnostics, reload, privacy, and the separate preview/create/start clicks. No live BB thread, remote host deployment, paid request, account handoff, or end-to-end install-to-completion walkthrough was exercised. The existing conversation timer was not changed.

## What it shows

BB shows thread context in one of two places. The plugin uses whichever one is showing:

- **The context banner above the message box.** This is the default. A `keepgoing: …` item sits at the right of the banner. Click it to expand the loop name, the last time something was sent, and a Pause or Resume button.
- **The thread context card** at the top right of a thread page. This shows when BB's thread context card experiment is on. The keepgoing row sits in the card's lower group.

Both read:

- `keepgoing: enabled`: the loop is enabled and this thread is its current target. This label alone does not prove a supervisor is running.
- `keepgoing: watching (<target>)`: this is the loop's primary, but a fallback has taken over.
- `keepgoing: paused`, `stop requested`, `awaiting user`, or `disabled`: the loop's control state.

Hovering shows the loop's note, the last time something was sent, and the fallback health summary.

## Commands

```sh
bb keepgoing status
bb keepgoing pause <loop>
bb keepgoing resume <loop>
bb keepgoing stop <loop>
```
