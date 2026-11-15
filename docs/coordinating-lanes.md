# Coordinating lanes

A convention for a coordinator: a conversation that hands out [lane](glossary.md) work and reviews it. It is written for agents and the people who set them up.

**looptender enforces none of this.** It does not read a ledger, check a report's length, record approvals, choose a lane's model or limit nesting. Each rule below is something a coordinator does by itself. What looptender does for lanes today is listed [at the end](#what-looptender-does-for-lanes-today).

## Create a lane when its work is assigned

Do not start a lane ahead of the work. Creating a conversation runs a turn, and every later wake of that conversation reads its whole history again, so a lane created early costs a turn now and context later, even if it never does anything.

Until a lane's work is assigned, keep it as a row in a ledger file the coordinator owns:

| Lane | Handoff file | Worktree or base |
| --- | --- | --- |
| `example-app · docs page` | `briefs/docs-page.md` | new worktree from `origin/main` |
| `example-app · usage reading` | `briefs/usage-reading.md` | after `docs page` merges |

Create the conversation when the row is assigned, and point it at its handoff file. Keep the ledger outside the repositories the lanes work in: it is the coordinator's record, and it is what lets another conversation take over coordination.

## Report by pointer

A lane reports with a path and a few lines: the pull request or file, the commit, what was checked, and what is open. Anything longer goes in a file whose path the report names. Do not restate a brief or a diff in a report; the coordinator reads the file if it needs the detail, and every restatement is read again on every later turn of the coordinator.

## Record approvals verbatim, with their source

When a person approves something, copy their exact words into the ledger with where they said it (the conversation and message, or the pull request comment). Then:

- A request is not an approval. "Could you look at merging this?" asks for a look.
- Nothing transfers by implication. Approval to merge one pull request does not approve the next, approval given to one lane does not pass to another, and an approval does not survive a change of coordinator unless the ledger records it.
- A lane acts on an approval only when the coordinator hands it the recorded words and their source.

## Choose each lane's model and effort when starting it

Decide the model and the effort level as part of creating the lane, from the work in its handoff file, and record the choice in the ledger row. A lane does not inherit a sensible choice by default, and looptender does not route lanes: its [routes](glossary.md) apply to the loop's own conversation only.

## Keep nesting shallow

Prefer two levels: a coordinator and its lanes. A lane coordinates lanes of its own only when the work needs it, and then it follows this page as their coordinator.

Each level costs a full turn at that level's context. A result from a lane's own lane reaches a person only after its parent lane runs a turn to relay it and the coordinator runs another to read it, and each of those turns reads that conversation's whole history. A relay is never cheaper than the conversation relaying it, and detail is lost at each one.

## Where the text lives

The full rules live on this page. A coordinator's wake prompt carries at most three short lines, because the wake prompt is sent again on every wake:

```text
Start a lane only when its work is assigned; until then it is a ledger row.
Lanes report by pointer: a path and at most five lines.
Act only on approvals recorded verbatim with their source; a request is not an approval.
```

Point the coordinator at this page, or a copy of it, from its handoff file rather than pasting the rules into the prompt.

## What looptender does for lanes today

looptender supervises the loop's own conversation. For a BB loop it also knows the loop's direct lanes by count and state, and acts on them in two cases only. The rules are in [architecture](architecture.md#rules-that-do-not-bend) and the [glossary](glossary.md); in short:

- **Does:** count lanes and their states; let a working lane keep the loop from looking idle, and wake the loop when a lane finishes; escalate a lane's pending question to a person; report lanes in error; retry a failed lane's turn when the loop opts in with `bb.children.retry_errored` ([lane recovery](operations.md#lane-recovery)); stop lanes when a person asks with `stop --lanes`; say in a handoff brief how many lanes were working. See [BB child awareness](operations.md#bb-child-awareness).
- **Does not:** create, message or move a lane; read a lane's message text; keep a ledger or know about lanes not yet created; record or check approvals; choose a lane's model or effort; limit nesting or report size.

Planned coordinator support, none of it built, is listed in the [roadmap](roadmap.md#coordinating-lanes-planned).
