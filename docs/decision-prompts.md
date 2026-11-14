# Decision needed: one question for every time a loop needs a person

Design, 2026-11-14. **Nothing here is built.** This page proposes the model, the words and the surfaces; code follows in small PRs once it is agreed. Mocks with fictional data are in [decision-prompts/](decision-prompts/): the [panel](decision-prompts/panel.html), the [BB thread card](decision-prompts/bb.html) and the [macOS menu](decision-prompts/menu.html). Open them from a checkout; they load the panel's own stylesheet.

## The rule

Every situation in which a loop needs a person becomes one **question**: what happened, in plain words; what is known; and the options, each saying exactly what it will do and any risk. It is never a hold with a free-text note, never a generic "It is held for a person.", and never a list of commands to look up.

- looptender raises the question and holds the loop when the situation calls for it, and only that loop.
- A person answers it on whichever surface is in front of them: the BB thread, the panel, the macOS menu, a terminal. Each answer goes through the existing review and apply path, bound to that question and to the loop as it was when they read it.
- The answer is recorded in the loop's history. looptender never picks an option on its own.

Today a hold is `awaitingUser` plus a `note` built from the decision's reason. Several reasons share the cause `other`, so the panel shows the same sentence for an auth failure, a target that is gone, errors that persisted and an unconfirmed brief ([dock_view.go](../cmd/keepgoing/dock_view.go), `dockHoldNeed`). The person then has to know which of `resume`, `retarget`, `complete` or a configuration edit applies. This design replaces that guesswork.

## The record

The record is called `question` in records and code, and "Decision needed" on screen. In records, "decision" already means the supervisor's own per-check decision (`decide.Decision`, and the `decision` block of every check record), so reusing it would make history ambiguous.

An open question lives in the loop's `control.json` as `question`. Each loop has at most one, and it replaces `note` as the reason for a hold; `awaitingUser` stays and means "held for this question".

```jsonc
"question": {
  "version": 1,
  "id": "q_5f0c9d2e4b7a41c88e1f6a3b2d9c0e17", // random, one per raise
  "kind": "refused",                          // closed list: the catalogue below
  "scope": "loop",                            // "loop", or "install" for update_available
  "raisedAt": "2026-11-14T05:00:12Z",         // the check that raised it
  "build": "v0.2.1",
  "holds": "loop",                            // "loop": this loop only is held meanwhile; "none": it keeps running
  "basis": "<64 hex>",                        // see "What an answer is bound to"
  "evidence": [                               // typed values only; never message, provider or tool text
    {"key": "declined_turns",            "type": "count", "value": 3},
    {"key": "current_streak",            "type": "count", "value": 3},
    {"key": "first_at",                  "type": "time",  "value": "2026-11-14T04:10:03Z"},
    {"key": "last_at",                   "type": "time",  "value": "2026-11-14T05:00:09Z"},
    {"key": "route",                     "type": "route", "value": "primary"},
    {"key": "error_class",               "type": "class", "value": "refused"},
    {"key": "recovered_by_continue_24h", "type": "count", "value": 2}
  ],
  "options": [                                // 1 to 4, in display order; exactly one primary
    {"id": "continue",      "primary": true,  "review": false, "sends": true},
    {"id": "fresh_session", "primary": false, "review": true,  "sends": true},
    {"id": "pause",         "primary": false, "review": false, "sends": false}
  ],
  "answerers": ["person"]                     // add "thread" only where the catalogue allows it
}
```

- **Evidence types:**
  - `count`
  - `time`
  - `duration`
  - `route`: a configured route name
  - `class`: an error class
  - `lane`: a lane's position and hashed key, never its title
  - `limit`: a configured value and how much is used
  - `version`: a build label

  Unknown keys are refused on load, the way the incident journal refuses unknown fields.
