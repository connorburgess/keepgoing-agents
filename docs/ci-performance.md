# CI performance evidence

This change partitions native command race tests within the existing Ubuntu and
macOS jobs. It adds no runner job, runner class, permission, required status,
provider call or runtime behavior. Native Windows diagnostics and repetitions,
Linux ACL execution, release/recovery smoke fixtures and Swift execution remain.

## Before: execution and acquisition are separate

Times below are observed seconds from the Actions API and job logs on 18 November
2026. Start delay means workflow creation to job start, not a pure scheduler
measurement. A job with no runner and no steps is not suite execution evidence.

| Run / source | Job | Start delay | Job duration | Race step / command package |
| --- | --- | ---: | ---: | --- |
| [44399774595](https://github.com/looptender/looptender/actions/runs/44399774595), `5242907017` | Ubuntu (failed static timeout guard) | 4 | 383 | 356 / 330.204 |
| same run / source | Windows storage / processes (passed) | 3 / 3 | 332 / 394 | selected native suites |
| [44388653635](https://github.com/looptender/looptender/actions/runs/44388653635), `6c80fa6446`, merge group | Ubuntu (passed) | 4 | 476 | 365 / 334.345 |
| same run / source | macOS (passed) | 5 | 796 | 624 / 586.504 |
| same run / source | Windows storage / processes (passed) | 4 / 4 | 364 / 373 | selected native suites |
| [44397269635](https://github.com/looptender/looptender/actions/runs/44397269635), `2ba40d7b52` | macOS (failed package timeout) | 31 | 667 | 626 / 600.194 |
| [44391667213](https://github.com/looptender/looptender/actions/runs/44391667213), `891d38460b` | macOS (passed) | 625 | 761 | 593 / not sampled |
| [44395565876](https://github.com/looptender/looptender/actions/runs/44395565876), `7e0a14386c` | macOS (canceled after acquisition) | 304 | 178 | 145 / incomplete |
| [44385632629](https://github.com/looptender/looptender/actions/runs/44385632629), `165bef4812`, merge group | macOS (canceled, no runner or steps) | 1 | 902 | no execution |

The command package is the observed execution bottleneck; runner scarcity is an
additional, independently visible delay. Run 44385632629 explicitly reports that
the hosted runner was not acquired after multiple attempts, with macOS arm64
capacity-constraint notices. A faster suite cannot guarantee faster macOS acquisition. Disabling matrix fail-fast keeps an Ubuntu failure from
canceling macOS evidence; it can use more runner time on a failing change.

The sampled Ubuntu and macOS setup-go logs report primary-key cache hits. Keys
include platform, architecture, Go version and the dependency hash, not each
source revision. Primary hits are immutable and were not saved again. Changing
cache keys is deferred: these runs spend most time executing fixtures, not
restoring dependencies. A rolling build-cache scheme would need measurements of
upload/restore size, misses and concurrent writers before adding churn.

## Coverage mapping and failure behavior

`scripts/ci-race.py` compiles `cmd/looptender` once with `-race`, then asks that
native binary for its inventory. It includes `Test`, `Example` and `Fuzz` entries
(fuzz seed execution, not a fuzz campaign), and excludes benchmarks, as ordinary
`go test -race ./...` does. No source filename list controls coverage.

Three static guards run first: hidden-tab reads, read timeouts and explicit quota
review. The timeout guard was the late Ubuntu failure above. The remaining sorted
inventory alternates between two concurrent processes on the same runner. Every
new entry is automatically included. Each anchored selection executes complete
top-level tests and their subtests. The script checks exact multiset equality
before execution and checks each partition's JSON start and terminal events after
execution. Missing/duplicate/extra entries, malformed output, failures and signals
fail the step. A skipped test remains a recorded skip, not native acceptance of
its body. A failed early guard stops before the expensive suites; a failed
partition does not suppress its sibling's logs or result.

All other packages come from `go list ./...`, excluding only the command package,
and run once via ordinary `go test -race`. Existing separately named Linux ACL
and Windows repetitions are unchanged. Command partitions share one ten-minute
wall-time budget from the first guard execution; compilation remains outside that
budget. Other packages retain Go's default per-package timeout. Job timeouts and
assertions are unchanged. `test2json` preserves Go output and exit failure;
complete JSON logs and inventory are retained as seven-day Actions artifacts.

The harness's `TestMain` gives each command process private registry/session/route,
account and state paths, clears inherited identities, and installs provider CLI
guards. `t.Setenv` and package globals are isolated by process, without introducing
`t.Parallel`. Panel fixtures use unique temporary socket/tool paths and ephemeral
TCP ports. Lazy fixture builds may repeat across processes; that overhead and
host-wide execution-policy/CPU/I/O contention still need observed timing. This is
not a claim that process separation proves every future fixture independent.

`scripts/ci-race-test.py` uses synthetic Python processes and a tiny fictional Go
module to test omissions, duplicates, examples/fuzz seeds, actual test2json failure
propagation, sibling failure, signal exits and timeout cleanup. It also checks that
every Windows test/vet step retains the docs-only guard. The assignment lifecycle
step had lacked that guard; it now follows the existing policy. Merge-group,
unknown, code and workflow changes still select full native coverage. Required
job names, preview prefixes, event triggers and concurrency groups are unchanged.

## Scope and alternatives

Documentation-only selection remains conservative: all Ubuntu checks still run,
including tests that read docs. This change does not expand the documentation
allowlist or introduce a new dependency job that would queue native work later.
It moves the existing cheap scope/contribution/timeout tests before expensive work.

More hosted partitions could reduce test time but also increase runner acquisition
pressure and duplicated setup. Larger paid runners might reduce CPU contention,
but no speedup or cost estimate has been measured here; acquisition and timed
fixture waits would remain. Neither alternative is activated.

Hosted before/after observations and local comparison belong in the PR, with source
commits and explicit limitations. The release workflow is unchanged. Native CI is
fixture evidence, not provider/account, installed-service or live acceptance.
