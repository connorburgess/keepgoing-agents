# The dock: one page for every loop

`keepgoing dock` serves one read-only page that shows every [registered](local-clients.md) loop: which ones need you, and for each loop its state, what it runs on, its child work, its last check, its handoff chain and its limits. It is unreleased source, tested with fixtures only.

```sh
keepgoing registry add -c /private/example-loop/keepgoing.yaml   # once per loop
keepgoing dock
```

```text
keepgoing dock is serving a read-only page for this computer only:

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

- **Needs you**: every registered loop held for a person. Each entry gives the reason from the fixed cause recorded when the hold began, the note left with the hold in its own words, how long it has waited, and what to do. The wait is measured from the retained history record of the hold starting; when retention has dropped that record the page says "waiting at least" and counts from the last control change. A loop whose registration no longer matches its configuration, or whose new-conversation handoff could not be confirmed, is listed here too.
- **One card per loop**: its name; its state in one phrase (supervising, waiting for you, waiting for a limit to reset, checks are failing, paused, stopped, finished, off, not checked recently, or unknown); what it is running on; child work as counts of working, waiting for an answer and failed; the last check and its age; the next check when a local daemon answers; and limits used (messages sent, deadline, time without Git progress, new conversations created, and a provider limit with its reset time).
- **Handoff chain**: the main target, then each fallback in configured order, with its readiness from the last probe and its usage reading with the reading's age. It is read-only in this slice.
- **Unknown and stale are shown, never guessed.** A missing record reads "unknown", "none recorded" or "no check recorded yet". A check older than `--stale-after`, a child listing the last check did not refresh, and a usage window that has since reset are labelled stale.
- **No identifiers by default.** Thread IDs, registration IDs and paths are not in the page or in the data it loads. **Show identifiers** adds the registration ID and the configured BB thread, which `client status` already prints. Paths, child thread IDs, fallback thread IDs, account directories, commands, credentials and logins are never included.

The page follows the browser's light or dark setting, works with a keyboard and a screen reader, and reflows to a phone-width window.

## What it reads, and what it never does

Each read goes through the code paths of commands that already exist, and adds no evidence of its own:

| Shown | Read through |
| --- | --- |
| The list of loops | `registry list` |
| Binding checks, holds, next check | `client status --id` (including its one-second query of the loop's existing local daemon endpoint) |
| Target, last check, limits, limit wait | `status --json` |
| Child work | `runs --registry-id` |
| Handoff chain, readiness, usage | `fallbacks list` |
| When and why a hold began | the retained history `timeline` reads |

It runs no provider, `bb`, scheduler or Git command, starts no daemon, takes no lock and writes no file. Everything is what an earlier check or control command recorded, not a live view of a conversation. A read is cut off after 20 seconds, at most four loops are read at once, a result is reused for two seconds so several open tabs do not multiply reads, and every list and text field is capped (a hold note at 280 characters, a handoff chain at 32 fallbacks, the registry's own limit of 128 loops).

## Access

- The server binds `127.0.0.1` only, never a wildcard address.
- The address contains a random 256-bit token created for that run. A request without it, or with another, is refused. The token is not stored; a new run prints a new address.
- Requests with a `Host` other than the loopback address and port, a foreign `Origin`, or cross-site fetch metadata are refused, and no cross-origin access is granted. This also covers DNS rebinding.
- Only `GET` and `HEAD` are accepted. There is no write endpoint: the page cannot pause, resume, stop, start or configure anything.
- Responses are never cached and carry a content security policy that allows only the page's own script, style and data endpoint.

The token keeps other local web pages and casual local requests out. It is not protection against another program running as you, which can already read keepgoing's files. Anyone who can see the address, for example in a shared screen or shell history, can read the page while that run lasts.

## The data endpoint

The page loads everything from one endpoint, `GET /<token>/api/v1/dock` (`?identifiers=1` for the identifier view). It returns `schemaVersion: 1`, `readOnly: true`, `readAt`, `refreshSeconds`, `staleAfterSeconds`, `registry`, `needsYou` and `loops`. Ages are computed by the server against `readAt`, so the page does not depend on the browser's clock or on being opened on the same computer. Consumers must check `schemaVersion` and tolerate added fields. An unreadable registry is `registry.availability: unavailable`, never an empty list of loops. The schema is unreleased and may change before a release.

## Planned slices

This is the first of three slices. The second and third are not implemented.

1. **Read-only view** (this slice).
2. **Controls**: pause, resume and stop from the page, through the existing registered commands (`client pause|stop`, and `client prepare-resume` followed by `client resume` with its reviewed single-use ticket). The page will not get its own control logic.
3. **Configuration**, starting with fallbacks: the handoff chain's order, adding and removing fallbacks, and turning automatic handoff on or off; later limits, the usage-switch threshold and which reports are sent. It will be a front end to the existing guided commands (`fallbacks add|move|remove|handoff`, with their `--dry-run --json` previews, confirmation, ownership and transfer guards and comment-preserving edits), never its own logic or its own file writes. Credentials and logins will never be shown or edited in the dock.

Also planned: a native menu-bar or tray item that shows presence and a "needs you" count and opens the page; installation and start at login; and answering permission prompts.

**Remote access** (reading and steering loops from a phone or another computer) is planned and not available. The slice is built so it can be added without a rewrite: all data comes through the one versioned endpoint, the access check is a replaceable layer separate from the network checks, the layout works at phone width, and nothing in the page assumes it runs on the same computer. Remote access will need real authentication of a person or device, transport encryption, and an explicit opt-in before the server listens beyond loopback. No control will be exposed remotely before that authentication exists. Do not expose this slice through a tunnel, a reverse proxy or a port forward: the per-run token is not designed for that.

## Verification

`go test ./cmd/keepgoing -run Dock` covers: the listener's address; refusal of a missing or wrong token, a foreign or rebound host, a foreign origin, cross-site requests and every write method; the report built from a fixture loop in each state; withheld identifiers and paths; unchanged registry, configuration and state directories after reads, including a loop whose state directory does not exist; caps on notes and chains; and exit on interrupt. All of it uses fictional loops and local files. The page has been rendered against those fixtures in a browser at desktop and phone widths, in light and dark. Not verified: a loop under live supervision, a responsive daemon's next-check time in the page, Windows and Linux browsers, a screen-reader walkthrough, and many loops at once.

To see the page with fictional loops, build the fixture registry into an empty private directory outside any checkout and serve it:

```sh
KEEPGOING_DOCK_SCRATCH=/private/example-scratch go test ./cmd/keepgoing -run TestDockScratchForScreenshots -count=1
keepgoing dock --directory /private/example-scratch/registry
```