- **Words are not stored.** The question, each option's label, its one-line effect, what follows it (`then`, neutral) and any real risk (`risk`, marked) are rendered from `(kind, option id, evidence)` by one Go function, the way `dockText` renders the panel's sentences today. Every surface takes the rendered words from `status --json`, the panel's report or `client review`, so the words cannot drift between surfaces. A later build may reword a question; history keeps ids, not text.
- **Who may answer.** `person` means anyone who can run looptender on this computer, which is who can already pause, stop and resume it. `thread` means the supervised BB thread itself, through `BB_THREAD_ID`. It is allowed only where the catalogue says so, and never for a question about the agent's own conduct (refused, restricted) or about the loop's own limits (deadline, sends).
- **One open question per loop.** A new raise withdraws the open one with the reason `superseded`. Every surface shows the newest.
- **Closing.** A question closes in one of two ways:
  - **Answered:** a person chose an option.
  - **Withdrawn:** the situation ended without an answer. The reasons are:
    - `superseded`;
    - `cleared`: for example, the lane got its answer or the scheduler came back;
    - `control`: a person ran a plain `pause`, `stop`, `resume` or `complete` instead. The command still works and the question records why it closed.

### What an answer is bound to

The `basis` is a SHA-256 over:
- the question's id, kind and evidence;
- the loop's current target;
- the gate fields of `control.json`: `enabled`, `paused`, `stop`, `awaitingUser` and `completed`.

So the basis changes whenever the target changes, or a stop, pause or resume is saved. An answer read before the situation moved therefore cannot act after it.

Answering reuses the review and apply path the panel and the menu already use ([client_review.go](../cmd/keepgoing/client_review.go), [panel_changes.go](../cmd/keepgoing/panel_changes.go)):

1. **Review.** `client review answer --loop KEY --question q_… --option fresh_session`, or the panel's `prepare` with `{"action":"answer","question":"q_…","option":"fresh_session"}`. It reads the control record once, under its lock, and returns:
   - the option's review lines;
   - an expect value `r1.<expiry>.<digest>.<check>`. As today, `<digest>` is the `controlDigest` of the record read, which now includes the question and its basis. The check covers the key and `answer:<question id>:<option id>`.
2. **Apply.** `client apply answer … --expect V`, or the panel's single-use `confirm`. It applies only while that question is open, under the same id and basis, before the expiry, and only for an answerer the question allows. It runs the option through the same functions as `pause`, `stop`, `resume`, `complete` and `retarget` today. Options that need new behaviour are marked in the catalogue.
3. **Record.** The save goes through `updateControlFromAnnotated` as every control change does. It writes a `control` record with operation `answer` (or `panel_answer`, or `client_answer`) and adds `question: {id, kind, option, basis}`, with no words. A raise is recorded on the check's record as `question_raised`, and a withdrawal as `question_withdrawn` with its reason.

`review: true` means the option shows a separate review step (the panel's inline review, the menu's second sheet, a terminal `y/N`) before it applies. `review: false` options apply from the card itself, because the card's one-line effect already is their review; they still carry an expect value, so they are just as bound. An option needs the review step when it does any of these:
- sends to a new session or another route;
- changes a limit;
- ends or finishes the loop;
- acts on another conversation.

Options that leave things as they are (pause, keep waiting, not now), and continue, which is the normal path, do not.

A stale answer does nothing, and says where the loop is now and what to do next. For example: "Nothing was changed. This loop was paused from the menu at 05:51, which closed this question. It is paused now; resume it from its page when you are ready."

## The surfaces

The words are the same everywhere; only the controls differ.

