import XCTest
@testable import KeepgoingMenuCore

final class GroupTests: XCTestCase {
    let now = Date(timeIntervalSince1970: 1_900_000_000)
    func expect(_ i: Int) -> String { "r1.1893499320." + String(repeating: String(format: "%02x", i), count: 32) + "." + String(repeating: "d2", count: 16) }
    func key(_ i: Int) -> String { String(format: "l%012x", i) }

    /// A review of `loops` loops, the first `fitting` of which it pauses.
    func reviewJSON(loops: Int, fitting: Int, action: String = "pause", stop: [String: Any]? = nil) -> Data {
        var lines: [String] = []
        for i in 0..<min(loops, 8) { lines.append(i < fitting ? "example-\(i) (Supervising) will be paused." : "example-\(i) is already paused.") }
        if loops > 8 { lines.append("And \(loops - 8) more.") }
        lines += ["Pauses \(fitting) of \(loops) loops; \(loops - fitting) are already paused.",
                  "keepgoing sends each loop it pauses nothing until that loop is resumed.",
                  "This review expires in 2 min (06:45 +07)."]
        let entries: [[String: Any]] = (0..<loops).map { i in
            var l: [String: Any] = ["key": key(i), "name": "example-\(i)"]
            if i < fitting { l["expect"] = expect(i) }
            return l
        }
        var group: [String: Any] = ["action": action, "title": "Pause all loops?", "lines": lines, "confirm": "Pause \(fitting) Loops",
                                    "expiresAt": "2030-01-01T12:02:00Z", "lost": "fictional lost", "refused": "fictional refused",
                                    "expired": "fictional expired", "loops": entries]
        if let stop { group["stop"] = stop }
        return try! JSONSerialization.data(withJSONObject: ["schemaVersion": 1, "group": group])
    }
    func review(loops: Int, fitting: Int) throws -> GroupReview {
        guard case .review(let r) = try Contract.groupReview(reviewJSON(loops: loops, fitting: fitting), exit: 0, action: .pause) else {
            XCTFail("not a review")
            throw ClientFailure.invalidResponse
        }
        return r
    }

    func testTheReviewIsShortAndConfirmsOnlyTheLoopsItChanges() throws {
        let r = try review(loops: 4, fitting: 3)
        XCTAssertEqual(r.targets, (0..<3).map { LoopExpect(key: key($0), expect: expect($0)) })
        let c = ConfirmationBuilder.build(r)
        XCTAssertEqual(c.messageText, "Pause all loops?")
        // The loops as one block, then the total and what pausing does.
        XCTAssertTrue(c.informativeText.hasPrefix("example-0 (Supervising) will be paused.\nexample-1 (Supervising) will be paused.\nexample-2 (Supervising) will be paused.\nexample-3 is already paused.\n\nPauses 3 of 4 loops"))
        XCTAssertEqual(c.buttons.map(\.title), ["Cancel", "Pause 3 Loops"])
        XCTAssertEqual(c.buttons.map(\.destructive), [false, false])
        XCTAssertNil(c.defaultButton.command)
        XCTAssertEqual(c.buttons[1].command, .applyAll(.pause, expects: r.targets))
        // Many loops: eight named, then "And N more.", still one block.
        let many = ConfirmationBuilder.build(try review(loops: 30, fitting: 30))
        let block = many.informativeText.components(separatedBy: "\n\n")[0].components(separatedBy: "\n")
        XCTAssertEqual(block.count, 9)
        XCTAssertEqual(block.last, "And 22 more.")
        XCTAssertTrue(many.informativeText.allSatisfy { $0 != "\u{7}" })
        XCTAssertTrue(ConfirmationBuilder.current(r, now: r.expiresAt.addingTimeInterval(-1)))
        XCTAssertFalse(ConfirmationBuilder.current(r, now: r.expiresAt))
    }

