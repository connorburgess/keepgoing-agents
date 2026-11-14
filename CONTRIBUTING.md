# Contributing to keepgoing-agents

Thanks for helping keep agent loops running. This guide is short on purpose.

## Build and test

```sh
go build ./...
go test ./...
gofmt -l .
go vet ./...
```

Go 1.24 or newer. No other toolchain is needed.

## Workflow

Open a branch and a pull request against `main`. Keep each PR to one outcome. Explain the problem, the resulting behavior, and how you verified it. A local pass is not a claim about CI.

Use the [review and execution evidence checks](docs/testing.md#review-and-execution-evidence), plus the platform checks on that page when applicable. Keep the PR description aligned with its current implementation and list checks not run explicitly. Cite CI with the specific run, jobs and source commit; a green check that skipped the affected platform is not execution evidence for that platform.

Capability and release changes also require a [README and documentation review](docs/testing.md#capability-and-documentation-review) before merge or roadmap check-off. Record which sections and command/release evidence you checked, what you corrected, and remaining gaps. Explain when no documentation change is needed; do not mark partial acceptance complete just because documentation was updated.

## Commit and PR titles

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):

```text
type(optional-scope): describe the result
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, plus `build`, `ci`, `perf`, `style`, `revert` when more precise. Lowercase, plain language, no `WIP` or `fix stuff`.

Examples:

```text
feat(decide): treat unknown errors as re-prompt, never failover
fix(bb): read thread status from JSON instead of exit text
docs: describe fallbacks.yaml fields
```

## Sign off your commits

Every commit needs a `Signed-off-by:` line matching its author:

```sh
git commit -s -m "fix(bb): read thread status from JSON instead of exit text"
```

By signing off, you confirm that you created the contribution or have the right and any necessary employer authorization to submit it under the applicable contribution terms. Use your real name and an address you control. A GitHub `noreply` address is fine; use the same identity for author and sign-off.

The sign-off records contribution origin and is different from cryptographically signing a commit. New contributions are distributed under the [Sustainable Use License](LICENSE), subject to any preserved third-party terms, and require the contributor agreement below. A sign-off alone does not accept that agreement. Historical DCO sign-offs remain associated with their original commits; the unchanged [DCO 1.1 text](LICENSES/DCO-1.1.txt) is retained for reference and is no longer the current sign-off policy.

The pull-request check requires an author-matching sign-off on each commit and rejects common tool-attribution credits. It does not establish a contributor's legal identity or verify contributor-agreement acceptance.

## Contributor agreement

Read the short [Contributor License Agreement](CONTRIBUTOR_LICENSE_AGREEMENT.md). You retain copyright and give Connor Burgess permission to license your contribution under other terms, including commercial terms. This supports the project's free personal and internal-business use and separate commercial licensing. It is not a copyright assignment.

For each PR, every contributor other than the project steward must personally post the following statement in their own PR description or comment, linking the agreement revision they read:

> I have read and agree to the keepgoing Contributor License Agreement at [link to the agreement revision] for my contributions in this pull request.

Use a GitHub permalink to the agreement at a specific commit, and confirm you have any necessary employer authorization. For a substantial contribution from someone employed elsewhere, the maintainer will ask for the employer's written confirmation that the contributor may make it on these terms before merging. Do not accept for another contributor. The maintainer must verify acceptance from every contributor before merging and retain the public record. The metadata check only verifies sign-offs; it does not automate this review. The steward does not need to grant rights to themself.

This agreement does not apply retroactively. Do not assume that an earlier DCO sign-off grants commercial relicensing permission. Preserve third-party notices and disclose imported material and its license in the PR. See the [licensing guide](LICENSING.md).

## Public data

- Use fictional loops, thread IDs, hostnames, and `example.com` addresses.
- Never include real transcripts, prompts from private projects, webhook URLs, or credentials in issues, PRs, fixtures, or docs.
- Add tests for behavior and failure cases. Avoid tests that restate the code.

## Authorship

The human contributor is responsible for their change. Do not add AI co-author trailers, credits, generated-with footers, or session links anywhere.

## Security

See [SECURITY.md](SECURITY.md).
