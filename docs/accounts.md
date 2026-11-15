# Accounts in the panel

**Unreleased source.** The persistent Accounts view lists contexts explicitly
selected in a private device-local catalog. It shows the owner-chosen name,
provider, selection mode and retained status evidence. It does not discover
every account on the computer or turn route group names into verified accounts.

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

Check runs only the selected executable's version command, then Claude's
auth status --json or Codex's login status. Each command has a five-second
deadline and 64 KiB stdout limit; stderr is discarded. Version output must
match the existing diagnostic version format. A recognized version string is
not a guarantee about every release's behavior. No login or model command runs.
The provider's own status command may contact its account service.

Claude uses the existing diagnostic loggedIn/exit semantics; Codex exit zero
reports credentials present and a nonzero exit is ambiguous. No account identity
is inferred. Only allowlisted broad Claude subscriptionType strings, when
actually supplied by structured status, become reported plan evidence.
Raw max displays as Max. Unknown plan values are discarded; no numeric tier,
capacity or quota is inferred. Codex plan remains unavailable.

Configured model, actual response model and route associations are unavailable
in this slice. A requested/probe model or configuration fingerprint is not an
actual response model or verified account identity.

A check's successful command exit means its minimized observation was saved;
it does not mean login passed. Inspect auth state and reason. Missing/corrupt
storage or a concurrent selection change refuses the operation. An overlapping
check for the same selection returns skipped. A recognized absent login,
unavailable result, timeout or cancellation is recorded as such. A failed latest
check preserves the previous recognized observation and its original time,
shown as previous/stale rather than refreshed success.

Evidence becomes display-stale after five minutes. The timestamp and original
fact remain visible. Freshness is not health, quota, account-identity verification
or permission to act. Ordinary page refresh never refreshes login evidence.

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

Metadata operations preserve the actual inherited BB_THREAD_ID privately.
They do not impersonate a person, strip caller identity, relax person-only
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
