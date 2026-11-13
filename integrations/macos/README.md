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
Open keepgoing panel
1 loop needs you
---------------------------------
example-api     Waiting for you
example-docs    Supervising
example-site    Paused
---------------------------------
Refresh
Quit keepgoing menu
---------------------------------
Read 2 min ago
```

- **Open keepgoing panel** runs `keepgoing panel`, which starts the panel's background server when none is running and opens the page in your browser. It is always there and always tries, whatever the menu could read.
- **N loops need you** appears only when a loop is held for a person or could not be read ("1 loop needs you, 2 could not be read"). It opens the panel, whose first view is what needs you. The same count sits beside the icon in the menu bar; the icon is plain when nothing needs a person.
- **One row per registered loop**: its name and its state in the panel's own words, loops that want attention first. Choosing a row opens that loop's page (`keepgoing panel --loop`). A name longer than 32 characters is shortened; the full name is in the row's tooltip and its accessibility label.
- The last line is the only one that cannot be chosen. It says when the list was read, and anything a person should know about that read: no loops registered, not every loop was read, the list could not be read, or this keepgoing is a different version from the menu.

Everything the earlier preview printed as lines (configured target, timestamps, goal revision, daemon evidence) is gone from the menu: the loop's page in the panel shows it, one choice away. The menu has no pause, resume or stop; those come to the page first.

The state words are not written in this app. They come from `keepgoing panel list --json`, which is built by the code that builds the page, so the menu and the page cannot disagree. A state this build has never heard of is shown with the phrase the CLI gave and a question-mark symbol.

**When it reads.** Once when the app is started, each time the menu is opened, and when **Refresh** is chosen. There is no timer, so the count beside the icon is as of the last of those and can be out of date; opening the menu brings it up to date. One read runs at a time. Quitting the menu leaves supervision and the panel alone.

**Accessibility.** Every row has a spoken label that says what it is and what choosing it does ("example-api, Waiting for you. Opens this loop in the panel."), and the menu-bar item's label includes the count. State is carried by words and a symbol shape, never by colour alone. Text and symbols use the system's menu colours, so they follow light and dark and increased contrast. Not yet checked with VoiceOver by a person.

## Bounds and failures

Every subprocess uses a fixed argument array without a shell and a 1 MiB cap per output stream. The menu runs exactly two commands: `keepgoing panel list --json` to read (25-second limit; the CLI bounds its own read at 20) and `keepgoing panel [--loop KEY]` to open (20-second limit). The only value ever taken from a reply and passed back is a loop's page key, and only after it matches the key pattern. There is no pause/resume/stop/enable/takeover/provider command path, and `panel list` starts no server and contacts no provider. Subprocess errors and stderr are never shown as raw text. A missing binary, malformed reply, timeout or failed read shows one plain line and leaves **Open keepgoing panel** working. Loop names are shown as inert text with control characters removed. New additive JSON fields are tolerated.

`keepgoing panel` starts a background server for this computer only, which stops by itself when unused; see [the panel](../../docs/dock.md#opening-it-with-one-command-keepgoing-panel). The menu never starts it except when a row is chosen.

These bounds apply to the directly invoked CLI and its output. An arbitrary replacement binary is not sandboxed, and process termination is not a process-tree containment mechanism. Use a trusted keepgoing binary. The earlier status reader (`client status`) is still in the library and its tests; the menu no longer shows it.

## Verification and remaining work

```sh
swift build --package-path integrations/macos
swift test --package-path integrations/macos
# Pass an already-built current Go executable:
python3 integrations/macos/scripts/smoke-contract.py /path/to/keepgoing
```

XCTest requires a toolchain containing that framework, such as full Xcode; some Command Line Tools installations can build the app but cannot run XCTest. macOS CI builds the app and runs the tests plus the contract smoke check. The smoke compiles the reader without XCTest and checks the real Go CLI against an isolated fictional disabled loop, unchanged files and a provider-command trap, including the menu's own read. Unit fixtures cover the menu's rows for each kind of read (which rows, which can be chosen, which command each runs), the count, long names, a hundred loops, unknown states, malformed/version-mismatched replies, inert text, argument separation, output caps and timeouts.

To look at the rows without opening a menu, draw them to a picture:

```sh
integrations/macos/.build/debug/KeepgoingMenu --executable /path/to/keepgoing --render-menu /private/example/menu.png [--dark]
```

This draws the menu-bar item and each row with the fonts, symbols and colours the menu uses. It is a drawing of the rows, not a capture of the screen.

Native build and the real-CLI fixture smoke pass locally. Automated evidence does not establish how the open menu looks or reads on a real screen, VoiceOver behavior, Spaces/full-screen behavior, live provider compatibility or secure distribution. No installed loop was used for these fixture checks. Pause/stop, reviewed-ticket resume, a count that updates without opening the menu, start at login, app-bundle signing/notarization and distribution remain planned. The existing CLI controls remain available separately; the preview does not add another supervisor.

## License

This preview is part of keepgoing and is source-available under the [Sustainable Use License 1.0](../../LICENSE); see the [licensing guide](../../LICENSING.md).
