# Panel web guidance and advisory scan

This project-local integration complements Impeccable's design guidance with
browser-platform guidance for cmd/looptender/dockui. It adds no runtime
dependency to the panel or Go supervision.

Use Baseline Widely available as the default. Newer features require progressive
enhancement, a usable fallback and tests with the feature unavailable. Check
current official browser support rather than assuming a guide's sample works
everywhere. The local .mwgrc sets the upstream retrieval target to widely.

## Local use

Requires Node.js 22 or newer and Python 3. CI pins Node.js 22.23.2.

    npm ci --prefix tools/modern-web-guidance --ignore-scripts --no-audit --no-fund
    node tools/modern-web-guidance/cli.mjs search "keyboard form submission"
    node tools/modern-web-guidance/cli.mjs retrieve "ime-safe-enter-submit"
    python3 tools/modern-web-guidance/report.py
    python3 -m unittest discover -s tools/modern-web-guidance -p 'test_*.py'

The report directory contains raw stdout/stderr, report.json and summary.md.
States are findings, no-patterns and error. Exit zero can mean findings or no
patterns; an upstream error can contain a zero count. The reporter treats
nonzero exits, an error field, missing/invalid JSON, invalid totals, missing
active rules, missing installation and timeouts as errors. Never execute the
upstream report's unpinned retrieveCommand; use the local entry point and guide ID.

The entry point accepts only search, retrieve and a fixed panel legacy-scan.
It forces DISABLE_TELEMETRY=1 and ENABLE_FILE_LOGGING=false and removes inherited
NODE_OPTIONS/NODE_PATH. The 1.0.0 wrapper otherwise constructs a Clearcut logger
after scanning, with a detached watchdog. Opting out prevents logger setup.
The scanner itself is deterministic parsing; no model service is needed.
Each child invocation has a 60-second timeout and a 4 MiB output budget.

The scanner process uses Node's experimental permission model, with reads for
the package and panel only, no child process or filesystem write permissions.
Search/retrieve read the package and this tool's .mwgrc only. The upstream git
enumeration attempt is denied and falls back to a sorted filesystem walk.
The outer local entry point starts this restricted child. Node permissions are
not a complete security sandbox and do not restrict network. Network denial was
tested separately on macOS; hosted CI network isolation is not established.
Package download needs network. No global install, hooks, update command,
automatic fixes, paid calls, secrets or PR comments are configured.

## Advisory CI

The separate Modern Web Guidance advisory workflow runs on panel, guidance,
tooling and its own workflow changes, plus manual dispatch. It uses read-only
repository permissions and pinned actions, disables credential persistence,
installs from the exact lock with lifecycle scripts disabled, runs report tests,
and always attempts reporting/artifact upload even after setup failure.

The job has continue-on-error enabled, so failures do not block merging.
Reports retain scanner failure separately from findings. If no report can be
written, the workflow summary says unavailable and artifact upload fails.
No required-check or protection setting changes. Artifacts expire after 14 days.
Cancellation/runner loss can prevent artifact delivery; inspect step results
rather than treating a successful advisory job conclusion as a clean scan.

## Provenance and notices

- Official documentation: https://developer.chrome.com/docs/modern-web-guidance
  and https://developer.chrome.com/docs/modern-web-guidance/modernization-workflow .
- Exact npm package: modern-web-guidance@1.0.0, published by GoogleChrome.
  Package repository metadata names GoogleChrome/modern-web-guidance-src.
- Distribution repository inspected:
  https://github.com/GoogleChrome/modern-web-guidance/tree/4dd968349e54ca45a05dd5c818fb035c9054b022 .
  This is the inspected distribution revision, not a verified npm source commit.
- Tarball: https://registry.npmjs.org/modern-web-guidance/-/modern-web-guidance-1.0.0.tgz .
  SHA-512 integrity is retained in package-lock.json and independently matched
  before execution. Registry signatures/attestations were advertised but not
  cryptographically verified in this integration.
- Apache-2.0 upstream LICENSE and THIRD_PARTY_NOTICES are retained verbatim as
  UPSTREAM-LICENSE and UPSTREAM-THIRD-PARTY-NOTICES. The installed package keeps
  its own notices as well. No upstream executable or guide was modified.
- The project skill is an original integration wrapper with explicit scope and
  local commands, not a copy or unmodified installation of the upstream skill.
  Upstream runtime attribution/update instructions do not override repository
  policy; legal notices remain intact.

## First baseline and triage

At source base 575d6f857e6dcb0f6c62aff2f8e877b9636710d4 on 2026-11-15,
the actual panel scan returned exit 0, totalFindings 0, activeRuleCount 35,
activeGuideCount 33. The same result completed with macOS sandbox-exec denying
network access on Node.js 22.23.2. Search and retrieval were also exercised.

There are no concrete panel findings to turn into a regression test now.
A fictional input Enter handler confirmed a positive ime-safe-enter-submit
finding, exit 0 and repeated identical JSON; a missing target returned exit 1,
an error field and zero findings. Reporter tests preserve that distinction.

Zero patterns does not establish compatibility, accessibility or
compliance. The default scanner covers roughly 20% of the guidance corpus and
can skip unsupported, docs/tests, oversized or unreadable files. Its limits and
heuristics can miss real problems. Browser verification remains a separate
requirement. Hosted workflow/artifact delivery has not yet been exercised for
this local review head; local report artifacts and failure paths were checked.
