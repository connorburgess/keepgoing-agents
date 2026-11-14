# Current scale limits and read-only baseline

looptender implements a supervisor beside agents on a device. The [across-device model](devices.md) and [fleet roadmap](roadmap.md#loops-across-devices-planned) are designs, not implemented fleet control. The measurements here cover two existing read-only operations. They establish neither a supported number of active loops nor a promise that work cannot be lost.

## Current bounds

These source limits apply to main at `9a1d149ec8c5ac926a5d4ef007ab8452190894df`. They are not recommended capacities.

| Boundary | Current behavior | Source |
| --- | --- | --- |
| Local discovery | At most 128 registrations in a 1 MiB registry. A full registry refuses additions without discarding references. Registration is discovery metadata, not authority to run. Loops outside it can still exist. | [registry](../internal/registry/registry.go) |
| Panel reads | Four loop reads at once, 20-second whole-read deadline, 15-second default refresh and 8 MiB JSON response ceiling. Reads validate registered configurations and inspect local records/daemon diagnostics. | [reader](../cmd/looptender/dock.go) |
| Supervision | One daemon/daemon lock per loop; default ordinary interval five minutes, control poll two seconds. BB early polls scan structured events through its CLI. Headless active turns also use wrappers and agent subprocesses. | [daemon](../cmd/looptender/supervise.go), [defaults](../internal/config/config.go), [early poll](../internal/adapter/bb/wake.go) |
| BB child observation | At most 200 discovered direct children. Each verified child may add show and interaction queries. Incomplete discovery cannot authorize an idle wake. | [children](../internal/adapter/bb/children.go) |
| Run references | 32 runs, 128 attempts, 256 links, 200 children and 256 KiB per loop. Retention drops older evidence with counters. Diagnostic only; a failed graph write does not fail supervision. | [graph](../internal/rungraph/graph.go) |
| Incident and delivery evidence | 256 incident records/512 KiB and 64 delivery records/128 KiB, independently bounded. Diagnostic evidence cannot authorize replay. | [state table](architecture.md#state-directory) |
| Event log | `runs.jsonl` is append-only; the append path has no rotation/size bound. Long-running loops therefore also need disk-growth measurement. | [append](../internal/state/state.go) |

There is no first-class lane resource admission or account-wide execution ceiling spanning all loops. Per-loop send caps and session locks do not supply one. [P6, P4](roadmap.md#preferred-routes-planned) and [U11](roadmap.md#usage-aware-timing-planned) remain planned. Increasing registry limits alone would not resolve runtime, account or visibility limits.

## Three different loss domains

1. **Supervision records and authority.** Controls, goals, used send allowance and pending transfers live outside the checkout. The [send ledger](../internal/state/sends.go) records intent before a call, counts uncertain attempts conservatively and prevents blind repetition; [ownership generations](architecture.md#ownership-and-control-writes) fence cooperating local daemons. Diagnostic graph identities and registration IDs are distinct, and missing/pruned history is not evidence that no loop or action existed. File flushes and atomic replacement are not backups, restore/rollback safety or power-loss certification. Disk loss, deletion, corruption and rollback remain separate hazards.
2. **Visibility and notifications.** Retained history is incomplete by design. A saved completion can outlive a lost notification; HTTP acceptance is not human receipt. See [completion delivery](operations.md#completing-a-task) and [notification evidence](diagnostics.md#notification-delivery-evidence). Missing daemon diagnostics do not prove a stopped agent. An unreachable device may still run its loops: show last-seen/unknown, never infer successful stop or authorize takeover from silence.
3. **Agent work.** Supervision cannot preserve unpushed commits, working-tree changes or external side effects after losing their device. The [cooperative-hop design](devices.md#what-crosses-and-what-never-crosses) transfers references and a brief, not checkouts, transcripts or credentials. It requires a live origin to grant once after fencing. No lease or unreachable-origin takeover is in the first version.

Local holds retain precedence. Observations cannot clear them; remote controls cannot clear a hold set on the owning device. Revocation cannot stop a compromised machine, revoke its provider credentials or erase plaintext it already received. Enrollment, revocation and remote controls remain [planned](devices.md#enrollment-and-revocation), with no carrier decision made here.

## Reproduce the bounded measurements

Default test runs execute two tiny fixture checks, not the benchmarks:

```sh
go test ./internal/registry -run '^TestScaleRegistryFixtureIsReadOnly$' -count=1
go test ./cmd/looptender -run '^TestScaleDockProjectionKeepsUnavailableEvidence$' -count=1
```

Benchmarks are explicit and limited to 1, 10, 100 and 128 entries. This command runs exactly 100 measured iterations per case, three times, with one Go execution CPU:

```sh
go test ./internal/registry ./cmd/looptender -run '^$' \
  -bench '^(BenchmarkRegistryLoad|BenchmarkDockProjection)$' \
  -benchmem -benchtime=100x -count=3 -cpu=1
```

- `BenchmarkRegistryLoad` reads a generated private temporary registry through the production loader. Referenced configs/state do not exist. Setup and fixture writes are outside timing; repeated reads are warm-cache candidates, not cold-disk trials. `input-bytes/op` is the fixture length, not measured physical I/O. Temporary path length affects that length and allocation costs.
- `BenchmarkDockProjection` runs the existing `buildDockReport` and JSON encoding on in-memory phrase-table inputs, with identifiers hidden. `mixed=false` uses enabled, recently checked loops. `mixed=true` repeats enabled, paused, awaiting-user and unavailable inputs; at one entry it contains only the enabled case. There are no routes, usage windows, children or retained history. `output-bytes/op` is the encoded response length. This is not a maximal payload or an end-to-end panel read.
- Both report elapsed time, bytes allocated and allocations per operation. Projection performs no filesystem reads, CLI calls or daemon requests. The registry loader reads its own file without following references. No runtime instrumentation was added: syscall counts, physical read bytes, cache hits and CLI/process counts were not measured. Neither benchmark queries a provider, network endpoint or owner state.
- No write is timed. The command test binary's existing initialization disables `snapshot.Sync` for tests; this does not affect this in-memory projection and must not be presented as production durability evidence. The registry fixture uses `os.WriteFile` outside timing. No flush/durability benchmark or power-cut test is included.

## Local measurements

Results below are local microbenchmarks, not service-level latency percentiles, concurrency tests or supported fleet sizes. No 1,000/10,000-loop workload was run. Future fleet targets require a separate design and evidence; none is an acceptance claim here.

Measured on 2026-11-14 at 22:24 UTC (2026-11-15 locally), against the base above plus the two benchmark files in this change. Go `go1.27.3`, `darwin/arm64`, `GOARM64=v8.0`, `CGO_ENABLED=1`; macOS 26.4 build 25E246; Apple M5 Pro, 18 logical CPUs, 48 GiB memory; APFS on solid-state storage. `-cpu=1` sets benchmark GOMAXPROCS to one. GOFLAGS, GOGC, GOMEMLIMIT and GOMAXPROCS were unset in the invoking environment. Other work on the shared machine was not controlled; no CPU affinity, cold-cache treatment or thermal/power isolation was applied. Raw results are in [scale-baseline-results.txt](scale-baseline-results.txt).

Benchmark source SHA-256 at measurement:

- `internal/registry/scale_bench_test.go`: `df33938dd18c98043b3b5c539e2faa5cc8cf092464655508c986f19c7f3af86c`
- `cmd/looptender/dock_scale_bench_test.go`: `1690406da28ebbd3406171a42bd117d26309a9dd2d8d7612a9c0616c8a0b131f`

The table reports the median and range of three per-run averages, not per-operation percentiles. Byte and allocation ranges include small Go pooling effects and temporary-path length variation.

| Operation | Entries | Median µs/op (min–max) | B/op | allocs/op | Input or output bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| Registry load | 1 | 14.84 (14.14–15.22) | 3165 | 22 | 380 |
| Registry load | 10 | 29.29 (28.28–29.95) | 22347 | 66 | 3521–3541 |
| Registry load | 100 | 169.51 (168.42–170.17) | 281187 | 387 | 35331 |
| Registry load | 128 | 213.81 (212.57–215.04) | 319244 | 514 | 45215 |
| Projection quiet | 1 | 6.23 (5.77–6.39) | 5921 | 33 | 2309 |
| Projection quiet | 10 | 57.60 (52.42–61.23) | 70602 | 229 | 17271 |
| Projection quiet | 100 | 453.77 (450.06–470.87) | 636019 | 2122 | 166854 |
| Projection quiet | 128 | 591.33 (573.92–596.91) | 706230 | 2710 | 213390 |
| Projection mixed | 1 | 4.57 (4.49–4.71) | 5921–5962 | 33 | 2309 |
| Projection mixed | 10 | 57.42 (57.10–57.49) | 72154 | 230 | 18067 |
| Projection mixed | 100 | 674.64 (662.38–702.07) | 666158–666161 | 1986 | 175873 |
| Projection mixed | 128 | 973.63 (892.62–991.94) | 736425–736428 | 2532 | 224922 |

At 128 entries, the quiet projection emits 213,390 bytes and allocates 706,230 B/op; the mixed projection emits 224,922 bytes and allocates 736,425–736,428 B/op. These inputs are small relative to the panel's maximal route/history fixtures and exclude all record acquisition. Do not extrapolate them to end-to-end panel refresh or active supervisor capacity.

The smallest next optimization to test is preallocating the projection's loop/result slices from the known input count. The current builder appends large loop structs to an initially empty slice. The observed allocation volume makes this a bounded candidate; no profile or comparison proves its benefit yet. Re-run the same cases and existing phrase/order/completeness tests before accepting it. Concurrent readers, admission and larger inventories need separate measurements and design review.
