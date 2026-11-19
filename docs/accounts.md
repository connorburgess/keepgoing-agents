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

Page refresh reads retained catalog and quota evidence only and executes no provider
command. In a panel started with `--allow-changes`, an explicit reviewed
**Refresh subscription quota** action contacts the selected Codex CLI without a
model request. The read-only panel offers no such action. The `accounts` commands below do not start
a loop, enroll a route, sign in or copy credentials. Complete provider sign-in in
the official provider tool first.

## Add an account, a subscription or a model

The short version. Run these in a terminal of your own, not inside a BB agent
thread: a BB thread is refused for the standard route list, and for a loop
that has no recorded owner. Released builds name the command `keepgoing`;
`looptender` is the same program. Names and paths are examples.

Two lists are involved. The **accounts catalog** is the accounts you name
(the panel's Accounts page). A **loop's routes** are the order it works
through when the one it is on runs out. Adding an account to the catalog
does not put it in any loop. The commands below name a loop by the name
`looptender registry list` shows; `-c FILE` with its configuration file works
in place of `--loop NAME` everywhere.

**Another subscription of a provider you already use.** Sign in to it in a
new, empty directory with the provider's own tool (for Claude Code, run it
with `CLAUDE_CONFIG_DIR` set to that directory; for Codex, `CODEX_HOME`), then
name it:

    looptender accounts add --label "personal-pro" --provider claude-code \
      --selection explicit_directory --account-dir /private/example/claude-personal

This adds one entry and prints its ID. It never signs in, reads a credential,
calls a model, or changes a loop or route.

**Another provider.** The same, with `--provider codex` and its `CODEX_HOME`.
A provider that manages its own login, such as Cursor or BB:

    looptender accounts add --label "example-cursor" --provider cursor \
      --selection externally_managed

**An API key.** Give it its own entry and label. If a CLI uses it, sign in with
the key in its own directory and add that directory as above. Otherwise:

    looptender accounts add --label "api-team" --provider other \
      --selection externally_managed

The key is never a field; looptender never stores or reads it.

**Put an account or model into a loop's order, and reorder it.**

    looptender fallbacks add --loop example-loop          # guided questions
    looptender fallbacks move claude-personal-pro --to 1 --loop example-loop
    looptender fallbacks list --loop example-loop

The guided questions list your named accounts for the provider; pick one by
number. Each command previews and writes only after you answer yes; nothing
runs. Two things to know. A new route counts as not healthy, and is not handed
off to, until a check of it passes, either the loop's scheduled checks or one
you run now:

    looptender fallbacks check --id claude-personal-pro --exact --dry-run --loop example-loop   # free preview
    looptender fallbacks check --id claude-personal-pro --exact --loop example-loop             # one real request

The second sends one real request on that account, which uses its quota and
may cost money. And automatic handoff stays off until you turn it on:

    looptender fallbacks handoff on --loop example-loop

To keep one order for every loop instead, use the [standard route list](routes.md):
`routes add`, `routes move`, then `routes follow-standard --loop NAME`.