| Surface | Where | Controls |
| --- | --- | --- |
| **BB thread** (plugin) | The supervised thread's context banner above the composer. Collapsed, it is one line: "Decision needed: the model declined 3 turns in a row". Expanded, it is the card. | A button for each option. A review option opens the review in the card. |
| **Panel** | "Needs you", each card inline, longest held first, with the terminal command once at the top. A loop's own page shows its card at the top. On a phone only the primary is shown, and the others sit behind "Other options (2)". | Panel started with `--allow-changes`: buttons, with the inline review. Read-only panel: the same options as words, and the command. |
| **macOS menu** | A notification when a question is raised. Clicking it opens a native sheet: an `NSAlert` with the question, the facts, and a radio group of the options with their effect and risk lines. The default button names the selected option and follows the selection. | The notification's action is the suggested option when it has no review step, and "Choose…" opens the sheet. A review option is never applied from a notification. In the sheet the suggested option is selected, and the default button names the selected option ("Review…" for one with a review step). "Not now" closes the sheet and leaves the loop held. |
| **Terminal** | `looptender answer` lists open questions. `looptender answer LOOP` shows one with numbered options. `looptender answer LOOP OPTION` reviews it; in a terminal it asks `y/N` for review options, and otherwise prints the apply command with its expect value. | Typing. `--json` for scripts. |
| **Report and webhook** | Each report that raises a question (`await_user` and the rest) carries `question`: the rendered question, facts and options (label, effect, risk) and the `looptender answer LOOP` command. | None. No buttons or links that act; nothing comes back in. |

Rules for every surface:
- **One primary action**, filled; the others are outlined. The primary is the option most people choose, not the safest-sounding one. Where the answer is a fact only the person can see (did that conversation take the task?), the primary is the way to look ("Open that conversation"), and it changes nothing.
- **Each consequence in one line**, beginning with a verb: "Sends nothing until you resume this loop."
  - What follows, if it matters, gets one neutral line: "If the model keeps declining, you are asked again."
  - A real risk gets a line labelled "Risk:". A risk is a cost, lost context, or something that cannot be undone; "you are asked again" is not a risk.
  - Labels are at most four words.
  - Every line fits the menu's 180-character cap (`clientReviewMenuCap`), and a test enforces both limits.
- **Reviews are questions** with the loop's name: "Start a fresh session for example-docs?". Their expiry is relative first: "Expires in 10 min, at 05:57, or sooner if the loop changes."
- **No jargon:**
  - "move to another route", not handoff or failover;
  - "try", not re-prompt;
  - "check", not tick;
  - "session" and "conversation" as the panel's glossary uses them.

  Basis, expect, episode and stage never appear.
- **Calm:**
  - one amber dot and "Decision needed";
  - the wait shown as "Held 50 min";
  - no red unless something is damaged;
  - no countdowns, and no "urgent".

  Every card says what is not affected: "Only this loop is held; the others keep running."
- **Specific:** at most four facts, all this loop's own (route, times, counts), as short label and value pairs. They are followed by one sentence on what is not tried or not affected. The primary reads "suggested" to a screen reader and, in the read-only form, shows a visible "Suggested" tag.
- **Accessible:**
  - The card is a group named by its question. Each option button is described by its effect and risk lines.
  - The review step takes focus to its confirm button; Back returns focus to the option.
  - The outcome goes to the page's live region.
  - In the menu, VoiceOver reads each radio's label and then its description.
  - Forced colours and text size follow the panel's existing rules.

## Catalogue

Every situation found in `decide`, `supervise`, the panel and the reports, as of `main` at c8f51d7, and the question it becomes. Option ids are the record's. ★ marks the primary. **R** means the option has a review step. "New" means applying it needs behaviour that does not exist today; each such option is a separate later PR, and until then it is left out of the record.

### Questions that hold the loop

