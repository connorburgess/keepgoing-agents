# macOS service diagnostic investigation

The standalone CLI manages GUI LaunchAgents, but cannot yet verify their process identity or startup/exit history through a supported structured interface. Status now reports these checks as unavailable for recorded, loaded services. This investigation is source/API review only; it did not install a service, change a timer, trigger a failure, or exercise login/reboot recovery.

## Interfaces examined

| Interface | Evidence and decision |
| --- | --- |
| `launchctl print` / `blame` | The installed `launchctl(1)` manual explicitly disclaims stability for print output and restricts blame output to debugging/profiling. Do not parse either. Existing registration checks consume numeric command exit status only. |
| `launchctl list` | The manual describes a legacy PID/last-exit/label table and optional per-label detail. The old `-x` option is unsupported. There is no documented typed, GUI-domain-qualified process-incarnation contract here; a PID alone is insufficient for service binding. No parser is proposed. |
| `launchctl plist` | Prints a plist embedded in a Mach-O executable, not a live service status dictionary. It cannot supply manager execution history. |
| `SMJobCopyDictionary` | Returns a job dictionary, but is deprecated. The SDK header also says its contents are unstable across releases and announces removal without a replacement. Dictionary-shaped output does not make those fields a supported runtime contract. |
| `launch_msg` | The SDK deprecates the legacy job API, including listing jobs. Legacy PID/last-exit key constants do not resolve the contract or domain/identity problem. |
| `SMAppService.status` | Describes registration/launch approval, not a running process identity or last exit. `enabled` means eligible to run, not running. |
| `SMAppService.statusForLegacyPlist(at:)` | A possible future read-only approval check for an existing LaunchAgent plist. It does not establish service PID, incarnation, or exit history. |

The local evidence was the installed `launchctl(1)` manual and Command Line Tools SDK headers `ServiceManagement.h`, `SMAppService.h`, and `launch.h`. Public references: Apple's [deprecated job-dictionary API](https://developer.apple.com/documentation/servicemanagement/smjobcopydictionary(_:_:)), [SMAppService](https://developer.apple.com/documentation/servicemanagement/smappservice), [status enum](https://developer.apple.com/documentation/servicemanagement/smappservice/status-swift.enum), and [legacy-plist status API](https://developer.apple.com/documentation/servicemanagement/smappservice/statusforlegacyplist(at:)). Recheck contracts against each supported SDK/OS before implementing an adapter; archived launchd source is not evidence of current behavior.

## Independent delivery gates

1. **Launch approval:** investigate a bounded native bridge to `statusForLegacyPlist(at:)` with typed results, platform availability checks, redaction, and no registration or System Settings side effects. The release build currently sets `CGO_ENABLED=0`; a bridge must work in distributed binaries without requiring a developer toolchain on the user's machine. An approval observation must remain separate from liveness and exit diagnostics. Fixtures must cover every enum value, unknown results, cancellation, and unchanged controls. No bridge is implemented yet.
2. **Service identity:** require a documented source linking the exact GUI-domain service to a stable process incarnation around the authenticated daemon challenge. Matching PID, parent PID, executable path, or a static environment label alone is insufficient. A manual responder and PID reuse must never verify ownership. If no supported contract is established, keep the check unavailable.
3. **Startup/exit details:** require documented structured values with clear current-versus-last-run semantics and exact service identity. A successful launchctl request is not a successful daemon run. Application-authored diagnostics could cover failures after the binary starts, but would not cover loader/exec failures or prove manager ownership; label that narrower evidence separately if implemented.
4. **Live verification:** test an explicitly selected scratch service with no provider calls, preserving existing timers and control holds. Record installation, startup, clean exit, failure, removal, and stale-endpoint behavior separately from logout/reboot/sleep recovery. Fake fixtures do not establish any of these live behaviors.

These gaps block their own implementation, not the remaining BB setup, provider discovery, or release work. They do not justify migrating existing services to app-bundle packaging, introducing private launchd protocols, or treating unknown observations as recovery/failover signals.
