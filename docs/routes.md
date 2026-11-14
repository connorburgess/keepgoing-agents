# The standard route list

**v0.2.0.** A route is a model on an account at an effort level ([glossary](glossary.md)). A loop's routes are its first route (`loop.adapter` and its block) followed by an ordered list of others. That list is either the loop's own [`fallbacks.yaml`](fallbacks.md) or, with `routes: standard`, the person's **standard route list**: one ordered list kept once per user, which every loop that follows it resolves for itself.

What exists in this build: the list's file format, how a loop follows it, what happens when the list cannot be read or a route is removed while in use, and the read side (`keepgoing routes list`). The commands that edit the list, and `routes follow-standard` and `routes own`, are not built yet; until they are, the file is written by hand and a loop follows it by a one-line edit.

## The file

`routes.yaml` lives in the user configuration directory, in `looptender/routes.yaml` beside the loop registry (`keepgoing/routes.yaml` on a computer that already has a `keepgoing` directory from before the rename; see [per-user directories](configuration.md#per-user-directories)), never inside a loop's directory or a repository. `LOOPTENDER_ROUTES_FILE` (an absolute path; also read under its [earlier name](configuration.md#environment-variables), `KEEPGOING_ROUTES_FILE`) names another location; that path is refused inside a Git checkout, because the list names private account directories. The default location is not checked for a checkout. A JSON schema is in [`schema/routes.schema.json`](../schema/routes.schema.json).

The file is read as the loop registry is: a regular file of at most 1 MiB, owned by you and not readable or writable by anyone else (`chmod 600`). A symbolic link you own is followed wherever it points, a dotfiles checkout included, as dotfile managers create them; the file it names must pass the same checks. The type is checked before the file is opened, so a FIFO or device at the path is refused rather than read. A file that fails any check is unreadable, and `routes list` gives the reason as a fixed code: `not_private`, `not_owned`, `not_regular`, `too_large`, `inside_checkout`, `named_twice` (the variable's two names are set to different files), `unreadable` or `invalid`. `validate` also lists what makes an invalid list invalid. Paths in a route (`account_dir`, `probe.account_dir`) are at most 4096 bytes.

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

On a following loop, `fallbacks add`, `move` and `remove` refuse and point to `routes.yaml`, and `fallbacks handoff` points to `errors.failover_on`; the `routes` editing commands come with P10b. A following loop cannot be transferred to another BB thread, whether or not its list can be read, as for a loop with a `fallbacks.yaml`.

The list is resolved each time the loop's configuration is loaded and is part of the [loaded configuration check](operations.md#loaded-configuration-checks). A change therefore applies at the loop's next check and never in the middle of a turn. The resolved routes are ordinary fallbacks from then on: scheduled and exact probes, recorded health, `min_healthy`, `failover_on`, the caps on rejected handoff messages and on new conversations all apply unchanged. Nothing is created or signed in, and a refusal still never causes a move.

## When the list changes or cannot be read

- **The list is missing, unreadable or invalid.** The loop keeps being supervised on the route it is on, with no other routes. Each check that observes the loop says `standard route list unreadable: no other routes available`, as do `status` and `validate`, and none of them fails. A held, stopped or unobserved check does not look at the list and does not say it. One `degraded` report is sent when this begins and one `recovered` report when it ends, each with `reason: standard_route_list_unreadable`. The loop never hands off in this state. Validation belongs to writing the list, not to running a loop: a loop that failed to load would make a scheduled check exit non-zero, and repeated failures make a scheduler disable itself.
- **A route is removed, switched off or stops applying while a loop is on it.** The loop stays on that route. Each check ends by saving a private record of the route the loop is on (`route-current.yaml` in the state directory), including a route it moved to during that check by a handoff or switch-back, and `retarget` saves it before the target. The loop goes on observing, stopping and finishing there from that record. The record holds the route's template only (provider, model, effort, group, how it is entered, its account and probe settings), never the loop's environment or executable. It is resolved again for the loop as it is configured now, exactly as the list would resolve it, so it can never become another adapter, command, conversation, directory or executable. A check killed after a handoff sent its message and before it finished leaves no record of the new route, as it leaves no record in `state.json`; the next check works from the previous target, as for a loop with a `fallbacks.yaml`. The check, `status` and `routes list` mark it as no longer in the list. The loop moves only by the ordinary rules: a handoff on a classified error, switch-back, or `keepgoing retarget`. Its saved session is kept.
- **No route applies to a loop.** It has no other routes; `validate` warns and `routes list` shows why each route was left out.
- **The loop's `tick_timeout` is shorter than the list's probe timeout allows.** The loop still loads, with a warning that scheduled checks of its routes cannot run.

An older build does not know `routes:` and refuses to load such a loop, naming the field. It cannot run it without fallbacks by mistake. The private digest of a loop's loaded sources, which a `client prepare-resume` ticket is bound to, now covers the standard list, so a ticket made by an older build is refused after the upgrade; prepare it again.

## Reading it

`keepgoing routes list` shows the standard list and, for every registered loop, its effective routes in order. `--loop NAME` or `-c FILE` shows one loop. It only reads: no lock, no probe, nothing written.

`--json` prints one object, `kind: "route_lists"`, `schemaVersion: 1`:

- `standard`: `state` (`ok`, `missing` or `unreadable`), `problem` (the reason code above, when unreadable), `redundancy`, `routes` in order (`id`, `provider`, `model`, `effort`, `group`, `enter`, `probeModel`, `disabled`) and `followers`, the names of the loops shown that follow it.
- `loops[]`: `loop` (the registered name, also for a loop whose configuration cannot be loaded), `list` (`standard`, `own` or `none`), `listUnreadable`, `handoff` (`on` or `off`), `allowsNewConversation`, `current` (the id of the route the loop is on), `first` (the loop's first route), `routes` in order, `skipped` (`id` and `reason`), `redundancy`, `warnings`, and `problem: "configuration_unavailable"` for a loop whose configuration cannot be loaded.
- Each route: `id`, `provider`, `model`, `effort`, `group`, `enter` (`first`, `same_conversation`, `other_conversation`, `new_conversation`, `headless` or `command`), `source` (`loop` for the first route, otherwise `standard` or `own`), `current`, `notInList`, `disabled`, `healthy`, `lastCheck` (`at`, `ok`, `errorClass`) and `usage` as [`fallbacks list`](fallbacks.md#usage-readings) reports it.

The object carries model, group and loop names only. It never includes a conversation ID, an account directory, a working directory or a file path.

## What is verified

Offline fixtures only: resolution for conversation and headless loops, which settings carry over and the budget case, every reason a route is left out, file validation and the file checks, a list that is missing or invalid at load and repaired later, the single pair of reports and an unsettled one, no handoff while unreadable, staying on a removed or switched-off route through a stop, a handoff or a retarget followed at once by a removal, an unreadable list or a stop, refusing saved records the list could not have produced, and the read side through the real commands. No live provider, and no editing commands yet.