| Kind | Raised when (today's code) | The question | Options | looptender never, on its own |
| --- | --- | --- | --- | --- |
| `refused` | The model or provider declined N turns in a row (#174, `refused` class). | "The model declined 3 turns in a row. How should this loop go on?" | ★ `continue`: "Goes back to the normal pace: the session is woken again, with spacing between tries." Then: "If the model keeps declining, you are asked again." · `fresh_session` **R**, new: "Opens a new session on primary with the task brief. This session is left as it is." Risk: "The new session reads the brief, not this conversation." · `pause`: "Sends nothing until you resume this loop." | Continues past the streak; starts a fresh session; moves to another route or account. |
| `restricted` | The provider denied permission for the request or the account (#174, `restricted` class). | "The provider denied access for this account. Has the access been fixed?" | ★ `resume` **R**, labelled "I've fixed the access": "Sends one try to the same session." Then: "If access is still denied, the loop holds and asks again." · `pause` · `stop` **R**: "Stops the session at the next check. Its lanes keep running." Then: "Nothing is sent until you resume this loop." No continue or fresh session: neither helps an account-level denial. | Tries again; moves to another account. |
| `errors_persisted` | `error persisted after re-prompts` (`decide.go`, `EpisodeSpent`). | "Errors kept coming after 3 tries. How should this loop go on?" Facts: error class, tries, first and last error. | ★ `continue`: "Starts a new round of tries, with spacing between them." · `move_route` **R**, new, shown only when a ready route exists: "Moves this loop to backup and briefs it on the task." · `pause` | Starts another round; moves for a class not in `failover_on`. |
| `auth` | `auth error` with no move possible. | "The session's sign-in failed. Has it been fixed?" | ★ `resume` **R**, labelled "I've signed in again": "Sends one try to the same session." · `pause` · `stop` **R** | Signs in; copies credentials; retries. |
| `stalled` | `no progress after N nudges` (`StallHold`). | "No progress after 4 nudges. How should this loop go on?" Facts: unchanged since, nudges, last change. | ★ `continue`: "Starts a new round of nudges." · `finish` **R**: "Marks the loop finished. looptender does not check the work." · `pause` | Nudges past the limit; declares it finished. |
| `hung_turn` | The hung-turn hold (`HungTurnHoldReason`), after nudges were held back because the turn looked stuck. | "The current turn has looked stuck since 03:12. Start it again?" | ★ `retry_turn` **R**, new: "Stops the stuck turn and sends one new try to the same session." · `keep_waiting`: "Waits for the turn; asks again after another 30 min." · `pause` | Stops a running turn. |
| `retry_silent` | `provider retry went silent` (no Git tracking). | "The provider's retry went quiet 40 min ago. Wake the session?" | ★ `continue`: "Wakes the session with one try." · `pause` | Wakes it past its patience. |
| `brief_unconfirmed` | `holdForBriefs`: a move's brief was recorded and not confirmed as delivered. | "A brief to backup may or may not have reached it. Did that conversation take the task?" | ★ Open that conversation (a link; changes nothing) · `took_it` **R**: "Moves supervision to that conversation." (`retarget`, then `resume`) · `stay`: "Keeps this loop where it is; nothing is briefed on the next check." (resume with `SkipHandoffOnce`) | Decides whether a brief arrived; re-sends a brief. |
| `lane_input` | `child_input`: a lane is waiting on a person. | "A lane is waiting for an answer only a person can give." Facts: which lane (position), asked at. | ★ Open the lane in BB (a link) · `answered`: "Closes this question and wakes the loop at the next check." Then: "If the lane is still waiting then, you are asked again." · `pause`. Answerers: person and thread, since the lead session may have answered it. | Answers a lane's question; reads it. |
| `interaction_cancelled` | `DeferredInteractionReason`: the agent's request was cancelled and nothing was approved. | "The agent's request was cancelled, so nothing was approved. Resume the loop?" | ★ Open the conversation (a link) · `resume`: "Wakes the session at the next check." · `stop` **R** | Approves anything. |
| `target_gone` | `target gone (archived, deleted, or not found)`. | "The conversation this loop supervises is gone. What now?" | ★ `move_here`: in BB this is "Move supervision here" (the plugin's transfer flow), elsewhere the `retarget` command · `stop` **R** | Picks another conversation. |
| `deadline` | `loop deadline reached` (stage `deadline`). | "This loop reached its deadline, 14 Nov 18:00. Finish, or give it more time?" | ★ `finish` **R** · `extend` **R**, new: "Moves the deadline to … (choose)." · `pause`. Answerers: person only. | Extends its own deadline. |
| `sends_used` | `loop send limit reached` (`max_sends`). | "This loop has used all 60 of its sends. Finish, or allow more?" | ★ `finish` **R** · `allow_more` **R**, new: "Allows 20 more sends." · `pause`. Answerers: person only. | Raises its own limit. |
| `progress_window` | `no Git progress within loop.limits.no_progress_timeout`. | "No change to its repositories in 6 h. How should this loop go on?" | ★ `continue` **R**, new: "Starts a new 6 h window." · `finish` **R** · `pause` | Starts a new window. |
| `new_thread_unsettled` | A conversation created for a move could not be confirmed (`unsettledReason`). | "A new conversation for the move to backup could not be confirmed. Was it created?" | ★ Open BB (a link) · `adopt` **R**: "Moves supervision to that conversation." · `discard` **R**: "Forgets it and stays here." (`retarget --discard-pending-thread`) | Creates another conversation. |
| `new_threads_used` | `NewThreadCapReason` (`max_new_threads`). | "This loop has used its 3 new conversations. Allow one more?" | ★ `pause` · `allow_one` **R**, new · `finish` **R**. Answerers: person only. | Raises its own cap. |
| `agent_asked` | `keepgoing await-user`, run by the agent or by a person: it was marked as waiting for a person. | "This loop was marked as waiting for a person." Its note is shown only with identifiers, as today. | ★ Open the conversation (a link) · `resume` · `stop` **R** | Reads or answers the note. |

### Questions that do not hold

| Kind | Raised when | The question | Options | Never, on its own |
| --- | --- | --- | --- | --- |
| `long_limit_wait` | A limit wait longer than `errors.report_wait_after` with no move possible (`limit_wait`, `limit_no_fallback`, `limit_failover_disabled`). The loop keeps checking. | "No route can take over until 14:00 (in 9 h). Keep waiting?" | ★ `keep_waiting`: "Keeps checking and continues when the limit lifts. You are not asked again for this wait." · `move_route` **R**, new, only when a route is ready but moves are off or not allowed for this class · `pause` | Turns failover on; moves when the configuration says not to. |
| `agent_input` | The supervised agent is waiting on a question or a permission request (`waiting on a person`). It keeps checking. | "The agent is waiting on a question or a permission request." | ★ Open the conversation (a link). Closes as `cleared` once answered. | Approves a permission; answers for the person. |
| `finished` | `keepgoing complete`, or a goal marked met. It is already finished. | "This loop was marked finished. looptender has not checked the work. Is it done?" | ★ `done`: "Closes this question." · `reopen` **R**: "Resumes the loop." | Declares success. |
| `update_available` | Versions view (phase 2): a newer release than the running binary, plugin or menu. `scope: "install"`, stored in the panel directory, not in any loop. | "looptender v0.2.2 is available; this computer runs v0.2.1." | ★ `not_now`: "Asks again tomorrow." · `update` **R** (Versions 2a/2b/2c) · `skip_version`: "Doesn't ask about v0.2.2 again." | Updates anything. |

### Shown as a question, raised by the reader

These cannot be raised by a check, because the problem is that checks are not running or their records cannot be read. The panel, `status` and the menu build them from what they read, and nothing is stored. They use the same card and words, and they close when a check reads cleanly again.

| Kind | When | The question | Options |
| --- | --- | --- | --- |
| `checks_stopped` | `Schedule.Overdue`: no check for max(15 min, 3 × the usual interval). | "No check has run for example-docs since 03:10. Start checks again?" | ★ `start` **R**, new, only for looptender's own daemon or service: "Starts background checks for this loop." When an outside scheduler such as a BB automation runs the checks, this says so: "Its checks come from a BB automation; start it there." · `turn_off`: "Turns supervision off for this loop." (`disable`) |
| `records_unreadable` | `dockUnreadable`: `control_unavailable`, `state_unavailable`, `binding_*` and the rest. | "example-docs's records could not be read, so whether it needs you is unknown." | No option acts, because looptender cannot safely change a record it cannot read. The card shows the one command that explains it (`looptender status -c …`, or `looptender transfer recover` for `transfer_pending`). This is the only card without a button, and that is deliberate. |

### Not questions

These are worth knowing, but nothing in them needs an answer, so they stay where they are today.

| Situation | Where it stays | Why it is not a question |
| --- | --- | --- |
| Paused, stopped or turned off by a person | The loop's state | The person chose it; resume is on the loop's page. |
| Network down (`network_wait`) | State and report | It continues by itself; there is nothing to choose. |
| Turns cannot start (`start_refused`) | Status and a report | A local cause must be fixed; the next accepted send clears it. If it lasts longer than `errors.report_wait_after`, it becomes a candidate `start_refused` question. |
| Switch-back stopped; the child notice gave up; too few healthy routes; the route list is unreadable | "Worth knowing" | The loop continues on its current route. |
| No-progress flag; build mismatch | "Worth knowing" | They are flags, never needs (#198, #204). |
| Takeover by another supervisor; session claims | History | Records of what happened. |
| Lane recovery verdicts (`child_recovery`) | Report | The lead session is told; a `declined` lane is covered by `refused` and `restricted` in that lane's own loop. |

## What changes for the person

- **Before:** "example-docs: It is held for a person. Look at the conversation, then resume the loop when it is ready to continue."
- **After:** "example-docs: The model declined 3 turns in a row. How should this loop go on? Continue · Start a fresh session · Pause", with one line under each saying what it will do. The same text appears in the BB thread, the panel, the menu, the terminal and the report.

## Build order

Each step is its own PR, after this one is agreed. The roadmap calls them Q1 to Q6.

1. **The record and its words, read-only.**
   - `internal/question`: the record, the strict load, the basis, the words function and the length test.
   - Raise questions for the existing hold kinds, replacing the note as the reason.
   - `question_raised` and `question_withdrawn` in history.
   - The panel's "Needs you", `status`, `status --json` and reports show it.
   - #174 records `refused` and `restricted` in this shape.
2. **Answering.** `looptender answer`, `client review answer` / `client apply answer`, the panel's `answer` prepare and confirm, and `question_answered` in history.
3. **The BB card** (plugin): the banner's summary and the card, through the plugin's existing host calls.
4. **The macOS menu**: the notification and the sheet, through `client review` / `client apply`.
5. **New options**, one PR each: `fresh_session`, `move_route`, `retry_turn`, `extend`, `allow_more`, `allow_one`, the progress window's `continue`, and `start` for checks.
6. **`update_available`**, with Versions phase 2.

## Open decisions

- `progress_window`'s `continue` needs a "new window" that the configuration does not have today (the timeout is measured from the last change). Until it exists, the option is left out and the question offers `finish` and `pause`, plus the configuration edit as words.
- Should the BB card appear on the lead thread only, or also on a lane's thread for `lane_input`? The proposal is the lead thread only, since lanes are not supervised conversations.
- Should the report carry the answer command with the loop's configuration path? The proposal is no; `looptender answer LOOP` finds the loop through the registry.
- One answer for several loops held for the same reason ("Continue both loops that declined") is left out for now: each answer is reviewed and bound to one loop. It could come later as a reviewed group, the way `client review --all` reviews a pause of every loop.

## Critique

Mocks reviewed with impeccable's critique (design review and detector, run separately), 2026-11-14. Findings and what was changed are recorded in [decision-prompts/critique.md](decision-prompts/critique.md).
