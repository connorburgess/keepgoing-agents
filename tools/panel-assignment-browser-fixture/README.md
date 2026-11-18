# Fictional assignment review browser fixture

Generate the real minimized projection with disposable fictional loops:

    KEEPGOING_ASSIGNMENT_BROWSER_REPORT=/tmp/assignment-panel.json go test ./cmd/looptender -run '^TestDockAssignmentBrowserFixture$' -count=1
    python3 tools/panel-assignment-browser-fixture/server.py --report /tmp/assignment-panel.json

Use a local headless Browser Automation session and run check.js. It refuses
unmarked pages. The loopback server serves only test JSON and checked-out assets,
rejects POSTs and never reads runtime data or runs BB/provider/Git commands.
Tests cover keyboard navigation, distinct missing/unavailable/stale/valid-zero
states, dates/partial direct-lane coverage, no new controls and 320–1280px in
both themes. Inspect the bounded fictional screenshot and close browser/server.
This is not installed UI, cross-browser or screen-reader acceptance.
