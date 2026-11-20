# Telegram remote channel

Design specification and delivery plan, 2026-11-20. This document specifies how a person reaches a device's loops through a personal Telegram bot—starting with redacted alerts and safe remote controls, and optionally extending to threaded chat steering as a lightweight mobile interface.

**Current status:** **TG1** (`internal/telegram` plus `loop.report.format: telegram` in `internal/report`, `internal/config`, and `schema/looptender.schema.json`), **TG2** (`looptender telegram setup|status|test|disconnect`), and **TG3** (`looptender telegram poll` outbound `getUpdates` long polling for `/status`, `Pause`, and `Stop` with persisted offsets, staleness cutoffs, and `telegram_pause`/`telegram_stop` incident attribution) are implemented on unreleased source and verified with offline HTTP stand-ins and unit tests. Terminal QR rendering, `doctor` integration, **TG4–TG5** (ticketed `Resume`, per-loop private-chat topic creation, opt-in `chat` steering, and unified `onboard`), and live Telegram Bot API verification remain **planned, not built**.

## Where the architectural line sits

The boundary is **who owns the device**, not macOS versus cloud:

- **Inside the boundary (any device the person owns and signed into):** a laptop, an always-on desktop or Mac mini, a home Linux machine, or a personal cloud virtual machine the person rents and controls. looptender runs on that device as its own [command center](command-center.md), driving the native agent CLIs (`claude`, `codex`, `cursor`, `bb`) and `gh` using the logins the person already created there. Telegram is only a **remote panel** reaching that device over outbound HTTPS.
- **Outside the boundary (a multi-tenant hosted supervisor):** a service operated by someone else that holds users' provider subscription logins or runs shared agent containers on their behalf. As decided in [where the supervisor runs](command-center.md#a-hosted-supervisor), looptender never collects, copies, or intermediates provider credentials.

A person who wants 24/7 Telegram responsiveness while their laptop sleeps can run looptender on an always-on home machine or their own small virtual machine; the device model stays identical.

## Why Telegram (and why not WhatsApp in core)

Telegram fits looptender's outbound-only, local-first model:

- **No inbound port or tunnel:** `getUpdates` long polling works behind NAT and firewalls over outbound HTTPS, with no webhook server, DNS, or TLS certificate to operate.
- **Free, official personal bots:** A person creates a private bot with `@BotFather` in under a minute with no business verification or per-message fees.
- **Private-chat topics (Bot API 9.3/9.4, 2026) and direct topic links (Bot API 10.4, 2026-11-09):** A bot can create forum topics directly inside a 1-on-1 private chat (`createForumTopic`) and link to them (`https://t.me/<bot_username>/<message_thread_id>`), giving each loop its own thread without requiring a separate supergroup.
- **Native controls and streaming:** Inline buttons support `Pause`, `Stop`, and ticketed `Resume`; Bot API 10.1–10.3 rich message drafts (`sendRichMessageDraft` with `can_stop`) support streamed summaries with a stop button if opt-in chat mode is added later. (Bot API 10.4 HTML widgets were restricted for general bots on 2026-11-13 and are deliberately excluded.)

WhatsApp is out of scope for core looptender: the official WhatsApp Cloud API requires Meta Business onboarding, a public HTTPS webhook endpoint, and pre-approved templates outside a 24-hour customer window; unofficial WhatsApp Web bridges depend on reverse-engineered protocols and violate platform terms, conflicting with looptender's safeguard rules. Users who want WhatsApp can run an external bridge behind [Standard Webhooks](configuration.md#report-yield-daemon).

## Modes and privacy boundaries

Telegram Bot API traffic is encrypted in transit to Telegram (TLS), **not end-to-end encrypted**: Telegram's infrastructure can read message bodies and callback payloads. Because of that, the channel separates two explicit modes:

