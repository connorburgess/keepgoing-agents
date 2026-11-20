# Fictional migration-review browser checks

Build canned JSON from the real dock projection with disposable fixture loops:

    KEEPGOING_MIGRATION_BROWSER_REPORT=/tmp/migration-panel.json go test ./cmd/looptender -run '^TestDockMigrationReviewBrowserFixture$' -count=1
    python3 tools/panel-migration-browser-fixture/server.py --report /tmp/migration-panel.json

Open the printed loopback URL in a local headless Browser Automation session,
then run check.js. It refuses unmarked pages. The server reads only test-generated
JSON and checked-out panel assets, refuses POSTs and never reads runtime data or
runs provider/BB/Git commands. Do not supply private JSON.

Checks cover overview discoverability, keyboard expansion/navigation, dated
historical/direct-lane limits, distinct missing/unavailable/binding/valid-zero
states, no migration actions, and 320/390/768/1280 widths in both themes.
The script captures one bounded fictional screenshot. Stop the server and close
the browser when finished. This is not installed-panel, screen-reader,
cross-browser or live migration acceptance.
