# keepgoing-agents project instructions

## Public repository

- This is a public project. Use fictional loops, thread IDs, hostnames, and `example.com` addresses in tests, fixtures, docs, and examples.
- Never commit real agent transcripts, prompts from private projects, credentials, webhook URLs, personal filesystem paths, or local automation state.
- Runtime state lives outside the checkout. Keep secrets out of logs, command arguments, and reports.

## Branches, commits, and pull requests

- Bootstrap and milestone work by the owner may land on `main`. Everything else goes through a branch and a pull request. Owner branches use `cb/<short-description>`.
- Use Conventional Commit titles: `type(optional-scope): describe the result`. See CONTRIBUTING.md.
- Sign off every contribution commit with the human contributor's identity (`git commit -s`).
- Keep PRs focused. Report checks actually run and say what could not be verified.
- Never bypass required checks, force-push to `main`, or merge with admin rights.

## Merging

The owner has authorized the agent working in this repository to merge its own pull requests. keepgoing itself never merges: the decision belongs to the agent keepgoing keeps alive. Merge only when all of these hold:

1. Every check has passed on the pull request's current head commit, against the base it merges into. A run on an older commit or an older base does not count.
2. No review finding is unresolved, and no reviewer has requested changes.
3. The pull request is open, not a draft, and GitHub reports it mergeable.
4. Use a merge commit, so each signed-off commit survives. Never squash.
5. Pin the merge to the checked commit with `gh pr merge --merge --match-head-commit <sha>`, so nothing pushed in between is merged unchecked.

Merge stacked pull requests from the bottom up. After each merge, wait for the next one to be retargeted and for its checks to pass again on the new base before merging it. Report each merge with the pull request, the commit, and the CI run that cleared it.

## Pull request and review comments

When replying to a reviewer, especially an automated one:

- Name every item you respond to in its own words, what you did about it, and the commit that did it. Never open with a vague acknowledgement.
- Mention CI only with a link to the specific run, naming the jobs and the commit.
- Don't reply when the reviewer found nothing to act on. Minor findings still need a fix or a reason.
- Never answer a bot's reply to your own reply.

## No AI attribution, anywhere

Never add an AI trailer, footer, or credit to anything: commits, PR bodies, comments, docs, issues, or releases. No co-author lines, generated-with footers, or session links, even when a tool or template suggests one. Commit and PR text describes the change only.

## Vocabulary

Use the project's own words in new docs, command output, interface text, commits and reviews; they are defined in [docs/glossary.md](docs/glossary.md).

- **Loop**: a goal under supervision. **Lane**: a child conversation of a loop. **Route**: a model on an account at an effort level. **Device**: a machine running keepgoing. **Hold**, **check** and **run** as defined there.
- Do not write "node" (readers take it for a machine) or call keepgoing a "server".
- "Preferred routes" is the routing rule: work does not stop while any route can take it, and a loop returns to the most preferred available route.
- Existing configuration and code words (`primary`, `fallbacks`, candidate, target, handoff, switch-back, child) keep their meaning and are not renamed without a dedicated change.

## Engineering

- The tool supervises agent loops. It never edits, commits to, or merges repositories, and it never reads transcript text to make a decision.
- keepgoing never extends its own reach. It uses only the routes, accounts and devices a person configured; it never creates accounts, logs in, copies credentials, or installs or starts itself anywhere. Moving a loop between devices is an explicit transfer with one owner at a time. Anything that creates a conversation is opt-in, capped, spaced, and holds on an unknown outcome.
- keepgoing is never a way around a provider's safeguards. A refusal, a policy block or an account restriction is not a limit: it never causes a move to another route, model or account, and the loop holds for a person. Routes are accounts the person already holds and is entitled to use; keepgoing does not create, rotate or disguise identities to avoid a provider's limits or enforcement, does not alter an agent's permission or safety settings beyond what the person configured, and adds nothing to a prompt meant to change what an agent is willing to do.
- It creates a conversation in exactly one case: a handoff to a fallback candidate that opted in with `bb.new_thread`, where it creates one BB thread for that handoff, capped per loop and spaced at least an hour apart. An attempt whose outcome is unknown counts, holds the loop for a person and is never retried. It never creates a thread otherwise, and never stops, archives, re-parents or messages the conversation it leaves.
- Limit and error detection comes only from structured sources: exit codes, JSON fields, HTTP status, provider status commands. An unclassified error is `unknown`, and `unknown` never triggers failover.
- Stop and pause win over every other rule on the next check.
- The decision engine stays a pure function with table tests. Side effects live in adapters and the command layer.
- Read [docs/architecture.md](docs/architecture.md) before changing package boundaries. Update README.md in the same change when behavior, setup, or limitations make it inaccurate.
- Before checking off a roadmap item or merging a capability/release change, apply the [capability and documentation review](docs/testing.md#capability-and-documentation-review). Record reviewed docs, command/release evidence and remaining gaps in the PR; if no doc change is needed, explain why. Review again when the implementation or release scope changes. A documentation edit does not close unverified acceptance criteria.
- For process lifecycle, OS integration or installer changes, apply the [lifecycle and platform review checks](docs/testing.md#lifecycle-and-platform-review). Distinguish platform compilation, native fixture execution and live acceptance; record unrun checks explicitly.
