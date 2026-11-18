<a id="the-keepgoing-panel-one-page-for-every-loop"></a>

# The looptender panel: one page for every loop

**Display name (unreleased).** Builds from `main` label the panel and dock as looptender, including browser titles and expired-link pages. Commands remain `keepgoing panel` and `keepgoing dock`. To see the new page, use an updated executable; an older running panel continues serving its own page until restarted. Restarting the panel leaves supervision running.

`keepgoing dock` serves one read-only page, the looptender panel, to this computer only. It shows every [registered](local-clients.md) loop: which ones need you, and for each loop its state, the routes it prefers and the one its records name as current, its lanes, its last check and its limits. It is v0.2.0, tested with fixtures only. "Dock" is the working name of the command and of the page plus a later menu-bar or tray item; the name is not settled. Words such as loop, lane and route are defined in the [glossary](glossary.md); where a configuration key is named (`primary`, a fallback's `id`), the configuration word is kept.

```sh
keepgoing registry add -c /private/example-loop/keepgoing.yaml   # once per loop
keepgoing dock
```

```text
The looptender panel is being served, read-only, for this computer only:

  http://127.0.0.1:49152/<token>/

Press Ctrl-C to stop it. Stopping the page leaves supervision running.
```

Open the printed address in a browser on the same computer. Nothing is hosted, there is no account, and the page makes no request to anywhere but the address it was loaded from.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port N` | `0` (a free port) | Loopback port to listen on. |
| `--refresh 15s` | `15s` | How often the page reads again (`5s` to `10m`). The page states the interval and the time of the last read. A hidden tab makes no request, so it never keeps a panel running; it reads once when shown. A read with no answer after a little under the interval (4 to 30 seconds) counts as a failed read. |
| `--stale-after 30m` | `30m` | Age at which a check or lane listing is shown as stale. Set it above your scheduler interval. |
| `--directory DIR` | the default registry | A non-default private registry, as for `registry` commands. |
| `--offline` | off | Do not ask each loop's local daemon for its next check. |

Exit codes: 0 after an interrupt, 1 if it cannot listen, 2 for invalid flags.

## Landing view and evidence limits

**Unreleased source.** The overview opens with the existing loop needs-you queue and its recorded reason and next action. Under **Loops and lanes**, expand a loop to see the direct lanes of its last verified listing, its supervision state, last recorded decision and next known check. Expansion and keyboard focus survive ordinary reads while the page remains open. Choosing another view and returning also preserves expansion; reloading the browser page starts over. The registry's existing order keeps loops needing attention first. A line under the heading says that only conversations looptender supervises are shown, with their lanes, and that another coordinator appears once a loop is created for it; the page does not look for other conversations.

Each loop's row labels its target "coordinator" when its last verified listing has lanes (the [glossary](glossary.md)'s role; nothing is renamed), and sums those lanes on one line, people first: "Lanes: 3 waiting · 2 failed · 35 working". A listing kept across a hold or pause says so on that line too. Rows without a listing keep "Lane evidence:" with its reason. When an expanded loop lists more than 8 lanes, the lanes waiting for an answer stay in view and the rest fold into one "Show the other N of M lanes" element, which also keeps its state across reads. The 24-lane cap and the count of lanes not listed are unchanged.

The tree groups existing recorded relationships, not a live inventory of every descendant. All registered loops remain independently listed, including a child that is also registered; a recorded registration key links that lane to its own loop page. This avoids treating a parent's old observation as the child's current supervision state. There is no direct link to an unregistered child conversation, no grandchildren inventory, and no inferred relationship from names. Stale lane evidence is not drawn as current; held evidence says which check supplied it. A held or paused loop's checks do not look at its lanes, so when the hold or pause came after a complete listing in the same run, that listing is shown with its age and marked as not current ("as of the check before it was paused, 3 h ago; not current"), and each lane reads "Was working", "Was waiting for an answer" or "Had failed". When no listing can be shown, the page says why in words ("Unknown: the loop is paused, so its checks do not look at its lanes, and no complete listing was recorded before the pause") rather than a bare "Unknown". Missing and omitted evidence stay explicit. Private lane titles and keys follow the existing **Show identifiers** setting and disappear when it is turned off.

An active observation is not proof of progress, connectivity or healthy provider retries. Neither an absent lane nor an agent's completion declaration establishes verified work acceptance. The tree adds no child needs-you claim beyond the structured lane state already in the feed, and adds no answer or approval control. The loop's existing needs-you queue remains authoritative for its recorded holds; it is not a complete child approval inbox.

**Recent activity across loops** merges up to five retained entries per loop and shows the newest eight, with each loop's link, recorded time, outcome and folded-check wording. It is a bounded sample, not a full chronology or a "while you were away" tracker. The page states missing history, incomplete registry reads, known pruning of older history for any loop and how many supplied entries were left out. Per-loop retained counts remain on individual loop pages; the overview counts only entries supplied to its sample. The sample may contain routine checks. Inspect an individual loop or use `keepgoing timeline` for more retained history. No transcript, provider-event or network read is added.

The schema, registry/record bounds, identifiers policy, panel startup caller/class, control reviews and authorization are unchanged. No loop, child, daemon or installed panel is restarted by this view. Fixture/browser evidence is recorded in the review handoff; live supervision, real screen-reader speech, Safari, Firefox and Windows acceptance remain unverified.

## Opening it with one command: `keepgoing panel`

`keepgoing dock` runs in the foreground and prints an address to copy. `keepgoing panel` is the ordinary way in: it opens the same page in your browser with nothing to copy. Released in v0.2.0; tested with fixtures, real processes and a real browser on macOS. The same tests are meant to run on Linux in CI and have not yet.

**Windows: unverified, so it refuses.** `keepgoing panel`, `panel status` and `panel stop` have never been run on Windows. Until they have, they stop there with a message that says so and points to `keepgoing dock`, which works as before. `keepgoing panel list` starts nothing and works on every platform.

```sh
keepgoing panel                  # start the panel if needed, and open it
keepgoing panel --loop NAME      # open one loop's page (a name, registration ID or key)
keepgoing panel --no-open        # print a single-use address and start no browser
keepgoing panel status           # running or not, since when, the port, the idle limit
keepgoing panel stop             # stop the panel's background process
keepgoing panel list [--json]    # every loop and its state in the page's words; starts nothing
```

What happens:

1. If the panel's background process is running, it is reused. Otherwise one is started and the command waits, up to ten seconds, until it answers. Two commands run at the same moment end with one process.
2. The command asks that process, over a private local socket, for an opening. The panel writes a small page into its private directory (mode `0600`, a random name) whose only content sends the browser to an opening address, `http://127.0.0.1:PORT/open/<code>`. The code works once and for 30 seconds.
3. The page is opened with the default web browser, found as the application that handles `http` links: on macOS through LaunchServices, on Linux through `xdg-settings get default-web-browser` and, failing that, the handler for the `http` scheme. It is never opened by whatever application opens `.html` files, which for many people is an editor and would show the page's source. The browser is given the path of that page and nothing else. No address, code or session is in any program's arguments, where other users of the computer could read them. `LOOPTENDER_PANEL_OPENER` names another program to use (also read under its [earlier name](configuration.md#environment-variables)).
   If no browser can be determined, nothing is launched and no launch page is written: the command prints what `--no-open` prints, and says why.
4. The browser loads the page and follows it to the opening address. The panel exchanges the code for a session cookie, deletes the launch page, and sends the browser on to the panel. The launch page is deleted after 30 seconds whether or not it was used. A used or expired address, or a page address whose session has ended, shows a short page that says to run `keepgoing panel` again and has a button that copies the command.

`--no-open` prints the opening address instead, for a person who asked for it. The output says that anyone who can read that terminal can use the address during its 30 seconds.

The page's footer says which command is serving it and when that stops: `keepgoing panel` and its idle limit, or `keepgoing dock` until Ctrl-C.

**When it stops.** The panel's background process stops by itself 30 minutes after the last page stopped reading from it (`--idle`, `1m` to `24h`). A page left open and visible reads every few seconds, so it keeps the panel running for as long as it is shown; a hidden tab does not. `panel status` says "in use" when a page read within the last minute, and otherwise how long is left. Asking for status, the two static files of the expired-link page, and refused requests do not count as use. It also stops on `keepgoing panel stop`. It is never started at login or by any other command. **Starting it, using it, leaving it idle and stopping it never affect supervision**: it reads what the page reads and writes only its own runtime files.

**How a running panel is known.** The panel's process holds a lock (`panel-server.lock`) for as long as it lives; the operating system releases it when the process ends, however it ends. That lock, and nothing else, says whether the panel is running. `panel.json` is there for a person to read and is never relied on: if it is deleted, damaged or replaced while the panel runs, the commands still find the panel through its socket and the panel writes its record back. Files a crash left behind are removed by the next command; nobody is asked to delete a file. A panel process that holds the lock and does not answer (starting, stopping, suspended or stuck) is reported as "running but not answering", never as "not running", and no second one is started beside it. A command waits three seconds for the panel's answer, and the panel ignores a request that reaches it more than two seconds after it was sent, so a stop that was reported as failed is never carried out later.

Two panel processes cannot persist. Each removes only the socket and the record it created itself, so one that is exiting cannot take another's away. Each also checks every few seconds that the lock it holds is still the file at that path; if the lock file was deleted or replaced, it exits, leaving whatever another has put in the directory.

**What the panel's process is started with.** A small fixed environment (a fixed `PATH`, and only `HOME`, the user name, the temporary directory, locale, time zone and the XDG directories from the caller), its private directory as its working directory, the null device for its input and both outputs, and none of the caller's other open files. It keeps no log.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--loop NAME` | none | Open that loop's page. A name shared by two loops is refused; pass the registration ID. |
| `--no-open` | off | Print the opening address. It still works once, within 30 seconds. |
| `--idle 30m` | `30m` | Idle limit for a panel this command starts. A running panel keeps its own. |
| `--directory DIR` | the default registry | A non-default private registry. A running panel for another registry is refused; stop it first. |
| `--panel-directory DIR` | `looptender/panel` (or `keepgoing/panel` where the tool [already keeps its files](configuration.md#per-user-directories) in a `keepgoing` directory; a panel server already running under the other name is used where it is) in the user state directory (`~/.local/state` or `$XDG_STATE_HOME` on Linux, `~/Library/Application Support` on macOS), beside the [session claims](operations.md#one-loop-per-session) | Where the runtime files below live. |
| `--refresh`, `--stale-after`, `--offline` | as for `dock` | Passed to a panel this command starts. |

**Earlier location.** Builds before v0.2.0 kept the runtime files in `keepgoing/panel` under the user configuration directory, which on Linux is `~/.config`. Only `keepgoing panel` (opening) and `keepgoing panel stop`, using the default directory, act on the old place: a panel server that an earlier build started there is stopped, so it is not left running unseen, and that directory's runtime files and the directory itself are removed, and they say so ("Stopped a panel an earlier build started at …"). After that the old place is not read again. A server there that does not stop is left, and the next of those commands tries again. `panel status` and `panel list` never stop anything: when they find such a server they say "A panel started by an earlier build is running at <old location>; keepgoing panel stop ends it.", and their JSON carries `earlierBuildPanel` with that location. They create nothing at the old place. On macOS both places are the same and nothing moves.

**Runtime files.** They live in one private directory (mode `0700`, never a loop's directory, never inside a Git checkout): `panel-server.lock` and `panel-open.lock`, `panel.sock` (the private socket, created private), `panel.json` (mode `0600`: process ID, port, start time, idle limit, registry directory, version and an instance ID), `clients.json` and `clients.lock` (the clients' version reports, written only by a command given `--client`; see Versions) and, for up to 30 seconds each, launch pages named `open-<random>.html`. Only a launch page holds anything that can open the panel, and only until it is used or expires. If the directory's path is too long for a local socket on this platform the command stops and says so before creating anything; it never falls back to anything weaker.

**`keepgoing panel list --json`** returns `schemaVersion: 1`, `readAt`, `registry`, `complete`, `allClear`, `needsYou`, `unread`, `omitted` and `loops`, each with `key`, `name`, `state`, `phrase`, `needsYou` and `can` (the changes that fit its control flags, as the page offers them), and `overdueSince` (the time of its last check) for a loop that needs a person because its [checks have stopped](operations.md#external-schedulers-can-stop-on-their-own); a new value is a new gap. It is built by the code that builds the page, so the words match, and it carries no registration ID, thread ID, path or note. `key` is what `panel --loop` accepts. It exists so that another local program, such as the menu-bar item, can list loops without a second copy of the wording. `panel status --json` carries the footer's `exposure` statement for the same reason, and [`client review` and `client apply`](local-clients.md#reviewed-pause-stop-and-resume-client-review-client-apply) give such a program the page's reviews and changes.

### Access through `keepgoing panel`

Everything under [Access](#access) holds for the panel's process too, with a session in place of the token in the address. Each line is a claim the tests check:

- The server listens on `127.0.0.1` only. Every request must pass the `Host`, `Origin`, fetch-metadata and `GET`/`HEAD` checks before anything else is looked at. No response the server writes carries a cross-origin header, and every one carries the content security policy, `no-store`, `nosniff` and frame denial. (A request too malformed to parse is answered by Go's HTTP library with a bare `400` before the server sees it; that reply has no body of ours and none of these headers.)
- An opening address accepts one more kind of request than the page does: a top-level navigation that began on another site, with no `Origin`. That is what a browser sends when the launch page, a local file, takes it there. A frame, an image, a script or a fetch from another site is refused there as everywhere else.
- An opening code is 256 random bits, compared in constant time, usable once, and dead after 30 seconds whether or not it was used. `HEAD`, refused requests and wrong codes do not spend it. At most 16 can be pending. A used code, an expired code and a code that never existed get the same page.
- An opening can only be created through the private socket. The command checks what the socket answers before using it: only an address of exactly the form above on the server's own port, or the name of a private launch page inside the panel's directory, is accepted.
- The session is a cookie with 256 random bits: `HttpOnly`, `SameSite=Strict`, no expiry (it ends with the browser session, or when the panel stops), scoped to the path `/s/<random segment>/`. Cookies are not separated by port, so the random segment is what keeps a browser from sending it to another local server; the segment alone grants nothing. A request carrying more than one session cookie is refused. At most 16 sessions exist; the oldest is dropped.
- The answer to a good opening address is a page that sets the cookie and sends the browser on, not a redirect. A strict cookie is not sent along a redirect that began on another site, and the launch page counts as one; a navigation started by the server's own page is same-site.
- Neither the opening code nor the session is ever in a program's arguments or environment, or in a log. The server keeps no log.
- Without a valid cookie nothing under `/s/` is served, and nothing is served at any other path except an opening address and two static files the expired-link page needs and which hold nothing private: the stylesheet (`/panel.css`) and its copy-button script (`/expired.js`).
- With a session, `api/v1/server` returns which command serves the page and its idle limit, and nothing about any loop.
- Without `--allow-changes` there is no write endpoint. With it, the [change requests](#changes-from-the-page-keepgoing-panel---allow-changes) have stricter checks of their own.

**What this protects against, and what it does not.**

- *Another web page in your browser*, including one that guesses the port: it cannot read the page or its data (no cross-origin access, strict cookie, host and origin checks) and cannot create a session (it has no opening code).
- *Another user of the same computer*: they can connect to the port, and get nothing without a session. They cannot read the private directory, so they cannot read a launch page, reach the socket or see the record. They can list your processes and their arguments; nothing there opens the panel. This has been tested with one user's processes and files only, not with a second operating-system account.
- *`--no-open`*: the address is printed where you asked for it. Anyone who can read that terminal, its scrollback or a recording of it within 30 seconds can use it once.
- *A program running as you*: not protected against, and cannot be. It can read the private directory, ask the socket for an opening, and read looptender's files directly.
- *Someone who can see your browser window*: they see what you see.

## What the page shows

In unreleased source, an explicitly superseded loop reads **Superseded** and offers no Resume action. Its retained source holds are not requests to continue it; stop remains available. The declared successor is not registered, activated or reconciled by this action. See [manual successor retirement](transfer.md#retire-an-obsolete-loop-after-a-manual-move-unreleased).

The page has two levels, so opening it never starts with a list of facts. The page's own footer explains the words it uses (loop, lane, route, account group, check, test, hold).

**Level 1, the picker.** A "needs you" line, a row for the **Routes** view, then one row per loop with its name, a status dot and its state in one phrase: supervising, waiting for you, waiting for a limit to reset, waiting for the network, checks are failing, outside its hours, paused, stopped, finished, off, not checked recently, or unknown. A loop that is on a later route says so in its row ("on its second choice"). Loops that want attention come first. The needs-you line and the Routes row stay in place and only the loop list scrolls, so neither can scroll out of reach. On a phone-width window the picker is the first screen and a pick replaces it, with a link back.

**Needs you.** With nothing picked, the page lists every loop a person should look at:

- A loop whose [checks have stopped](operations.md#external-schedulers-can-stop-on-their-own): enabled, not held, and with no check for longer than the overdue rule allows, with the sentence `status` gives and what to do.
- A loop held for a person, with the reason from the fixed cause recorded when the hold began, how long it has waited, and what to do. A read-only page cannot resume the loop, so the card also gives the command that does, `keepgoing resume -c CONFIG`, with a button to copy it, and says that `keepgoing registry list --private` lists the configuration path that replaces CONFIG (the page never carries paths). A panel started with `--allow-changes` resumes from the loop's own page instead and does not show the command. The wait is measured from the retained history record of the hold starting; when retention has dropped that record the page says "waiting at least" and counts from the last control change. A loop whose new-conversation move could not be confirmed is listed the same way.
- A loop that could not be read: its control record or check history is unreadable, its registration no longer matches its configuration, a move to another conversation is in progress, or the read was cut off. Whether such a loop is held is unknown, so it is listed as "could not be read" with what to do, never left out.

"Nothing needs you" is shown only when the registry was read, every loop in it was read, and none is held or has stopped checking. Otherwise the line counts what needs you and what could not be read.

Below that, **Worth knowing** lists loops that do not need a person but are not routine: waiting on a limit or the network, checks failing, not checked recently, outside their hours, running on a later route, or with a lane that its last check found failed, each with the recorded reason in one line.

**A failed lane.** When a loop's last check lists a lane in error, the loop is not held for it (a failed lane is reported, not held), so it is never under "Needs you". It is not routine either, so a supervising loop is not left to read as all well: its picker row adds "1 lane failed", its row under "Loops and lanes" carries a "1 lane failed" tag, its page has the same tag beside its state and says it in the line under its name, and it is listed under "Worth knowing" with a dot that is not green. Only the lanes' recorded status is used, never their text, and only from the last check's own listing: lanes kept from before a hold are not said to have failed now.

The note left with a hold, the person's own explanation of why the loop needs them, is shown by default on the needs-you card and on the loop's page, cut to 280 characters. Because a note can contain identifiers, each word shaped like one is replaced by "[hidden]": BB IDs (`thr_…`, `loop_…`, `proj_…`, `env_…`, `host_…`), absolute and home paths, web addresses, email addresses, network (IPv4) addresses with or without a port, and long codes (20 or more characters with both a letter and a digit, as keys, tokens and hashes are). Version numbers, long words and long numbers stay. Only the shape of a word is looked at; nothing is read from the note's meaning and no decision is made from it. When something was hidden the card says so and offers **Show identifiers**, which shows the note whole. The advice line under a note points at it ("See the note left with the hold, and the conversation"), so it never contradicts what the note says. `client status` still withholds notes.

**Level 2, one loop, shown through its routes.** Under the loop's name are short answers:

- **Last decision**: what the last recorded check did and why, in the fixed vocabulary of causes ("Sent nothing: lanes are working", "Sent a wake: the session was idle"), its age, and since when an unbroken run of checks has had that same result. A cause outside the vocabulary is not worded, and free-text reasons are never shown.
- **Hold**: none, or the hold in force (paused, stop requested, held for a person, finished) and for how long. The duration comes from the retained history record of the hold starting; without one it reads "for at least" and counts from the last control change. Off is a state, not a hold.
- **Route**, when the loop is not on its first route: which route it is on and since when, and why ("first choice at a usage limit until Wed 18 Nov"); the loop's row says "on its second route after a usage limit" when the first was left at a limit.

Then the loop's **preferred routes** in order: its first route (`primary`), then each route of the loop's own list in configured order. A route is a model on an account group at an effort level; its title is its model, or the tool and "default model" or "model not configured" when no model is configured, with the account group, effort and how the loop gets onto it (continues in the same conversation, moves to another conversation, starts a new one, runs without a window, runs a command) on the line below. Each route card reads top to bottom:

1. What is recorded against the route, first. Each route the loop moved off says why from the record of that departure alone: the check that moved the loop (with the error class it saw on that route: a usage limit, a failed sign-in, an overloaded provider, the route unreachable, another error) or a retarget command. The newest record wins, so a command that moved the loop after a handoff is what is said. Nothing is inferred from which errors the configuration hands off on. When the history no longer reaches a departure it says the record is no longer retained, and with no record at all that why is not recorded. Only the first route has a reported reset time, kept by the loop's state from the handoff that left it, so only the first route's card says the reset ("the provider reported a reset for …", "… has passed", "no reset time") and what is recorded about going back ("keepgoing returns after that, once a test passes", "a return was last tried …", "no return has been tried yet", "keepgoing has stopped trying to return", "keepgoing will not return by itself: returning is switched off for this loop", or, when state records no handoff because a command moved the loop, "keepgoing will not return by itself: the loop was moved by a command"). A reported reset more than 400 days away is said as "a reset more than a year away; it is not shown", not as no reset. A route whose newest usage reading shows a window used up says that, with its reset.
2. Its readiness, as a sentence tied to its last test: "Ready: passed its test 47 min ago", "Passed its test 8 h ago; a new test is overdue (it is tested every 4 h)", "Sign-in failed when tested 2 h ago", "Not tested yet", "Turned off in its configuration". A test is called overdue only where looptender would run one now; otherwise the card says why none ran: scheduled tests are off, the loop is paused, held, off, stopped, finished, outside its hours or not being checked, the network is down, the route shares the account group of the route the loop is on, which the scheduler does not test, or the day's scheduled tests for it are used up. "Test" is the page's word for what the configuration calls a probe: one small real request.
3. Its usage reading with the reading's age and each window's reset, or "No usage reading" with the reason when a record shows it (this kind of test does not report usage, a passing Codex test does not report usage, it has not been tested yet, it is turned off). Under each window that has not reset, what the recorded readings project for it at its reset, in the words `status` uses ([reading history and projection](fallbacks.md#reading-history-and-projection)), with lengths of time in the page's own style: "60% at reset (3 readings over 12 h)", "Runs out about 2 days 9 h before it resets, at …", "Used up since …", or "Can't project:" with the reason, such as "2 of the 3 readings needed in this window". When a window cannot be projected for want of readings, the card says once, below its windows, "Readings for this route come only from its probes." Today that holds for every route.

The route the loop's records name as current is marked, in one of two ways:

- **In use now**, only when the loop is on with no hold, its last check is recent, and that check recorded this route.
- Otherwise "Current route" with what is recorded: loop held, paused, stopped, finished or off, outside its hours, no recent check, or not confirmed by the last check.

The current route is not tested, so it gets no readiness verdict, only the age of its last test, unless that test recorded a failure. When another loop holds a newer usage reading or test result for the same route, the page shows it, marked as another loop's, so one route does not read differently on different pages.

**Add a route to this loop** and **Change the order** follow the preferred routes, as closed disclosures with commands to copy. The page runs nothing and changes nothing; each command is for a person to run, and each block ends "Run in Terminal, not in a BB agent thread." The loop's name comes filled in, as `--loop NAME` quoted for a shell by the server (a name too long to show in full gets the placeholder `LOOP_NAME` instead). For a loop with its own list: `looptender fallbacks add --loop NAME` (the guided setup), then `fallbacks check --id ROUTE_ID --exact --loop NAME`, said to be one real request that uses the account's quota, because a new route is used only after a check passes, and `fallbacks handoff on --loop NAME` when automatic handoff is off. Change the order gives `fallbacks move ROUTE_ID --to PLACE --loop NAME`, where place 1 is the next choice after the first; with **Show identifiers** there is one command per route with its ID filled in. For a loop that follows the [standard route list](routes.md) the same blocks give that list's `routes add`, `routes check ROUTE_ID --exact --loop NAME` and `routes move ROUTE_ID --to PLACE`, and say that every loop following the list gets the change; handoff for such a loop is set in its configuration file, which the page says in words. A value the page does not know is a placeholder in capitals with one line saying what to put there. The blocks are not shown when the loop's configuration could not be read.

**Lanes** are listed one per row under the loop's current route, the ones that need a person first: its state (working, waiting for an answer, failed) and "Lane 1", "Lane 2" and so on. looptender records a lane's state, a hashed key and, for a BB thread, the lane's title, but not its thread or which route it is on, and the page says so once under the list. A lane that is itself a registered loop is named and linked. The hashed key appears only with identifiers. The heading keeps the counts and the age of the listing. A listing kept from before a hold or pause says so in the heading and is never shown as current; one kept across a hold that a lane waiting on a person caused reads "as of the check that held it".

**Lane titles.**
- **Source:** a lane's title is the `title` BB reported for that child thread in the `bb thread show` reply the check already makes; nothing else of the lane is read, no log and no message.
- **Shown only with "Show identifiers"**, because it is the person's own text and often names a thread or a path. By default a lane row says "title withheld" when one is recorded, so the panel's default is safe for a shared screen.
- **Matched to the listing:** a title is shown only when it was recorded by the same check as the listing shown; otherwise the lane reads as having none.
- **Storage:** titles are kept in a private file, `lane-titles.json` (mode 0600) in the loop's state directory:
  - only for the lanes of the newest complete listing, at most 64, each cut to 120 characters with control characters removed;
  - an incomplete listing leaves the file as it was;
  - a title is not in the run graph, `state.json`, the incident history, `status`, the timeline, `runs`, a report or a webhook, and the macOS menu and `keepgoing panel list` never carry one.
- **No effect on supervision:** no decision reads a title, and removing the file changes only what the panel can show.

**Recent activity** lists the five newest entries of the retained history, newest first, in the same plain words: checks with their action and cause, moves to another route and returns to the first with the route's name, holds, control changes worded from the flags they changed, and reports with their recorded outcome. An unbroken run of identical checks is one line with the count and when it began. A send that returned is shown as sent with "delivery is not confirmed". It says how many entries are retained and whether older history was pruned; `keepgoing timeline` has the rest.

**Checks and limits** gives the last check and its age, the next check, limits used (messages sent, deadline, time without Git progress, new conversations created, and a provider limit with its reset time), and the device. A reached time without Git progress also says what that does: "a check holds the loop for a person and sends nothing. It stays held until a person resumes it, and resuming with no new Git change holds it again" (see [no-progress timeout](operations.md#no-progress-timeout)). The device is "This computer", with its role from a record (it runs this loop's checks when a check is recorded in the loop's state here, otherwise it holds the loop's records). There is one device, the one running `keepgoing dock`; its hostname is an identifier and is included only with identifiers.

With **Show identifiers** on, the end of **Checks and limits** lists the loop's registration and, when the loop's target is a BB thread, its configured thread with the command that opens it, `bb thread open <thread-id>`, and a **Copy the command** button. The page only copies the text to the clipboard; nothing is sent and nothing is run, and you run the command yourself. The command is built in the page from the thread ID already shown, only when the ID has the form `thr_` followed by letters, digits and underscores. BB documents no link format, so the page makes no link to BB. Where the browser cannot copy, the button is left out and the command stays as selectable text. "Copied" is announced to screen readers. With identifiers off, nothing about the thread is shown, and a loop whose target is not a BB thread shows no command.

**Which build.** The footer says which looptender build serves the page ("This page is served by keepgoing v0.1.5"), and `api/v1/server` carries the same label as `version`. A loop's page also says which build ran its last check. The label comes from that check's record in the loop's state, or, when the check recorded none, from the newest retained check in its history; a control command's record does not count. When that build differs from the panel's, the loop is listed under "Worth knowing", which is never a need.

- **Same or different:** labels are compared whole. `v0.1.5-dev.26f91db` and `v0.1.5-dev.ab565b1` are two dev builds of one release, and so different builds.
- **"dev":** the label of a build made without a version says nothing about which build it is. When either side is `dev`, whether they are the same build reads "unknown". The same is true when both share one `-dirty` label, which names a commit plus uncommitted changes that can differ.
- **Older or newer:** said only when both labels are plain releases (`vX.Y.Z`), compared by number, so `v0.1.10` is newer than `v0.1.9`. A dev build is never called older or newer.
- **Nothing recorded:** "Which keepgoing build ran its last check is not recorded".

**Versions.** The picker's Versions view, also linked from the footer's build line, lists every place a looptender build runs on this computer, as the records say. It never runs a binary to ask its version, and it fetches nothing from the network. The places are:

- this panel;
- each loop's last check, by the label the check recorded;
- each loop's daemon, by the label it reported when the panel asked it;
- each loop's service, as "the same file as this panel" or "a different file". The file is compared by content, as `keepgoing service status` does, and is never run, so its version is not known;
- the BB plugin and the macOS menu, once they report themselves, and the looptender each of them runs.

How the rows are compared:

- **The reference** is the newest plain release among the places seen now (this panel, each loop's last check and daemon) and the client reports of the last seven days. When none is a plain release, the reference is this panel's own build.
- **Labels** follow the rules above: whole labels; `dev` and a shared `-dirty` label are unknown; "older" is said only between two plain releases. A dev or dirty build against a release is "a different build", and which of them is newer is not known.
- **Advice** is given only to a place that is older than the reference, and to a service on a different file when this panel runs the newest release here. Because that file's build is not known, the service's advice is "restart the service, or install it again from this panel's build if its file is older". Advice is never given to move to an older build, or to a build whose order is not known.
- **The picker's count** is of the places known to need something: older than the reference, or a service on a different file. A build whose order is not known is shown but not counted. When every place seen now runs one dev build and the only release is a client's report, the count is zero.
- **The picker's line** gives that count ("2 places need a look, against v0.1.5"), then how many places run a different build whose order is not known ("3 run a different build; which is newer is not known") and how many cannot be compared at all, such as a `dev` build or a check with no label recorded ("1 cannot be compared"). Only when every place was compared and none needs a look does it say "All on v0.1.5", or, when the reference is this panel's own build, "Everything here runs this panel's build". It never reads as an all-clear while a place cannot be compared.
- **This panel's own row** always reads as this panel's own build, whatever its label.
- **The plugin and the menu** are compared with the versions this build expects, never with looptender's own numbers. One newer than that reads "newer than this build expects", with no advice.
- **Reports over seven days old** read "not seen in the last seven days", with the date of the last report in the page's time zone. They count for nothing.
- **No paths.** The view shows no path, of a service's file or of any executable.
- **Loops with nothing recorded** about their builds are named in one line after the list.

A loop whose daemon runs a different build from the panel, or whose service runs a different file, is also listed under "Worth knowing", never as a need. Updating from the page is planned, not built.

**How a client reports itself.** The plugin passes `--client bb-plugin@VERSION` when it calls `keepgoing status`, and the menu passes `--client macos-menu@VERSION` when it calls `keepgoing panel list`; `client status` takes the flag too. The CLI then records, in the panel's private `clients.json`:

- the client's name and version;
- the CLI's own build label;
- when it was seen.

It records one report per client name, the newest, and no path. The rules:

- Nothing is recorded without the flag.
- The panel's directory is never created for it: before a panel has run once on this computer, no report is kept.
- An unchanged report under an hour old is not written again.
- A value the CLI cannot use is ignored.
- Recording never changes the command's output or exit code.
- The file is `0600` and decoded strictly; a damaged file is replaced.
- A CLI from before this flag refuses it with a usage error. The plugin and the menu then ask again without it, and remember that CLI only if that second call succeeds.

**Known limits.** Reports are recorded in the default panel directory. A panel started with `--panel-directory X` reads `X/clients.json`, so it does not see them. The plugin and the menu report only on the calls named above, and appear only once `keepgoing panel` has run once on this computer, since a report is never what creates the panel's directory.

**Usage.** The picker's Usage row, after Versions, opens a page about what looptender did for each loop, in counted sentences; its first sentence says so, and points to the Routes page for allowance readings, which are a different thing. The row's second line is a plain count of the week's withheld wakes across loops ("42 wakes withheld this week", "Nothing withheld this week"), with a neutral dot. Nothing on the page is a warning.

The page words the `savings` and `contextWindow` values of `status --json` (see [Savings figures](operations.md#savings-figures) and [Context window reading](operations.md#context-window-reading)); it computes no figure of its own. "This week" is their period, the last 7 UTC days, today included. For each loop it says:

- how many checks sent nothing, by reason ("173 checks sent nothing: 150 because the loop is held for a person, 23 because lanes are working");
- how many wakes were withheld, by reason, or "No wakes were withheld this week.";
- how many messages it sent, by kind ("6 messages sent: 3 wakes, 2 nudges, 1 retry"), and that each is a turn looptender added; and how many checks' messages carried the supervision context, when any did;
- the spaced wakes, or "unavailable" when the loop's records hold no count of them;
- the estimate, as a range with its baseline in the sentence: the withheld wakes times the lowest and the highest context reading of the week, against a plain timer that wakes the session at every check, "estimate" in the sentence and never tokens billed; or why there is none;
- under **Context window** (BB threads only): the latest reading and its age, the week's lowest and highest, the last readings, each session reset with the size before and after where recorded, and a table of the week's readings by UTC day.

A reason left out of a breakdown has a count of zero; a total sentence keeps its zero. When retention removed records from inside the week, every count and the estimate say "at least" and one sentence says why. A loop whose history cannot be read, or whose status cannot be read, says so instead of counting. A loop on another agent says it reports no context window size.

The page opens with the plain total across loops (checks that sent nothing, wakes withheld, messages sent, session resets), the one sentence on how a withheld wake saves anything (a timer would have woken the session at each such check, and each wake starts a turn that re-reads the conversation), and what looptender does and does not do about context today: withholding a wake keeps a turn out of the conversation; `prompt_goal_on_change` can send an unchanged goal as one line; a session reset is something a person runs; the reset at a threshold is a preview only, and this build never resets or compacts by itself. One sentence says that in an ordinary chat nobody is woken, so looptender adds messages there. There is no headline number, no money and no percentage of savings.

Each loop's page ends with a short block of that loop's three or four sentences (withheld wakes, messages sent, the estimate, the latest context reading) and a link to the Usage page.

The next check is shown only when the loop's own looptender scheduler answers. Otherwise it is unknown and says why: no looptender scheduler is on record for the loop, a recorded one did not answer, or it was not asked. It is never inferred from the cadence of past checks.

Only `keepgoing start` records a scheduler that the panel can ask. A loop whose checks are started by `keepgoing tick` from elsewhere (a BB automation, cron, a service that runs `tick`) has none on record, even while it is checked every few minutes. So that this does not read as "nothing is checking it", the line then says what the loop's state shows of past checks: "Unknown · the last check ran 1 min ago, and recent checks ran about 10 min apart (past checks, not a forecast). No looptender scheduler is on record to say when the next is due." The gap is the overdue rule's usual interval (the second longest of the last five gaps between checks inside the operating window), given once three gaps are recorded, also while the loop is held. Without three gaps the gap is left out; a last check started by a BB automation adds "started by a BB automation" (never its ID). Only a loop never checked (or whose last check is dated ahead of the clock) reads "no looptender scheduler is on record for this loop, and one outside looptender is not visible".

**Routes, across loops.** First an **Allowances** strip: one line per account with the newest usage reading recorded for any of its routes (each window, how much is used, when it resets, what the readings project for it at its reset or why they cannot, and how old the reading is; when a window cannot be projected for want of readings, "Readings for this account come only from its routes' probes."), "No reading" where none exists, and a note when a loop left one of its routes on a usage limit whose reported reset has not passed. That note, and the shared-limit note on a route below, give the reset the provider reported with the limit; when it reported none, the reset of the used-up window in the usage reading shown with the note, said as the reading's ("no reset was reported with the limit, and the usage reading shows the 7-day allowance resetting …"), or, when the reading shows resets but no window used up, that the reading shows when each window resets. "No reset time was reported" is said only when neither gives one. An account is what selects the account a test reads, not the account group's name alone, so two accounts configured under one group name are two lines, numbered. A route's account counts as identified only when its test settings select one (an environment or an executable of its own) or a usage reading was recorded under it; a route without either is a line of its own, marked "which account is not identified by a test", and is never said to share anything. When two or more lines share a provider and a name, each says what its routes are after its route count (the kind of route, such as a BB thread or the Codex CLI, and the model and effort when configured; at most three, then how many more), so they can be told apart without being said to be, or not to be, the same account; the "account N of M" numbering counts only identified accounts. Then every route once, those at a limit or not ready first, using the same route component: what is recorded against it (each loop that moved off it, in that departure's own words; a used-up window; or that it shares its account with a route a loop left on a usage limit, which does not change its own readiness and is said only when both read the same account), its readiness, its usage, and four lists: loops running on it now, loops for which it is the current route but that are not running, loops keeping it on standby with which choice it is for each, and loops that list it but whose current route could not be read. A line above counts the routes, those a running loop is on now ("2 running now"), and those at a limit or not ready. A route is what the configuration says: looptender does not verify which account or model a BB thread is actually using. Two accounts configured under one group are two routes, because a route is keyed by what selects the account a test reads; they can carry the same title. Lanes are not attributed to a route in this view.

Throughout:

- **Unknown and stale are shown, never guessed.** A missing record reads "unknown", "none recorded" or "no check recorded". A lane listing the last check did not refresh is labelled stale. A check's age is said in plain words ("Checked; sent nothing · 45 min ago") with no badge: a loop that is on and has no check for longer than `--stale-after` is in its own state, "Not checked recently", and an old check of a paused, stopped or held loop is expected. A usage reading's age is said in plain words ("Read 32 h ago") and is not a warning on its own: a reading older than `--stale-after` gets one amber line, "This reading is 32 h old and was near its limit; it may have changed.", only when it leaves a real question open: a window that had not ended was at 70% to 99% when read, no window that had not ended was used up (100% or more), and the route or account it is shown on is not already at a usage limit. A used-up reading cannot have improved before its reset, which the page already gives ("Used up since …", "Resets in …"), and a place at a limit already says so, so their age stays in the neutral read line. A usage window that has reset since its reading says "That window ended" and is drawn empty, not full; a reading whose windows have all ended carries the neutral label "Windows ended": a reading can be old and still describe current windows, or recent and describe windows that have all ended. The header says when the panel last read its records ("Records read at 12:46 · read again every 15 s"), which is not how new they are; when any usage reading shown is older than `--stale-after`, it adds the age of the oldest ("oldest usage reading 1 day 3 h old"). Every sentence is assembled in Go and the page only shows its times in the browser's time zone (with the year when a time is more than twelve months from the read), so table tests tie each sentence to the record that produces it, and wording that says more than its record fails.
- **No identifiers by default.** Thread IDs, registration IDs, paths, this computer's hostname, lane keys, lane titles and the configuration's names for routes are not in the page by default, and are not in the data it loads. A hold's note is shown by default with each word shaped like an ID, path, web address, email address, network address or long code replaced by "[hidden]" (see Level 1). **Show identifiers** (the header button labelled **Identifiers**; its full name, "Show identifiers, lane titles and notes", is its tooltip and what a screen reader says) adds them and shows notes whole; turning it off hides them at once, without waiting for the next read. Paths of looptender's own files, lane thread IDs, standby thread IDs, account directories, commands, credentials and logins are never included. Account group names from the configuration are shown, and so are the ID and label of an accounts catalog entry a route states, as in the Accounts view.
- Nothing on the page acts. **Read now** reads again; **Pause updates** stops the periodic reading until it is turned off. The header keeps these buttons on one line at every width: when room runs short the status line takes its own row first, past two lines it is cut (its tooltip has it whole), and only on a phone-width window do the buttons show icons alone. A longer status line does not move the buttons or the page.

The page follows the browser's light or dark setting and text size, works with a keyboard, and reflows to a phone-width window. With JavaScript turned off it says that it needs JavaScript, and shows no header buttons and no "Reading…" line, since neither could work. Choosing another view, or the skip link, moves the keyboard focus to the view's heading, marked by the page's focus ring when the keyboard is in use. When it reads again, the keyboard focus, the scroll position and any text being selected stay where they were. A screen reader is told when what needs you changes and when a read fails, not the time of every read. A failed read is said once, when reads start failing, and once more if they keep failing for two minutes, not on every read that fails again. Its notice sits over the foot of the window, so it does not move the page. It stays neutral while the page tries again by itself; after two minutes of failed reads it turns to a warning, says since when reads have failed, and says how to start the command again.

### The no-progress flag

A loop that keeps being prompted without results is flagged. The flag is built only from recorded facts: the transcript is never read, and no quality is judged. It informs a person and does nothing else. It is never "needs you", never stops "nothing needs you" being said, is never sent in a report or webhook, and never pauses, moves or stops anything.

**Where it shows.**
- The picker: a flagged loop's row says "no progress", and it is listed under "Worth knowing" with the fact that raised the flag.
- The loop's page: a Progress card, and a Progress line under "Checks and limits".
- `keepgoing panel list --json` and the macOS menu carry the same sentence.
- `status` prints it after the label "progress flag:" (a neutral label, since the line may say "Not available"), and `status --json` carries it as `noProgress`. Both come from the same function as the panel, so they cannot disagree.

It is shown only for a loop that is on. An off, stopped or finished loop reads "Not shown: the loop is stopped" (or off, or finished).

**The window** starts when a check last saw the repositories' fingerprint change. This time is recorded in the loop's state as `progressChangedAt` and is never moved by `no_progress_timeout`'s credits for sleep or an outage. When no change has been seen since a check first recorded the repositories, that first record starts the window as a baseline, and the flag says "in at least 3 h". A loop whose state predates this field starts a baseline at its next check. A message or check made by the check that recorded the change counts as after it.

**The facts**, counted in the window:

- **Messages:** each send in the loop's run graph that the adapter returned from. That covers wakes, nudges, re-prompts sent as a prompt, notices to a parent about failed lanes, and the brief sent when the loop moves to another route or back to its first.
  - **Turn retries** count only when the provider accepted them (`retried` or `queued`), and that is read from the check's recorded answer. The run graph records every turn retry as returned, whatever the provider answered, so those attempts are not counted from it.
  - **Not counted:** a send the adapter refused, and a turn retry that sent nothing (`already_queued`, `not_in_error`, `refused`). A turn retry that fell back to a prompt (`nothing_to_retry`, `unsupported`) counts once, as the prompt.
  - **"At least":** a check that recorded a send with no matching attempt in the run graph (its write failed), a send whose result was never saved (`intent`), or a retry whose queueing was unconfirmed makes the count "at least".

  Six raise the flag by default (`progress.flag_after_sends`).
- **Errors and retries:** checks that found the session in error, or whose turn retry the provider accepted. Four raise the flag by default (`progress.flag_after_errors`).
- **Held for a person:** how long the loop was held waiting for a person in the time its history covers, worded "Held for a person for 2 h 10 min of the last 6 h", or "Not held for a person in the last 6 h" when it was not (a pause is a different hold and is not counted). It raises nothing. No "working" time is given, because looptender sees a session only at its checks.

**When the records are short.** The run graph keeps its newest 128 attempts, and the incident history prunes each tier and records the newest pruned time. When either no longer covers the whole window for a fact, that count is "at least N". A lower bound that has reached its threshold still raises the flag. One below it cannot say "clear", so the flag reads "Not enough history to tell: the retained history covers only the last 40 min". Held time is counted only over the covered part, and then says "of the last 40 min, which is as far back as its history covers". A run of identical checks that the history folded into one record counts each check when the whole run is in the window, and is "at least" otherwise.

**Other cases.**
- Without `progress.repositories`: "Not available: no progress source is configured".
- Before any check has recorded the repositories: "Not available: no check has recorded its progress repositories yet".
- **When the flag and the limit disagree.** The flag's start (`progressChangedAt`, in the loop's state) is written only by a check that gets as far as looking at the session. `no_progress_timeout` keeps its own record (`progress.json`), written earlier in each check. A check of a held loop, or one that stops because the limit is reached, ends before the flag's step. So a loop that was held, or reached its limit, before its state first recorded `progressChangedAt` can show "Time without Git progress: 2 days 6 h of 24 h" and no flag. The flag then does not say that no check has recorded the repositories. It says "Not counted yet: checks start this count only once they look at the session, and they do not while the loop is held" (or "…they stop before that while its no-progress limit is reached", or "no check has looked at the session since this count began"), followed, when the limit has a record, by "The time without Git progress under its limits comes from the limit's own record". The two times also differ when both exist: the limit's is moved forward for sleep and outages, the flag's never is.
- With the change time ahead of the computer's clock: "Not available: the progress time is ahead of this computer's clock".
- With any record it reads dated after the clock (the clock stepped back): "Not available: some of its records are dated after this computer's clock". No count or span measured to now could be exact then.
- A window under a minute reads "in the last minute".
- With both thresholds at 0, the flag is off.
- Thresholds must be whole numbers from 0 to 1000; `2.5` or `"6"` is refused.
- The flag reads the run graph, then the history, then the loop's state, without a lock, so it is not one snapshot. The state that sets the window is read last. A read made while a check is writing can therefore leave that check's own records out for one refresh, but it cannot make a change look like no change.
- **Rolling back** to a binary from before this flag: it drops `progressChangedAt` when it saves the loop's state, which only means the flag starts a new baseline after upgrading again. It also refuses a configuration that sets `progress.flag_after_sends` or `progress.flag_after_errors` as unknown fields, so remove them first. `examples/looptender.bb.yaml` sets both.

Not in this flag:
- **Lanes:** looptender records a loop's lanes only as counts and hashed keys, with no sends or progress of their own, so a lane cannot be flagged until lanes have their own records.
- **Allowance used:** only the newest usage reading is kept, with no history, so "used since the last change" cannot be computed.

## What it reads, and what it never does

Each read goes through the code paths of commands that already exist, and adds no evidence of its own:

| Shown | Read through |
| --- | --- |
| The list of loops | `registry list` |
| Binding checks, holds, next check | `client status --id` (including its one-second query of the loop's existing local daemon endpoint) |
| Current route, last check, limits, limit wait | `status --json` |
| Lanes | `runs --registry-id` |
| Preferred routes, readiness, usage | `fallbacks list` |
| A route the loop left: when and why | the handoff checks and retarget commands in the retained history `timeline` reads |
| The first route's reported reset, the last return tried | `status --json` (the saved handoff time, reset time and last switch-back attempt) |
| Last decision's cause and run, the hold's start, recent activity | the retained history `timeline` reads |
| Versions: each loop's daemon label | the same daemon query as `client status` |
| Versions: each loop's service file | the service record `service status` reads, and a content comparison of that file with this panel's binary; neither is run |
| Versions: the plugin's and the menu's reports | the panel directory's `clients.json` |
| Usage: what looptender did, and the context window | `status --json` (`savings` and `contextWindow`, from the same read; no check, probe, usage refresh or model call) |
| Whether the file that runs its checks may be replaced, and a replacement's progress | the loop's private `check-executable.json` and the panel's private replacement receipt, file identity, and (for a live Undo) the backup/target hashes and record schema markers; no binary is run |

It runs no provider, `bb`, scheduler or Git command, starts no daemon and writes no loop record. Build offers probe the existing daemon lock briefly; live Undo offers also verify hashes and record schema markers. Building the Versions view probes whether a panel server's lock is held, to find the panel directory, as `panel status` does; a command given `--client` probes it the same way and takes the short `clients.lock` while it writes its report. Everything is what an earlier check or control command recorded, not a live view of a conversation.

A read belongs to the command, not to the browser request that started it, so a page that reloads mid-read does not cut it short. Each registered configuration is validated once per read and at most four loops are read at once. A read is cut off after 20 seconds; a read that was cut off is marked incomplete, is never reused, lists its unread loops as "could not be read" and cannot say that nothing needs you. A complete read is reused for two seconds so several open tabs do not multiply reads. Every list and text field is capped, each list is sorted by importance before its cap (routes at a limit, then not ready, then in use; departures at a limit first; a used-up usage window is said even when it is past the windows shown), and what a cap leaves out is counted on the page: a name at 120 characters (and 240 bytes), a hold note at 280, 8 fallbacks, 24 lanes, 5 history entries and 4 usage windows per loop, 64 routes and 32 accounts across loops, 16 loops per list on a route, and the registry's own limit of 128 loops. A reply is refused above 8 MiB. The largest registry, 128 loops with every list at its cap and every name at its longest, measured about 5.5 MB with identifiers, and 6.2 MB with names of four-byte characters, with the longest projection sentence in every usage window; tests keep both under 6 MiB.

## Access

- It listens on `127.0.0.1` only, never a wildcard address.
- The address contains a random 256-bit token created for that run. A request without it, or with another, is refused. The token is not stored; a new run prints a new address.
- Requests with a `Host` other than the loopback address and port, a foreign `Origin`, or cross-site fetch metadata are refused, and no cross-origin access is granted. This also covers DNS rebinding. The page answers only to `127.0.0.1` and `localhost` on its own port, so a request that names it any other way, such as a private-network or tailnet name, or a forwarded port with a different number, is refused; reaching it that way will need an explicit setting for the names it may be reached by ([roadmap V5a](roadmap.md#panel-feature-set)), which does not exist yet.
- Only `GET` and `HEAD` are accepted. There is no write endpoint: the page cannot pause, resume, stop, start or configure anything.
- Replies are never cached by the browser and carry a content security policy that allows only the page's own script, style and data endpoint.

The token keeps other local web pages and casual local requests out. It is not protection against another program running as you, which can already read looptender's files. Anyone who can see the address, for example in a shared screen or shell history, can read the page while that run lasts.

## The data endpoint

The page loads everything from `GET /<token>/api/v1/dock` (`?identifiers=1` for the view with identifiers), except the Usage page's detail. It returns `schemaVersion: 1`, `readOnly: true`, `readAt`, `refreshSeconds`, `staleAfterSeconds`, `oldestUsageAgeSeconds` (only when a usage reading shown is older than `staleAfterSeconds`), `registry`, `complete`, `allClear`, `unread`, `needsYou`, `loops`, `routes`, `allowances` and `versions` (the Versions view: `newest`, `reference`, `summary`, `behind`, `lede`, `rows`, `unrecorded` and `clients`; each row has `kind`, `title`, `availability` and `text`, and may have `key`, `label`, `update` and `seen`, never a path). Only this endpoint builds `versions`; `panel list` and `client review` and `apply` do not. Each loop carries its current route, its ordered preferred routes (`chain.entries`, each with a `route` key, `current`, `inUse`, `effort`, `enter`, `source`, readiness, usage and resets, and `catalogAccount`: the [accounts catalog entry the route states](accounts.md#say-which-account-a-route-uses) with its check, or the derived account context of a route that states none, compared with the catalog read once per report; the Accounts view uses it only to say whether a route names each entry, as the card's `routes`: `named`, `none`, or `unknown` when not every loop or fallback was read), `away` when it is not on its first route (when it left, why, the reported reset, and whether returning is on), and its `decision`, `hold`, `device`, `lanes` and `events`; `routes` is the same data grouped by route, with `notes`, `leftBy` and `attention`, and `allowances` groups the routes by account, so every view is one feed. A held entry in `needsYou` carries the hold's `note`, masked unless the view has identifiers, with `noteMasked` true when a word was hidden; with identifiers the note is whole and `maskedNote` is the masked text, so the page can hide identifiers at once. `noteWithheld` is no longer set. A held entry also carries `resume`, the command that resumes the loop with its configuration path left as `CONFIG`. Each usage window may carry `projection`, a sentence as parts, and each usage reading `readingsNote`, `old` (true when it is older than `staleAfterSeconds`; its `availability` is `stale` only when every window it describes has reset since), and `needsPerson` with `ageNote` (only for an old reading that was near its limit and not used up or at a limit, as above). Each sentence is sent as parts (`{"t": text}` or `{"at": time, "f": "date"|"datetime"|"clock"}`) that the page joins, showing each time in its own time zone. A route is named by its opaque `route` key; the configuration's name for it (`id`, `named`) is included only with identifiers. Each loop also carries `loopArg`, `--loop` and its registered name quoted for a shell (at most 160 bytes; absent for a longer or clipped name), and its `chain` carries `standard: true` when it follows the standard route list; the page builds its commands to copy from these and adds no command per route. `enter` and `source` use the vocabulary of the planned route lists (`first`, `same_conversation`, `other_conversation`, `new_conversation`, `headless`, `command`; `loop` for the first route and `own` for the rest, until a shared standard list exists). Ages are computed against `readAt` when the report is built, so the page does not depend on the browser's clock or on being opened on the same computer. Consumers must check `schemaVersion`, tolerate added fields, and show an all-clear only when `allClear` is true. An unreadable registry is `registry.availability: unavailable`, never an empty list of loops. The schema is in v0.2.0 and may change before a release.

The report's `usagePicker` is the Usage row's line. The Usage page and each loop page's usage block read `GET /<token>/api/v1/usage`, which takes the same session check and the same refusals as the report, accepts only reads, and is worded by the same read as the report, encoded on its own: a request within two seconds of a read reuses it, and otherwise starts the same read. It returns `schemaVersion: 1`, `readOnly: true`, `readAt`, `complete`, `lede`, `period`, `total`, `mechanism`, `context`, `chat` and `loops`, each with `key`, `name`, `brief`, `did`, `window` and `days` (`day`, `lowest`, `highest`, `last`, `window`, `estimated`), all already worded. It carries no identifier and no path. Each breakdown names at most 6 reasons and sums the rest, each loop lists at most 8 session resets and counts the rest, and the days are the week's; a reply is refused above 2 MiB, and the largest registry with every list at its cap measured about 0.6 MB.

## Changes from the page: `keepgoing panel --allow-changes`

```sh
keepgoing panel --allow-changes
```

A panel started this way lets the page pause, stop and resume one loop at a time, and replace a loop’s own build through the separate review below. Unreleased source also offers the [route configuration editor](#configure-routes-in-the-panel), subject to the retained startup caller’s authority. Codex cards also offer an explicit reviewed subscription-quota read. Without the flag the panel is read-only, and `keepgoing dock` is read-only for good. `keepgoing dock`'s token is in its address, which ends up in terminal scrollback and shared screens, and it runs with its terminal's environment, so it would act as whatever BB thread it was started in. A running server never switches mode. `keepgoing panel --allow-changes` against a read-only server says so and asks for `keepgoing panel stop` first, and `keepgoing panel` against a server that allows changes asks the same. The footer says which mode the page is in, for how long changes have been allowed, and what that exposes: "Changes allowed for 2 h: started with keepgoing panel --allow-changes at 14:02. While changes are allowed, anyone using this window can pause, stop or resume loops, replace their check builds with this panel's build, edit routes within the startup caller's authority, and read selected Codex subscription quota. keepgoing panel stop turns them off." The command's output and `keepgoing panel status` say the same. With changes off the page shows no controls at all.

**Pause all.** On the overview, while any loop can be paused, **Pause all…** reviews every registered loop at once. The review names each loop with a one-line effect ("example-api (Supervising) will be paused.", "example-site is already paused."), up to eight, then "And N more."; then the total ("Pauses 3 of 4 loops; 1 is already paused."), what pausing does, and when the review expires. It never repeats each loop's whole review. Confirming changes each loop on its own, through the same function as a single pause, and only if that loop's control record is still the one reviewed: a loop changed in between is not changed, and the rest are. The result names each loop's outcome, those not changed first, and a partial result never reads as success: "Paused 2 of 3; 1 needs a new review." A loop that could not be read or was in the middle of a check is named as not paused, and not counted as changed.

**Stop all.** Beside it, while any loop can be stopped, **Stop all…** reviews every loop the same way, with each loop's own stop options (as its own stop review would offer them) behind one line each, for example "example-api (Supervising) will be stopped; 2 of its lanes are running." It then says which stops are carried out when you confirm and which at each loop's next check, and offers **Stop the Conversations**, or, when any loop has lanes, **Stop Them and Their Lanes**, which stops the lanes of the loops that have them as each loop's own stop would and the others' conversation alone. Each loop is stopped at once only where its own review said it can be. Deleting messages queued in lanes is never offered for every loop at once: the review counts them and says to stop that loop on its own. Every reviewed stop is saved first; only then are the stops that can be carried out at once carried out, in the time left, so running out of time leaves a stop to the loop's next check and never leaves a loop unsaved. The result says which loops were stopped now and which wait for their next check ("Stopped 2 loops now; stop requested for 1, which its next check carries out."), and, for each loop stopped now with its lanes, whether every lane was reached.

**On a loop's page**, Pause, Stop and Resume appear as the loop's control flags allow: Pause when it is not paused, Stop when no stop is requested, Resume when any hold is set. Each opens a review before anything changes. The review is built from a fresh read and says only what the records support:

- **Pause:** "keepgoing sends it nothing until it is resumed. A check already running finishes. Its route, note, other holds and ownership do not change."
- **Stop:** in v0.2.0 the review offers the stop's options, one button each, in the same words as the menu's ([stop options](local-clients.md#reviewed-pause-stop-and-resume-client-review-client-apply)): **Stop the Conversation**, **Stop It and Its N Lanes** when lanes are running, and **Stop It, Its Lanes and Their N Queued Messages** when messages wait in lanes that have not started; that last one deletes those messages, whose text is not kept, and only it does, ([stopping the lanes too](operations.md#stopping-the-lanes-too)). Either is carried out when you confirm, as a check would carry it out, within the 20 seconds a change is given; what cannot be reached in that time, or a stop that meets a running check, is left to the next check, and the result says so. The released v0.2.0 page acts with no BB caller. In unreleased source, the startup BB caller, if any, is stopped last when the stop includes it. A confirmation can only choose an option its review offered.
- **Resume:** exactly what it will do, from the same function the resume saves with: which holds it clears (paused, stop requested, waiting for you, finished, and the note left with a hold, whose text is not shown); that it removes the record of a question that was open for a person when the loop was marked finished, when there is one; and that it starts a new error episode, so the re-prompt and nudge counts start over, when the hold was raised because they were spent. It also says that resume does not turn supervision on or start a turn, the loop's current route, and what is known of the next check; if supervision is off, that resume will not turn it on. A resume's own ticket is prepared only when you confirm, so opening a review, or cancelling one, never cancels a resume another client prepared; confirming does, and the review says so.
- **A loop owned by a conversation:** when the loop's records name an owning conversation, the stop and resume reviews say so without naming it: "This loop is supervised for a conversation. Resuming here clears the holds that conversation or keepgoing set; it does not answer any question the conversation asked."
- **The record reviewed:** every fact a review states about the loop's holds and ownership comes from one read of its control record, taken under that record's lock. The confirmation is bound to that record: if it has changed by the time the save runs (a pause from a terminal, a check that held the loop), nothing is changed and the page asks you to review again. This is checked inside the control lock, for all three actions.
- **Expiry:** each review ends with when it expires: 2 minutes for pause and stop, 5 for resume.

Focus starts on Cancel, and Escape cancels. The confirm button names the action and the loop. The outcome is shown in the page and announced, and says only what the save evidence supports:

- **Saved:** "Paused: keepgoing will send nothing; a check already running finishes." "Stop requested: the next check carries it out." "Resumed: holds cleared; the next check decides what to send."
- **Refused:** a busy loop reads "keepgoing is in the middle of a check on this loop. Nothing was changed." Any other refusal gives its fixed reason, then "Nothing was changed."
- **Review again:** a loop that changed since the review, or an expired or already-used review, says to review it again.
- **Uncertain:** an unconfirmed save, or a reply that never arrived, reads "keepgoing could not confirm whether … Check its state before doing anything else." A confirmation the panel refuses (a `4xx` answer, which it gives before doing anything) reads "The panel refused this request before doing anything. Nothing was changed."

Nothing is retried by itself; a new attempt is a new review. A saved hold is not the provider acting.

**What runs.** The panel calls the same functions as `client pause|stop` and `client prepare-resume` followed by `client resume` ([registered controls](local-clients.md#registered-pause-and-stop)), in its own process. So a change takes the same locks, makes the same checks and refusals, and records the same history. The released v0.2.0 panel acts with no BB caller, including when opened from BB; it can resume a loop another conversation owns without taking over ownership.

**Unreleased source caller parity:** the panel retains the exact `BB_THREAD_ID` that started the background panel, or no thread when started outside BB. Pause and stop may still place a hold on any registered loop without taking over ownership. Resume checks that retained caller before offering a review and again under the resume locks: an agent cannot resume a loop owned by another conversation; the owning conversation and a person outside BB retain the CLI behavior. The private resume ticket binds the same caller. A stop carried out immediately stops that caller last when it is included, and Stop Every Loop carries out that caller’s own loop last. Later openings of the same caller class reuse the original startup identity, even when another BB thread opens the page; they do not adopt the opener’s thread. Stop the panel and start a new process to change that identity. This is environment-based CLI parity, not human authentication. A resume of a hold looptender raised for a spent allowance starts a new error episode, as `client resume` does. The resume ticket never leaves the server. The history records each change as `panel_pause`, `panel_stop` or `panel_resume`, and the page words it "from the panel". The origin is part of the operation's value, not a separate field, because the history is decoded strictly: a binary from before such a field would refuse the whole history. **An older binary reads these values as "unavailable", and overwrites them on its next write to the history**: it saves the record back with the operation "unavailable" and without a resume's new-episode marker, so that record no longer says the change came from the panel or started an episode; its holds before and after are kept. After upgrading, restart any looptender daemon or scheduled check that still runs an older binary.

**Replacing the build that runs a loop's checks.** A panel started with `--allow-changes` by a person can replace a loop's own copy of looptender, after a review and confirmation. The file must be inside the directory containing that loop's configuration, after resolving symlinks. Shared installs (`~/go/bin`, `~/.local/bin`, GOBIN, asdf, mise and package trees) are updated with the tool that installed them. A file named by another registered loop is refused, with those loops named. The selected registry and relevant default registries are all checked, with duplicate references collapsed and unreadable registries refused.

- At startup the panel opens its running executable and retains that handle, its SHA-256 and its build label. It copies only from that handle, checking the hash again. Renaming another build over the source path or re-pointing a source symlink cannot change what it installs. If opening or hashing fails, this feature is unavailable.
- Each check privately records its startup-bound executable's path, build label, size, modification time, device, inode and link count in `check-executable.json`. The process retains its executing image and SHA-256 from initialization, verifies that image and pathname before recording, and omits a mismatched record rather than labeling a newly installed file with old running code. Unchanged identity and label cause no rewrite. The offer requires the file still to match that identity. If it changed since its last check, the page says its build is unknown until another check. Dirty or unversioned builds and downgrades are refused.
- **Older builds never write this record.** An existing loop must have its file replaced by hand once with a build that records it before this feature can be used. A check on an older build does not add it.
- Symlinks, hard-linked files, files not owned by the current user, the panel's own file, shared and package-managed files, and a loop with a running daemon or service are refused. The daemon's actual lock is checked even with `--offline`, including a stopped or unresponsive daemon. Windows is refused; the panel does not start there.
- Build actions are neither offered nor accepted when the panel process has `BB_THREAD_ID`. The background panel retains that startup identity. Change-enabled panel openings must match its immutable startup caller class: a person-started panel refuses agent openings, and an agent-started panel refuses person openings. Missing/old opening or server authority is refused. Automatic legacy-location retirement also refuses an ambiguous or cross-class change-enabled restart. Explicitly stop the panel and start a new process from the intended caller to change that class; existing agent sessions are never promoted. Pause/stop hold authority stays broad; in unreleased source, resume and immediate-stop ordering use the retained startup caller described above.
- Read-only pages carry no build action. A panel started with changes allowed can offer it; `keepgoing dock` and a panel without `--allow-changes` cannot.
- The review alone names source and target paths, shortening a resolved home directory to `~`. Preparation and confirmation bind the loaded configuration to the registered loop name and canonical state directory. Confirmation rereads that binding under the locks after it waits up to **10 seconds** for a running check, holds the tick and daemon locks, and reviews again. A daemon starting during the wait refuses the change.
- The binary, configuration and state (at most 64 MiB) are backed up under the panel's private `build-replacements/<key>/upgrade-backups/<time>/`, not the loop's state directory. The replacement receipt is also private to the panel. The newest three backups plus any older backup needed by a live Undo are retained; failed swaps remove their partial backup.
- The swap creates and flushes a file relative to an opened, verified directory and renames relative to the same directory handle. It rechecks the directory and target identity first. A substituted ancestor cannot redirect the swap. Nothing is downloaded or restarted; the next scheduled check runs the replacement.
- **Undo restores only the binary.** It does not write, remove or restore controls, pause/stop, sends, goals, daemon or service records, check records, or history. Its review says “the file backed up at” the recorded time; it does not infer an old build label. It verifies the backup hash, replacement identity and current hash, and applies the same file, service, daemon and lock refusals as replacement.
- Undo is available for **seven days**. It refuses if a JSON record's format marker differs from the backup, or if formats cannot be checked. The replacement stops contributing to “checks have stopped” after seven days. Nothing rolls back automatically.
- **Manual recovery:** stop checks, the scheduler and any daemon first. Copy the current configuration and state somewhere safe. If the previous binary cannot read the current record formats, restore the binary together with compatible backed-up configuration and state. Restoring the state discards later sends accounting, goals, holds and history, so inspect and reapply needed changes before restarting. The private backup is for this explicit recovery, never automatic state rollback.
- Replacement records `panel_replace_build` in history, without a path or build label. Undo adds no history record. Older builds render unknown operations as unavailable. Build actions remain unavailable through the menu, `client review`/`apply`, and the every-loop endpoint.

**Each change request must pass every one of these checks:**

- Two `POST` requests under the session path: `api/v1/loops/<key>/prepare` returns the review, and `api/v1/loops/<key>/confirm` performs it. The page names a loop only by its key; the server finds the registration.
- `Host` exactly `127.0.0.1:<port>`, and `Origin` exactly `http://127.0.0.1:<port>`: the only address the session cookie is sent to. `localhost` is refused for changes.
- `Sec-Fetch-Site: same-origin` with a fetch's mode and destination. A browser that sends no fetch metadata can read the page but cannot change anything.
- A body of at most 4 KiB, with its length stated (a chunked body is refused), that names one supported action (`pause`, `stop`, `resume`, or one loop’s build action): one key, spelled exactly so (JSON field matching in Go ignores case, so this is checked by hand), once.
- Exactly one valid session cookie.
- An anti-forgery header. For prepare it carries a value derived from the session, which only `api/v1/server` hands out, and only to a session. For confirm it carries the single-use value the prepare returned. That value is bound to the session, the loop and the action, is spent on its first use whatever the outcome, and expires with its review. At most 32 reviews are pending.
- `GET` and `HEAD` still never change anything. A panel started without `--allow-changes` routes no write request at all. A `POST` to a panel without `--allow-changes` gets `403`, the same answer as a change request that fails a check, so a request without a session cannot tell the two modes apart.

**What this does not protect against.** Everything under [what this protects against](#access-through-keepgoing-panel) still holds, with one more consequence: *anyone who can use your browser window, or who uses a `--no-open` address within its 30 seconds, can change loops*, not only read them. Use `--allow-changes` only when you mean to, and `keepgoing panel stop` when you are done. A program running as you can change loops anyway, with the same commands.

## Planned slices

This is the first slice. The rest is not implemented.

1. **Read-only view** (this slice): the picker, needs you, one loop through its routes with its last decision, hold, lanes and recent activity, and routes across loops with each account group's allowance. A lane's title is recorded privately for a BB thread and shown with identifiers (see above).
2. **Controls**: pause, resume and stop from a loop's page are built ([above](#changes-from-the-page-keepgoing-panel---allow-changes)), through the existing registered commands' own functions. Pause all and stop all are built (below). Still planned: other picker commands. The page will not get its own control logic.
3. **Configuration**, starting with a loop's routes: their order, switching one off or on, and whether the loop follows a shared standard list or has its own; later limits, the usage-switch threshold and which reports are sent. It will be a front end to the existing guided commands (`fallbacks add|move|remove|handoff`, with their `--dry-run --json` previews, confirmation, ownership and transfer guards and comment-preserving edits), never its own logic or its own file writes. The route rows already carry a position and an opaque route key for this. Credentials and logins will never be shown or edited.
4. **Actions on many lanes at once** from the routes view, such as moving every lane on one route to another or changing their effort level. This depends on two things looptender does not have: lanes as first-class records (today it knows a loop's lanes only as counts and hashes, and never changes them), and a group (a coordinator and its lanes) so one action can apply to all members.

A macOS menu-bar preview now opens the panel and lists a row per loop with its state in the page's words, and pauses, stops and resumes a loop after a native confirmation that shows the page's own review; see [its README](../integrations/macos/README.md). It also offers Pause All Loops… and Stop All Loops…, each after one review of every loop. Still planned: commands in it such as new loop, a tray item on other platforms, installation and start at login, and answering permission prompts.

**Remote access** (reading and steering loops from a phone or another computer) is planned and not available. The slice is built so it can be added without a rewrite: all data comes through the one versioned endpoint, the access check is a replaceable layer separate from the network checks, the layout works at phone width, and nothing in the page assumes it runs on the same computer. Remote access will need real authentication of a person or device, transport encryption, and an explicit opt-in before it listens beyond loopback. No control will be exposed remotely before that authentication exists. Do not expose this slice through a tunnel, a reverse proxy or a port forward: the per-run token is not designed for that.

## Verification

`go test ./cmd/looptender -run Dock` covers: the listener's address; refusal of a missing or wrong token, a foreign or rebound host, a foreign origin, cross-site requests and every write method; the report built from a fixture loop in each state, including three loops on their second route (the first route's reset ahead, passed with no return tried, and not reported) and a route whose tool, provider and account group are the same word; a table from each record to the exact phrase shown; that "nothing needs you" requires every loop to have been read; that a read is not tied to a request and an incomplete read is not reused; that only a loop that is on, unheld and recently checked on a route counts as in use; each route a loop left attributed to its own record, with the time of its own hop (a retarget alone, a sign-in failure then a usage limit, a usage limit with a reset then a sign-in failure, a retarget newer than the handoff, pruned history), and no promised return after a command moved the loop; the shared-limit note and allowances matched on a recorded account identity, and nothing shared without one; allowance lines that share a name each saying what their routes are; the reset in those notes taken from the limit, else from the usage reading, and "no reset time was reported" only when neither has one; that a test is overdue only where the scheduler would run one (not with scheduled tests off, read through the real reader, and not once the day's tests are used up); a reset more than a year away said as such; that each of five wordings the review named fails a test if reintroduced (a sign-in failure worded as a limit, "this result since" without a matching previous check, lanes from a stale listing, overdue with tests off, and a network wait over a stale check); time, ordinal and route-name rules; withheld identifiers, notes, hostname, lane keys and paths; unchanged registry, configuration and state directories after reads; caps on notes, lanes, history entries and routes, each applied after sorting by importance and counted; the 128-loop maximal registry under 6 MiB, also with four-byte names; and exit on interrupt. All of it uses fictional loops and local files.

`go test ./cmd/looptender -run 'TestVersion|TestBundled|TestClientReports|TestClientFlag'` covers the Versions view and the clients' reports:

- each label against a reference, as exact words, including a dev build against a release and the newest release across labels;
- the plugin and menu comparison, including one newer than this build expects;
- the review's case of dev builds everywhere with an old client report: the old report is not the reference, no dev row is advised, and nothing is advised toward an older build;
- the picker's line, as exact words: all on one build, older releases, builds that cannot be ordered, and places that cannot be compared, never an all-clear while one cannot be compared;
- reports over seven days old;
- one report per client name with no path, refused values, the hour without rewriting, `0600` (not checked on Windows, where Go reports other bits), a damaged or older file replaced, and no directory created;
- `status`, `client status` and `panel list` with `--client` answering exactly as without it, and `pause --client` refused;
- a daemon or service difference under "Worth knowing", never a need;
- the view built only when asked for;
- the daemon's label kept out of status JSON;
- the bundled plugin and menu versions equal to their sources.

The plugin's `client-flag.test.ts` and the menu's `ClientTests` cover each client's flag, the fallback for an older CLI, and that a timeout, another exit code or a failed second call is not taken for a refusal.

The page itself was checked in Chromium against those fixtures (script and results are not committed):

| Check | Result |
| --- | --- |
| Keyboard only: every link and button is a tab stop, in order, with a visible focus ring; nothing focused sits under the top bar, forwards or backwards | Checked at 1280 and 390 px on the overview, a loop, a loop on its second route, and Routes |
| Focus, scroll position and selected text survive a re-read, including focus on the third of several identical links; the skip link keeps the view | Checked |
| "N at a limit or not ready, listed first" opens Routes at the first such route and focuses it | Checked |
| No configuration name for a route in the page or its data by default; route elements carry only the opaque key | Checked on every view |
| Announcements: one polite live region, which changes only when what needs you changes, when reads start failing and if they are still failing after two minutes; the read time is not announced | Checked through the accessibility markup. Real screen-reader speech (VoiceOver) not checked |
| Identifiers: none in the page with the toggle off; hidden at once when turned off while reads fail | Checked |
| Contrast of every text element, light and dark | Checked: lowest 4.8:1 light, 7.05:1 dark |
| Width: no sideways scroll at 320, 390, 768, 1280, 1920 and 2560 px, also with 120-character unbroken names | Checked |
| No loops; a hundred loops; a loop with every optional field missing; unparseable dates; a reply without a refresh interval | Checked by altering the reply in the browser: no error, no malformed text |
| Text size: follows the browser's root size (200%), no sideways scroll | Checked |
| Reduced motion: nothing moves except **Read now**'s icon, which turns slowly while a read runs; colour changes stay | Checked |
| Forced colours: status dots, the current row, pressed header toggles and the badge of the route in use remain visible | Checked |
| The picker in a 600 px tall window: needs-you line, Routes row and the current loop visible; scroll kept across a re-read | Checked |
| Pause updates: no read while paused | Checked |
| A hidden tab makes no request over four refresh intervals and reads once when shown; a read that is accepted and never answered fails after its timeout, shows the failed-read notice, and the next read recovers | Checked headless in Chromium against fixture loops, with `document.hidden` set by script; the same check against the previous page found requests from the hidden tab and a read stuck for good |
| Safari and Firefox; touch hardware; a tab hidden by the browser itself | Not checked |

An independent review of an earlier commit of the read-only page ran its request suite against a 238-entry tree that stayed byte-identical, and the project coordinator served it read-only against two live loops with their files unchanged. Not verified for this page's newer parts: a loop under live supervision, a responsive looptender scheduler's next-check time, and the command executing on Windows (the tests are compiled and vetted there, not run).

To see the page with fictional loops, build the fixture registry into an empty private directory outside any checkout and serve it:

```sh
KEEPGOING_DOCK_SCRATCH=/private/example-scratch go test ./cmd/looptender -run TestDockScratchForScreenshots -count=1
keepgoing dock --directory /private/example-scratch/registry
```

## Accounts

Unreleased source adds a persistent Accounts view backed by a private selected-context catalog. Refresh reads retained evidence only. Setup/status are explicit CLI operations; sign-in stays with the official provider tool. Names are owner labels, and login, plan, model and route-use evidence remain distinct. Each card says its source and age once when login and plan come from the same check, and says why a value is missing when nothing is wrong (a plan has not been read, the catalog records no model, no route names the entry yet) in neutral text; "Needs you" stays for no login, a failed check and a changed context. One footnote at the end names the check command, `looptender accounts check --id CONNECTION_ID --json`. Under the count, **Add an account** is a closed disclosure (open when there are no accounts) with three cases, each with its `looptender accounts add` command to copy: another subscription of a provider you use (sign in with the provider's tool in a new folder, then `--selection explicit_directory --account-dir FOLDER`), a provider that manages its own login (Cursor, BB), and an API key, which gets its own entry and is never taken. After the three cases it says once: "This names the account. It does not put it in any loop. To use it, add it to a loop's order." With no accounts the page says, in three sentences, what an account, a route and a loop's order are. The **Routes** view offers the [standard route editor](#configure-routes-in-the-panel), with a concise command-reference link. Each card lists the routes that use it, the loops on each and each route's own reading, with "… allowance was out when read … · resets …" in the summary when any route's reading had a window used up that has not ended (naming the route when there are several), else "Not out when read …". Routes without an explicit catalog binding appear separately under **Routes with unknown accounts**, with each route's own recorded reading. Only registered selections count as selected accounts in the page and sidebar. Configuration contexts, providers, models and groups do not establish distinct subscriptions or match routes to selected accounts; no inferred account match or extra-account naming command is offered for these routes. A loop's page shows a bound route as "on *Label* · assigned by you · identity not verified" and an unbound one as "Not assigned to an account." See [Accounts](accounts.md) for commands, caller/privacy boundaries and unverified live acceptance.

## Configure routes in the panel

Unreleased source adds a Routes editor for the standard preferred-route list.
Open a panel **outside BB** with `looptender panel --allow-changes`. If an
existing panel is read-only or was started by a BB thread, stop it with
`looptender panel stop` outside BB, then open a new panel there with changes
allowed. Reopening a window keeps the running panel's original caller.
`looptender dock` remains read-only. This is the existing environment-based
caller rule, not a new human-authentication mechanism.

Choose **Read current configuration**, then add a route using an existing
Claude Code or Codex catalog selection. The provider comes from that selection.
A catalog association is your statement; it does not verify the account used
by a live thread, model access or available quota. Conversation routes require
an externally managed selection and keep the conversation's existing login.
CLI routes use an inherited or existing-directory selection; executable and
working-directory settings still come from each loop. No credentials are
copied or accounts created.

Enter a supported model and optional effort, or explicitly request **Load models
reported by BB**. That button runs the existing bounded, read-only structured
`bb provider models` listing. Nothing loads automatically, no model is called,
and the listing does not establish an account's access. When unavailable, enter
names from the provider's supported settings. A listed model offers only the
effort levels present in that response; an omitted effort keeps the provider's
default. Headless Codex routes do not support an effort override.

Choose whether the route continues a BB conversation, may start a new one at
handoff, or runs a CLI. A new conversation requires explicit route permission;
each loop must already allow it, and existing creation caps, spacing and holds
still apply. Choose which recorded limit the route shares, or supply a new
label after checking the relationship yourself. Labels do not establish
independent subscriptions or quota. Route identifiers are generated internally.

Every add, move, on/off or removal has a review showing the resulting order,
affected following loops, skipped routes and recorded-health warnings. Save
uses the same validated in-process route writer as the CLI, with its private
file rules, locks and history. Reviews expire after two minutes and are single
use, session-bound and tied to the list, account selection and affected loop
configuration. A concurrent change requires a new review. A lost save response
has an unknown outcome; it is never retried automatically. Read the configuration
before preparing another change.

Adding to the standard list does not enroll other loops. Select up to 64 eligible loops per review
under **Loops following this order**, review, then save that separate change.
Each loop keeps its existing handoff setting, holds, budgets, ownership and
conversation-creation opt-in. Each save uses the existing follow-standard
writer and its own lock. This is **not an atomic batch**: a refusal stops the
remaining saves; the result distinguishes changed, refused and unattempted
loops. Earlier successful saves remain. Loops that cannot follow now say why.
A BB-started panel offers no standard-list editor, even if its caller owns a
loop. Loop-specific custom-list editing remains a CLI capability.

The command reference remains in [preferred routes](routes.md).

### Selected-context subscription quota

Accounts cards keep Codex subscription quota separate from configured route
observations and token totals. Page refresh reads the private cache only. A
panel started with `--allow-changes` offers **Refresh subscription quota…**
inside an expanded Codex card. Review and confirm the selected context before
it contacts that CLI. The review is session-bound, expires after two minutes
and is single use. No route, login, model request or loop control is changed.
The same operation is available as `looptender accounts usage --id ACCOUNT_ID`.

Successful readings show their source, original read time, primary/secondary
window percentages and any reported durations/resets. A failed refresh leaves
the earlier reading visible with its original age and a separate failure time;
unknown or absent windows are unavailable, never zero. See the [quota contract
and recovery limits](accounts.md#read-selected-context-subscription-quota).

The macOS menu preserves the complete validated panel-authority disclosure in
its controls confirmation, including combined route-editing and subscription
quota capabilities. It refuses empty, control-containing or over-1024-byte
disclosures instead of truncating authority text. Compact menu metadata keeps
its separate 180-character limit.
