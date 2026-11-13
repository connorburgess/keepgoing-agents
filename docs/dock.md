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

## Opening it with one command: `keepgoing panel`

`keepgoing dock` runs in the foreground and prints an address to copy. `keepgoing panel` is the ordinary way in: it opens the same page in your browser with nothing to copy. Unreleased source; tested with fixtures, real processes and a real browser on macOS. The same tests are meant to run on Linux in CI and have not yet.

**Windows: unverified, so it refuses.** `keepgoing panel`, `panel status` and `panel stop` have never been run on Windows. Until they have, they stop there with a message that says so and points to `keepgoing dock`, which works as before. `keepgoing panel list` needs no server and works on every platform.

```sh
keepgoing panel                  # start the panel if needed, and open it
keepgoing panel --loop NAME      # open one loop's page (a name, registration ID or key)
keepgoing panel --no-open        # print a single-use address and start no browser
keepgoing panel status           # running or not, since when, the port, the idle limit
keepgoing panel stop             # stop the background server
keepgoing panel list [--json]    # every loop and its state in the page's words; no server
```

What happens:

1. If a panel server is running, it is reused. Otherwise one is started in the background and the command waits, up to ten seconds, until it answers. Two commands run at the same moment end with one server.
2. The command asks that server, over a private local socket, for an opening. The server writes a small page into its private directory (mode `0600`, a random name) whose only content sends the browser to an opening address, `http://127.0.0.1:PORT/open/<code>`. The code works once and for 30 seconds.
3. The browser launcher (`open` or `xdg-open`; `KEEPGOING_PANEL_OPENER` names another program) is given the path of that page and nothing else. No address, code or session is in any program's arguments, where other users of the computer could read them.
4. The browser loads the page and follows it to the opening address. The server exchanges the code for a session cookie, deletes the launch page, and sends the browser on to the panel. The launch page is deleted after 30 seconds whether or not it was used. A used or expired address, or a page address whose session has ended, shows a short page that says to run `keepgoing panel` again and has a button that copies the command.

`--no-open` prints the opening address instead, for a person who asked for it. The output says that anyone who can read that terminal can use the address during its 30 seconds.

The page's footer says which command is serving it and when that stops: `keepgoing panel` and its idle limit, or `keepgoing dock` until Ctrl-C.

**When it stops.** The background server stops by itself 30 minutes after the last page stopped reading from it (`--idle`, `1m` to `24h`). A page left open and visible reads every few seconds, so it keeps the server running for as long as it is shown; a hidden tab does not. `panel status` says "in use" when a page read within the last minute, and otherwise how long is left. Asking for status, the two static files of the expired-link page, and refused requests do not count as use. It also stops on `keepgoing panel stop`. It is never started at login or by any other command. **Starting it, using it, leaving it idle and stopping it never affect supervision**: it reads what the page reads and writes only its own runtime files.

**How a running server is known.** The server holds a lock (`panel-server.lock`) for as long as it lives; the operating system releases it when the process ends, however it ends. That lock, and nothing else, says whether a server is running. `panel.json` is there for a person to read and is never relied on: if it is deleted, damaged or replaced while the server runs, the commands still find the server through its socket and the server writes its record back. Files a crash left behind are removed by the next command; nobody is asked to delete a file. A server that holds the lock and does not answer (starting, stopping, suspended or stuck) is reported as "running but not answering", never as "not running", and no second server is started beside it. A request that reaches the server more than five seconds late is ignored, so a stop that was reported as failed is not carried out later.

**What the server is started with.** A small fixed environment (a fixed `PATH`, and only `HOME`, the user name, the temporary directory, locale, time zone and the XDG directories from the caller), its private directory as its working directory, the null device for its input and both outputs, and none of the caller's other open files. It keeps no log.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port N` | `0` (a free port) | Loopback port to listen on. |
| `--refresh 15s` | `15s` | How often the page reads again (`5s` to `10m`). The page states the interval and the time of the last read. A hidden tab does not poll; it reads again when shown. |
| `--stale-after 30m` | `30m` | Age at which a check or lane listing is shown as stale. Set it above your scheduler interval. |
| `--directory DIR` | the default registry | A non-default private registry, as for `registry` commands. |
| `--offline` | off | Do not ask each loop's local daemon for its next check. |

Exit codes: 0 after an interrupt, 1 if it cannot listen, 2 for invalid flags.

