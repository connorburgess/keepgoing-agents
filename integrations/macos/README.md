# Keepgoing menu preview

An optional native macOS menu-bar item that opens the [keepgoing panel](../../docs/dock.md) and lists explicitly registered local loops. This is an unreleased, **read-only development preview**, built separately from the Go executable. It uses AppKit's native status item and menus. It is not installed by Homebrew or `keepgoing init`.

## Build and open explicitly

Requires macOS 13 or newer, Swift 5.9 or newer, and a trusted keepgoing development executable with `keepgoing panel` and schema-1 `panel list`. No published release has those commands yet. Read-only commands still execute the selected binary: choose a binary you trust, not one supplied by loop metadata.

```sh
# Registration is an explicit, separate operation. Use the same registry for both commands.
/path/to/keepgoing registry add -c /private/example-loop/keepgoing.yaml
swift build --package-path integrations/macos
integrations/macos/.build/debug/KeepgoingMenu --executable /path/to/keepgoing
```

For a non-default registry, pass `--directory /private/example-registry` to the CLI registration and `--registry-directory /private/example-registry` to the menu executable. Executable and directory arguments must be absolute. The preview does not discover binaries, scan for loops, register loops, retain configuration, or start at login. Quitting the menu leaves supervision alone.

## What the menu shows

```text
Open keepgoing Panel
1 loop needs you
---------------------------------------------
example-api     Waiting for you
example-docs    Supervising
example-site    Paused
---------------------------------------------
Stop the Panel Page   stops by itself in 23 min
Quit keepgoing Menu
---------------------------------------------
Read 2 min ago
```

- **Open keepgoing Panel** runs `keepgoing panel`, which starts the panel's background process when none is running and opens the page in your browser. It is always there and always tries, whatever the menu could read.
- **N loops need you** appears only when a loop is held for a person or can't be read ("1 loop needs you, 2 can't be read"). It opens the panel, whose first view is what needs you. The same count sits beside the icon in the menu bar. The icon is plain only when a complete read found that nothing needs a person. It shows `?` when keepgoing could not be read, when the list of loops could not be read, or when not every loop was read, so none of those is ever taken for "nothing needs you".
- **One row per registered loop**: its name and its state in the panel's own words, loops that want attention first. Choosing a row opens that loop's page (`keepgoing panel --loop`). At most 25 are listed; when there are more, **All N Loops in the Panel** opens the rest. A name longer than 28 characters is shortened in the middle, because loop names often share a beginning; the full name is in the row's tooltip and its accessibility label. A loop the panel flags for [no progress](../../docs/dock.md#the-no-progress-flag) adds "· no progress" to its row, as the panel's picker does, and the flag's sentence to its accessibility label; the flag never counts as needing a person.
- **Stop the Panel Page** is there only while the panel's background process is running, and says when it would stop by itself. Choosing it runs `keepgoing panel stop`. It stops the page, never a loop.
- The last line is the only one that cannot be chosen. It says when the list was read, and anything a person should know about that read: no loops registered, not every loop was read, the list could not be read, keepgoing can't be reached, or the menu and keepgoing are different versions.

If the panel cannot be opened or stopped, a small alert says so and names the command to run in a terminal; the menu has closed by then, so a line in the menu alone would not be seen.

Everything the earlier preview printed as lines (configured target, timestamps, goal revision, daemon evidence) is gone from the menu: the loop's page in the panel shows it, one choice away. The menu has no pause, resume or stop for loops; those come to the page first.

The state words are not written in this app. They come from `keepgoing panel list --json`, which is built by the code that builds the page, so the menu and the page cannot disagree. A state this build has never heard of is shown with the phrase the CLI gave and a question-mark symbol.

**When it reads.** When the app is started, each time the menu is opened, and once a minute while the app runs. The once-a-minute read **replaces the earlier rule that this preview had no timer**: without it, the count beside the icon could only change when the menu was opened, which is the moment a person no longer needs it. Each read is one `keepgoing panel list --json` and one `keepgoing panel status --json`: local files only, no provider call, and the panel is not started. After three failed reads in a row it slows to one every five minutes until a read succeeds. It does not read on the timer while the Mac is asleep (it reads once on wake) or in Low Power Mode, where it reads only when the menu is opened. One read runs at a time, and the status line says when the last one finished. Quitting the menu leaves supervision and the panel alone.

