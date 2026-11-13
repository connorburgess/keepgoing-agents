# Keepgoing menu preview

An optional native macOS status menu for explicitly registered local loops. This is an unreleased, **read-only development preview**, built separately from the Go executable. It uses AppKit's native status item and menus. It is not installed by Homebrew or `keepgoing init`.

## Build and open explicitly

Requires macOS 13 or newer, Swift 5.9 or newer, and a trusted keepgoing development executable with schema-1 `registry list` and `client status` support. Published v0.1.3 predates those commands. Read-only commands still execute the selected binary: choose a binary you trust, not one supplied by loop metadata.

```sh
# Registration is an explicit, separate operation. Use the same registry for both commands.
/path/to/keepgoing registry add -c /private/example-loop/keepgoing.yaml
swift build --package-path integrations/macos
integrations/macos/.build/debug/KeepgoingMenu --executable /path/to/keepgoing
```

For a non-default registry, pass `--directory /private/example-registry` to the CLI registration and `--registry-directory /private/example-registry` to the menu executable. Executable and directory arguments must be absolute. The preview does not discover binaries, scan for loops, register loops, retain configuration, or start at login. Quitting the menu leaves supervision alone.

Open the circular-arrow menu to read the registry and the selected loop. Choose another registered loop or use **Refresh** to read again. Duplicate names have an ID suffix. Only one request runs at a time; controls remain absent. There is no refresh or supervision timer. The icon is neutral and does not claim agent health. With the menu left open, displayed evidence retains its read timestamp; refresh explicitly for a new read.

**Show private goal text** is off on each launch. Turning it on requests only the current goal title and next-action summary, which may be visible to screen sharing or screenshots. Turning it off immediately replaces the old display before rereading. Metadata is inert text; the preview never reads task files, opens references or interprets goals as commands. A goal revision is not a progress percentage or acceptance result. Stage metadata is not yet supplied by the contract, so the preview says it is unavailable.

## Evidence and failures

Enabled configuration, explicit holds, historical activity, declared completion and a responsive daemon appear separately. A missing daemon endpoint does not establish that an external scheduler is stopped. Expected next checks come only from a responsive daemon; external scheduler timing stays unknown. Timestamps are displayed in UTC.

Every subprocess uses a fixed argument array without a shell, a five-second request deadline, and a 1 MiB cap per output stream. The selected CLI's `client status` may query its existing local daemon endpoint but never starts one. Only registry listing and status reads are exposed; there is no pause/resume/stop/enable/takeover/provider command path. Subprocess errors and stderr are never shown as raw text. A missing binary, malformed or unsupported response, mismatched registration/loop, timeout or failed read replaces the prior status with unavailable evidence. Partial status retains usable fields with a warning. An empty registry is shown only after a successful versioned list response. New additive JSON fields are tolerated; unknown state labels remain unknown.

These bounds apply to the directly invoked CLI and its output. An arbitrary replacement binary is not sandboxed, and process termination is not a process-tree containment mechanism. Use a trusted keepgoing binary. The preview's reads are independent snapshots, not an atomic or continuous account of agent activity.

## Verification and remaining work

```sh
swift build --package-path integrations/macos
swift test --package-path integrations/macos
# Pass an already-built current Go executable:
python3 integrations/macos/scripts/smoke-contract.py /path/to/keepgoing
```

XCTest requires a toolchain containing that framework, such as full Xcode; some Command Line Tools installations can build the app but cannot run XCTest. macOS CI builds the app and runs the tests plus the contract smoke check. The smoke compiles the reader without XCTest and checks the real Go CLI against an isolated fictional disabled loop, unchanged files and a provider-command trap. Unit fixtures cover malformed/version-mismatched/partial replies, private metadata, identity mismatches, inert text, argument separation, output caps and timeouts.

Native build and the real-CLI fixture smoke pass locally. Automated evidence does not establish visual/accessibility correctness, Spaces/full-screen behavior, live provider compatibility or secure distribution. No installed loop or GUI was used for these fixture checks. Pause/stop, reviewed-ticket resume, open-thread support, optional setup installation, app-bundle signing/notarization, distribution and live UI verification remain planned. The existing CLI controls remain available separately; the preview does not add another supervisor.

## License

This preview is part of keepgoing and is source-available under the [Sustainable Use License 1.0](../../LICENSE); see the [licensing guide](../../LICENSING.md).