    func testGroupContract() throws {
        let refused = Data("{\"schemaVersion\":1,\"result\":{\"outcome\":\"refused\",\"text\":\"No loop can be paused now: 4 are already paused. Nothing was changed.\"}}".utf8)
        XCTAssertEqual(try Contract.groupReview(refused, exit: 1, action: .pause), .refused(ChangeResult(outcome: "refused", text: "No loop can be paused now: 4 are already paused. Nothing was changed.")))
        let invalid: [(Data, Int32)] = [
            (reviewJSON(loops: 3, fitting: 0), 0), (reviewJSON(loops: 3, fitting: 2, action: "stop"), 0), (reviewJSON(loops: 3, fitting: 2), 1),
            (refused, 0), (Data("fictional-private-bad-json".utf8), 0),
        ]
        for (data, exit) in invalid {
            XCTAssertThrowsError(try Contract.groupReview(data, exit: exit, action: .pause))
        }
        // A partial result is read as partial, with each loop's line.
        let partial = Data("{\"schemaVersion\":1,\"result\":{\"outcome\":\"partial\",\"text\":\"Paused 2 of 3; 1 needs a new review.\",\"retry\":true,\"lines\":[\"example-1: changed since you reviewed it, so it was not paused; review it again.\",\"example-0: paused.\",\"example-2: paused.\"]}}".utf8)
        let r = try Contract.groupApplied(partial)
        XCTAssertEqual(r.outcome, "partial")
        XCTAssertEqual(r.text, "Paused 2 of 3; 1 needs a new review.")
        XCTAssertEqual(r.lines?.first, "example-1: changed since you reviewed it, so it was not paused; review it again.")
        XCTAssertThrowsError(try Contract.groupApplied(Data("{\"schemaVersion\":1,\"result\":{\"outcome\":\"mostly\",\"text\":\"x\"}}".utf8)))
    }

    func testArgumentsForEveryLoopAreExact() {
        XCTAssertEqual(MenuBuilder.arguments(.reviewAll(.pause)), ["client", "review", "pause", "--all", "--json"])
        XCTAssertEqual(MenuBuilder.arguments(.reviewAll(.stop)), ["client", "review", "stop", "--all", "--json"])
        XCTAssertNil(MenuBuilder.arguments(.reviewAll(.resume)), "not offered for every loop")
        let two = [LoopExpect(key: key(1), expect: expect(1)), LoopExpect(key: key(2), expect: expect(2))]
        XCTAssertEqual(MenuBuilder.arguments(.applyAll(.pause, expects: two)),
                       ["client", "apply", "pause", "--all", "--expect", key(1) + "=" + expect(1), "--expect", key(2) + "=" + expect(2), "--json"])
        for bad in [[], [two[0], two[0]], [LoopExpect(key: "example", expect: expect(1))], [LoopExpect(key: key(1), expect: "--json")],
                    (0..<129).map { LoopExpect(key: key($0), expect: expect($0 % 200)) }] as [[LoopExpect]] {
            XCTAssertNil(MenuBuilder.arguments(.applyAll(.pause, expects: bad)), "\(bad.count)")
        }
        XCTAssertNil(MenuBuilder.arguments(.applyAll(.resume, expects: two)))
        // --lanes for a stop alone.
        XCTAssertEqual(MenuBuilder.arguments(.applyAll(.stop, expects: two, lanes: true)),
                       ["client", "apply", "stop", "--all", "--expect", key(1) + "=" + expect(1), "--expect", key(2) + "=" + expect(2), "--lanes", "--json"])
        XCTAssertEqual(MenuBuilder.arguments(.applyAll(.stop, expects: two))?.contains("--lanes"), false)
        XCTAssertNil(MenuBuilder.arguments(.applyAll(.pause, expects: two, lanes: true)))
    }

    func testTheRowIsOfferedOnlyWhenTheCLISaysSo() {
        let loop = PanelLoop(key: key(1), name: "example-api", state: "supervising", phrase: "Supervising", needsYou: false, can: ["pause", "stop"])
        func rows(_ canAll: [String]?) -> [MenuCommand?] {
            var list = PanelList(schemaVersion: 1, registry: "available", complete: true, allClear: true, needsYou: 0, unread: 0, omitted: 0, loops: [loop])
            list.canAll = canAll
            return MenuBuilder.build(.read(list, at: now), now: now).rows.map(\.command)
        }
        XCTAssertTrue(rows(["pause"]).contains(.reviewAll(.pause)))
        XCTAssertFalse(rows([]).contains(.reviewAll(.pause)))
        XCTAssertFalse(rows(nil).contains(.reviewAll(.pause)), "a CLI without --all")
        var list = PanelList(schemaVersion: 1, registry: "available", complete: true, allClear: true, needsYou: 0, unread: 0, omitted: 0, loops: [loop])
        list.canAll = ["pause"]
        let row = MenuBuilder.build(.read(list, at: now), now: now).rows.first { $0.command == .reviewAll(.pause) }!
        XCTAssertEqual(row.title, "Pause All Loops…")
        XCTAssertEqual(row.accessibilityHelp, "Shows which loops pausing every loop changes, then asks you to confirm.")
    }

