# Sessionpipe reporting integration (unreleased)

Source supports opt-in **OUT reporting** to a receiver the person runs. This is
not a supervision adapter: `adapter: sessionpipe` is unsupported and configuration
rejects it. It cannot observe, wake, send to, stop or own an agent session, answer
a hold, or change routes. Supervision continues through the loop's existing
adapter, controls, ownership and budgets. Delivery results never classify a
provider error or decide failover. Unknown, refused and restricted provider
results retain their existing handling.

## Setup

Run and configure your own Sessionpipe receiver separately. Supply its **origin**
(no path, query, fragment or embedded credentials) and sink bearer token through
the existing report secret environment or private secret file. HTTPS is required
except on localhost/127.0.0.1/::1. No hosted destination is selected automatically.
This change installs nothing, pairs nothing and touches no accounts or outbox.

```yaml
loop:
  # Other loop settings and the existing agent adapter remain required.
  report:
    format: sessionpipe
    scheme: bearer
    url_env: EXAMPLE_SESSIONPIPE_ORIGIN
    secret_env: EXAMPLE_SESSIONPIPE_TOKEN
    events: [await_user, complete, stop, error]
    timeout: 15s
```

Set the environment privately; do not put tokens in YAML or command arguments.
The configured URL identifies an origin such as `https://receiver.example.com`.
The receiver must publish `/.well-known/sessionpipe`, advertise protocol 1,
`events`, bearer authentication, a tier cap from 0 to 3, and a same-origin events
endpoint. Discovery is unauthenticated. Cross-origin endpoints and redirects are
refused, so discovery cannot forward the token elsewhere. Receivers requiring
authenticated discovery are not supported by this slice.

Preview without network or persistent state using the built source command:

```sh
keepgoing report test --dry-run -c looptender.yaml
```

The preview uses an all-zero example session identity and sequences 1 and 2;
event IDs are illustrative. To send a real test, explicitly run `keepgoing report
test -c looptender.yaml`. It sends an extension event, not a human attention or
an agent wake. That live command was not exercised for this implementation.

## Payload and mapping

Only tier 0 is supported. The harness is `looptender`, with a random opaque
session identity for this loop's report stream. Loop names, `report.source`,
task text, `Event.Text`, arbitrary `Event.Data`, paths, hostnames, model/account
identities, report error details and transcripts are omitted. No tier selection
can enable their transmission.

Each report is a two-event batch. `session.started` with source `resume` precedes
the mapped report so even a receiver that missed earlier batches sees a start.
It describes the report stream, not the creation or resumption of an agent.
Both events have distinct ULIDs and increasing sequences.

| Report | Sessionpipe event | Tier-0 data |
| --- | --- | --- |
| `await_user`, `error`, `child_error`, `hung_turn`, `refusal_streak` | `attention.needed` | opaque attention ID, kind `error` |
| `complete`, `stop` | `session.ended` | reason `other`; completion is a declaration, not verified task success |
| All other subscribed report types | `looptender.<type>` | empty |

Unknown Sessionpipe types must be stored by receivers, but a board may not render
their meaning. In particular, `limit_wait` remains `looptender.limit_wait`, not a
usage reading or a claim that work is running. The protocol's `limits.observed`
and attention words require tier 1, which this integration never sends. A board
can show needs-you/ended facts; it cannot explain the hold or certify work state
from this report stream. There is no periodic heartbeat, attention-answer flow,
phone push or GUI acceptance claim. Repeated starts and later events can affect a
receiver's attention display; use looptender status as the authority for holds.

## State and delivery limits

`sessionpipe.json` in the loop's private state directory holds schema 1, the
opaque identity and the next sequence. The existing nonwaiting `delivery` lock
serializes reservations across callers, including separate reporter instances.
Two sequences are reserved and atomically published before the POST. Contention,
corrupt/oversized/nonregular state or publication failure refuses that send. Existing snapshots require non-null version, identity and next-sequence fields; defaults apply only when the file is genuinely absent. Invalid snapshots remain unchanged.
Sequences consumed by failures are not reused. Retain this file across restart
and migration; do not copy the same loop state to concurrent independent owners.
Removing it starts a new report identity. Publication uses the existing private
snapshot primitives; it does not promise power-loss durability.

Discovery and POST share a maximum 15-second deadline (or the shorter configured
report timeout). Responses are bounded to 64 KiB. The two-event body must fit the
receiver's advertised batch limits; no splitting occurs. One POST is attempted,
without redirects or automatic retry. A network failure, unexpected status,
malformed response, rejected event or acknowledgement count mismatch leaves
delivery unverified and consumes its sequence reservation. Only HTTP 202 with
accepted plus duplicate counts totaling two, no rejected events and a valid tier
cap is recorded as HTTP accepted. No HTTP acknowledgement proves board or phone
receipt. Diagnostic history remains in `deliveries.json`; the new `protocol_failed` outcome makes older strict history readers report unavailable until upgraded (it does not change loop state); discovery/setup failure
has no POST attempt, and protocol failure is recorded separately.

This is **best-effort reporting**, not a conformant durable Sessionpipe sender:
it has no outbox, cursor, indefinite backoff, replay, daily discovery cache or
persistent authentication-error pause. It does not retry uncertain delivery;
a later report is a new event with new sequences. Discovery runs per report.
Report history failure never changes loop holds or authorizes another send.
The existing report event subscription still applies. No delivery outcome is
used as a provider limit, refusal, policy or availability signal.

## Evidence and remaining scope