| Mode | Default | What crosses Telegram | What can it do on the device? |
| --- | --- | --- | --- |
| **`alerts`** | Yes | Redacted event summaries only (loop name, event type, recorded cause, device name, hold/limit counts). Never prompts, transcripts, diffs, file paths, or credentials. | Receive alerts; `/status` read-only view; `Pause` and `Stop` holds; `Resume` only through a short-lived single-use ticket bound to the exact hold (`client prepare-resume` / `client resume`). |
| **`chat`** | No (explicit opt-in) | Everything in `alerts`, plus human steering messages sent in a loop's topic and bounded, opt-in summaries of the agent's latest completed turn. Setup states plainly that Telegram servers see this text. | Everything in `alerts`, plus forwarding a human reply in a topic as the next prompt to a held (`await-user`) or idle loop, and starting a new bounded loop for an explicitly allowlisted repository. |

Rules that hold in **both** modes:

- **Structured signals only for supervision.** Neither mode parses Telegram prose or agent output to decide holds, handoffs, retries, or completion. Human messages in `chat` mode are explicit person-initiated actions, never automated supervision logic.
- **Never loosen agent permissions.** Telegram cannot change an agent's sandbox mode, approval policy, or extra CLI arguments, and cannot enroll accounts or modify logins.
- **Local stop always wins.** A local `stop` or `pause` takes precedence; Telegram cannot bypass an active stop or override a refusal/restriction hold without the normal explicit `resume` ticket bound to current evidence.
- **Mandatory bounds for remote starts.** Any loop created from Telegram must carry explicit bounds (`max_sends`, `no_progress_timeout`, or `deadline`) and target only a repository the person pre-registered on that device.

## User setup and onboarding

### Option A — Local zero-infrastructure pairing (default, planned in TG2)

No third-party setup service is involved; only the person and their device ever hold the bot token:

1. Create a bot via `@BotFather` (`/newbot`), choose a name and username, and copy the token.
2. Run `looptender telegram setup` (or open **Telegram** in `looptender panel`), paste the token (hidden input, written with `0600` permissions to a private secret file outside any repository), and choose `alerts` (default) or `chat`.
3. looptender validates the token with `getMe`, calls `setMyCommands` to register the slash menu, and prints a one-time deep link and QR code:
   ```text
   https://t.me/example_looptender_bot?start=pair_<nonce>
   ```
4. The person opens the link in Telegram and taps **Start**. Telegram sends `/start pair_<nonce>` to the bot; looptender reads it via `getUpdates`, verifies the nonce and expiry, rejects non-private chats or bot senders, and persists `pairing.json` locked strictly to that `allowed_user_id` and `allowed_chat_id`.
5. If `getMe` reports `has_topics_enabled: true`, looptender creates one private-chat topic per registered loop (`createForumTopic`) and stores the mapping in private state. Otherwise it uses the single private DM cleanly.

### Unified onboarding direction (`looptender onboard`, planned with TG5)

To make "install, connect GitHub and AI accounts, connect Telegram, and get going" one guided sequence on a person's own device without violating credential rules:

1. **Auto-detect** installed CLIs (`claude`, `codex`, `cursor`, `bb`, `gh`) and existing logins using the existing read-only status checks (`doctor` / `accounts`).
2. **1-click native handoffs** for missing sign-ins (`gh auth login` device flow; native provider login handoff already modeled in `panel_account_connect.go`). looptender never reads, copies, or stores provider credentials.
3. **Propose default preferred routes** from the detected accounts and select allowed local repositories with default bounds.
4. **Pair Telegram** via Option A above and optionally install/start the background service (`launchd` / `systemd`).

### Option B — Telegram Managed Bots (undecided)

Bot API 9.6 (2026-04-03) introduced Managed Bots (`can_manage_bots`, `KeyboardButtonRequestManagedBot`, `getManagedBotToken`), which allows a project-operated manager bot to create a user's bot in one confirmation tap without visiting `@BotFather`.

This is **not chosen** for initial delivery because it requires a hosted helper and leaves the manager bot with `getManagedBotToken` / `replaceManagedBotToken` capability on the child bot—placing the project operator inside the user's trust boundary. If reconsidered later, it must remain an explicitly disclosed optional alternative beside Option A, never a requirement.

## Threat model and security invariants

