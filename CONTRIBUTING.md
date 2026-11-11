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

The sign-off records contribution origin under the [Developer Certificate of Origin 1.1](https://developercertificate.org/). Use your real name and an address you control. A GitHub `noreply` address is fine. Contributions are licensed under [Apache-2.0](LICENSE).

## Public data

- Use fictional loops, thread IDs, hostnames, and `example.com` addresses.
- Never include real transcripts, prompts from private projects, webhook URLs, or credentials in issues, PRs, fixtures, or docs.
- Add tests for behavior and failure cases. Avoid tests that restate the code.

## Authorship

The human contributor is responsible for their change. Do not add AI co-author trailers, credits, generated-with footers, or session links anywhere.

## Security

See [SECURITY.md](SECURITY.md).
