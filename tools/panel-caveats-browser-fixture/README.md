# Fictional caveats and picker checks

This standard-library helper serves checked-out panel assets and canned,
fictional records. It reads no runtime/catalog, runs no provider and accepts no
POST. Extract baseline assets outside the checkout, then start it:

```sh
mkdir -p /tmp/example-panel-before
git archive MAIN_COMMIT cmd/looptender/dockui | tar -x -C /tmp/example-panel-before
python3 tools/panel-caveats-browser-fixture/server.py --before /tmp/example-panel-before/cmd/looptender/dockui
```

Open the printed URL in an explicit local headless Browser Automation session.
Run `check.js` using that session's DevBrowser API. It refuses unmarked pages.
The 48-load sweep compares overview/loop pages, both themes and six widths from
320–1920. It checks control text wrapping, overflow, Versions fit, first-loop
placement, neutral no-standby presentation and migration-review visibility.

G8 checks measure hover/keyboard reveal at 390/1440, stable row height and
chevron clearance; reduced-motion and removed optional CSS rules retain an
ellipsis and Enter opens the full wrapping heading. Removing those rules is a
feature-loss simulation, not execution evidence for another browser engine.
No screenshot is required by the script; take bounded first-screen captures
separately. Stop the helper and close the browser after use. These are fictional
Chromium checks, not live acceptance, touch-device or other-engine evidence.