## Opening it with one command: `keepgoing panel`

`keepgoing dock` runs in the foreground and prints an address to copy. `keepgoing panel` is the ordinary way in: it opens the same page in your browser with nothing to copy. Unreleased source; tested with fixtures, real processes and a real browser on macOS. The same tests are meant to run on Linux in CI and have not yet.

**Windows: unverified, so it refuses.** `keepgoing panel`, `panel status` and `panel stop` have never been run on Windows. Until they have, they stop there with a message that says so and points to `keepgoing dock`, which works as before. `keepgoing panel list` needs no server and works on every platform.

```sh
keepgoing panel                  # start the panel if needed, and open it
keepgoing panel --loop NAME      # open one loop's page (a name, registration ID or key)
keepgoing panel --no-open        # print a single-use address and start no browser
keepgoing panel status           # running or not, since when, the port, the idle limit
keepgoing panel stop             # stop the background server
keepgoing panel list [--json]    # every loop and its state in the page's words; no server
```

What happens:

1. If a panel server this user started is running and answers, it is reused. Otherwise one is started in the background and the command waits, up to ten seconds, until it answers. Two commands run at the same moment end with one server.
2. The command asks that server, over a private local socket, for an opening address: `http://127.0.0.1:PORT/open/<code>`. The code works once and for 30 seconds. That address is the only thing handed to the browser launcher (`open`, `xdg-open` or the Windows URL handler; `KEEPGOING_PANEL_OPENER` names another program).
3. The browser loads it. The server exchanges the code for a session cookie and redirects to the page. A used or expired address, or a page address whose session has ended, shows a short page that says to run `keepgoing panel` again.

The page's footer says which command is serving it and when that stops: `keepgoing panel` and its idle limit, or `keepgoing dock` until Ctrl-C. The expired-link page has a button that copies the command.

