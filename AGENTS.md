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

## Engineering

- The tool supervises agent loops. It never edits, commits to, or merges repositories, and it never reads transcript text to make a decision.
- Limit and error detection comes only from structured sources: exit codes, JSON fields, HTTP status, provider status commands. An unclassified error is `unknown`, and `unknown` never triggers failover.
- Stop and pause win over every other rule on the next check.
- The decision engine stays a pure function with table tests. Side effects live in adapters and the command layer.
- Read [docs/architecture.md](docs/architecture.md) before changing package boundaries. Update README.md in the same change when behavior, setup, or limitations make it inaccurate.
- For process lifecycle, OS integration or installer changes, apply the [lifecycle and platform review checks](docs/testing.md#lifecycle-and-platform-review). Distinguish platform compilation, native fixture execution and live acceptance; record unrun checks explicitly.
