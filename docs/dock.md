# The keepgoing panel: one page for every loop

`keepgoing dock` serves one read-only page, the keepgoing panel, that shows every [registered](local-clients.md) loop: which ones need you, and for each loop its state, the routes it prefers and the one it is on, its lanes, its last check and its limits. It is unreleased source, tested with fixtures only. "Dock" is the working name of the command and of the page plus a later menu-bar or tray item; the name is not settled.

Words used here: a **loop** is one supervised piece of work; a **lane** is a child conversation of a loop; a **route** is a model on an account, which today is a loop's main target or one of its fallbacks as configured.

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
| `--refresh 15s` | `15s` | How often the page reads again (`5s` to `10m`). The page states the interval and the time of the last read. |
| `--stale-after 30m` | `30m` | Age at which a check or child listing is shown as stale. Set it above your scheduler interval. |
| `--directory DIR` | the default registry | A non-default private registry, as for `registry` commands. |
| `--offline` | off | Do not ask each loop's local daemon for its next check. |

Exit codes: 0 after an interrupt, 1 if it cannot listen, 2 for invalid flags.

## What the page shows

The page has two levels, so opening it never starts with a list of facts.

**Level 1, the picker.** A "needs you" line (a count, or "nothing needs you"), then one row per loop with its name, a status dot and its state in one phrase: supervising, waiting for you, waiting for a limit to reset, checks are failing, paused, stopped, finished, off, not checked recently, or unknown. Loops that want attention come first. Below them is one row for the view across loops, **Routes**. On a wide window the picker stays at the side; on a phone-width window it is the first screen and a pick replaces it, with a link back.

**Needs you.** With nothing picked, the page shows every loop held for a person. Each entry gives the reason from the fixed cause recorded when the hold began, the note left with the hold in its own words, how long it has waited, and what to do. The wait is measured from the retained history record of the hold starting; when retention has dropped that record the page says "waiting at least" and counts from the last control change. A loop whose registration no longer matches its configuration, or whose new-conversation handoff could not be confirmed, is listed too.

**Level 2, one loop, shown through its routes.** The loop's preferred routes in order are the main structure: the main target, then each fallback in configured order. The route it is on now is marked. Each route shows its readiness from the last probe, its usage reading with the reading's age, and when each usage window resets. The route in use is not probed, so it gets no readiness verdict: it reads "in use now" with the age of its last probe, unless that probe recorded a failure. The loop's lanes appear under the route it is on, as counts of working, waiting for an answer and failed, with the age of that listing. Around the routes sit the loop's state, what needs you if anything, the last check and its age, the next check when a local daemon answers (otherwise unknown, with the reason: no keepgoing daemon runs the loop and outside schedulers are not visible, or a recorded daemon did not answer; it is never inferred from the cadence of past checks), and limits used (messages sent, deadline, time without Git progress, new conversations created, and a provider limit with its reset time).

**Routes, across loops.** The same route component, once per configured model on an account, named by its model and account group, with the newest readiness and usage reading any loop holds for it, the loops running on it now with their state, and the loops keeping it on standby. A line above counts the routes, those in use, and those at a limit or not ready. keepgoing does not verify which account or model a BB thread is actually using, so a route is what the configuration says. Lanes are not attributed to a route in this view: keepgoing records a loop's lanes only as counts.

Throughout:

- **Unknown and stale are shown, never guessed.** A missing record reads "unknown", "none recorded" or "no check recorded yet". A check older than `--stale-after`, a lane listing the last check did not refresh, and a usage window that has since reset are labelled stale.
- **No identifiers by default.** Thread IDs, registration IDs and paths are not in the page or in the data it loads. **Show identifiers** adds the registration ID and the configured BB thread, which `client status` already prints. Paths, lane thread IDs, fallback thread IDs, account directories, commands, credentials and logins are never included. Account group names and fallback names from the configuration are shown.
- Nothing on the page acts. The picker has no commands yet (see [planned slices](#planned-slices)).

The page follows the browser's light or dark setting, works with a keyboard and a screen reader, and reflows to a phone-width window.

## What it reads, and what it never does

Each read goes through the code paths of commands that already exist, and adds no evidence of its own:

| Shown | Read through |
| --- | --- |
| The list of loops | `registry list` |
| Binding checks, holds, next check | `client status --id` (including its one-second query of the loop's existing local daemon endpoint) |
| Target, last check, limits, limit wait | `status --json` |
| Lanes | `runs --registry-id` |
| Preferred routes, readiness, usage | `fallbacks list` |
| When and why a hold began | the retained history `timeline` reads |

It runs no provider, `bb`, scheduler or Git command, starts no daemon, takes no lock and writes no file. Everything is what an earlier check or control command recorded, not a live view of a conversation. A read is cut off after 20 seconds, at most four loops are read at once, a result is reused for two seconds so several open tabs do not multiply reads, and every list and text field is capped (a hold note at 280 characters, a loop's routes at 32 fallbacks, 128 routes across loops, the registry's own limit of 128 loops).

## Access

- The server binds `127.0.0.1` only, never a wildcard address.
- The address contains a random 256-bit token created for that run. A request without it, or with another, is refused. The token is not stored; a new run prints a new address.
- Requests with a `Host` other than the loopback address and port, a foreign `Origin`, or cross-site fetch metadata are refused, and no cross-origin access is granted. This also covers DNS rebinding.
- Only `GET` and `HEAD` are accepted. There is no write endpoint: the page cannot pause, resume, stop, start or configure anything.
- Responses are never cached and carry a content security policy that allows only the page's own script, style and data endpoint.

The token keeps other local web pages and casual local requests out. It is not protection against another program running as you, which can already read keepgoing's files. Anyone who can see the address, for example in a shared screen or shell history, can read the page while that run lasts.

## The data endpoint

The page loads everything from one endpoint, `GET /<token>/api/v1/dock` (`?identifiers=1` for the identifier view). It returns `schemaVersion: 1`, `readOnly: true`, `readAt`, `refreshSeconds`, `staleAfterSeconds`, `registry`, `needsYou`, `loops` and `routes`. Each loop carries its current route and its ordered preferred routes (`chain.entries`, each with a `route` key, readiness, usage and resets); `routes` is the same data grouped by route, so both views are one feed. Ages are computed by the server against `readAt`, so the page does not depend on the browser's clock or on being opened on the same computer. Consumers must check `schemaVersion` and tolerate added fields. An unreadable registry is `registry.availability: unavailable`, never an empty list of loops. The schema is unreleased and may change before a release.

## Planned slices

This is the first slice. The rest is not implemented.

1. **Read-only view** (this slice): the picker, needs you, one loop through its routes, and routes across loops.
2. **Controls**: pause, resume and stop from a loop's page, and picker commands such as pause all, through the existing registered commands (`client pause|stop`, and `client prepare-resume` followed by `client resume` with its reviewed single-use ticket). The page will not get its own control logic.
3. **Configuration**, starting with a loop's routes: their order, adding and removing fallbacks, and turning automatic handoff on or off; later limits, the usage-switch threshold and which reports are sent. It will be a front end to the existing guided commands (`fallbacks add|move|remove|handoff`, with their `--dry-run --json` previews, confirmation, ownership and transfer guards and comment-preserving edits), never its own logic or its own file writes. The route rows already carry a position and an ID for this. Credentials and logins will never be shown or edited.
4. **Actions on many lanes at once** from the routes view, such as moving every lane on one route to another or changing their effort level. This depends on two things keepgoing does not have: lanes as first-class records (today it knows a loop's lanes only as counts and hashes, and never changes them), and a group (a coordinator and its lanes) so one action can apply to all members.

Also planned: a native menu-bar or tray item whose click opens the same short picker (the needs-you line, a row per loop, and a few commands such as new loop, open the panel and pause all), not a list of facts; installation and start at login; and answering permission prompts.

**Remote access** (reading and steering loops from a phone or another computer) is planned and not available. The slice is built so it can be added without a rewrite: all data comes through the one versioned endpoint, the access check is a replaceable layer separate from the network checks, the layout works at phone width, and nothing in the page assumes it runs on the same computer. Remote access will need real authentication of a person or device, transport encryption, and an explicit opt-in before the server listens beyond loopback. No control will be exposed remotely before that authentication exists. Do not expose this slice through a tunnel, a reverse proxy or a port forward: the per-run token is not designed for that.

## Verification

`go test ./cmd/keepgoing -run Dock` covers: the listener's address; refusal of a missing or wrong token, a foreign or rebound host, a foreign origin, cross-site requests and every write method; the report built from a fixture loop in each state; withheld identifiers and paths; unchanged registry, configuration and state directories after reads, including a loop whose state directory does not exist; caps on notes and routes; the routes feed; and exit on interrupt. All of it uses fictional loops and local files. The page has been rendered against those fixtures in a browser at desktop and phone widths, in light and dark. Not verified: a loop under live supervision, a responsive daemon's next-check time in the page, Windows and Linux browsers, a screen-reader walkthrough, and many loops at once.

To see the page with fictional loops, build the fixture registry into an empty private directory outside any checkout and serve it:

```sh
KEEPGOING_DOCK_SCRATCH=/private/example-scratch go test ./cmd/keepgoing -run TestDockScratchForScreenshots -count=1
keepgoing dock --directory /private/example-scratch/registry
```
