# Accounts in the panel

**Unreleased source.** The persistent Accounts view lists contexts explicitly
selected in a private device-local catalog. It shows the owner-chosen name,
provider, selection mode and retained status evidence. It does not discover
every account on the computer or turn route group names into verified accounts.

Login wording is scoped to the selected CLI: "Selected CLI reports login" or
"Selected CLI reports no login". It does not classify every BB session or a
provider account globally. Another running session can have a different context.

**View recorded usage** opens Routes and its existing Allowances section.
Those readings belong to configured route groups, are not live, and are not
linked to these account cards. Read their recorded age, ended windows or
unavailable state; an old limit reading does not establish current exhaustion. Selecting an
account does not assign its ID to a route group or usage identity. The link
reads only the existing recorded projection; it does not invoke BB usage APIs
or refresh provider measurements, including APIs whose refresh:false option
may still contact a provider.

The page is read-only. Refresh reads the catalog only, executes no provider
command and sends no model request. The setup commands below do not start a
loop, enroll a route, sign in or copy credentials. Complete provider sign-in in
the official provider tool first.

## Select an existing context

Examples use fictional labels and directories. Substitute existing directories;
never move an authenticated directory to match an account name.

    looptender accounts add --label "Example studio" --provider claude-code \
      --selection explicit_directory --account-dir /private/example/claude-a \
      --cli /usr/local/bin/claude --workdir /projects/example --json

    looptender accounts add --label "Example workshop" --provider codex \
      --selection inherited --cli /usr/local/bin/codex \
      --workdir /projects/example --json

The command returns an opaque connection ID and revision. Omitting selection
uses inherited environment and provider defaults. Inherited is not an isolated
login or a stable subscription identity. Explicit directory selects the existing
CLAUDE_CONFIG_DIR or CODEX_HOME context. The environment can still supply other
authentication inputs; selection does not override arbitrary credential sources.

Only Claude Code and Codex have status checks in this slice. Cursor, BB and
other providers may be selected as externally_managed entries, without CLI,
account-directory or working-directory flags. Their cards say external or
unsupported; use their own controls. This is not generic API authentication.

    looptender accounts add --label "Example external" --provider bb \
      --selection externally_managed --json

No credentials, email or provider account identifiers are accepted as fields.
Labels are owner-selected plain text up to 120 bytes, not identity evidence.
The catalog holds at most 64 entries. There is no removal/route-assignment
command in this slice.

## Explicit status check

    looptender accounts check --id CONNECTION_ID --json
    looptender accounts list --json

The Accounts page names this command in its footnote and in a card's
"To check again" line.
Run it yourself to record a new observation for one entry; the panel never runs
it. CONNECTION_ID is the opaque ID that accounts add returned, also listed by
accounts list.

Check runs only the selected executable's version command, then Claude's
auth status --json or Codex's login status. Each command has a five-second
deadline and 64 KiB stdout limit; stderr is discarded. The commands do not
receive the variables of the session that ran the check (the list below). Version output must
match the existing diagnostic version format. A recognized version string is
not a guarantee about every release's behavior. No login or model command runs.
The provider's own status command may contact its account service.

Claude uses the existing diagnostic loggedIn/exit semantics; Codex exit zero
reports credentials present and a nonzero exit is ambiguous. No account identity
is inferred. Only allowlisted broad Claude subscriptionType strings, when
actually supplied by structured status, become reported plan evidence.
Raw max displays as Max. Unknown plan values are discarded; no numeric tier,
capacity or quota is inferred. Codex's login status reports no plan, so a
Codex card says "Codex does not report a plan." A Claude card without a plan
says why: "Claude Code's status reported no recognized plan." after a login,
"No plan is read without a login." after none, and "Not checked" before any
check. These are not failures and need nothing; the card's JSON carries the
reason as `plan.missing` (`not_reported`, `not_recognized`, `no_login`,
`not_checked`). A check that established no login leaves the plan Unavailable.

The catalog records no model: a route chooses the model, and a check never
calls one. The page says so in one line instead of showing configured and
actual response model rows; `configuredModel` and `observedModel` stay in the
JSON with `missing: not_recorded`. A requested/probe model or configuration
fingerprint is not an actual response model or verified account identity.

The page's Routes row says whether a route [names the entry](#say-which-account-a-route-uses),
from the routes the panel read ("A route names this account."). When none
does, it says "No route names this account yet. Add `account: <ID>` to a route
to link it", with the ID filled in under **Show identifiers**. When not every
loop or route could be read, it says the answer is not known rather than
none. The panel data carries this as the card's `routes` (`named`, `none`,
`unknown`); `accounts list` does not read routes and leaves it out.

