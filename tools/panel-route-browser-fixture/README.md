# Fictional route editor browser checks

This fixture serves the checked-out panel assets and canned route-editor JSON.
It reads no runtime, accounts, credentials or loop configuration, and runs no
provider command. Its confirmation responses simulate a successful route save
and a partial three-loop save. These are UI checks, not evidence that the Go
writers, native CLIs or live accounts work; `TestPanelRoute*` tests exercise the
real in-process writers and refusal paths separately.

Start the standard-library fixture:

```sh
python3 tools/panel-route-browser-fixture/server.py --artifacts /tmp/fictional-route-ui-evidence
```

Open the printed loopback URL in a local headless Browser Automation session.
Run `check.js` in that session after navigation. The script expects the
DevBrowser page API (`browser.getPage("main")`), checks native labels and
validation, explicitly requested model/effort metadata, cross-model effort clearing and
visible-form payload parity, required creation
opt-in, retained draft after Read now, keyboard Tab/Enter confirmation,
partial outcomes, and early capability limitations. It measures document
width and button targets at 13 widths from 320 to 1920 in light and dark
schemes, then takes one bounded JPEG. It throws on a failed assertion and
prints a JSON check list on success. The artifact directory records fictional
POST requests so that listing can be checked as an explicit action.

Stop the fixture and close its browser session after use. Keep artifacts
outside the repository. Browser checks use synthetic metadata and do not
establish actual model availability, account identity, quota or live acceptance.
