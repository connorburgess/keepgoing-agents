# The standard route list

**v0.2.0.** A route is a model on an account at an effort level ([glossary](glossary.md)). A loop's routes are its first route (`loop.adapter` and its block) followed by an ordered list of others. That list is either the loop's own [`fallbacks.yaml`](fallbacks.md) or, with `routes: standard`, the person's **standard route list**: one ordered list kept once per user, which every loop that follows it resolves for itself.

What exists in this build: the list's file format, how a loop follows it, what happens when the list cannot be read or a route is removed while in use, the read side (`keepgoing routes list`), the commands that change the list (`routes add`, `move`, `remove`, `off`, `on` and `check`), and the commands that make a loop follow it or leave it (`routes follow-standard` and `routes own`, and the offer in `init`).

## The file

`routes.yaml` lives in the user configuration directory, in `keepgoing/routes.yaml` beside the loop registry, never inside a loop's directory or a repository. `KEEPGOING_ROUTES_FILE` (an absolute path) names another location; that path is refused inside a Git checkout, because the list names private account directories. The default location is not checked for a checkout. A JSON schema is in [`schema/routes.schema.json`](../schema/routes.schema.json).

The file is read as the loop registry is: a regular file of at most 1 MiB, owned by you and not readable or writable by anyone else (`chmod 600`). A symbolic link you own is followed wherever it points, a dotfiles checkout included, as dotfile managers create them; the file it names must pass the same checks. The type is checked before the file is opened, so a FIFO or device at the path is refused rather than read. A file that fails any check is unreadable, and `routes list` gives the reason as a fixed code: `not_private`, `not_owned`, `not_regular`, `too_large`, `inside_checkout`, `unreadable` or `invalid`. `validate` also lists what makes an invalid list invalid. Paths in a route (`account_dir`, `probe.account_dir`) are at most 4096 bytes.

```yaml
version: 1
redundancy: {min_healthy: 1, span: provider}   # as in fallbacks.yaml
probe: {every: 4h, ttl: 6h}                    # as in fallbacks.yaml
routes:
  - id: large-same
    provider: claude-code          # claude-code or codex
    model: example-large
    effort: high                   # optional
    group: example-large-limit     # the usage limit this route draws on
    enter: same_conversation
    probe: {model: example-small}  # optional cheaper model for scheduled checks
  - id: codex-new
    provider: codex
    model: example-codex
    group: codex-main
    enter: new_conversation
  - id: second-account
    provider: claude-code
    model: example-large
    group: second-subscription
    enter: headless
    account_dir: /private/example/second-account   # an existing login
```

A route never names a conversation, a project, a working directory or an executable. `enter` says how a loop takes it up:

| `enter` | For a loop that supervises a BB thread | For a headless Claude Code or Codex loop |
| --- | --- | --- |
| `same_conversation` | The loop's own thread, switched to this model and effort. Applies only when the provider is the thread's provider and the model differs from the loop's. | Not used. |
| `new_conversation` | A conversation started at the handoff, under the [same caps and rules](fallbacks.md#hand-off-to-a-new-conversation). Applies only if the loop has `routes_allow: [new_conversation]`; off by default. | Not used. |
| `headless` | Not used. | That agent in the loop's working directory, with `account_dir` as its account. See below for which of the loop's settings it keeps. |

For a headless route on the loop's own tool, the loop's execution settings carry over: executable, permission mode or sandbox, `extra_args`, `env` and `max_budget_usd`. On the other tool only what means the same thing to both carries over: the working directory and `env`. The loop's `extra_args` are specific to its tool and never carry over. A Codex route is left out for a Claude Code loop that sets `max_budget_usd`, because Codex has no such setting and the budget would be lost, and a headless Codex route that sets `effort` is left out, because a headless Codex agent has no effort setting here.

A route is also left out for a loop when it is switched off (`disabled: true`), when its `group` is the group of the loop's first route (it would draw on the same limit), or when it is the agent, model and account the loop already uses. Every route that is left out is listed with the reason; nothing is guessed.

Left to a loop's own list: a specific other conversation, a different working directory, custom probe commands, and process or Cursor targets. A loop that needs any of those keeps a `fallbacks.yaml`. There are no per-loop overrides of the standard list.

## Following the list

```yaml
loop:
  routes: standard
  # routes_allow: [new_conversation]
```

`routes: standard` and `fallbacks:` exclude each other. `bb`, `claude` and `codex` loops can follow the list. Loop-side settings stay in `keepgoing.yaml`: `errors.failover_on`, `switch_back`, `timing` and the caps on new conversations. `timing` uses the standard list's probe policy, and its probes pause while no route applies or the list cannot be read. A loop with a `fallbacks.yaml` is unchanged by any of this.

### Following it and leaving it

```sh
keepgoing routes follow-standard --loop example-loop   # or -c keepgoing.yaml
keepgoing routes own --loop example-loop [--file fallbacks.yaml]
```

`follow-standard` replaces `loop.fallbacks: …` with `loop.routes: standard` in keepgoing.yaml, or adds that line below `loop.name` for a loop with no other routes, and changes no other line. The loop's fallbacks file is kept and no longer read. The loop must be on its first route (`keepgoing retarget primary` first otherwise), and the list must be written and usable. Check results, rejected-handoff counts and sessions recorded under the ids the list gives the loop are cleared before keepgoing.yaml changes, as `fallbacks remove` clears them, because they may be for a route of the loop's own that had the same id.

`own` writes the routes the list resolves to for this loop now, exactly as resolved, into a new private file beside keepgoing.yaml (`fallbacks.yaml` unless `--file` names another; an existing file is never overwritten), then replaces `loop.routes: standard` with `loop.fallbacks: FILE` and removes `loop.routes_allow`. Recorded check results stay, since they are for the same routes. The loop must be on its first route or on a route the list still gives it. From then on changes to the standard list no longer reach it, and its list is edited with `fallbacks` or `routes --loop`.

Both are a loop's owner's or a person's to make, under the loop's tick lock, with the same preview, `--dry-run`, `--yes`, `--expect` and `route_change` object as the list commands (`op` is `follow_standard` or `own`, `list` the list the loop ends up with). A comment on the changed line is kept. Handoff (`errors.failover_on`) is not changed; the preview warns when it is off. `init` offers to follow the list (see [setup](setup.md)).

On a following loop, `fallbacks add`, `move` and `remove` refuse and point to the [`routes` commands](#changing-it), and `fallbacks handoff` points to `errors.failover_on`. A following loop cannot be transferred to another BB thread, whether or not its list can be read, as for a loop with a `fallbacks.yaml`.

The list is resolved each time the loop's configuration is loaded and is part of the [loaded configuration check](operations.md#loaded-configuration-checks). A change therefore applies at the loop's next check and never in the middle of a turn. The resolved routes are ordinary fallbacks from then on: scheduled and exact probes, recorded health, `min_healthy`, `failover_on`, the caps on rejected handoff messages and on new conversations all apply unchanged. Nothing is created or signed in, and a refusal still never causes a move.

## When the list changes or cannot be read

- **The list is missing, unreadable or invalid.** The loop keeps being supervised on the route it is on, with no other routes. Each check that observes the loop says `standard route list unreadable: no other routes available`, as do `status` and `validate`, and none of them fails. A held, stopped or unobserved check does not look at the list and does not say it. One `degraded` report is sent when this begins and one `recovered` report when it ends, each with `reason: standard_route_list_unreadable`. The loop never hands off in this state. Validation belongs to writing the list, not to running a loop: a loop that failed to load would make a scheduled check exit non-zero, and repeated failures make a scheduler disable itself.
- **A route is removed, switched off or stops applying while a loop is on it.** The loop stays on that route. Each check ends by saving a private record of the route the loop is on (`route-current.yaml` in the state directory), including a route it moved to during that check by a handoff or switch-back, and `retarget` saves it before the target. The loop goes on observing, stopping and finishing there from that record. The record holds the route's template only (provider, model, effort, group, how it is entered, its account and probe settings), never the loop's environment or executable. It is resolved again for the loop as it is configured now, exactly as the list would resolve it, so it can never become another adapter, command, conversation, directory or executable. A check killed after a handoff sent its message and before it finished leaves no record of the new route, as it leaves no record in `state.json`; the next check works from the previous target, as for a loop with a `fallbacks.yaml`. The check, `status` and `routes list` mark it as no longer in the list. The loop moves only by the ordinary rules: a handoff on a classified error, switch-back, or `keepgoing retarget`. Its saved session is kept.
- **No route applies to a loop.** It has no other routes; `validate` warns and `routes list` shows why each route was left out.
- **The loop's `tick_timeout` is shorter than the list's probe timeout allows.** The loop still loads, with a warning that scheduled checks of its routes cannot run.

An older build does not know `routes:` and refuses to load such a loop, naming the field. It cannot run it without fallbacks by mistake. The private digest of a loop's loaded sources, which a `client prepare-resume` ticket is bound to, now covers the standard list, so a ticket made by an older build is refused after the upgrade; prepare it again.

## Changing it

```sh
keepgoing routes add --id codex-new --provider codex --model example-codex --group codex-main --enter new_conversation
keepgoing routes add --like example-loop:claude-other-account --id second-large   # from a loop's own route
keepgoing routes move codex-new --to 1
keepgoing routes off codex-new        # and: routes on codex-new
keepgoing routes remove codex-new
keepgoing routes check                # validate the list; nothing is run
keepgoing routes check --loop example-loop [ID] [--dry-run] [--exact]   # probe that loop's routes
```

`add` takes `--provider`, `--model`, `--group` and `--enter`, and optionally `--effort`, `--permission-mode`, `--account-dir`, `--probe-model`, `--probe-account-dir` and `--at N` (the place; last by default). `--like LOOP:ID` starts from one of a registered loop's routes and the other flags change it; a route that names another conversation than the loop's own has no standard form and is refused.

**Who may change what.** Only a person changes the standard list: a command run inside a BB thread (with `BB_THREAD_ID` set) is refused with `agent_identity`, because every loop that follows the list uses the change. With `--loop NAME`, `move`, `remove`, `off` and `on` change that registered loop's own `fallbacks.yaml` instead, through the same plans and checks as [`fallbacks move` and `remove`](fallbacks.md): its owner or a person may make them. To add to a loop's own list, use `keepgoing fallbacks add`. The fallback a loop is on cannot be switched off under it.

**Preview, then write.** Each command first shows the list before and after and, for every registered loop the change applies to, the routes it hands off to before and after, the routes that newly do not apply to it and why, whether it is on a route the change takes out of its list (it stays there, as above), whether no route is left, and whether it would have fewer healthy routes than `min_healthy` by its last checks. It writes only after you answer yes, or with `--yes`; `--dry-run` writes nothing.

**Line by line.** A change touches only the lines of the route it is about. Comments, blank lines, the order of keys and the style of every other entry stay as they were: a move cuts and pastes the route's lines with the comment directly above it, `off` and `on` change or add only its `disabled` setting, and `add` writes one line in the list's indentation. The result is parsed and compared with the intended list before anything is written; a list written in flow style (`routes: [...]`), or an entry that cannot be changed this way, is refused with `edit_by_hand`.

**Writing.** Changes to the standard list are written one at a time under `routes.lock` beside `routes.yaml`, through a private temporary file renamed into place, so a check never reads half a list. When `routes.yaml` is a symbolic link you own, the file it names is replaced and the link is kept. A list that fails the file checks above is never changed.

**Ids.** A route's id never changes once it is added: a move changes the order, never an id, and there is no rename. An id is also never used again after its route is removed, so check results recorded for the old route can never count for a new one. Every change is appended to `routes-history.jsonl` beside `routes.yaml` (when, what, the order before and after, `by: person`, `source: cli`) before the list is written, and `add` refuses any id in the list (`id_taken`) or named in the history (`id_used_before`). A history that cannot be read refuses `add` (`history_unreadable`) rather than assume an id is new.

**What a following loop records.** The next check of each following loop notes in its timeline, once, that the standard list changed (`routes.listChanged` in the check's record). Nothing is reported: a change is a person's own act.

### For a page

`--json` (with `--dry-run` or `--yes`) prints one object, `kind: "route_change"`, `schemaVersion: 1`:

- `outcome`: `preview`, `changed`, `unchanged` (with `reason`), `refused` or `review_again`, with `refusal` (`code` and `text`) for the last two.
- `op` (`add`, `move`, `remove`, `off`, `on`), `list` (`standard` or `own`), `loop` for an own list, `route` (the id), `from` and `to` (1-based places).
- `expect`: the value an apply is bound to.
- `before` and `after`: the list's ids in order, switched-off ones included; `routes`: the list after the change, rows as in `routes list`.
- `loops[]`: `loop`, `current`, `before` and `after` (the routes it hands off to), `newlySkipped` (`id` and `reason`), `currentLeavesList`, `noRouteApplies`, `belowMinHealthy`, and `problem: "configuration_unavailable"` for a loop that cannot be loaded.
- `warnings`.

The ordering contract: a page names routes by id, previews with `--dry-run --json`, and applies with `--yes --expect VALUE`. The apply plans the change again under the lock. If the list's bytes, the request, or any following loop's configuration or current route differ from what the preview saw, the answer is `review_again`, exit code 75, and nothing is written. A page that reviewed a change by id therefore never acts on another route, or on a list that has moved under it. Refusal codes: `agent_identity`, `not_owner`, `sources_changed`, `follows_standard`, `no_list`, `not_following`, `not_on_first_route`, `current_not_in_list`, `no_route_applies`, `file_exists`, `list_missing`, `unknown_loop`, `unknown_route`, `id_taken`, `id_used_before`, `history_unreadable`, `history_failed`, `invalid_route`, `bad_position`, `like_unavailable`, `edit_by_hand`, `busy`, `write_failed`, `not_possible` (a loop's own list, with the reason), and the file reason codes above.

`routes check --json` prints `kind: "route_check"`: `state`, `problem`, `errors` (what makes an invalid list invalid, naming no path), `routes` (how many), and `loops[]` with the routes that apply to each following loop and those that do not. It exits non-zero for an unreadable or invalid list.

## Reading it

`keepgoing routes list` shows the standard list and, for every registered loop, its effective routes in order. `--loop NAME` or `-c FILE` shows one loop. It only reads: no lock, no probe, nothing written.

`--json` prints one object, `kind: "route_lists"`, `schemaVersion: 1`:

- `standard`: `state` (`ok`, `missing` or `unreadable`), `problem` (the reason code above, when unreadable), `redundancy`, `routes` in order (`id`, `provider`, `model`, `effort`, `group`, `enter`, `probeModel`, `disabled`), `followers`, the names of the loops shown that follow it, and `can`: the changes this caller may make (`add`, `move`, `remove`, `off`, `on`; only `add` while no list is written). When `can` is empty, `canReason` is `agent_identity` or `list_unreadable`.
- `loops[]`: `loop` (the registered name, also for a loop whose configuration cannot be loaded), `list` (`standard`, `own` or `none`), `listUnreadable`, `handoff` (`on` or `off`), `allowsNewConversation`, `current` (the id of the route the loop is on), `first` (the loop's first route), `routes` in order, `skipped` (`id` and `reason`), `redundancy`, `warnings`, `can` (what this caller may change with `--loop`: `move`, `remove`, `off`, `on` and `follow_standard` for a loop with its own list, `follow_standard` for a `bb`, `claude` or `codex` loop with none, `own` for a following loop) with `canReason` when empty (`no_list`, `not_owner`, `ownership_unknown` or `configuration_unavailable`), and `problem: "configuration_unavailable"` for a loop whose configuration cannot be loaded.
- Each route: `id`, `provider`, `model`, `effort`, `group`, `enter` (`first`, `same_conversation`, `other_conversation`, `new_conversation`, `headless` or `command`), `source` (`loop` for the first route, otherwise `standard` or `own`), `current`, `notInList`, `disabled`, `healthy`, `lastCheck` (`at`, `ok`, `errorClass`) and `usage` as [`fallbacks list`](fallbacks.md#usage-readings) reports it.

The object carries model, group and loop names only. It never includes a conversation ID, an account directory, a working directory or a file path.

## What is verified

Offline fixtures only: resolution for conversation and headless loops, which settings carry over and the budget case, every reason a route is left out, file validation and the file checks, a list that is missing or invalid at load and repaired later, the single pair of reports and an unsettled one, no handoff while unreadable, staying on a removed or switched-off route through a stop, a handoff or a retarget followed at once by a removal, an unreadable list or a stop, refusing saved records the list could not have produced, the read side through the real commands, and the editing commands through the real commands: the exact bytes after each change of a hand-written list with comments, flow and block entries, a symbolic link kept, a refused open file and flow-style list, id reuse refused (also with an unreadable history), person-only changes, `review_again` after the list or a following loop's current route changed, a loop's own list switched off and moved, `routes check`, the timeline note at the next check, `follow-standard` and `own` there and back with the exact keepgoing.yaml lines, stale results under a shared id cleared, `routes_allow` removed, refusal off the first route, and the `init` offer (asked and defaulting to yes; never in a script without `--routes standard`). No live provider.