**Say which account a route uses.** Picking a named account in the guided
`init` or `fallbacks add` states it. With flags, `--account ID` does the same
on `init` (the loop's first route), `fallbacks add` (a new fallback) and
`routes add` (the standard list). For a route that already exists, add
`account: ID` to it by hand, then run `looptender validate -c FILE`. See
[below](#say-which-account-a-route-uses). It is your statement and changes
nothing about how the route runs.

**Check an account again.** `looptender accounts check --id ID`, then
`looptender accounts list`. This runs the CLI's version and login-status
commands, plus Codex's bounded plan read when login is reported (the provider's
status command may contact its account service). It
never signs in or calls a model, and it does not establish quota or which
account a login really is.

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

**Warning: the default Claude Code login is `inherited`, not an explicit
directory.** Claude Code treats CLAUDE_CONFIG_DIR set to its default directory
(`~/.claude`, `%USERPROFILE%\.claude` on Windows) as a different login context from the default login, with the
variable unset. An explicit_directory entry for `~/.claude` can therefore report
no login while you are signed in. `accounts add` and `accounts update` print a
warning for such an entry (they still save it), and
[doctor](diagnostics.md#stated-accounts) flags an existing one. Use
`--selection inherited` for the default login. This was observed for Claude
Code; it is not claimed for Codex, so a CODEX_HOME at `~/.codex` gets no
warning.

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
auth status --json or Codex's login status. After Codex reports login, it also
starts the selected `codex app-server`, sends `initialize`, `initialized`, and
`account/read` with `{"refreshToken":false}`, then terminates that owned process.
Each command has a five-second deadline and 64 KiB stdout limit; stderr is
discarded. The commands do not
receive the variables of the session that ran the check (the list below). Version output must
match the existing diagnostic version format. A recognized version string is
not a guarantee about every release's behavior. No sign-in, explicit token
refresh or model request runs.
The provider's own status command may contact its account service.

Claude uses the existing diagnostic loggedIn/exit semantics; Codex exit zero
reports credentials present and a nonzero exit is ambiguous. No account identity
is inferred. Only allowlisted broad Claude subscriptionType strings and Codex
ChatGPT planType values become reported plan evidence.
Raw max displays as Max. Unknown plan values are discarded; no numeric tier,
capacity or quota is inferred. Codex retains only the plan value, fixed
`codex account/read` source and
observation time, plus an allowlisted unavailable reason; email, account IDs,
credentials and raw RPC/error data are discarded. Plus, Pro and other known
tiers are labels, never capacity claims. Null accounts, API keys, unknown tiers,
unsupported older CLIs, malformed responses and failed/timed-out reads have
distinct unavailable reasons. Older login-only snapshots say to check again.
A successful login check stays independent of a failed plan read. A transient
plan failure preserves an earlier plan as stale with its original time and
`plan.reason`; an authoritative null, API-key or unrecognized plan clears it.
Unsupported or malformed responses withhold the plan as unavailable.
A Claude card without a plan says why: "Claude Code's status reported no
recognized plan." after a login,
"No plan is read without a login." after none, and "Not checked" before any
check. Claude checks with no reported plan need no action; the card's JSON carries the
reason as `plan.missing` (`not_recognized`, `no_login`, `not_checked`,
or the specific Codex plan-read reason). A check that established no login
leaves the plan Unavailable.

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

When routes name the entry, the Routes row lists each of them, the loops that
use each, and each route's own recorded usage reading; readings of different
routes are never merged. It says once "assigned by you · identity not
verified", and under a BB thread route "assigned by you · not verified for BB
threads (account set per project)". A route that names the entry but whose
check found a mismatch is still listed, says what its check says in place of
"assigned by you", and its reading is not counted for the entry. The card's
summary is from those readings, never merged: if any route's reading had a
window used up that has not ended, the newest such, naming its route when the
card has more than one ("opus-example: Five-hour allowance was out when read
2 h ago · resets in 3 h"); otherwise "Not out when read 2 h ago" from the
newest reading, or that no reading is recorded.
Being out is information, not a warning.

### Routes with unknown accounts

Only catalog entries count as selected accounts in the Accounts page and
sidebar. Routes that do not explicitly name a catalog entry appear in a
separate **Routes with unknown accounts** section, with their configured
context, loops and each route's own recorded usage reading. Configuration
contexts are not additional accounts: several routes may use one selected
account, and the page cannot tell without an explicit binding. Provider,
model, group and inherited or matching directories do not establish an
account identity or a distinct subscription. The page offers no inferred
account match or per-context command to register another account.

These rows come only from loop configurations and the catalog. Nothing on
the computer is searched, no provider is called, and nothing is adopted.
After checking which account a route uses, assign it explicitly with
`account: <ID>` in its loop configuration. The bounded panel data retains
`accountUses` (catalog cards first, then configuration contexts; route and
loop keys only); these contexts are presentation groups, not account counts.

Above the cards, **Add an account** gives the `accounts add` command to copy
for three cases: another subscription of a provider you use (sign in with the
provider's own tool in a new folder, then name that folder as an
explicit_directory entry), a provider that manages its own login (Cursor, BB:
externally_managed), and an API key (its own entry; the key is never taken).
Values the page cannot know are placeholders (`ACCOUNT_LABEL`, `PROVIDER`,
`FOLDER`), each explained in one line. It is open when there are no accounts,
under three sentences on what an account, a route and a loop's order are, and
closed otherwise. The page runs none of these commands.

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
entry per directory. The directory is the CLAUDE_CONFIG_DIR that entry selects.
The default login (`~/.claude`, with CLAUDE_CONFIG_DIR unset) is an `inherited`
entry: an explicit directory is for a second, separate config directory, and
naming `~/.claude` explicitly selects a different login context (see the
warning above):

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
zero means present and any other exit is ambiguous. The separate structured
`account/read` distinguishes a ChatGPT plan from an
API-key account without retaining identity. Neither read verifies which
provider account the owner meant; real-account acceptance remains unrun.

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
`fallbacks.yaml`, or on a route in the [standard list](routes.md). Commands
write it for you: `routes add --account ID` for the standard list, and
`init --account ID` and `fallbacks add --account ID` for a loop's first route
and its fallbacks (unreleased). The guided `init` and `fallbacks add` list the
catalog's entries for the route's provider by name, and picking one by number
uses its directory, suggests a group made from its name, and states it. A
directory typed instead states nothing. Setup refuses an entry that `validate`
would reject, and also one it cannot compare but that does not fit: an entry
on the normal login for a route with its own directory, and, for a BB thread,
an entry for neither BB nor the thread's provider. The refusal names the
entries that fit.

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
The label and ID are shown as they are in the Accounts view. A loop's page
shows a route that names a consistent entry as "on *Label* · assigned by you ·
identity not verified" (for a BB thread route, "not verified for BB threads"),
and a route that names none as "Not assigned to an account." with its account
group. The Allowances strip on Routes combines headings only for consistent explicit
catalog assignments. Each route retains its own dated probe reading and windows;
a shared context never selects one route's windows for another. Other routes
keep their configured group and route labels and say why no account label is
used. Selected-context subscription quota on Accounts is separate evidence,
never a substitute for route readings.

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

### Codex plan read verification

The protocol follows [official app-server authentication documentation](https://learn.chatgpt.com/docs/app-server).
The plan allowlist follows Codex CLI 0.163.0's generated `GetAccountResponse`
`PlanType` enum, excluding `unknown`; future values stay unavailable until reviewed.
No shared adapter/package boundary changes. Panel refresh remains snapshot-only.
The updated reader accepts private catalog schemas 1 and 2. Schema 1 remains
unchanged for legacy-compatible writes: no-op updates, Claude-only checks and
observations with no plan omit absent `planObservedAt` and `planReason` fields.
Reading a catalog never upgrades or rewrites it.

A write that records either new field or a newly recognized plan tier promotes
the entire catalog to schema 2. This includes a Codex `pro` timestamp even though
old readers recognize the word `pro`, and a failed or no-plan Codex read that
records only `planReason`. Schema 2 stays at version 2 if evidence is later
cleared. Schema 1 containing new metadata or tiers, unsupported future schemas,
and malformed catalogs are refused without rewriting them. The CLI/panel JSON
projection retains its existing schema; this version change concerns private
`accounts.json` storage only.

All old strict readers reject the whole schema-2 catalog, not just Codex evidence.
Mixed old/new readers are unsupported after promotion. Before the first production
plan check, stop the old catalog readers (including running panels), preserve a
private schema-1 metadata backup outside the checkout, upgrade every reader and
writer, and restart readers with the updated binary. Keep acceptance checks in a
private scratch catalog until that deployment is coordinated. No credentials
need to be copied. A no-op update to a schema-1 Claude-only catalog does not
require that migration.

To roll back, stop the updated readers and writers, restore the matching private
pre-upgrade schema-1 catalog backup, then restart the old binaries. Restoring it
loses observations/catalog edits recorded after the backup. If no compatible
backup exists, keep the new reader and review recovery; do not relabel a schema-2
file or strip fields to make an old reader accept it. No production migration or
rollback has been run for this change.

After review, a person may explicitly check a selected catalog entry and print
only its plan (substitute the catalog ID; this records an observation):

```sh
id=CONNECTION_ID
looptender accounts check --id "$id" --json | jq -r --arg id "$id" '.accounts.cards[] | select(.id == $id) | (.plan.value // .plan.text)'
```

Coordinator acceptance of the earlier implementation read Plus through a private
scratch metadata catalog with host access. The schema compatibility correction
has fixture evidence only; production catalog/panel and live migration/rollback
remain untested and unchanged.

## Read selected-context subscription quota

After selecting an existing Codex context, explicitly read its subscription
windows without a model request:

```sh
looptender accounts usage --id ACCOUNT_ID --json
```

Use the same effective environment as the catalog selection. This command uses
exactly its configured executable, working directory and account-directory
selection. Inherited contexts must still match the retained environment
fingerprint. A panel started with `--allow-changes` offers the same read after
an inline review and confirmation on that Accounts card. Ordinary page refresh,
`accounts list`, the read-only panel and route observations never trigger it.

The [official Codex app-server protocol](https://learn.chatgpt.com/docs/app-server)
exchange is `initialize`, `initialized`, then
`account/rateLimits/read` with `excludeResetCreditDetails:true`. It does not
start a login, explicitly refresh tokens, open credential files, call a model
or call the separate token-usage endpoint. The official CLI may perform its
own ordinary authentication/network work. The read has a five-second deadline,
a 64 KiB stdout bound, discarded stderr, strict response correlation and owned
process cleanup. A rejected or unsupported CLI returns unavailable evidence.

The supported Codex response can expose several metered buckets. Up to eight
are retained separately, with primary and secondary windows where present,
including fractional percentages, duration and reset times. The legacy single
bucket is used only when the bucket map is absent or null; an explicitly empty
map has no supported quota evidence. The legacy bucket is never added a second
time. Individual limits retain decimal used/limit values in provider
units and their reported remaining percentage. Credits retain availability,
unlimited status and a bounded decimal balance; the response supplies no
currency, so no currency or percentage is invented. A nullable ordinary-usage
permission remains unknown; percentages and reset times never establish access.
No reserve-experiment capability or reset-credit consumption is requested.

Unknown or malformed parts are visibly partial or unavailable; missing values
are never zero. A reported zero is preserved. Raw provider display names,
account IDs, email addresses, banners and plan/model aliases are discarded.
Known Codex labels and generic additional-bucket labels keep independent rows
without implying a model, subscription identity or quota pooling. Plan and
login evidence remain independent. Changing credentials inside the same
selected directory cannot be detected as an identity change by this mechanism.

**Claude account quota remains unavailable.** No supported standalone,
model-free selected-context quota source has been established. The experimental
Claude usage control path is not used: its default reads local transcripts and
its reply can conceal cached data and lost freshness/error provenance. Existing
dated Claude route observations remain on Routes; they are not borrowed as
selected-account quota. This is a remaining acceptance gap, not complete
acquisition of all Claude and Codex quotas.

The Accounts page shows compact named windows before expansion. Expanded rows
show used and remaining values, independent reset timestamps, source, read age,
partial evidence and failed refreshes. The native menu shows the same saved
summary in account submenus, capped at six accounts/eight details per submenu
with explicit omitted counts; the panel lists the remaining details. Neither
page polling nor native menu polling contacts providers. Native output contains
no catalog IDs, account-directory paths or provider account identities.

The private `subscription-quota.json` cache remains bounded to 1 MiB and 64
entries. A refresh writes quota-cache schema 2 for separate bucket/unit data;
schema 1 remains readable without rewriting it on reads and is marked partial
because it cannot establish additional buckets. Older quota readers reject
schema 2 as unavailable. Back up this private cache before downgrading; no
automatic migration repair or rollback rewrite is performed. Catalog schema is
unchanged. Stored data includes opaque catalog ID/revision/context bindings,
bounded meter keys and typed quota detail, successful read time and attempt
failure evidence; it never stores executable/account paths or raw payloads.
The native additive `panel list` contract is optional for older CLIs, strictly
bounded and reads saved evidence only. This source change does not establish
installed or live provider acceptance.

Reads run outside the catalog writer lock. Checks and quota reads for one
selection share a single-flight guard. Publication revalidates the ID, revision,
effective context, canonical executable and directory snapshots under the
catalog lock. A removed/replaced selection, changed executable or directory,
older response, symlink, unsafe permissions or corrupt/future cache is refused
without replacing evidence. External non-cooperating edits after validation
remain outside the lock contract. Corrupt cache bytes are preserved; inspect
and back them up privately before an operator removes the cache to start over.
No automatic repair or provider retry occurs. After a lost reply, read the
cached state before explicitly retrying.

A successful reading retains its original timestamp. After 30 minutes it is
marked stale; an ended reset is shown as an ended window and needs a fresh
read. A later failed refresh has a separate time/reason and never makes old
windows fresh. Future-dated or context-mismatched evidence is withheld. These
are subscription-quota observations for the selected context, not turn token
usage, route availability, or a promise of entitlement. They do not attach the
selection to any route, repoint a thread, change a default, or influence
supervision.

Fictional RPC/cache/panel fixtures cover correlation, bounds, timeout, selected
environment, privacy, failed-refresh retention, concurrency and review fences.
Native installed-panel and cross-platform process acceptance remain separate;
platform compilation alone is not native execution evidence.

The public quota view serializes the shared summary once; raw typed bucket
storage stays private to the cache and Go projection. Legacy window fields
remain for compatibility. Both interfaces retain the summary's independent
rows, source, dates, missing/partial state and failed-read context.

The main web report advertises saved account availability, selected count and
an opaque snapshot reference, without full account cards or duplicate quota
summaries. Only the Accounts page reads `GET api/v1/saved-accounts?reference=…`
under the same token/session, origin and read-method safeguards. That endpoint
returns only the already-projected snapshot, never reads provider files, starts
a registry read, queries a provider or writes anything. It keeps at most the
latest snapshot for each of the two identifier views, for 30 seconds, with a
1 MiB response cap. Superseded, expired or unknown references return a conflict;
the page says the saved read failed and offers the existing Read now action.
There is no additional polling timer or automatic provider refresh.

A new main report invalidates delayed replies for earlier references; changing
the identifier view also prevents applying a reply for the previous view.
Loading, missing and failed companion reads stay explicit and do not block
the overview or imply zero quota. Reports without a reference retain the prior
inline Accounts rendering. Native continues to project directly from the same
Go evidence, with no dependency on this browser endpoint and unchanged old-CLI
fallback.

The web hard response ceiling remains the existing 8 MiB. The combined widest
registry/route-list plus 64 detailed quota fixture requires the existing main
6 MiB regression threshold; all quota rows remain obtainable in the separate
1 MiB response. Native remains bounded to 1 MiB.