The integration follows the upstream draft
[protocol v1](https://github.com/spacesheep-dev/sessionpipe/blob/baf8022ce0b685dc93393557208593e479dea3ed/spec/PROTOCOL.md),
[HTTP binding](https://github.com/spacesheep-dev/sessionpipe/blob/baf8022ce0b685dc93393557208593e479dea3ed/spec/HTTP.md)
and [privacy rules](https://github.com/spacesheep-dev/sessionpipe/blob/baf8022ce0b685dc93393557208593e479dea3ed/spec/PRIVACY.md),
read from a local upstream checkout. The specification is licensed CC BY 4.0;
fixtures here use fictional envelopes derived from its documented shapes, with
no copied transcripts. This is a narrow integration, not full upstream
conformance certification. The earlier roadmap research used core `8eada32` and
GUI `c7a813d`; the current protocol was rechecked before implementation.

Regression fixtures cover privacy, restart identity/sequence, reservation
contention/concurrency, corrupted state, acknowledgement mismatch and bounded
transport. See [testing](testing.md#sessionpipe-reporting-integration-unreleased).
Live receiver, GUI, phone, native Windows/Linux execution, installed artifacts and
upstream conformance remain unverified. No subprocess or OS control is added.

**IN is separate:** reading a configured filtered tier-0 file sink needs F8 typed
intake and an owner decision about observing a loop that cannot be woken. Never
read the full-detail outbox. Tier-0 input has no usage limits; silence is unknown,
not idle. No general Sessionpipe ingest CLI or API for agent observation/send/stop
was established by this work. Those functions are not exposed speculatively.

**BACK remains held by #233:** the signed control channel targets harnesses and
has no documented command for resuming a looptender hold. It requires an explicit
owner decision and upstream protocol/daemon/board support. The OUT slice grants no
remote-answer or coordinator-message authority.

## Protocol coverage matrix

| Surface | Supported and exercised | Deliberate limits / next work |
| --- | --- | --- |
| Compatibility | Protocol integer 1; draft core source `baf8022`, local built core 0.11.0 schema/data/privacy validation | Earlier research pins are not acceptance evidence; future incompatible integers refuse. No release or installed-package claim. |
| Discovery | Origin-root GET, protocol/events/bearer/tier checks; same-origin endpoint; absent batch limits use 50/262144 | Per-report read, no 24-hour cache; no authenticated discovery, redirected discovery or foreign endpoint. |
| Event endpoint | POST `{events:[...]}`, content type, bearer and `sessionpipe-protocol: 1`; advertised two-event/byte bounds | No native endpoint, file sink, outbox, control or pairing endpoint. No webhook HMAC is needed for v1 bearer delivery. |
| Envelopes / fields | Protocol, ULID, fixed harness, opaque session ID, reserved seq, UTC milliseconds, tier 0, empty privacy rulesets, typed data | No source/loop/task names, transcript, arbitrary report data, account/model, machine/path/repo/title/URL or attention words. |
| Event vocabulary | `session.started`, `attention.needed`, `session.ended`; empty-data `looptender.<report>` extensions | No invented provider turns, limit readings, periodic heartbeat, attention-cleared/answer assertion, backfill or forget. Extensions may need a receiver view. |
| Authentication / errors | Explicit bearer config; token withheld from discovery and errors; HTTP/transport failures diagnostic only; no redirects | No automatic reauth, login, credential copying or persistent 401 sink pause. Error classification never reaches routing. |
| Acknowledgements | Only 202; accepted+duplicates exactly two, zero rejected, valid tier; duplicate acknowledgement accepted | Invalid/oversized/missing/rejected/mismatched responses remain unverified; no automatic resend or cursor advance. |
| Restart / concurrency | Fresh CLI processes retain identity and sequences; private two-sequence reservation under nonwaiting delivery lock; rejected/corrupt state refuses send | Contention can lose a best-effort report. No replay/outbox/indefinite retry, crash-before-POST recovery or power-loss guarantee. |
| Evidence | Native macOS fictional Go receivers, race tests, optional local demo with actual built core schema/data/filter | Local reference receiver package is a placeholder. This is not upstream receiver/conformance certification, GUI/phone or live provider acceptance. |
| IN | None exposed | F8 typed intake + observe-only ownership decision first; explicit filtered file sink only, silence unknown; usage limits unavailable at tier 0. |
| BACK | None exposed; #233 remains held | Needs owner authorization plus upstream non-harness control dispatch and a looptender-hold command/board support. Existing prompt/cancel commands cannot grant loop authority. |

## Repeatable local demo and upstream documentation preparation

With an existing built Sessionpipe core checkout (no installation is performed):

```sh
go build -o /tmp/example-keepgoing ./cmd/looptender
node scripts/sessionpipe-demo.mjs /tmp/example-keepgoing /path/to/sessionpipe/packages/core
```

The fixture binds an ephemeral loopback receiver, imports the actual core
`Batch`, `EventData`, `BatchResponse` and `filterEvent`, and starts two bounded
`report test` CLI processes with fictional configuration. It validates the wire
shape, tier-0 filtering, private-field exclusion and identity/sequences across
process restart, then removes its temporary state. It does not start a provider,
Sessionpipe daemon or GUI. Node 20+ and already-built core dependencies are
prerequisites; no npm download or upstream build runs. Use only a trusted checkout.

The local demo was exercised against existing core 0.11.0 artifacts; those
artifacts were not rebuilt or certified against upstream HEAD. The primary
reference receiver's source entry was still a placeholder when read. The demo
therefore uses our fixture HTTP endpoint with real core validation, not a claim
that the upstream receiver accepted events.

The [upstream integration contribution draft](sessionpipe-upstream.md) is ready
for review with this change. It is not submitted or published. The next
maintainable OUT work is durable pending batches, restart replay with the same
identity/seq, bounded worker backoff and persistent authentication pause, proved
with core delivery scenarios. Those are local implementation gaps, not missing
upstream protocol capabilities. IN requires local ownership/intake work; BACK has
an actual documented upstream capability gap as well as the owner hold.