**While the menu is open** it is never rebuilt under the pointer: when a read finishes and the rows are the same rows, their text is updated in place; when the rows changed, the change waits until nothing is highlighted or the menu has closed. The count beside the icon is always updated.

**Accessibility.** Every row's spoken label says what it is ("example-api, Waiting for you") and its help says what choosing it does ("Opens this loop in the panel."), and the menu-bar item's label includes the count. State is carried by words and a symbol shape, never by colour alone; the symbols are decorative and are not read. Text and symbols use the system's menu colours. Loop names are laid out left to right whatever script they are in. Not yet checked by a person with VoiceOver, with Increase Contrast, or on a real menu's highlighted row.

## Bounds and failures

Every subprocess uses a fixed argument array without a shell and a 1 MiB cap per output stream. The menu runs exactly four commands: `keepgoing panel list --json` (25-second limit; the CLI bounds its own read at 20) and `keepgoing panel status --json` (5 seconds) to read, `keepgoing panel [--loop KEY]` to open and `keepgoing panel stop` to stop the page (20 seconds each). The only value ever taken from a reply and passed back is a loop's page key, and only after it matches the key pattern. There is no command path that pauses, resumes, stops, enables or takes over a loop, or that reaches a provider; the two reads never start the panel. Subprocess errors and stderr are never shown as raw text. A missing binary, malformed reply, timeout or failed read shows one plain line and leaves **Open keepgoing panel** working. Loop names are shown as inert text with control characters removed. New additive JSON fields are tolerated.

`keepgoing panel` starts the panel's background process, for this computer only, which stops by itself when unused; see [the panel](../../docs/dock.md#opening-it-with-one-command-keepgoing-panel). The menu never starts it except when a row that opens the panel is chosen.

These bounds apply to the directly invoked CLI and its output. An arbitrary replacement binary is not sandboxed, and process termination is not a process-tree containment mechanism. Use a trusted keepgoing binary. The earlier status reader (`client status`) is still in the library and its tests; the menu no longer shows it.

## Verification and remaining work

```sh
swift build --package-path integrations/macos
swift test --package-path integrations/macos
# Pass an already-built current Go executable:
python3 integrations/macos/scripts/smoke-contract.py /path/to/keepgoing
```

XCTest requires a toolchain containing that framework, such as full Xcode; some Command Line Tools installations can build the app but cannot run XCTest. macOS CI builds the app and runs the tests plus the contract smoke check. The smoke compiles the reader without XCTest and checks the real Go CLI against an isolated fictional disabled loop, unchanged files and a provider-command trap, including the menu's own read. Unit fixtures cover the menu's rows for each kind of read (which rows, which can be chosen, which command each runs), the count and the failed-read mark, the read schedule and its back-off, the stop row, when an open menu may be updated in place, long names, a hundred loops, unknown states, malformed/version-mismatched replies, inert text, argument separation, output caps and timeouts.

To look at the rows without opening a menu, draw them to a picture:

```sh
integrations/macos/.build/debug/KeepgoingMenu --executable /path/to/keepgoing --render-menu /private/example/menu.png [--dark]
```

This draws the menu-bar item and each row with the fonts, symbols and colours the menu uses. It is a drawing of the rows, not a capture of the screen.

Native build and the real-CLI fixture smoke pass locally. Automated evidence does not establish how the open menu looks or reads on a real screen, VoiceOver behavior, Spaces/full-screen behavior, live provider compatibility or secure distribution. No installed loop was used for these fixture checks. Pause/stop for loops, reviewed-ticket resume, start at login, app-bundle signing/notarization and distribution remain planned. The existing CLI controls remain available separately; the preview does not add another supervisor.

## License

This preview is part of keepgoing and is source-available under the [Sustainable Use License 1.0](../../LICENSE); see the [licensing guide](../../LICENSING.md).