| Threat | Mitigation |
| --- | --- |
| **Bot token leaked in error logs or delivery history** | Telegram puts the bot token in the request URL path (`/bot<token>/<method>`). `internal/telegram.Client` strips and replaces all `net/http` and `url.Error` details so the token can never appear in returned errors, logs, or `deliveries.json`. The token is never stored inside `pairing.json`. |
| **Token redirected to a third-party host** | `Client` enforces `https://` (allowing `http://` only for loopback in offline tests) and disables HTTP redirects (`http.ErrUseLastResponse`). |
| **Stranger messaging the bot** | Every inbound update and callback query must match both `allowed_user_id` and `allowed_chat_id` recorded during one-time nonce pairing, and `from.is_bot` must be false. All other updates are silently ignored and never acknowledged with loop details. |
| **Stale button tap after laptop sleep or restart** | `last_update_offset` is persisted atomically before acting; updates older than a bounded staleness window are discarded. `Resume` callbacks carry a short-lived, single-use ticket bound to the exact hold/state snapshot (`client prepare-resume` / `client resume`), so a replayed or delayed tap cannot resume a loop whose state changed. |
| **Lost or compromised phone** | In `alerts` mode, a finder can at most view redacted summaries, pause, stop, or consume a fresh single-use resume ticket. Revoking is one local command (`looptender telegram disconnect`) or `/revoke` in `@BotFather`. |
| **Prompt injection from external repos into chat** | Supervision decisions never read message or transcript text. In `chat` mode, only messages from `allowed_user_id` are accepted; agent turn summaries are bounded plain text, never executed as commands. |
| **Oversized or malformed API responses** | Responses are read through `io.LimitReader` (1 MiB cap) and decoded strictly; transport failures return sanitized errors. |

## Phased delivery plan

Each phase requires offline fixture tests before merge; live Telegram checks are tracked separately and never assumed from fixtures.

- **TG1 — Internal Telegram client, pairing primitives, redacted event formatter, and report format** *(implemented and verified against offline HTTP stand-ins; live Telegram check open)*. Pure Go stdlib Bot API client (`GetMe`, `SendMessage`, `GetUpdates`, `AnswerCallbackQuery`, `SetMyCommands`, `CreateForumTopic`), strict token redaction and redirect refusal, one-time pairing nonce/matcher and atomic `0600` pairing/token state store, redacted event formatter, and outbound `loop.report.format: telegram` in `internal/report` with `deliveries.json` recording.
- **TG2 — Guided local pairing (`looptender telegram setup|status|test|disconnect`)** *(implemented and verified against offline HTTP stand-ins; terminal QR, `doctor` integration, and live Telegram check open)*. Token prompt into a `0600` secret file, `getMe` check, `setMyCommands`, deep-link `?start=pair_<nonce>` pairing, strict `user_id`/`chat_id` lock, and redacted `status`/`test`/`disconnect`.
- **TG3 — Outbound polling runner: `/status`, `Pause`, and `Stop`** *(implemented via `looptender telegram poll` and verified against offline HTTP stand-ins; background service wiring and live Telegram check open)*. Long-poll `getUpdates` under a single-instance lock with atomic offset persistence before acting, staleness cutoffs, read-only `/status` across registered loops, and restrictive `Pause`/`Stop` inline buttons bound to a 64-bit control-digest prefix and routed strictly through `client pause` / `client stop` with `telegram_pause`/`telegram_stop` incident attribution.
- **TG4 — Ticketed `Resume` and private-chat topics per loop**. Bind `Resume` inline buttons to `client prepare-resume` / `client resume` single-use tickets; automatically map registered loops to private-chat forum topics (`createForumTopic`) when `has_topics_enabled` is true, with fallback to single DM.
- **TG5 — Opt-in `chat` mode and unified `onboard` flow**. Explicit opt-in chat steering in a loop's topic (`await-user` replies, bounded turn summaries), `/new <repo> <goal>` restricted to allowlisted repositories with mandatory bounds and fixed local permission posture, and unified `looptender onboard` connecting detected AI CLIs, GitHub (`gh`), preferred routes, Telegram, and background service.