A check's successful command exit means its minimized observation was saved;
it does not mean login passed. Inspect auth state and reason. Missing/corrupt
storage or a concurrent selection change refuses the operation. An overlapping
check for the same selection returns skipped. A recognized absent login,
unavailable result, timeout or cancellation is recorded as such. A latest
check that produced no login result, including one refused because the context
changed, preserves the previous recognized observation and its original time.
The card keeps showing that login and plan with their own age, not as refreshed,
with one line, marked "Needs you", saying when the latest check ran and why it
gave no result, for example "could not be compared: the context differs from
the selection". A
card with no earlier recognized observation shows Unavailable and the reason.

Nothing checks again on its own, so the page shows each observation's age in
plain words ("Checked 3 h ago") and never as a warning. A login or plan
observation older than 24 hours adds "· check again to confirm", and the card
shows the check command to copy (its connection ID appears only with
**Show identifiers**). Warning style, with the words "Needs you", is
kept for what a person must act on: no login reported, a latest check that
failed (timed out, was cancelled, or did not establish a result), or a changed
context. A card whose provider is externally managed, or that was never
checked, is not a warning. Age is not health, quota, account-identity
verification or permission to act. Ordinary page refresh never refreshes login
evidence.

In the data, each evidence keeps `freshness` (`stale` five minutes after the
observation, and always for an observation kept after a failed check) for
existing consumers, and adds
`ageSeconds` and `checkAgain`. Each card adds `needsPerson` and, when it is
true, `attention`: `no_login`, `check_failed` or `context_changed`.

## Several accounts of one provider

One person can keep several accounts of the same provider apart. Each account
is a separate catalog entry with its own login context and its own label. The
label is plain text you choose. looptender never derives, generates or checks
it, and it is not evidence of which account a context is signed in to.

**Several Claude Code subscriptions.** Sign in to each one in its own config
directory with the official Claude Code tool, then add one explicit_directory
entry per directory. The directory is the CLAUDE_CONFIG_DIR that entry selects:

    looptender accounts add --label "work" --provider claude-code \
      --selection explicit_directory --account-dir /private/example/claude-work \
      --cli /usr/local/bin/claude --workdir /projects/example --json

    looptender accounts add --label "personal" --provider claude-code \
      --selection explicit_directory --account-dir /private/example/claude-personal \
      --cli /usr/local/bin/claude --workdir /projects/example --json

**Several Codex accounts.** Use a separate CODEX_HOME for each account and add
one explicit_directory entry per home, in the same way. Codex keeps its
credentials under that home ([fallbacks](fallbacks.md)), so separate homes are
separate logins. The check reports only whether credentials are present: exit
zero means present and any other exit is ambiguous. Not verified: whether any
Codex status output names the account, or tells a ChatGPT sign-in from an
API-key sign-in. looptender does not claim either.

**An API key beside a subscription.** Give the key its own entry with its own
label, for example "api-team". If you use the key through the provider CLI, sign
in with it in its own config directory or CODEX_HOME and add an
explicit_directory entry for that directory. If it reaches the CLI only through
the inherited environment, the entry is inherited, which is not an isolated
login. A key used with no supported CLI can be an externally_managed entry with
--provider other. In every case the key itself is never a field: looptender
never stores or reads it, and the entry records only the label and the
selected context.

**Route account groups are separate labels.** A route's group (shown in the
panel as its account group) is also free text you choose. It names the usage
limit the route draws on and is not checked against any login or against these
entries. A route can name an entry with its own `account` field (below); the
group stays a separate label either way.

**BB thread routes have no per-thread account.** BB selects the config directory
per project, not per thread, so neither a route nor a catalog entry can pin a BB
thread to an account. A local probe for a BB route reads the CLI on this
computer, which is BB's login only when they share it
([fallbacks](fallbacks.md)).

## Say which account a route uses

A route can state which catalog entry it uses, with an optional `account`
field holding the entry's ID (the 32 hexadecimal characters `accounts add`
prints). It goes on a loop's first route (`loop.account`), on a fallback in
`fallbacks.yaml`, or on a route in the [standard list](routes.md) (also with
`routes add --account ID`):

    loop:
      adapter: claude
      provider: claude-code
      group: work
      account: 0123456789abcdef0123456789abcdef   # the "work" entry
      claude: {workdir: /projects/example, env: {CLAUDE_CONFIG_DIR: /private/example/claude-work}}

