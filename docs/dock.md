# The keepgoing panel: one page for every loop

`keepgoing dock` serves one read-only page, the keepgoing panel, to this computer only. It shows every [registered](local-clients.md) loop: which ones need you, and for each loop its state, the routes it prefers and the one its records name as current, its lanes, its last check and its limits. It is unreleased source, tested with fixtures only. "Dock" is the working name of the command and of the page plus a later menu-bar or tray item; the name is not settled. Words such as loop, lane and route are defined in the [glossary](glossary.md); where a configuration key is named (`primary`, a fallback's `id`), the configuration word is kept.

```sh
keepgoing registry add -c /private/example-loop/keepgoing.yaml   # once per loop
keepgoing dock
```

```text
The keepgoing panel is being served, read-only, for this computer only:

  http://127.0.0.1:49152/<token>/

Press Ctrl-C to stop it. Stopping the page leaves supervision running.
```

Open the printed address in a browser on the same computer. Nothing is hosted, there is no account, and the page makes no request to anywhere but the address it was loaded from.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port N` | `0` (a free port) | Loopback port to listen on. |
| `--refresh 15s` | `15s` | How often the page reads again (`5s` to `10m`). The page states the interval and the time of the last read. A hidden tab does not poll; it reads again when shown. |
| `--stale-after 30m` | `30m` | Age at which a check or lane listing is shown as stale. Set it above your scheduler interval. |
| `--directory DIR` | the default registry | A non-default private registry, as for `registry` commands. |
| `--offline` | off | Do not ask each loop's local daemon for its next check. |

Exit codes: 0 after an interrupt, 1 if it cannot listen, 2 for invalid flags.

## What the page shows

The page has two levels, so opening it never starts with a list of facts. The page's own footer explains the words it uses (loop, lane, route, account group, check, test, hold).

**Level 1, the picker.** A "needs you" line, a row for the **Routes** view, then one row per loop with its name, a status dot and its state in one phrase: supervising, waiting for you, waiting for a limit to reset, waiting for the network, checks are failing, outside its hours, paused, stopped, finished, off, not checked recently, or unknown. A loop that is on a later route says so in its row ("on its second choice"). Loops that want attention come first. The needs-you line and the Routes row stay in place and only the loop list scrolls, so neither can scroll out of reach. On a phone-width window the picker is the first screen and a pick replaces it, with a link back.

**Needs you.** With nothing picked, the page lists every loop a person should look at:

- A loop held for a person, with the reason from the fixed cause recorded when the hold began, how long it has waited, and what to do. The wait is measured from the retained history record of the hold starting; when retention has dropped that record the page says "waiting at least" and counts from the last control change. A loop whose new-conversation move could not be confirmed is listed the same way.
- A loop that could not be read: its control record or check history is unreadable, its registration no longer matches its configuration, a move to another conversation is in progress, or the read was cut off. Whether such a loop is held is unknown, so it is listed as "could not be read" with what to do, never left out.

"Nothing needs you" is shown only when the registry was read, every loop in it was read, and none is held. Otherwise the line counts what needs you and what could not be read.

Below that, **Worth knowing** lists loops that do not need a person but are not routine: waiting on a limit or the network, checks failing, not checked recently, outside their hours, or running on a later route, each with the recorded reason in one line.

The note left with a hold is withheld by default, as `client status` withholds it, because a note can contain identifiers and paths; the card says one exists and offers to show it. **Show identifiers and notes** includes it.

**Level 2, one loop, shown through its routes.** Under the loop's name are short answers:

- **Last decision**: what the last recorded check did and why, in the fixed vocabulary of causes ("Sent nothing: lanes are working", "Sent a wake: the session was idle"), its age, and since when an unbroken run of checks has had that same result. A cause outside the vocabulary is not worded, and free-text reasons are never shown.
- **Hold**: none, or the hold in force (paused, stop requested, held for a person, finished) and for how long. The duration comes from the retained history record of the hold starting; without one it reads "for at least" and counts from the last control change. Off is a state, not a hold.
- **Route**, when the loop is not on its first route: which choice it is on and since when, and why ("first choice at a usage limit until Wed 18 Nov").

Then the loop's **preferred routes** in order: its first route (`primary`), then each route of the loop's own list in configured order. A route is a model on an account group at an effort level; its title is its model, or the tool and "default model" or "model not set" when no model is configured, with the account group, effort and how the loop gets onto it (continues in the same conversation, moves to another conversation, starts a new one, runs without a window, runs a command) on the line below. Each route card reads top to bottom:

1. What is recorded against the route, first. A route the loop moved off says so from the loop's state: when, why, the reset time the provider reported or that none was reported, and what is recorded about going back ("keepgoing returns after that, once a test passes", "a return was last tried …", "no return has been tried yet", or "keepgoing will not return by itself: returning is switched off for this loop"). A route whose newest usage reading shows a window used up says that, with its reset.
2. Its readiness, as a sentence tied to its last test: "Ready: passed its test 47 min ago", "Passed its test 8 h ago; a new test is overdue (it is tested every 4 h)", "Sign-in failed when tested 2 h ago", "Not tested yet", "Turned off in its configuration". "Test" is the page's word for what the configuration calls a probe: one small real request.
3. Its usage reading with the reading's age and each window's reset, or "No usage reading" with the reason when a record shows it (this kind of test does not report usage, a passing Codex test does not report usage, it has not been tested yet, it is turned off).

The route the loop's records name as current is marked, in one of two ways:

- **In use now**, only when the loop is on with no hold, its last check is recent, and that check recorded this route.
- Otherwise "Current route" with what is recorded: loop held, paused, stopped, finished or off, outside its hours, no recent check, or not confirmed by the last check.

The current route is not tested, so it gets no readiness verdict, only the age of its last test, unless that test recorded a failure. When another loop holds a newer usage reading or test result for the same route, the page shows it, marked as another loop's, so one route does not read differently on different pages.

**Lanes** are listed one per row under the loop's current route, the ones that need a person first: its state (working, waiting for an answer, failed) and "Lane 1", "Lane 2" and so on. keepgoing records a lane's state and a hashed key, not its title, its thread or which route it is on, and the page says so once under the list; a lane that is itself a registered loop is named and linked. The hashed key appears only with identifiers. The heading keeps the counts and the age of the listing.

**Recent activity** lists the five newest entries of the retained history, newest first, in the same plain words: checks with their action and cause, moves to another route and returns to the first with the route's name, holds, control changes worded from the flags they changed, and reports with their recorded outcome. An unbroken run of identical checks is one line with the count and when it began. A send that returned is shown as sent with "delivery is not confirmed". It says how many entries are retained and whether older history was pruned; `keepgoing timeline` has the rest.

**Checks and limits** gives the last check and its age, the next check, limits used (messages sent, deadline, time without Git progress, new conversations created, and a provider limit with its reset time), and the device: "This computer" and its role from a record (it runs this loop's checks when a check is recorded in the loop's state here, otherwise it holds the loop's records). There is one device, the one running `keepgoing dock`; its hostname is an identifier and is included only with identifiers.

The next check is shown only when the loop's own keepgoing scheduler answers. Otherwise it is unknown and says why: no keepgoing scheduler is on record for the loop (and one outside keepgoing is not visible), a recorded one did not answer, or it was not asked. It is never inferred from the cadence of past checks.

**Routes, across loops.** First an **Allowances** strip: one line per account group with the newest usage reading recorded for any of its routes (each window, how much is used, when it resets, and how old the reading is), "No reading" where none exists, and a note when a loop left a route in the group on a usage limit whose reported reset has not passed. Then every route once, those at a limit or not ready first, using the same route component: what is recorded against it (each loop that moved off it, a used-up window, or that it shares its account group with a route at a limit, which does not change its own readiness), its readiness, its usage, and four lists: loops running on it now, loops for which it is the current route but that are not running, loops keeping it on standby with which choice it is for each, and loops that list it but whose current route could not be read. A line above counts the routes, those in use, and those at a limit or not ready. A route is what the configuration says: keepgoing does not verify which account or model a BB thread is actually using. Two accounts configured under one group are two routes, because a route is keyed by what selects the account a test reads; they can carry the same title. Lanes are not attributed to a route in this view.

Throughout:

- **Unknown and stale are shown, never guessed.** A missing record reads "unknown", "none recorded" or "no check recorded". A check older than `--stale-after`, a lane listing the last check did not refresh, and a usage window that has since reset are labelled stale; a stale usage window is drawn empty, not full. Every phrase is tied to the record that produces it by a table test, so wording that says more than its record fails.
- **No identifiers by default.** Thread IDs, registration IDs, paths, hold notes, this computer's hostname, lane keys and the configuration's names for routes are not in the page by default, and identifiers and notes are not in the data it loads. **Show identifiers and notes** adds them; turning it off hides them at once, without waiting for the next read. Paths of keepgoing's own files, lane thread IDs, standby thread IDs, account directories, commands, credentials and logins are never included. Account group names from the configuration are shown.
- Nothing on the page acts. **Read now** reads again; **Pause updates** stops the periodic reading until it is turned off.

The page follows the browser's light or dark setting and text size, works with a keyboard, and reflows to a phone-width window. When it reads again, the keyboard focus, the scroll position and any text being selected stay where they were. A screen reader is told when what needs you changes and when a read fails, not the time of every read.

## What it reads, and what it never does

Each read goes through the code paths of commands that already exist, and adds no evidence of its own:

| Shown | Read through |
| --- | --- |
| The list of loops | `registry list` |
| Binding checks, holds, next check | `client status --id` (including its one-second query of the loop's existing local daemon endpoint) |
| Current route, last check, limits, limit wait | `status --json` |
| Lanes | `runs --registry-id` |
| Preferred routes, readiness, usage | `fallbacks list` |
| A route the loop left: when, the reported reset, the last return tried | `status --json` (the saved handoff time, reset time and last switch-back attempt) |
| Last decision's cause and run, the hold's start, recent activity | the retained history `timeline` reads |

It runs no provider, `bb`, scheduler or Git command, starts no daemon, takes no lock and writes no file. Everything is what an earlier check or control command recorded, not a live view of a conversation.

A read belongs to the command, not to the browser request that started it, so a page that reloads mid-read does not cut it short. Each registered configuration is validated once per read and at most four loops are read at once. A read is cut off after 20 seconds; a read that was cut off is marked incomplete, is never reused, lists its unread loops as "could not be read" and cannot say that nothing needs you. A complete read is reused for two seconds so several open tabs do not multiply reads. Every list and text field is capped (a hold note at 280 characters, a loop's routes at 32 fallbacks, 64 lanes and 5 history entries per loop, 128 routes across loops, the registry's own limit of 128 loops), and a reply is refused above 8 MiB; the largest registry measured just under 2 MiB.

## Access

- It listens on `127.0.0.1` only, never a wildcard address.
- The address contains a random 256-bit token created for that run. A request without it, or with another, is refused. The token is not stored; a new run prints a new address.
- Requests with a `Host` other than the loopback address and port, a foreign `Origin`, or cross-site fetch metadata are refused, and no cross-origin access is granted. This also covers DNS rebinding.
- Only `GET` and `HEAD` are accepted. There is no write endpoint: the page cannot pause, resume, stop, start or configure anything.
- Replies are never cached by the browser and carry a content security policy that allows only the page's own script, style and data endpoint.

The token keeps other local web pages and casual local requests out. It is not protection against another program running as you, which can already read keepgoing's files. Anyone who can see the address, for example in a shared screen or shell history, can read the page while that run lasts.

## The data endpoint

The page loads everything from one endpoint, `GET /<token>/api/v1/dock` (`?identifiers=1` for the view with identifiers and notes). It returns `schemaVersion: 1`, `readOnly: true`, `readAt`, `refreshSeconds`, `staleAfterSeconds`, `registry`, `complete`, `allClear`, `unread`, `needsYou`, `loops`, `routes` and `allowances`. Each loop carries its current route, its ordered preferred routes (`chain.entries`, each with a `route` key, `current`, `inUse`, `effort`, `enter`, `source`, readiness, usage and resets), `away` when it is not on its first route (when it left, why, the reported reset, and whether returning is on), and its `decision`, `hold`, `device`, `lanes` and `events`; `routes` is the same data grouped by route, with `leftBy`, `sharedLimit` and `attention`, and `allowances` groups the routes by account group, so every view is one feed. `enter` and `source` use the vocabulary of the planned route lists (`first`, `same_conversation`, `other_conversation`, `new_conversation`, `headless`, `command`; `loop` for the first route and `own` for the rest, until a shared standard list exists). Ages are computed against `readAt` when the report is built, so the page does not depend on the browser's clock or on being opened on the same computer. Consumers must check `schemaVersion`, tolerate added fields, and show an all-clear only when `allClear` is true. An unreadable registry is `registry.availability: unavailable`, never an empty list of loops. The schema is unreleased and may change before a release.

## Planned slices

This is the first slice. The rest is not implemented.

1. **Read-only view** (this slice): the picker, needs you, one loop through its routes with its last decision, hold, lanes and recent activity, and routes across loops with each account group's allowance. A lane's title is not shown because none is recorded; recording one privately is a separate planned change, and the lane rows have room for it.
2. **Controls**: pause, resume and stop from a loop's page, and picker commands such as pause all, through the existing registered commands (`client pause|stop`, and `client prepare-resume` followed by `client resume` with its reviewed single-use ticket). The page will not get its own control logic.
3. **Configuration**, starting with a loop's routes: their order, switching one off or on, and whether the loop follows a shared standard list or has its own; later limits, the usage-switch threshold and which reports are sent. It will be a front end to the existing guided commands (`fallbacks add|move|remove|handoff`, with their `--dry-run --json` previews, confirmation, ownership and transfer guards and comment-preserving edits), never its own logic or its own file writes. The route rows already carry a position and an ID for this. Credentials and logins will never be shown or edited.
4. **Actions on many lanes at once** from the routes view, such as moving every lane on one route to another or changing their effort level. This depends on two things keepgoing does not have: lanes as first-class records (today it knows a loop's lanes only as counts and hashes, and never changes them), and a group (a coordinator and its lanes) so one action can apply to all members.

Also planned: a native menu-bar or tray item whose click opens the same short picker (the needs-you line, a row per loop, and a few commands such as new loop, open the panel and pause all), not a list of facts; installation and start at login; and answering permission prompts.

**Remote access** (reading and steering loops from a phone or another computer) is planned and not available. The slice is built so it can be added without a rewrite: all data comes through the one versioned endpoint, the access check is a replaceable layer separate from the network checks, the layout works at phone width, and nothing in the page assumes it runs on the same computer. Remote access will need real authentication of a person or device, transport encryption, and an explicit opt-in before it listens beyond loopback. No control will be exposed remotely before that authentication exists. Do not expose this slice through a tunnel, a reverse proxy or a port forward: the per-run token is not designed for that.

## Verification

`go test ./cmd/keepgoing -run Dock` covers: the listener's address; refusal of a missing or wrong token, a foreign or rebound host, a foreign origin, cross-site requests and every write method; the report built from a fixture loop in each state, including three loops on their second route (the first route's reset ahead, passed with no return tried, and not reported) and a route whose tool, provider and account group are the same word; a table from each record to the exact phrase shown; that "nothing needs you" requires every loop to have been read; that a read is not tied to a request and an incomplete read is not reused; that only a loop that is on, unheld and recently checked on a route counts as in use; the limit recorded on a route a loop left, the note on routes sharing its account group, and the allowances; withheld identifiers, notes, hostname, lane keys and paths; unchanged registry, configuration and state directories after reads; caps on notes, lanes, history entries and routes; and exit on interrupt. All of it uses fictional loops and local files.

The page itself was checked in Chromium against those fixtures (script and results are not committed):

| Check | Result |
| --- | --- |
| Keyboard only: every link and button is a tab stop, in order, with a visible focus ring; nothing focused sits under the top bar, forwards or backwards | Checked at 1280 and 390 px on the overview, a loop, a loop on its second route, and Routes |
| Focus, scroll position and selected text survive a re-read; the skip link keeps the view | Checked |
| Announcements: the live region changes only when what needs you changes; a failed read is an alert; the read time is not announced | Checked through the accessibility markup. Real screen-reader speech (VoiceOver) not checked |
| Identifiers: none in the page with the toggle off; hidden at once when turned off while reads fail | Checked |
| Contrast of every text element, light and dark | Checked: lowest 4.8:1 light, 7.05:1 dark |
| Width: no sideways scroll at 320, 390, 768, 1280, 1920 and 2560 px, also with 120-character unbroken names | Checked |
| No loops; a hundred loops; a loop with every optional field missing; unparseable dates; a reply without a refresh interval | Checked by altering the reply in the browser: no error, no malformed text |
| Text size: follows the browser's root size (200%), no sideways scroll | Checked |
| Reduced motion: nothing animates | Checked |
| Forced colours: status dots and the current row remain visible | Checked |
| The picker in a 600 px tall window: needs-you line, Routes row and the current loop visible; scroll kept across a re-read | Checked |
| Pause updates: no read while paused | Checked |
| Safari and Firefox; touch hardware; a hidden tab not polling | Not checked |

An independent review of an earlier commit of the read-only page ran its request suite against a 238-entry tree that stayed byte-identical, and the project coordinator served it read-only against two live loops with their files unchanged. Not verified for this page's newer parts: a loop under live supervision, a responsive keepgoing scheduler's next-check time, and the command executing on Windows (the tests are compiled and vetted there, not run).

To see the page with fictional loops, build the fixture registry into an empty private directory outside any checkout and serve it:

```sh
KEEPGOING_DOCK_SCRATCH=/private/example-scratch go test ./cmd/keepgoing -run TestDockScratchForScreenshots -count=1
keepgoing dock --directory /private/example-scratch/registry
```
