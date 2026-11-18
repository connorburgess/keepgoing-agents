# Fictional saved account quota fixture

Generate the actual Go projection with disposable fictional loops:

    LOOPTENDER_QUOTA_FIXTURE=/tmp/quota-report.json go test ./cmd/looptender -run '^TestQuotaSummaryAndNativeFixture$' -count=1
    python3 tools/panel-quota-browser-fixture/server.py --report /tmp/quota-report.json

This also writes `/tmp/quota-report.json.native.json`. The server binds loopback,
serves checkout assets and this marked fiction, and refuses every POST. No live
catalog/cache/provider is read. Update check.js origin to its printed port, open
an owned local browser session using the Browser Automation skill, and run it.
Inspect the bounded mobile/light and desktop/dark captures; close server/browser.
The checks exercise summary and expanded values, reported zero versus missing,
units, separate buckets/resets, stale/ended/failed reads, Claude unavailability,
route73% versus selected37.5%, privacy, keyboard disclosure,13 widths in both
themes and empty/unavailable selections. Native meter labels include the named
window, used and remaining values; numeric text is always visible.

The same report exercises native decoding/rendering without provider calls:

    swiftc -o /tmp/quota-smoke integrations/macos/Sources/KeepgoingMenuCore/*.swift integrations/macos/scripts/QuotaSmoke.swift
    /tmp/quota-smoke /tmp/quota-report.json.native.json

This is contract/menu-model execution, not installed native GUI or screen reader
acceptance. Full XCTest belongs the normal hosted macOS job when unavailable
locally. Browser automation here cannot emulate forced colors; that check is
explicitly unrun. The native HTML meter is [Baseline Widely available](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/meter);
text values do not rely on a meter's appearance. No custom meter pseudo-elements
or accent-color support is required. No runtime data or screenshots are committed.
