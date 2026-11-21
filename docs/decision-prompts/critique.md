# Critique of the "Decision needed" mocks

impeccable's critique, 2026-11-14, of [panel.html](panel.html), [bb.html](bb.html) and [menu.html](menu.html). It ran as two independent passes: a design review, and a detector and browser pass. The scores below are for the first drafts. Everything listed under "Changed" is in the mocks and in the design doc now.

## Design review

**Verdict:** the content is specific to this product, and the structure is familiar on purpose. The question, facts, and action-with-consequence layout is a common approval pattern. What makes it looptender is the detail: "41 of 60 sends left", "brief saved at 04:02", "It is not closed", "looptender does not answer a lane's question and does not read it", and answers that do nothing once the loop has moved on. It sits inside the panel's existing look.

| # | Heuristic | Score | Key issue |
| --- | --- | --- | --- |
| 1 | Visibility of system status | 3 | Held time and the answered state are clear. The expiry is a clock time the person has to work out. |
| 2 | Match with the real world | 2 | Jargon in the facts ("Route primary", "Not a limit", "streaks"). The thread's wording differed from the card's. |
| 3 | User control and freedom | 3 | Back, Not now and Pause exist. The review hides the other options. |
| 4 | Consistency and standards | 3 | The words were close across surfaces, but drifted. |
| 5 | Error prevention | 4 | Answers are bound to the loop's state, reviews expire, and no review option is applied from a notification. |
| 6 | Recognition rather than recall | 3 | "Suggested" was shown only in the read-only form. |
| 7 | Flexibility and efficiency | 3 | Every surface and the terminal work. The list was not ordered by wait. |
| 8 | Aesthetic and minimalist design | 3 | The terminal line repeated on every card. A "Risk:" label sat on harmless lines. |
| 9 | Error recovery | 3 | A stale answer said "Nothing was changed" without saying where the loop is now. |
| 10 | Help and documentation | 2 | Nothing explained route or brief. |
| | **Total** | **29/40** | Good |

**Priority issues** raised, and what was done about each:

1. **"Risk:" on lines that were not risks.** "If the model keeps declining, you are asked again" is what happens next, not a risk. **Changed:** each option has an optional neutral `then` line, and `risk` is kept for a cost, a loss of context or something that cannot be undone. Only "The new session reads the brief, not this conversation." is still marked as a risk.
2. **Facts in jargon.** **Changed:**
   - "Not a limit: no other route is tried" is now a sentence: "This is not a usage limit, so looptender does not move the loop to another route or account."
   - The 24-hour fact now reads "Continue cleared 2 earlier streaks".
   - The mock thread shows a refusal, so "declined" matches what the person sees.
   - **Not changed:** the provider's refusal text is not shown. Questions never carry message or provider text; "Open the conversation" is beside the question instead.
3. **Screen reader.** **Changed:**
   - Each card is a group named by its loop and question.
   - Each option button is described by its effect and its then or risk line, and the primary also reads "suggested".
   - The review heading is an `h3`, and it takes focus first, so its lines and expiry are read before the confirm button.
   - Answers are announced in a live region.
4. **Too tall on a phone.** **Changed:**
   - Under 40rem only the primary option is shown; the others sit behind "Other options (2)".
   - The terminal command appears once per page, not on every card.
   - Cards are ordered longest-held first.
5. **The words drifted between surfaces.** **Changed:**
   - The question text is the same everywhere, and the loop name sits beside it.
   - Every surface shows the same facts.
   - The review title is the same question everywhere ("Start a fresh session for example-docs?", "Send one try to example-billing?").
   - Expiry reads "Expires in 10 min, at 05:57".
   - The notification's action is the suggested option when it needs no review step, with "Choose…" for the rest.

Also changed:
- **Stop says what it does:** "Stops the session at the next check. Its lanes keep running." That is the panel's existing stop review, not the earlier "ends supervision".
- **The stale message names the current state and the next step.**
- **"Held 3 h 5 min" is in neutral ink, not amber.**
- **"I've answered it" on a lane says what follows:** you are asked again if the lane is still waiting.

**Considered and not taken:**
- *"Continuing in 10 min unless you choose otherwise."* Refused, because looptender never picks an option on its own. A countdown would make the default the choice.
- *One answer for every loop held for the same reason.* Left out for now, since each answer is reviewed and bound to one loop. It is noted as a possible later addition.
- *Hiding the facts behind "Details".* Refused, because there are at most four short facts and they are what makes the question answerable without opening the conversation.

## Detector and browser pass

The CLI detector found 9 findings on the first drafts and 4 after the changes.

| Rule | Where | Verdict |
| --- | --- | --- |
| `skipped-heading` ×2 | panel: review `h4` after the question `h2` | Real. Fixed (`h3`). |
| `low-contrast` ×2 | menu, dark: white on `#3b82f6`, 3.7:1 | Real for the mock. Fixed (`#2563eb`). The built sheet uses the system accent. |
| Light-mode captions and radio descriptions, 3.2:1 to 4.3:1 (measured in the browser) | menu | Real. Fixed. |
| `straggler`, `unbalanced-heading` | one-word last lines; the sheet title | Fixed with `text-wrap: pretty` and `balance`. |
| `nested-cards` | the review inside a card, inside the amber "Needs you" group | Partly real. The amber group is the panel's existing "Needs you" container, kept as it is; the review is a sunk area, not a card. |
| `repeating-stripes-gradient` | `dock.css` `.meter.is-stale` | False positive. It is the existing hatch that marks a stale reading. |
| `icon-tile-stack` | menu sheets | False positive. It is the native `NSAlert` layout: the app icon above the message. |
| `cramped-padding` | the mock's menu bar strip | False positive. It is a stand-in for the macOS menu bar. |

Measured contrast of the new text, light and dark:

| Text | Light | Dark |
| --- | --- | --- |
| Effect, then and fact lines | 7.66:1 | 8.76:1 |
| "Risk:" | 5.88:1 | 8.36:1 |
| Review expiry | 6.74:1 | 9.17:1 |

At 390px no page scrolls sideways.
