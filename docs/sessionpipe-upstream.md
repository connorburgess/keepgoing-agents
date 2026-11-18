# Proposed Sessionpipe integration documentation (not submitted)

This is a contribution draft for review, not an upstream issue, PR or published
demo. No maintainer has been contacted. Keep it aligned with the final looptender
implementation and evidence before requesting permission to submit it.

## Proposed entry: looptender tier-0 loop reports

looptender supervises coding-agent loops using structured signals and bounded
controls. Its unreleased source can report loop attention and end facts directly
to a Sessionpipe v1 receiver that the person configures. It identifies itself as
`looptender`, uses opaque per-loop report-stream IDs and monotonic sequences,
and sends only tier 0. This integration needs no new harness hook, account,
Sessionpipe daemon, GUI modification or hosted service.

Configure looptender's `loop.report` with `format: sessionpipe`, `scheme: bearer`,
an environment variable naming the receiver origin and an environment variable
holding its sink token. Configure the receiver separately. The receiver must
advertise protocol 1/events/bearer and accept same-origin two-event batches.
[Setup, mapping and limits](sessionpipe.md) document the exact fields and commands.

`await_user` and selected error reports become `attention.needed` without words;
`complete` and `stop` become `session.ended`. Other reports are empty-data
`looptender.<type>` extensions, which conforming receivers store but may not
render. Every batch first announces `session.started` for the report stream.
There is no inference of provider work state, success or usage allowances.
No loop/task names, prompts, transcript, files, account/model or private report
data are included. No remote command can answer a looptender hold through this
integration.

Delivery is best effort, with one bounded POST, bearer authentication, no
redirects, strict 202 counts, and private sequence reservation before sending.
It does not claim the full durable sender contract: outbox replay, indefinite
backoff, persistent authentication pause and full conformance remain follow-up.
Receiver/board/phone live acceptance remains open.

A reproducible local fixture uses the real Sessionpipe core schema and privacy
filter to validate two separate looptender CLI report processes. It needs an
already-built trusted core checkout, installs nothing, binds only loopback and
uses fictional data. The script and its limits are in
[`scripts/sessionpipe-demo.mjs`](../scripts/sessionpipe-demo.mjs).

## Proposed upstream contribution path

The primary project is [spacesheep-dev/sessionpipe](https://github.com/spacesheep-dev/sessionpipe).
Start with its [contribution guide](https://github.com/spacesheep-dev/sessionpipe/blob/baf8022ce0b685dc93393557208593e479dea3ed/CONTRIBUTING.md),
[protocol](https://github.com/spacesheep-dev/sessionpipe/blob/baf8022ce0b685dc93393557208593e479dea3ed/spec/PROTOCOL.md),
[HTTP binding](https://github.com/spacesheep-dev/sessionpipe/blob/baf8022ce0b685dc93393557208593e479dea3ed/spec/HTTP.md)
and [website source](https://github.com/spacesheep-dev/sessionpipe/tree/baf8022ce0b685dc93393557208593e479dea3ed/website).
A proposed documentation PR would add a short external-sender example to the
integration documentation (location to agree with maintainers), link the
looptender setup and fixture, and explicitly label partial sender coverage.
It would not add a fake hook adapter in `packages/core/src/adapters`: upstream's
adapter process requires actual documented harness hooks and recorded scrubbed
payloads, while this feature is a direct external sender.

No protocol change is required for OUT: custom harness names and unknown dotted
event types are already allowed. A core-first example can validate the standard
attention/end events and safe empty extensions without a GUI fork. A GUI-specific
view for extension meanings belongs to that separate project's maintainers.
Do not submit invented provider hook payloads as upstream conformance fixtures.

Upstream spec changes use the contribution guide's spec-proposal process with
motivation, exact RFC 2119 change, proving fixture and compatibility statement.
A future BACK proposal would need a non-harness dispatch contract and explicit
hold command with ownership, replay, expiry, caller permission and unknown-outcome
rules; existing harness `prompt`/`cancel` commands do not establish that capability.
That proposal is blocked by the owner's #233 hold and is not being pursued here.

IN needs no speculative new upstream control command: first implement local typed
intake and resolve observe-only loop ownership, then read only a person-configured
filtered file sink. Tier-0 limits are a separate upstream capability gap; reading
tier 1 would also receive words and paths, outside this integration's boundary.
