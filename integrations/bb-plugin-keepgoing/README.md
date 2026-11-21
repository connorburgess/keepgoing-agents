# looptender for BB

**The product is now looptender (formerly keepgoing).** From plugin v0.3.1 (on `main`, not yet in a release) BB shows this plugin as looptender, and its labels and messages say looptender. Everything you type is unchanged for now: the plugin id (`keepgoing`), the `bb keepgoing` command, the `keepgoingPath` setting and the `keepgoing` executable. Guided setup with a CLI from after v0.2.0 creates `looptender.yaml` in place of `keepgoing.yaml` (the plugin accepts either; existing loops keep their file). Updating keeps the plugin's settings, secrets and schedules, because BB keeps them under the plugin id. This guide calls the product looptender and keeps executable, command and configuration identifiers literal.

A thin [BB](https://getbb.app) plugin for [looptender](../../README.md). It shows looptender's state on every thread a loop supervises, with Pause controls, guided setup and explicit held transfers. It also adds a `bb keepgoing` command. All decisions stay in looptender; the plugin only runs the `keepgoing` CLI and renders its JSON.

The plugin reports its version with `--client bb-plugin@VERSION` when it reads a loop's status, so the looptender panel's Versions view can say which plugin and which looptender build it runs. The version is a constant in `client-flag.ts`, kept equal to `package.json` by a test. A looptender too old to know the flag answers with a usage error. The plugin then asks again without it, and stops sending it to that executable only if that second call succeeds. It sends the flag again once the executable changes: a different path, size or modification time, as after an upgrade. A plugin reload also resets this.

## Install

Use **CLI v0.2.0 + plugin v0.3.0** (its labels say keepgoing). Verify that the [v0.2.0 tag and release](https://github.com/looptender/looptender/releases/tag/v0.2.0) are available before using this pinned installation command:

```sh
bb plugin install git:https://github.com/looptender/looptender.git@v0.2.0 --plugin keepgoing
bb plugin config keepgoing set keepgoingPath /absolute/path/to/keepgoing
```

Confirm BB's displayed source/tag before installation. The repository collection selects this package; BB installs production dependencies and builds its server/app/host entries. It needs Git and network access, but not a separate global npm install. The command does not install the looptender CLI, configure a loop or start supervision. New UI-created loops need no manual `configs` entry. Existing managed installs use `bb plugin outdated` and `bb plugin update`; a pinned tag remains pinned, so inspect the recorded source and update policy rather than expecting it to follow new tags. Local path development installs use the path/build workflow below. No live Git installation or installed-plugin acceptance is claimed yet.

For a local source build:

```sh
cd integrations/bb-plugin-keepgoing
npm install
bb plugin build
bb plugin install .
bb plugin config keepgoing set keepgoingPath /absolute/path/to/keepgoing
bb plugin config keepgoing set configs "/absolute/path/to/keepgoing.yaml"
```

`configs` takes one existing server-local configuration file path per line (`looptender.yaml`, or `keepgoing.yaml` from releases up to v0.2.0). Those loops, setups created through the UI, and explicitly imported registry references (unreleased source) are shown and controlled. UI-created setups are registered separately with their host and configuration path in private plugin storage. A path sent from the browser is checked against this list, so the plugin never runs looptender against an arbitrary file. The plugin needs a looptender build whose `status --json` includes `primaryAgent` and `targetAgent`, which is any build after 0.1.0.

<a id="supervise-this-thread"></a>

## Add to Looptender

This flow is implemented on `main` and requires a looptender build containing `init --non-interactive --json` and stdin task input (after v0.1.1). The plugin must be rebuilt/reloaded to expose the UI and its host entry. The configured looptender executable and BB CLI must be available on each selected host; a bare binary name uses that host's PATH. Setup does not install tools or change logins.

1. Open **Add to Looptender** in the context banner, or its button in the context card. Enter a loop name and task with completion criteria. The **Record a goal for this task** checkbox starts selected; it uses the loop name as the initial title and shares current goal references in later prompts. Goal setup needs CLI v0.1.5 with `init --with-goal`; update the selected host binary or explicitly deselect the choice for the existing task-only flow. The plugin never silently falls back or upgrades the CLI.
2. Choose **Preview setup**. The server resolves the existing thread, provider, ready Git checkout, and connected host from BB metadata. Review the displayed project, target, configuration path, and operating limits. The initial UI uses one agent and default limits with no fallbacks; advanced setup remains available through the CLI.

   **Follow my standard route list for handoffs** appears, unchecked, only when the host's looptender reports a usable [standard route list](../../docs/routes.md) with a route that could apply to this thread: a switched-on same-conversation route on the thread's provider, on another usage limit (`routes list --json`, asked outside the setup queue so it never holds up Resume or a move). An older CLI that has no such command, or v0.2.0, which cannot follow the list, gets setup exactly as before. A build between the commands that change the list and `init --routes standard` lists routes but refuses the flag; the checked preview then fails closed. If the preview finds that no route applies after all, it says so plainly. Checked, the preview runs `init --routes standard` and names each route that applies to this loop, by id, model and usage limit, never by path or login, and says that handoff on a rate limit is turned on. Creation must report the same routes as the preview, and the saved setup notes that it follows the list. A following setup stays inspectable and controllable after it hands off to one of its routes, which is this thread on another model: diagnostics and moves accept it, and Pause and Stop never check the target. The plugin never resumes it; a person does. Unreleased; the New thread screen's **Keep going** switch stays single-agent.

   **Going back to an older plugin build.** A build without this change drops the record's note that a setup follows the list, and then refuses to verify that setup once it has handed off or reports its routes; use the CLI for it there. This build restores the note from the loop's own status the next time it inspects it.
3. Choose **Create disabled setup**. The host runs the shared `init` command, with task text on stdin. For goal setup, one private `task.md` snapshot is referenced by goal revision 1 while `prompt.md` holds stable wake instructions. Configuration and prompt files go into the CLI's new private setup directory outside the checkout. No agent, probe, scheduler, or account is started or created.
4. Review the shared doctor results. **Start background supervision** appears only when the saved target matches, BB identity diagnostics pass without errors, no control hold prevents start, and no responding daemon was found. Clicking it calls the existing `start --background` command on the recorded host. It enables supervision with the thread's ownership identity; it does not take over another owner or clear holds. Subsequent ticks may prompt the agent normally.

The **Setup** control / expanded banner can refresh diagnostics later. Pause/stop route to the recorded host. The plugin does not resume a loop (unreleased source): a Resume click, an agent's `bb plugin rpc call` and `bb keepgoing resume` all reach it the same way, so it cannot tell a person from the session the loop supervises, and supervision never clears its own hold. A held loop shows where a person resumes it instead, from an ordinary terminal: `keepgoing resume -c <config>`, or the looptender panel started there with `keepgoing panel --allow-changes`. A terminal BB opened on the loop's thread carries that thread's identity and is refused. Resume explicitly clears holds; it does not start a missing daemon. New disabled setups use the separate start action. Stop/completion behavior remains in looptender, with no separate loop skill required.

The setup service never spawns, forks, re-parents, sends to, or changes the provider/model of a BB thread. Preview tickets expire after five minutes or plugin reload, are bound to the task hash, goal choice, target metadata, binary selection and resolved plan, and permit one creation attempt. Task text is not persisted in plugin storage, logged, or put in subprocess arguments; it remains in private `task.md` for goal setup or `prompt.md` for task-only setup after creation. Task input is limited to 256 KiB in the UI flow. Paths and target references in private plugin storage are operational metadata.

Existing configured supervision or a failed status lookup blocks creating another loop through this flow. When looptender itself refuses a second loop on the same session and reports that in a structured form, **Add to Looptender** shows its reason with the other loop's name; it never offers to override it. This only covers loops known to the plugin, not arbitrary timers/configurations elsewhere. Setup does not detect or replace an external timer. Do not start a second supervisor for a loop already run by another schedule.

### Recovery and verification limits

- Failed/expired previews require a new preview. Changing task text or name clears the UI preview. Target/provider/environment changes require a new setup review; existing loops are never silently transferred.
- Creation and saving the plugin reference are not atomic. If reference persistence fails, the UI retains the created config path for recovery. Inspect that disabled setup using the CLI on its host; do not create or start another automatically. A command timeout or lost response can also leave a disabled directory; existing destinations are never overwritten.
- A failed start response can mean startup was not confirmed, not that nothing started. Refresh diagnostics before retrying. A responsive daemon prevents another start; the CLI's daemon/tick locks and ownership checks provide the final guard.
- Thread/config checks are point-in-time observations. Manual edits and external scheduling changes are not serialized by the plugin. Keep configurations unchanged while creating/starting a loop. Full model/account validation remains unimplemented; explicit transfers use the recovery protocol below.
- The plugin retains at most 32 UI-created setup references. There is no UI deletion operation yet. Unreleased source adds the explicit registry attachment below; older plugin builds need the existing server-local setting for CLI-created configurations. Identical loop names on different hosts are ambiguous to `bb keepgoing <action> <name>` and are refused; use the thread's controls instead.

Verification uses SDK fake-host/RPC tests, local subprocess fixtures, controller tests, DOM interaction tests, typechecking, and plugin bundle builds. Tests cover host routing, preview changes/replay/concurrency, holds, failed diagnostics, reload, privacy, and the separate preview/create/start clicks. No live BB thread, remote host deployment, paid request, account handoff, or end-to-end install-to-completion walkthrough was exercised. The existing conversation timer was not changed.

## Keep going from the New thread screen

Unreleased source, tested only with BB's SDK test hosts and stand-ins. It has not been run in a real BB, so nothing below is live-verified. It uses BB plugin APIs that BB marks experimental (`composer.submit`'s `experimental_data`, `bb.experimental_hooks` and its `experimental_submission`); they can change between BB versions.

The New thread composer gets a **Keep going** switch in its action row, and a **Keep going…** row in the menu beside Send for the compact layout, where BB draws no action row. Turning it on shows a banner that says what will happen and holds one button, **Send and keep going**. That button is the send that asks for supervision. A plain Enter sends the message without supervision and turns the switch off; the new thread then shows the ordinary **Add to Looptender** offer.

What happens after **Send and keep going**:

1. The plugin asks its server for a single-use request (valid for two minutes, one per draft) and sends the message through BB's own composer with that request attached. BB creates the thread and delivers the message as usual.
2. BB hands the request back to the plugin's `message.dispatch` hook together with the thread it created. That is the only link the plugin accepts. It never guesses from project, time or text. If the request is unknown, expired, already used, or does not describe a person's first message on a thread created after the button was pressed, nothing is created, and the thread says so.
3. Setup waits until the thread's first turn has started, which is what shows that BB delivered the message, and until its environment and host are ready. Then the plugin runs the same setup as **Add to Looptender**: preview, then create. The first message is the task, recorded as the loop's goal and passed on stdin. Later wakes point the agent at that saved task (`task.md`); the first message is not sent again.
4. Supervision starts, once, when setup's own diagnostics allow it. Pressing **Send and keep going** is the consent to that start. The thread's banner or card then shows `looptender: keeping this going`, the loop's limit, and **Pause** and **Stop**. A start is reported only when the start command confirmed it.

If anything about the start is uncertain, the setup is left switched off and the thread shows `looptender: ready, not started`, the reason, and one button, **Start keeping this going**. That covers diagnostics that report an error, a start that could not be confirmed, and a hold or a responding supervisor already present. A start is never tried a second time by the plugin.

**The loop's name** is the project directory's name and a short random suffix, such as `example-app-k3f9`. It is never taken from the message or from the thread's title, which BB makes from the message: a loop name reaches command arguments, plugin storage and a directory name, and task text must not. The plugin has no rename; the name is the `name` in the loop's `keepgoing.yaml`.

**A loop-wide limit, always.** Every loop created this way stops for a person after 40 automatic messages, or after 24 hours without progress, which looptender measures as a Git change. The numbers are defined once, in `new-thread-contract.ts`, and both can be changed under **Details** in the banner before sending (1 to 1000 messages; 2, 6, 12, 24 or 72 hours). Edited limits apply to that send only. The limits the thread shows afterwards are the ones looptender's own setup plan reports, not the request; if that plan does not carry exactly the limits asked for, nothing is created or started.

**The setting** *Keep going from the New thread screen* has two values. *Start automatically* is the default and is described above. *Review first; I start it* always creates the setup switched off and leaves the start to the button on the thread. The mode the banner showed when the button was pressed is the most that happens: if the setting has since become stricter, the stricter one applies, and a stored value the plugin cannot read is treated as review-first.

**A send BB did not deliver never leads to supervision.** If BB's send fails, the control withdraws the request and says the message may not have been sent (BB may already have delivered it); a request withdrawn while setup is running stops at its next step, so nothing more is created, and a setup already created is not started. Setup does not run before the thread's first turn has started; a different message starting that turn while the thread is still pending ends the request (a follow-up after the first turn has started does not); and a request whose thread never starts expires after ten minutes, with its task text.

When it cannot go ahead, it says why and does nothing else:

- The looptender program does not run on the BB server: the switch stays off and the banner explains. This check runs on the BB server, not on the host a new thread will use; a host without looptender is reported on the thread, after creation.
- This BB does not offer the dispatch hook, or refuses it: the control is unavailable with that reason. Status, Pause, Stop and **Add to Looptender** on existing threads do not depend on the hook and keep working.
- The plugin already holds 32 setups: the switch stays off and says so. Details shows the count before that. The plugin cannot remove a setup yet (see the limits below).
- Another loop already supervises the thread, or that could not be checked: nothing is created or started.
- The thread does not work in a Git repository: nothing is created.
- The message has no text, or more than 256 KiB.
- looptender itself refuses, for example because another loop already supervises the thread's session: the thread shows looptender's reason in plain words with the other loop's name, and nothing else from the CLI. The plugin offers no way to override the refusal. looptender reports the refusal on stdout as `{"schemaVersion":1,"outcome":"refused","refusal":{"code":"second_loop_on_session","otherLoop":"<name>"}}` from `init --json` and `start --json` (v0.2.0 and later); an older looptender reports it only as text, and the plugin then shows its ordinary "could not complete" message. For `start`, the plugin passes `--json` only when the setup preview that created the loop advertised `startJson: true`, and only while the same looptender binary is configured; otherwise it starts the loop exactly as before. With `--json` a start counts as confirmed only when looptender's own report says `started`. The plugin never tries a flag to find out whether it works.
- Creation was asked for and could not be confirmed: the thread names the loop and shows its configuration path, because a switched-off setup may exist on the host. Check it there with `keepgoing status -c <path>`; to discard it, delete its directory. It is never started and never created again automatically.

Limits of this flow:

- The plugin never creates, sends to, re-parents or changes a thread. BB creates the thread; the plugin only learns of it.
- The task text is held in the plugin server's memory from the send until setup has been asked to create the loop (at most ten minutes), and is never written to plugin storage, logged, or put in subprocess arguments. Reloading the plugin in that window drops the request; the thread then shows the ordinary offer.
- The plugin's dispatch hook runs for every message BB dispatches and always answers "proceed". It reads the text only of a message that carries its own request. It does not delay, queue or reject a send.
- No removal of setups. The plugin keeps at most 32 references and each **Send and keep going** uses one. looptender has no command that removes a loop, so the plugin cannot offer one for a switched-off, never-started setup; what is needed is a CLI command that removes such a loop's private directory, and a plugin action that calls it and drops the reference. There is no supported way to free a slot by hand: the references are in the plugin's private storage, which BB does not offer for editing, deleting a loop's directory does not free its slot, and `bb plugin remove` would also delete the plugin's settings. If the slots run out, stop using **Send and keep going** and set loops up with the CLI, which has no such limit.
- There is no keyboard shortcut for **Send and keep going**.
- The limits and the cadence shown on the thread after creation are kept in memory; after a plugin reload the thread shows the ordinary status row and **Setup** panel instead.

Not verified: the fakes do not prove that BB delivers experimental_submission for a New thread send, or that createdAt shares the server clock; every break traced fails closed, silently if BB drops experimental_data. Nothing here was run in a real BB.

## CLI menu publication (unreleased source)

After a confirmed setup, start or supported move/recovery, the rebuilt host entry explicitly publishes a reference to the CLI registry. Setup still leaves supervision disabled; publication changes no controls or work allowances. The result distinguishes lifecycle success from `registered`/`repair_needed` publication evidence. Publication cannot verify a provider account or model. An older CLI, unavailable lock, changed binding, corrupt registry, lost publication reply or storage failure leaves explicit repair needed; never repeat setup/start/move just to populate the menu.

Plugin settings/saved setups and CLI registry entries remain separate sets. Expanded status identifies the reference source and last publication outcome; `bb keepgoing status` explains its limited coverage. Reads do not import, publish, scan directories or prune. External CLI setup/moves and manually listed configurations need [explicit bound registry reconciliation](../../docs/local-clients.md#bound-bb-publication-and-repair-unreleased-source) on the host, under the saved owner or an ordinary person terminal. No caller identity workaround or takeover is part of repair. Existing superseded markers retain their historical/no-Resume behavior; paused-only predecessors are never inferred to be retired.

The lifecycle operation, registry publication and plugin persistence are separate writes. A successful init can leave a disabled configuration and published CLI reference even if the plugin cannot save its reference; retain the reported identity/path privately and recover via attachment instead of recreating it. A lost move response uses the existing held-move inspection/recovery flow, which does not publish during reads. Explicit later reconciliation repairs menu visibility without replaying the move. Unrelated references survive. This source needs rebuilt app/host and a CLI with `registry reconcile`; no registry schema change, installation or live acceptance is implied.

## Attach an existing loop (unreleased source)

The CLI registry and the plugin's registrations are separate. A loop can be registered and running while BB still offers **Add to Looptender** because its configuration is absent from the plugin. Attachment imports the reference; it never creates a loop, starts or resumes supervision, changes a login, or transfers ownership.

1. On the loop's current BB target, open **Add to Looptender** (or **Setup**). Under **Already have a loop for this thread?**, choose **Find registered loops**. This reads only the CLI registry on the thread's verified host; it does not search directories. For a loop missing from that registry, first use the explicit bound registry reconciliation procedure above on its host.
2. Select the existing loop and choose **Preview attachment**. The preview verifies the registry/name/runtime binding, config digest, primary/current BB target, provider and recorded ownership. BB supplies the exact project, environment, host and working directory, checked again before saving. Review the configuration, current control state, daemon response and check evidence. Registry presence and a responsive daemon do not establish operational supervision.
3. Choose **Attach existing loop** to save or reconcile only the plugin reference. A stale plugin-created reference left behind after an external Claude-to-Codex transfer can be reconciled to the already-owned successor in the same project/environment/host/path, preserving its plugin record ID. The attachment path calls no transfer command and does not require the provider-transfer feature to be installed.
4. After an uncertain save/response or plugin reload, select the same registry entry and choose **Check attachment status**. This resolves the existing identity without saving again. Repeated attachment of the verified identity is a no-op. If no attachment is recorded, preview again before an explicit retry; never create a replacement loop to recover a lost response.

The project/environment/working-directory check binds the BB target metadata supplied by the SDK. Configured `progress.repositories` are independent Git progress evidence; attachment does not compare them with that working directory or verify repository activity. Valid existing-primary BB configs cannot set `bb.project` or `bb.environment` (those fields are restricted to new-thread fallbacks).

This first slice supports only an existing single BB primary whose current owner is that target thread. Missing/unverifiable ownership, mismatched providers/targets, pending CLI/plugin moves, fallback configurations, standard routes, conflicting registrations, changed metadata/config/owner generation, and references inside a checkout or the target project are refused. A configuration already present in the server-local **Loop configs** setting must be removed from that list before importing; attachment does not edit settings. Unrelated setups/settings survive. The existing 32-reference limit applies. Imported loops use CLI transfer followed by attachment reconciliation, rather than the plugin move action. Imported loops do not offer a start action through setup; a person reviews status and uses the host CLI separately when appropriate.

Imported status revalidates the registry and target binding. An unavailable host or changed identity withholds the loop with an incomplete-status warning. An enabled imported loop with failed checks says **checks failing**, even when its daemon responds; one without a check says **no check recorded**. A failed last check remains failure evidence. A successful recorded check is a dated snapshot, never proof of current provider health. Startup acknowledgement only confirms daemon startup: first-check success/failure must be inspected separately, including session-claim permission failures. Attachment does not repair startup permissions or services.

Registration writes share the plugin-instance mutation queue with setup and transfer. Verification and the latest registration list are read inside that boundary; other clients cannot overwrite unrelated entries through concurrent attachment. Tickets bind the target, binary, registry entry, config digest and owner generation for five minutes and expire on reload. Holds may change between observations; fresh controls are displayed and never written by attachment. These are cooperating-process, point-in-time checks, not a lock against external config/registry/settings writers or an atomic snapshot of BB and loop state.

Evidence is local host/controller/SDK/DOM fixtures, including lost responses, concurrent imports, cross-provider existing-target metadata and failed-check diagnostics. No installed plugin, live BB loop, account, service or scheduler was changed. Remote deployment, native platform/browser acceptance, automatic successor detection, generic cross-IDE continuity and startup permission repair remain open. Build both app and host artifacts before installing this unreleased source.

## Move supervision here

Implemented on `main` after v0.1.3 and fixture-tested. The transfer CLI is released in v0.1.4; plugin v0.2.0 and later are distributed through the separate tagged Git installation above. Both the plugin and the host CLI need the transfer build; rebuild the plugin’s app/host bundles before installing an update. Older CLIs are rejected when the preview does not advertise apply support.

1. Open the destination thread’s looptender banner or **Add to Looptender** / **Setup** card panel. Under **Move supervision here**, choose an existing plugin-created loop in the same project, environment, host, workspace and provider.
2. Click **Preview move**. Review the old/new threads, provider, current owner, all control flags and any goal ID/revision. Model/account identity remains unverified. Five-minute tickets are single-use and expire on reload; changed target metadata, controls, goal or configured labels require a new preview.
3. Click **Move and keep held**. This explicitly authorizes destination ownership takeover. The CLI preserves existing holds, goals and work accounting, adds awaiting-user, resets thread observations and fences stale configs/daemons. It does not cancel the old thread’s running turn, send work, create/re-parent a thread or change a timer.
4. Once status verifies the held destination, the plugin updates its saved reference. Review **Setup** diagnostics. Resuming is a separate explicit action by a person, from the looptender panel or a terminal, that clears holds; it does not start a missing daemon. The plugin does not resume. A disabled loop uses its separate start action after holds are resolved. Never resume a completed task or a task still waiting on a person.

Only plugin-created setups appear. Manually listed configurations still use `keepgoing transfer preview|apply|recover` on their host; unreleased source can import a compatible existing registry reference through the explicit attachment flow above. A duplicate manual reference to the selected setup blocks the UI move. Known destination supervision or unverified status blocks another loop; arbitrary external timers/configurations are not discovered.

### Interrupted moves

A private saved request is written before calling apply. It retains the original registration, destination metadata and binary selection, with no prompt, control note, credentials or raw command output. While it exists, the banner says **move requires inspection** and plugin start is refused. The plugin never resumes a loop in any case. Pause/stop remain available through the commands and are not queued behind a slow transfer.

After a timeout, lost reply, plugin reload or persistence failure, open the destination, select the pending loop and choose **Check move status**. If the CLI already completed and still verifies the expected held destination/owner, this repairs the plugin reference without replaying apply. Otherwise **Recover move** is an explicit action: it resumes the observed CLI pending ID, or revalidates and retries the saved source/destination request if the CLI had not begun. It always leaves holds intact. There is no automatic retry or recovery timer.

CLI mutation, plugin reference update and saved-request removal are separate writes. Failures retain a recovery path; a completed move can coexist with an old plugin reference until reconciled. Changed metadata/binary, conflicting registrations, an externally cleared hold, malformed records or an unrelated CLI move require private inspection on the host. Do not delete recovery records or replay moves blindly. There is no cancel UI in this first transfer scope; registry attachment is a separate explicit flow, and the plugin queue does not serialize external writers or other plugin instances. Point-in-time preview reads do not replace the CLI’s final checks.

Verification uses SDK fake-host/host-entry tests, controller failure fixtures and DOM interactions, plus typechecking and bundle builds. A local check also exercised the actual built CLI through the plugin controller/host wrapper with fake BB metadata, verifying destination registration and preserved holds. The CLI’s own process-crash and new-target-only wake fixtures cover runtime transfer behavior. Live BB transfer, remote deployment, model/account identity and end-to-end migration readiness remain unverified. No existing timer, installed plugin or supervised thread was changed during these tests.

## What it shows

BB shows thread context in one of two places. The plugin uses whichever one is showing:

- **The context banner above the message box.** This is the default. A `looptender: …` item sits at the right of the banner. Click it to expand the loop diagnostics and a Pause button, or for a held loop where a person resumes it.
- **The thread context card** at the top right of a thread page. This shows when BB's thread context card experiment is on. The looptender row sits in the card's lower group.

Unreleased source shows **all matching loops**, including retired references and completed loops, rather than treating the first result as current. The card names each loop when there is more than one; the banner summary reports the count and its expanded view lists each loop’s status and controls. Refreshing or reordering references preserves each control’s configuration and managed-record identity. Duplicate names receive visible reference numbers that remain stable within the plugin session; expand **Configuration reference** to inspect the configuration path before choosing a control. These numbers are local display aids, not persistent loop IDs. Matching a thread or sharing a loop name does not establish a single current owner. New-thread Pause/Stop appears only for one unambiguous saved setup for that thread; other loops retain their own ordinary controls. This is SDK/fictional-browser tested, with installed-plugin refresh and live BB layout still unverified.

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
bb keepgoing resume <loop>   # refused: prints where a person resumes the loop
bb keepgoing stop <loop>
```

## License

This plugin is part of looptender and is source-available under the [Sustainable Use License 1.0](../../LICENSE); see the [licensing guide](../../LICENSING.md). The plugin at the `v0.1.5` tag, like every release through v0.1.5, remains under Apache-2.0.