**Upgrade first.** Configuration files are read strictly: a looptender build
from before this field refuses a whole loop configuration or `fallbacks.yaml`
that contains `account`, naming the unknown field, and cannot supervise, show
or check that loop until the field is removed. It reads such a `routes.yaml` as
invalid: every loop that follows the list then has no other routes to hand off
to, and the `routes` commands refuse to change it. Add `account`
only after every build that reads the file knows it: the loop's supervisor
(its daemon or service), the panel, the BB plugin's CLI and the menu's CLI,
and for `routes.yaml` every loop that follows it. Check with the panel's
Versions view, which lists each place a build runs and whether it is behind,
and with `doctor`, whose `supervisor.live_version` check compares the running
daemon's build with this one. Run `validate` with each of those builds if you
are unsure.

It is your statement. looptender never sets, derives or guesses it, and it
does not change how a route runs, is tested, is chosen or how its usage is
recorded. Nothing refuses to run because a route has no `account`.

`validate` and `doctor` compare it with the catalog, for consistency only:

- The ID must be in the catalog, the entry must be for the route's provider
  (an `other` entry stands for any provider the catalog has no word for, such
  as a defined agent), and when both the route and the entry name an account
  or config directory (`CLAUDE_CONFIG_DIR` or `CODEX_HOME` in the route's
  environment, and the entry's directory) they must be the same directory.
  For a headless or command route a mismatch is a configuration error:
  `validate` exits 1 and `doctor` reports `agent.<route>.account` as an error.
- A BB thread route's statement is accepted as given, because BB has no
  account per thread. It is shown as "assigned by you; not verified". An ID
  that is not in the catalog is a warning.
- A catalog that cannot be read is a warning in `doctor` (`accounts.catalog`)
  and `validate`, never a refusal. A running loop never reads the catalog.
- No credential is read and no provider command is run. Messages name the
  entry's ID and label, never a directory.

`status --json` carries, on `primaryAgent` and `targetAgent`, and the panel
data carries, on each route of a loop (`catalogAccount`), an `account` object:
`id` and `label` of the entry the route names, `assigned` (`by_you` or
`none`) and `check` (`consistent`, `not_verified`, `not_in_catalog`,
`provider_mismatch`, `directory_mismatch` or `catalog_unavailable`). For a
route that names no entry it carries `context` instead: a hash of the
provider, how the account is selected (its directory, `inherited`,
`bb_thread` or `externally_managed`) and the quota group, computed as usage
identities are, so no path is included. Two routes with the same `context`
select the same account context as configured; that does not make them the
same account, and different contexts are not said to be different accounts.
The label and ID are shown as they are in the Accounts view. The panel page
does not show these fields yet.

## Change a selection deliberately

List JSON includes a private-command revisions map. Review the current entry,
then supply its expected revision and the complete replacement selection:

    looptender accounts update --id CONNECTION_ID --expect CURRENT_REVISION \
      --label "Example revised name" --provider claude-code \
      --selection explicit_directory --account-dir /private/example/claude-a \
      --cli /usr/local/bin/claude --workdir /projects/example --json

Update retains the opaque ID, assigns a new revision and clears old status.
It does not rename/move a provider directory. A changed context is not silently
accepted by check: review and update it explicitly first.

Context binding includes provider, canonical executable and working directory,
selection mode/account directory, home/config locations and effective
authentication-related environment prefixes and proxy/TLS inputs. Values are
fingerprinted with a per-catalog keyed digest, never copied into storage.

Two kinds of variable describe the session running the command, not the
account context, and are not bound:

- The exact CODEX_TOOL_CALL_ID name identifies one agent tool invocation.
  Windows applies its existing case-insensitive name comparison; Unix excludes
  only that exact case. Its value still passes through to execution.
- The caller-session list that supervised turns already drop (see
  [what a supervised session inherits](operations.md#what-a-supervised-session-inherits)):
  BB_THREAD_ID, BB_THREAD_STORAGE, BB_PROJECT_ID, BB_ENVIRONMENT_ID,
  CLAUDE_CODE_SESSION_ID, CLAUDE_CODE_MESSAGING_SOCKET,
  CLAUDE_CODE_MESSAGING_TOKEN, CLAUDE_PID, CLAUDECODE and
  CLAUDE_CODE_ENTRYPOINT. They are matched by exact name in any letter case,
  never by prefix, so CLAUDE_CONFIG_DIR and other CLAUDE_ inputs stay bound.
  They change between agent sessions and tool calls, so the same account
  context looked changed when added in one session and checked in another.
  The status commands do not receive them either.

All other bindings remain.

**Entries recorded by an earlier build.** Earlier builds bound those session
variables too, and their digests cannot be recomputed without the old values.
A digest now carries a keyed tag of the binding definition, so this build
recognizes an earlier one without a new stored field (older builds refuse
unknown fields). The first check of such an entry by this build is not reported
as a changed context: it runs, and records the current digest in place of the
earlier one, keeping the entry's revision and its previous observation. From
then on the binding compares exactly. That first check therefore cannot detect
an authentication-environment change made between the earlier selection and
that check; the provider, executable, working directory and account directory
are verified as before. To bind deliberately instead, review the entry and
use an explicit revision-bound same-selection update before checking. An older
build checking an entry bound by this build reports a changed context.
Environment duplicates use last-wins semantics. Windows names are compared
case-insensitively, including provider prefixes and proxy names. Unix names
remain case-sensitive; lowercase http_proxy/https_proxy/all_proxy/no_proxy are
also bound as separate relevant inputs. Explicit account-directory overrides
use those same platform name semantics in both fingerprinting and execution.
Paths are revalidated before and after checks; status is committed only if
the selected revision/context still match under the catalog lock.
This detects input/context changes, not credential rotation inside provider
storage; credentials are never inspected and no permanent account identity is
claimed. Executable version and provider output shapes remain separately checked.

## Private storage and caller boundaries

The catalog is accounts/accounts.json under the existing per-user Looptender
configuration directory, including its legacy-directory selection rules.
Metadata and retained observations share one atomic private record, at most
1 MiB. On Unix, the catalog directory must be owner-only and files 0600;
ownership, regular-file, size, schema and revision guards reject unsafe data.
The reader uses snapshot LstatRecord/OpenRecord semantics: on Windows an
existing publication marker with a missing primary means unavailable, never a
new empty catalog. The opened handle is validated and read with a hard byte
limit; checked shared handles allow atomic replacement while a reader remains
open. Genuine never-created absence stays empty without creating files.
Windows uses existing private-file/ownership conventions and filesystem ACLs.
No new Windows ACL policy is claimed.

Optional --directory selects another private location outside a Git checkout.
Reads create nothing. Writers serialize with a nonwaiting lock and flush an
atomic replacement; corruption is refused rather than repaired or truncated.
Checks use a separate per-selection lock while the external command runs,
then re-read and validate the revision before committing.

Metadata operations preserve the actual inherited BB_THREAD_ID privately, as
the recorded caller; it is not passed to the provider's status commands.
They do not impersonate a person, drop the recorded caller identity, relax person-only
standard-route edits or grant loop/control/build/answer authority. The browser
receives only the allowlisted display projection: no private paths, raw CLI
output, environment values, context fingerprints, caller identity or revisions.
The catalog and selected CLI contexts must be configured explicitly; no home
directory/account enumeration occurs.

Panel startup caller/class, read-only defaults and reuse rules are unchanged.
There is no browser mutation endpoint. Official-login orchestration, watched
completion, secret capture, OAuth callbacks, notifications, paid tests and
automatic route enrollment are outside this slice. #233 authority dependencies,
Open at Login and website holds remain.

## Verification and remaining acceptance

Fictional fixture tests cover bounded private storage, corruption/permission
refusals, atomic readers, context/revision changes, concurrent checks, exact
status argv, caller preservation, version/status ambiguity, timeout/cancellation,
plan allowlisting, private-output exclusion and no subprocesses on refresh.
Local stand-ins exercise actual process execution/output limits. Chromium
fixtures exercise native disclosure and focus after completed refresh, empty
and unavailable states, stale evidence, markup rendered as text, duplicate long
names and 64 cards at widths from 320 to 2560 px.

This is not live account acceptance. No real catalog was populated or provider
called for implementation. Real selected-account confirmation, installed-panel
opening, Safari/Firefox, screen-reader speech and Windows execution remain
unverified. The owner should be notified accounts are running only after a
separately authorized live opening actually shows their selected cards.

Native Windows regression coverage for marked-missing refusal, genuine absence
and held-reader replacement is wired into the Windows storage/controls job:
the whole internal/accounts package runs with race detection, followed by
TestAccounts command fixtures. Local Windows cross-compilation is not native
execution; native reproduction and acceptance remain pending CI.
