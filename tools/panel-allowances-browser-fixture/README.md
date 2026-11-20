# Fictional allowance evidence

The Go projection fixture reads no registry, catalog, provider or quota cache.
Generate its current report with:

```sh
LOOPTENDER_ALLOWANCES_FIXTURE=/tmp/allowance-evidence/after.json \
  go test ./cmd/looptender -run TestDockAllowancesBrowserFixture -count=1
```

Create the output directory first. `server.py --reports /tmp/allowance-evidence
--before /tmp/baseline/cmd/looptender/dockui` serves checkout assets and the
reports, binds only loopback at an ephemeral port, and refuses every POST.
`before.json` is the same fictional report projected by the baseline. For the
current-main comparison, use e4014c07 in a separate checkout, temporarily copy
`dock_allowances_fixture_test.go` there, adapt its legacy call to
`dockAllowances(routes)`, and omit the final five-block assertion. Remove that
temporary copy afterward. Never use a live panel feed.

Open the fixture with the browser automation skill. Set `origin` in `check.js`
to the printed loopback address and run it in that browser session. It checks
newest-window substitution before the change, independent readings after it,
assignment wording, out-first ordering, dates and sources, reset repetition,
stale and missing evidence, selected-context quota separation, keyboard route
links, long unbroken labels, 18 widths in both themes, narrow containers,
removed-container-query fallback, and empty/unavailable views. Screenshots are
bounded. The server and browser should be closed after inspection.

Size container queries are Baseline Widely available according to [MDN's
container reference](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/At-rules/%40container).
The existing viewport rule remains the fallback. These checks simulate the
feature's absence in Chromium; they do not claim execution in other engines.