The background server stops by itself after 30 minutes without a page request (`--idle`, `1m` to `24h`; `panel status` states the limit and the time left). Health checks, `panel status` and refused requests do not count as use. It also stops on `keepgoing panel stop`. It is never started at login or by any other command. **Starting it, using it, leaving it idle and stopping it never affect supervision**: it reads what the page reads and writes only its own runtime files.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--loop NAME` | none | Open that loop's page. A name shared by two loops is refused; pass the registration ID. |
| `--no-open` | off | Print the opening address. It still works once, within 30 seconds. |
| `--idle 30m` | `30m` | Idle limit for a server this command starts. A running server keeps its own. |
| `--directory DIR` | the default registry | A non-default private registry. A running panel for another registry is refused; stop it first. |
| `--panel-directory DIR` | `keepgoing/panel` in the user configuration directory | Where the runtime files below live. |
| `--refresh`, `--stale-after`, `--offline` | as for `dock` | Passed to a server this command starts. |

**Runtime files.** They live in one private directory (mode `0700`, never a loop's directory, never inside a Git checkout): `panel-server.lock` and `panel-open.lock`, `panel.sock` (the private socket, created private), `panel.json` (mode `0600`: process ID, port, start time, idle limit, registry directory, version and an instance ID) and, for up to 30 seconds each, launch pages named `open-<random>.html`. Only a launch page holds anything that can open the panel, and only until it is used or expires. If the directory's path is too long for a local socket on this platform the command stops and says so before creating anything; it never falls back to anything weaker.

**`keepgoing panel list --json`** returns `schemaVersion: 1`, `readAt`, `registry`, `complete`, `allClear`, `needsYou`, `unread`, `omitted` and `loops`, each with `key`, `name`, `state`, `phrase` and `needsYou`. It is built by the code that builds the page, so the words match, and it carries no registration ID, thread ID, path or note. `key` is what `panel --loop` accepts. It exists so that another local program, such as the menu-bar item, can list loops without a second copy of the wording.

### Access through `keepgoing panel`

Everything under [Access](#access) holds for this server too, with a session in place of the token in the address. Each line is a claim the tests check:

- The server listens on `127.0.0.1` only. Every request must pass the `Host`, `Origin`, fetch-metadata and `GET`/`HEAD` checks before anything else is looked at. No response the server writes carries a cross-origin header, and every one carries the content security policy, `no-store`, `nosniff` and frame denial. (A request too malformed to parse is answered by Go's HTTP library with a bare `400` before the server sees it; that reply has no body of ours and none of these headers.)
- An opening address accepts one more kind of request than the page does: a top-level navigation that began on another site, with no `Origin`. That is what a browser sends when the launch page, a local file, takes it there. A frame, an image, a script or a fetch from another site is refused there as everywhere else.
- An opening code is 256 random bits, compared in constant time, usable once, and dead after 30 seconds whether or not it was used. `HEAD`, refused requests and wrong codes do not spend it. At most 16 can be pending. A used code, an expired code and a code that never existed get the same page.
- An opening can only be created through the private socket. The command checks what the socket answers before using it: only an address of exactly the form above on the server's own port, or the name of a private launch page inside the panel's directory, is accepted.
- The session is a cookie with 256 random bits: `HttpOnly`, `SameSite=Strict`, no expiry (it ends with the browser session, or with the server), scoped to the path `/s/<random segment>/`. Cookies are not separated by port, so the random segment is what keeps a browser from sending it to another local server; the segment alone grants nothing. A request carrying more than one session cookie is refused. At most 16 sessions exist; the oldest is dropped.
- The answer to a good opening address is a page that sets the cookie and sends the browser on, not a redirect. A strict cookie is not sent along a redirect that began on another site, and the launch page counts as one; a navigation started by the server's own page is same-site.
- Neither the opening code nor the session is ever in a program's arguments or environment, or in a log. The server keeps no log.
- Without a valid cookie nothing under `/s/` is served, and nothing is served at any other path except an opening address and two static files the expired-link page needs and which hold nothing private: the stylesheet (`/panel.css`) and its copy-button script (`/expired.js`).
- With a session, `api/v1/server` returns which command serves the page and its idle limit, and nothing about any loop.
- There is still no write endpoint.

**What this protects against, and what it does not.**

- *Another web page in your browser*, including one that guesses the port: it cannot read the page or its data (no cross-origin access, strict cookie, host and origin checks) and cannot create a session (it has no opening code).
- *Another user of the same computer*: they can connect to the port, and get nothing without a session. They cannot read the private directory, so they cannot read a launch page, reach the socket or see the record. They can list your processes and their arguments; nothing there opens the panel. This has been tested with one user's processes and files only, not with a second operating-system account.
- *`--no-open`*: the address is printed where you asked for it. Anyone who can read that terminal, its scrollback or a recording of it within 30 seconds can use it once.
- *A program running as you*: not protected against, and cannot be. It can read the private directory, ask the socket for an opening, and read keepgoing's files directly.
- *Someone who can see your browser window*: they see what you see.

## What the page shows

The page has two levels, so opening it never starts with a list of facts.

**Level 1, the picker.** A "needs you" line, then one row per loop with its name, a status dot and its state in one phrase: supervising, waiting for you, waiting for a limit to reset, checks are failing, outside its hours, paused, stopped, finished, off, not checked recently, or unknown. Loops that want attention come first. Below them is one row for the view across loops, **Routes**. On a wide window the picker stays at the side; on a phone-width window it is the first screen and a pick replaces it, with a link back.

**Needs you.** With nothing picked, the page lists every loop a person should look at:

- A loop held for a person, with the reason from the fixed cause recorded when the hold began, how long it has waited, and what to do. The wait is measured from the retained history record of the hold starting; when retention has dropped that record the page says "waiting at least" and counts from the last control change. A loop whose new-conversation move could not be confirmed is listed the same way.
- A loop that could not be read: its control record or check history is unreadable, its registration no longer matches its configuration, a move to another conversation is in progress, or the read was cut off. Whether such a loop is held is unknown, so it is listed as "could not be read" with what to do, never left out.

"Nothing needs you" is shown only when the registry was read, every loop in it was read, and none is held. Otherwise the line counts what needs you and what could not be read.

The note left with a hold is withheld by default, as `client status` withholds it, because a note can contain identifiers and paths; the page says one exists. **Show identifiers and notes** includes it.

**Level 2, one loop, shown through its routes.** The loop's preferred routes in order are the main structure: its first route (`primary`), then each fallback in configured order, each named by model and account group. For each: its readiness from the last probe, its usage reading with the reading's age, and when each usage window resets. The route the loop's records name as current is marked, in one of two ways:

- **In use now**, only when the loop is on with no hold, its last check is recent, and that check recorded this route.
- Otherwise what is recorded: "Recorded route; loop is off", "…is held", "…is paused", "…is stopped", "…is finished", "…outside its hours", "…no recent check", or "…the last check did not record it".

The current route is not probed, so it gets no readiness verdict: only the age of its last probe, unless that probe recorded a failure. The loop's lanes appear under the current route as counts of working, waiting for an answer and failed, with the age of that listing. Around the routes sit the loop's state, what needs you if anything, the last check and its age, the next check, and limits used (messages sent, deadline, time without Git progress, new conversations created, and a provider limit with its reset time).

The next check is shown only when the loop's keepgoing daemon answers. Otherwise it is unknown and says why: no daemon endpoint is recorded for the loop (a scheduler outside keepgoing is not visible), a recorded endpoint could not be verified, or the daemon was not asked. It is never inferred from the cadence of past checks.

**Routes, across loops.** The same route component, once per configured model on an account, with the newest readiness and usage reading any loop holds for it, and four lists: loops in use on it now, loops whose records name it as current but that are not in use, loops keeping it on standby, and loops that list it but whose current route could not be read. A line above counts the routes, those in use, and those at a limit or not ready. A route is what the configuration says: keepgoing does not verify which account or model a BB thread is actually using. Two accounts configured under one group are two routes, because a route is keyed by what selects the account a probe reads; they can carry the same title. Lanes are not attributed to a route in this view: keepgoing records a loop's lanes only as counts.

Throughout:

- **Unknown and stale are shown, never guessed.** A missing record reads "unknown", "none recorded" or "no check recorded". A check older than `--stale-after`, a lane listing the last check did not refresh, and a usage window that has since reset are labelled stale. Every phrase is tied to the record that produces it by a table test, so wording that says more than its record fails.
- **No identifiers by default.** Thread IDs, registration IDs, paths and hold notes are not in the page or in the data it loads. **Show identifiers and notes** adds the registration ID and the configured BB thread, which `client status` already prints, and the hold note. Paths of keepgoing's own files, lane thread IDs, standby thread IDs, account directories, commands, credentials and logins are never included. Account group names and fallback names from the configuration are shown.
- Nothing on the page acts. The picker has no commands yet (see [planned slices](#planned-slices)).

The page follows the browser's light or dark setting, works with a keyboard and a screen reader, and reflows to a phone-width window.

## What it reads, and what it never does

Each read goes through the code paths of commands that already exist, and adds no evidence of its own:

| Shown | Read through |
| --- | --- |
| The list of loops | `registry list` |
| Binding checks, holds, next check | `client status --id` (including its one-second query of the loop's existing local daemon endpoint) |
| Current route, last check, limits, limit wait | `status --json` |
| Lanes | `runs --registry-id` |
| Preferred routes, readiness, usage | `fallbacks list` |
| When and why a hold began | the retained history `timeline` reads |

It runs no provider, `bb`, scheduler or Git command, starts no daemon, takes no lock and writes no file. Everything is what an earlier check or control command recorded, not a live view of a conversation.

A read belongs to the command, not to the browser request that started it, so a page that reloads mid-read does not cut it short. Each registered configuration is validated once per read and at most four loops are read at once. A read is cut off after 20 seconds; a read that was cut off is marked incomplete, is never reused, lists its unread loops as "could not be read" and cannot say that nothing needs you. A complete read is reused for two seconds so several open tabs do not multiply reads. Every list and text field is capped (a hold note at 280 characters, a loop's routes at 32 fallbacks, 128 routes across loops, the registry's own limit of 128 loops), and a reply is refused above 8 MiB; the largest registry measured just under 2 MiB.

## Access

- It listens on `127.0.0.1` only, never a wildcard address.
- The address contains a random 256-bit token created for that run. A request without it, or with another, is refused. The token is not stored; a new run prints a new address.
- Requests with a `Host` other than the loopback address and port, a foreign `Origin`, or cross-site fetch metadata are refused, and no cross-origin access is granted. This also covers DNS rebinding.
- Only `GET` and `HEAD` are accepted. There is no write endpoint: the page cannot pause, resume, stop, start or configure anything.
- Replies are never cached by the browser and carry a content security policy that allows only the page's own script, style and data endpoint.

The token keeps other local web pages and casual local requests out. It is not protection against another program running as you, which can already read keepgoing's files. Anyone who can see the address, for example in a shared screen or shell history, can read the page while that run lasts.

## The data endpoint

The page loads everything from one endpoint, `GET /<token>/api/v1/dock` (`?identifiers=1` for the view with identifiers and notes). It returns `schemaVersion: 1`, `readOnly: true`, `readAt`, `refreshSeconds`, `staleAfterSeconds`, `registry`, `complete`, `allClear`, `unread`, `needsYou`, `loops` and `routes`. Each loop carries its current route and its ordered preferred routes (`chain.entries`, each with a `route` key, `current`, `inUse`, readiness, usage and resets); `routes` is the same data grouped by route, so every view is one feed. Ages are computed against `readAt` when the report is built, so the page does not depend on the browser's clock or on being opened on the same computer. Consumers must check `schemaVersion`, tolerate added fields, and show an all-clear only when `allClear` is true. An unreadable registry is `registry.availability: unavailable`, never an empty list of loops. The schema is unreleased and may change before a release.

## Planned slices

This is the first slice. The rest is not implemented.

1. **Read-only view** (this slice): the picker, needs you, one loop through its routes, and routes across loops.
2. **Controls**: pause, resume and stop from a loop's page, and picker commands such as pause all, through the existing registered commands (`client pause|stop`, and `client prepare-resume` followed by `client resume` with its reviewed single-use ticket). The page will not get its own control logic.
3. **Configuration**, starting with a loop's routes: their order, adding and removing fallbacks, and turning automatic moves on or off; later limits, the usage-switch threshold and which reports are sent. It will be a front end to the existing guided commands (`fallbacks add|move|remove|handoff`, with their `--dry-run --json` previews, confirmation, ownership and transfer guards and comment-preserving edits), never its own logic or its own file writes. The route rows already carry a position and an ID for this. Credentials and logins will never be shown or edited.
4. **Actions on many lanes at once** from the routes view, such as moving every lane on one route to another or changing their effort level. This depends on two things keepgoing does not have: lanes as first-class records (today it knows a loop's lanes only as counts and hashes, and never changes them), and a group (a coordinator and its lanes) so one action can apply to all members.

Also planned: a native menu-bar or tray item whose click opens the same short picker (the needs-you line, a row per loop, and a few commands such as new loop, open the panel and pause all), not a list of facts; installation and start at login; and answering permission prompts.

**Remote access** (reading and steering loops from a phone or another computer) is planned and not available. The slice is built so it can be added without a rewrite: all data comes through the one versioned endpoint, the access check is a replaceable layer separate from the network checks, the layout works at phone width, and nothing in the page assumes it runs on the same computer. Remote access will need real authentication of a person or device, transport encryption, and an explicit opt-in before it listens beyond loopback. No control will be exposed remotely before that authentication exists. Do not expose this slice through a tunnel, a reverse proxy or a port forward: the per-run token is not designed for that.

## Verification

`go test ./cmd/keepgoing -run Dock` covers: the listener's address; refusal of a missing or wrong token, a foreign or rebound host, a foreign origin, cross-site requests and every write method; the report built from a fixture loop in each state; a table from each record to the exact phrase shown; that "nothing needs you" requires every loop to have been read, with unreadable, moving and cut-off loops listed; that a read is not tied to a request and an incomplete read is not reused; that only a loop that is on, unheld and recently checked on a route counts as in use; withheld identifiers, notes and paths; unchanged registry, configuration and state directories after reads, including a loop whose state directory does not exist; caps on notes and routes; and exit on interrupt. All of it uses fictional loops and local files. The page has been rendered against those fixtures in a browser at desktop and phone widths, in light and dark.

An independent review of an earlier commit of this slice ran the request suite against a 238-entry tree that stayed byte-identical, and the project coordinator served that commit read-only against two live loops with 74 files unchanged. Not verified: a responsive daemon's next-check time in the page, the command executing on Windows (the tests are compiled and vetted there, not run), Linux and Windows browsers, and a screen-reader walkthrough.

To see the page with fictional loops, build the fixture registry into an empty private directory outside any checkout and serve it:

```sh
KEEPGOING_DOCK_SCRATCH=/private/example-scratch go test ./cmd/keepgoing -run TestDockScratchForScreenshots -count=1
keepgoing dock --directory /private/example-scratch/registry
```
