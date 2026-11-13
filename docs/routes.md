# The standard route list

**Unreleased source.** A route is a model on an account at an effort level ([glossary](glossary.md)). A loop's routes are its first route (`loop.adapter` and its block) followed by an ordered list of others. That list is either the loop's own [`fallbacks.yaml`](fallbacks.md) or, with `routes: standard`, the person's **standard route list**: one ordered list kept once per user, which every loop that follows it resolves for itself.

What exists in this build: the list's file format, how a loop follows it, what happens when the list cannot be read or a route is removed while in use, and the read side (`keepgoing routes list`). The commands that edit the list, and `routes follow-standard` and `routes own`, are not built yet; until they are, the file is written by hand and a loop follows it by a one-line edit.

## The file

`routes.yaml` lives in the user configuration directory, in `keepgoing/routes.yaml` beside the loop registry, never inside a loop's directory or a repository. `KEEPGOING_ROUTES_FILE` (an absolute path) names another location. A JSON schema is in [`schema/routes.schema.json`](../schema/routes.schema.json).

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
| `headless` | Not used. | That agent in the loop's working directory, with the loop's own execution settings when it is the same tool, and `account_dir` as its account. |

A route is also left out for a loop when it is switched off (`disabled: true`), when its `group` is the group of the loop's first route (it would draw on the same limit), or when it is the agent, model and account the loop already uses. Every route that is left out is listed with the reason; nothing is guessed.

Left to a loop's own list: a specific other conversation, a different working directory, custom probe commands, and process or Cursor targets. A loop that needs any of those keeps a `fallbacks.yaml`. There are no per-loop overrides of the standard list.

## Following the list

```yaml
loop:
  routes: standard
  # routes_allow: [new_conversation]
```

`routes: standard` and `fallbacks:` exclude each other. `bb`, `claude` and `codex` loops can follow the list. Loop-side settings stay in `keepgoing.yaml`: `errors.failover_on`, `switch_back`, and the caps on new conversations. A loop with a `fallbacks.yaml` is unchanged by any of this.

The list is resolved each time the loop's configuration is loaded and is part of the [loaded configuration check](operations.md#loaded-configuration-checks). A change therefore applies at the loop's next check and never in the middle of a turn. The resolved routes are ordinary fallbacks from then on: scheduled and exact probes, recorded health, `min_healthy`, `failover_on`, the caps on rejected handoff messages and on new conversations all apply unchanged. Nothing is created or signed in, and a refusal still never causes a move.

## When the list changes or cannot be read

- **The list is missing, unreadable or invalid.** The loop keeps being supervised on the route it is on, with no other routes. Each check says `standard route list unreadable: no other routes available`, as do `status` and `validate`, and none of them fails. One `degraded` report is sent when this begins and one `recovered` report when it ends, each with `reason: standard_route_list_unreadable`. The loop never hands off in this state. Validation belongs to writing the list, not to running a loop: a loop that failed to load would make a scheduled check exit non-zero, and repeated failures make a scheduler disable itself.
- **A route is removed, switched off or stops applying while a loop is on it.** The loop stays on that route. Each check on a route saves a private record of it (`route-current.yaml` in the state directory), and the loop goes on observing, stopping and finishing there from that record. The check, `status` and `routes list` mark it as no longer in the list. The loop moves only by the ordinary rules: a handoff on a classified error, switch-back, or `keepgoing retarget`. Its saved session is kept.
- **No route applies to a loop.** It has no other routes; `validate` warns and `routes list` shows why each route was left out.
- **The loop's `tick_timeout` is shorter than the list's probe timeout allows.** The loop still loads, with a warning that scheduled checks of its routes cannot run.

An older build does not know `routes:` and refuses to load such a loop, naming the field. It cannot run it without fallbacks by mistake.

## Reading it

`keepgoing routes list` shows the standard list and, for every registered loop, its effective routes in order. `--loop NAME` or `-c FILE` shows one loop. It only reads: no lock, no probe, nothing written.

`--json` prints one object, `kind: "route_lists"`, `schemaVersion: 1`:

- `standard`: `state` (`ok`, `missing` or `unreadable`), `redundancy`, `routes` in order (`id`, `provider`, `model`, `effort`, `group`, `enter`, `probeModel`, `disabled`) and `followers`, the names of the loops shown that follow it.
- `loops[]`: `loop`, `list` (`standard`, `own` or `none`), `listUnreadable`, `handoff` (`on` or `off`), `allowsNewConversation`, `current` (the id of the route the loop is on), `first` (the loop's first route), `routes` in order, `skipped` (`id` and `reason`), `redundancy`, `warnings`, and `problem: "configuration_unavailable"` for a loop whose configuration cannot be loaded.
- Each route: `id`, `provider`, `model`, `effort`, `group`, `enter` (`first`, `same_conversation`, `other_conversation`, `new_conversation`, `headless` or `command`), `source` (`loop` for the first route, otherwise `standard` or `own`), `current`, `notInList`, `disabled`, `healthy`, `lastCheck` (`at`, `ok`, `errorClass`) and `usage` as [`fallbacks list`](fallbacks.md#usage-readings) reports it.

The object carries model, group and loop names only. It never includes a conversation ID, an account directory, a working directory or a file path.

## What is verified

Offline fixtures only: resolution for conversation and headless loops, every reason a route is left out, file validation, a list that is missing or invalid at load and repaired later, the single pair of reports, no handoff while unreadable, staying on a removed or switched-off route through a stop, and the read side through the real commands. No live provider, and no editing commands yet.
