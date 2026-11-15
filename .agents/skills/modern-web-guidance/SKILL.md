---
name: modern-web-guidance
description: Consult pinned browser-platform guidance when changing the panel's HTML, CSS or client-side JavaScript, alongside Impeccable for design.
---

# Panel web guidance

This is a project integration skill, not an unmodified upstream skill installation.
It applies to cmd/looptender/dockui web frontend work. It does not govern Go
supervision, native apps, backend code or copy-only edits.

1. Use Impeccable for design and interaction quality. Before selecting a web
   platform feature, consult the pinned local guidance:
   node tools/modern-web-guidance/cli.mjs search "two to five word topic".
2. Retrieve the relevant guide:
   node tools/modern-web-guidance/cli.mjs retrieve "guide-id".
   Read its constraints, keepWhen and dropWhen context before applying it.
3. Default to **Baseline Widely available** features. Confirm the feature's
   current support in official browser documentation. A newer feature requires
   progressive enhancement, a usable fallback and browser tests of that fallback.
   A guide recommendation or scanner silence is not compatibility evidence.
4. Run python3 tools/modern-web-guidance/report.py for a bounded advisory scan
   of the panel only. Review findings; do not chase a zero count or broadly
   modernize unrelated UI. Add a focused regression test only for a justified
   recurring risk.
5. Record relevant guide IDs, compatibility evidence, tests and remaining gaps
   in normal review notes. Preserve upstream copyright/license notices. Do not
   add optional generated-by credits or runtime attribution headings.

Setup is project-local:
npm ci --prefix tools/modern-web-guidance --ignore-scripts --no-audit --no-fund.

Use the checked-in exact pin and lock. Do not use upstream install/update
commands, @latest, global installs, hooks or automatic fixes. The local entry
point disables telemetry/file logging and grants the scanner read access only to
its package and the panel, without child/write permissions. Node permissions do
not deny network; see the verified limits and provenance in
[the integration notes](../../../tools/modern-web-guidance/README.md).
Do not invoke other package entry points or scan runtime data.

Source: GoogleChrome Modern Web Guidance 1.0.0, Apache-2.0,
https://developer.chrome.com/docs/modern-web-guidance .