    func testApplyAllRunsOnceAndAPartialResultIsNotSuccess() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let binary = dir.appendingPathComponent("fictional keepgoing")
        try "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$0.args\"\ncat \"$0.reply\"\n".write(to: binary, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        let client = LocalClient(executable: binary)
        try String(data: reviewJSON(loops: 3, fitting: 3), encoding: .utf8)!.write(toFile: binary.path + ".reply", atomically: true, encoding: .utf8)
        guard case .review(let r) = try client.reviewAll(.pause) else { return XCTFail("no review") }
        try FileManager.default.removeItem(atPath: binary.path + ".args")
        try "{\"schemaVersion\":1,\"result\":{\"outcome\":\"partial\",\"text\":\"Paused 2 of 3; 1 needs a new review.\",\"lines\":[\"example-1: changed since you reviewed it, so it was not paused; review it again.\"]}}".write(toFile: binary.path + ".reply", atomically: true, encoding: .utf8)
        let command = ConfirmationBuilder.build(r).buttons[1].command!
        let result = client.applyAll(command, review: r)
        XCTAssertEqual(result.outcome, "partial")
        XCTAssertNotEqual(result.outcome, "done")
        let args = try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(args.filter { $0 == "apply" }.count, 1)
        XCTAssertEqual(args.filter { $0 == "--expect" }.count, 3)
        // An unreadable reply is uncertain, in the review's words.
        try "fictional-private-bad-json".write(toFile: binary.path + ".reply", atomically: true, encoding: .utf8)
        XCTAssertEqual(client.applyAll(command, review: r), ChangeResult(outcome: "uncertain", text: "fictional lost"))
        // Only an apply to every loop is run as one.
        XCTAssertEqual(client.applyAll(.reviewAll(.pause), review: r).outcome, "refused")
    }

    // Stop all: one button per option of the whole, in keepgoing's words,
    // destructive; --lanes only for the option that says so; an option that
    // would delete queued messages is never offered for every loop.
    func testStopAllOffersTheGroupsOptions() throws {
        let stop: [String: Any] = ["immediately": true, "options": [
            ["label": "Stop the Conversations", "lanes": false], ["label": "Stop Them and Their Lanes", "lanes": true],
            ["label": "Stop Them, Their Lanes and Their Queued Messages", "lanes": true, "dropQueued": true],
        ]]
        guard case .review(let r) = try Contract.groupReview(reviewJSON(loops: 3, fitting: 3, action: "stop", stop: stop), exit: 0, action: .stop) else {
            return XCTFail("not a review")
        }
        let c = ConfirmationBuilder.build(r)
        XCTAssertEqual(c.buttons.map(\.title), ["Cancel", "Stop the Conversations", "Stop Them and Their Lanes"])
        XCTAssertEqual(c.buttons.map(\.destructive), [false, true, true])
        XCTAssertNil(c.defaultButton.command)
        XCTAssertEqual(c.buttons[1].command, .applyAll(.stop, expects: r.targets, lanes: false))
        XCTAssertEqual(c.buttons[2].command, .applyAll(.stop, expects: r.targets, lanes: true))
        XCTAssertEqual(MenuBuilder.arguments(c.buttons[2].command!)?.last(where: { $0.hasPrefix("--") && $0 != "--expect" && $0 != "--all" }), "--json")
        XCTAssertTrue(MenuBuilder.arguments(c.buttons[2].command!)!.contains("--lanes"))
        XCTAssertFalse(MenuBuilder.arguments(c.buttons[1].command!)!.contains("--lanes"))
        // No options: the one stop, destructive.
        guard case .review(let plain) = try Contract.groupReview(reviewJSON(loops: 2, fitting: 2, action: "stop"), exit: 0, action: .stop) else { return XCTFail() }
        let p = ConfirmationBuilder.build(plain)
        XCTAssertEqual(p.buttons.map(\.destructive), [false, true])
        XCTAssertEqual(p.buttons[1].command, .applyAll(.stop, expects: plain.targets))
        // The row, only when keepgoing says stop can be made to every loop.
        let loop = PanelLoop(key: key(1), name: "example-api", state: "supervising", phrase: "Supervising", needsYou: false, can: ["pause", "stop"])
        var list = PanelList(schemaVersion: 1, registry: "available", complete: true, allClear: true, needsYou: 0, unread: 0, omitted: 0, loops: [loop])
        list.canAll = ["pause", "stop"]
        let rows = MenuBuilder.build(.read(list, at: now), now: now).rows
        XCTAssertEqual(rows.first { $0.command == .reviewAll(.stop) }?.title, "Stop All Loops…")
        list.canAll = ["pause"]
        XCTAssertFalse(MenuBuilder.build(.read(list, at: now), now: now).rows.contains { $0.command == .reviewAll(.stop) })
    }
}