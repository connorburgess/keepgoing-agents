import Foundation

@main struct ContractSmoke {
    static func main() throws {
        let args = CommandLine.arguments
        guard args.count == 4 || (args.count == 5 && args[4] == "apply") else { fatalError("expected CLI, registry and panel directories, then optionally apply") }
        // A short panel directory: a local socket's path is bounded.
        let client = LocalClient(executable: URL(fileURLWithPath: args[1]), directory: args[2], panelDirectory: args[3])
        if args.count == 5 {
            try applyStop(client)
            return
        }
        let entries = try client.registry()
        guard entries.count == 1, entries[0].loop == "example-menu-loop" else { fatalError("registry contract mismatch") }
        let report = try client.status(entries[0], privateText: false)
        let lines = Contract.lines(report, privateText: false)
        guard lines.contains("Supervision: disabled"), lines.contains("Goal: not recorded"),
              lines.contains("Next normal check: unknown"), lines.contains("External scheduler: not inspected") else {
            fatalError("status projection mismatch")
        }
        // The menu's own read: one loop, in the panel's words, with a key the
        // opener accepts. Nothing is opened here: that would start a server.
        let list = try client.panelList()
        guard list.loops.count == 1, list.loops[0].name == "example-menu-loop", list.loops[0].phrase == "Off", list.loops[0].state == "off",
              list.needsYou == 0, MenuBuilder.arguments(.openLoop(key: list.loops[0].key)) != nil else {
            fatalError("panel list contract mismatch")
        }
        let model = MenuBuilder.build(.read(list, at: Date()), now: Date())
        guard model.badge.isEmpty, model.rows.contains(where: { $0.kind == .loop && $0.title == "example-menu-loop" && $0.detail == "Off" }) else {
            fatalError("menu model mismatch")
        }
        // The loop's choices come from the CLI's own list of what fits.
        let row = model.rows.first { $0.kind == .loop }!
        guard list.loops[0].can == ["pause", "stop"], row.submenu.compactMap(\.command) == [.openLoop(key: list.loops[0].key), .review(.pause, key: list.loops[0].key), .review(.stop, key: list.loops[0].key)] else {
            fatalError("loop choices mismatch")
        }
        // A review reads and changes nothing; the caller checks the files.
        guard case .review(let review) = try client.review(.stop, key: list.loops[0].key), review.title == "Stop example-menu-loop?",
              review.lines.contains(where: { $0.hasPrefix("The loop's next check carries out the stop") }),
              review.lines.last?.hasSuffix(" +07).") == true else {
            fatalError("review contract mismatch")
        }
        let confirmation = ConfirmationBuilder.build(review)
        guard confirmation.defaultButton.command == nil, confirmation.buttons.count == 2, confirmation.buttons[1].destructive,
              MenuBuilder.arguments(confirmation.buttons[1].command!) != nil else {
            fatalError("confirmation mismatch")
        }
        let status = try client.panelStatus()
        guard !status.running, let exposure = status.exposure, ConfirmationBuilder.openWithControls(status)?.informativeText.contains(exposure) == true else {
            fatalError("panel status contract mismatch")
        }
        print("Native client read the real CLI contract for a disabled fixture loop, and its stop review")
    }

    /// Stops the fixture loop as the menu does: review, confirm, apply. Then a
    /// second apply of the same review is refused, because the record changed.
    static func applyStop(_ client: LocalClient) throws {
        let key = try client.panelList().loops[0].key
        guard case .review(let review) = try client.review(.stop, key: key) else { fatalError("no review") }
        let command = ConfirmationBuilder.build(review).buttons[1].command!
        let done = client.apply(command, review: review)
        guard done.outcome == "done", done.text == "Stop requested: the next check carries it out." else { fatalError("apply: \(done)") }
        let again = client.apply(command, review: review)
        guard again.outcome == "review_again" else { fatalError("second apply: \(again)") }
        guard try client.panelList().loops[0].can?.contains("resume") == true else { fatalError("no resume after a stop") }
        print("Native client stopped the fixture loop through a reviewed apply, and the stale review was refused")
        // Pause all, as the menu does: one review, one confirmed apply.
        let canAll = try client.panelList().canAll
        guard canAll == ["pause"], case .review(let all) = try client.reviewAll(.pause),
              all.lines.first?.hasPrefix("example-menu-loop (") == true, all.lines.first?.hasSuffix(") will be paused.") == true,
              all.targets.count == 1 else { fatalError("pause-all review: \(String(describing: canAll)) \(String(describing: try? client.reviewAll(.pause)))") }
        let paused = client.applyAll(ConfirmationBuilder.build(all).buttons[1].command!, review: all)
        guard paused.outcome == "done", paused.text == "Paused 1 loop.", paused.lines == ["example-menu-loop: paused."] else { fatalError("pause all: \(paused)") }
        guard try client.panelList().canAll == [] else { fatalError("canAll after pausing every loop") }
        print("Native client paused every loop through one reviewed apply")
    }
}
