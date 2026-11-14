import XCTest
@testable import KeepgoingMenuCore

final class ControlsTests: XCTestCase {
    let key = "l0123456789ab"
    let expect = "r1.1893499320." + String(repeating: "c1", count: 32) + "." + String(repeating: "d2", count: 16)
    let lines = ["Now: Supervising. Last check: 2 min ago.",
                 "The loop's next check carries out the stop; this review cannot confirm that the session stopped. Nothing is sent until it is resumed.",
                 "This stops the conversation only: its lanes, if it has any, keep running.",
                 "This review expires in 2 min (12:02)."]

    func reviewJSON(action: String = "stop", key: String? = nil, expect: String? = nil, stop: String? = nil, extra: String = "") -> Data {
        let body: [String: Any] = ["action": action, "key": key ?? self.key, "title": "Stop example-api?", "lines": lines, "confirm": "Stop example-api",
                                   "expect": expect ?? self.expect, "expiresAt": "2030-01-01T12:02:00Z", "lost": "keepgoing could not confirm whether example-api was stopped. Check its state before doing anything else.",
                                   "refused": "The panel refused this request before doing anything. Nothing was changed.", "expired": "This review expired. Nothing was changed. Review it again."]
        var json = String(data: try! JSONSerialization.data(withJSONObject: ["schemaVersion": 1, "preview": body], options: [.sortedKeys]), encoding: .utf8)!
        if let stop { json = json.replacingOccurrences(of: "\"title\"", with: "\"stop\":\(stop),\"title\"") }
        return Data((json.dropLast() + extra + "}").utf8)
    }
    func review(_ stop: String? = nil, action: LoopAction = .stop) throws -> Review {
        guard case .review(let r) = try Contract.review(reviewJSON(action: action.rawValue, stop: stop), exit: 0, action: action, key: key) else {
            XCTFail("not a review")
            throw ClientFailure.invalidResponse
        }
        return r
    }

    func testReviewContract() throws {
        let r = try review()
        XCTAssertEqual(r.title, "Stop example-api?")
        XCTAssertEqual(r.lines, lines)
        XCTAssertEqual(r.expiresAt, Date(timeIntervalSince1970: 1_893_499_320))
        XCTAssertNil(r.stop, "today's CLI offers no stop options")
        // New additive fields are tolerated.
        XCTAssertNoThrow(try Contract.review(reviewJSON(extra: ",\"addedLater\":true"), exit: 0, action: .stop, key: key))
        // A refusal is read, in the page's words, from a failed exit.
        let refused = Data("{\"schemaVersion\":1,\"result\":{\"outcome\":\"refused\",\"text\":\"keepgoing is in the middle of a check on this loop. Nothing was changed.\",\"retry\":true}}".utf8)
        XCTAssertEqual(try Contract.review(refused, exit: 1, action: .stop, key: key),
                       .refused(ChangeResult(outcome: "refused", text: "keepgoing is in the middle of a check on this loop. Nothing was changed.", retry: true)))
        XCTAssertThrowsError(try Contract.review(Data("{\"schemaVersion\":2}".utf8), exit: 0, action: .stop, key: key)) { XCTAssertEqual(String(describing: $0), "unsupported") }
        // A review of another loop or action, a bad expect value, options on
        // anything but a stop, or a reply that disagrees with its exit code
        // is not shown.
        let invalid: [(Data, Int32, LoopAction)] = [
            (reviewJSON(key: "l000000000000"), 0, .stop), (reviewJSON(action: "pause"), 0, .stop), (reviewJSON(expect: "abc"), 0, .stop),
            (reviewJSON(expect: String(repeating: "C1", count: 32)), 0, .stop), (reviewJSON(), 1, .stop), (refused, 0, .stop),
            (reviewJSON(action: "pause", stop: conversation), 0, .pause),
            (reviewJSON(expect: String(repeating: "c1", count: 32)), 0, .stop),
            (Data("{\"schemaVersion\":1,\"result\":{\"outcome\":\"maybe\",\"text\":\"x\"}}".utf8), 1, .stop),
            (Data("fictional-private-bad-json".utf8), 0, .stop), (Data(), 0, .stop),
        ]
        for (data, exit, action) in invalid {
            XCTAssertThrowsError(try Contract.review(data, exit: exit, action: action, key: key)) { error in
                XCTAssertEqual(String(describing: error), "invalidResponse", String(data: data, encoding: .utf8) ?? "")
            }
        }
    }

    // Today's CLI: one stop, of the conversation, at the next check, which
    // the review's own lines say. Cancel is the default; Stop is destructive.
    func testAStopConfirmationShowsTheReviewWithCancelAsDefault() throws {
        let c = ConfirmationBuilder.build(try review())
        XCTAssertEqual(c.messageText, "Stop example-api?")
        XCTAssertEqual(c.informativeText, lines.joined(separator: "\n\n"))
        XCTAssertEqual(c.buttons, [
            ConfirmButton(title: "Cancel", destructive: false, command: nil),
            ConfirmButton(title: "Stop example-api", destructive: true, command: .apply(.stop, key: key, expect: expect, lanes: false, immediately: false)),
        ])
        XCTAssertNil(c.defaultButton.command)
        XCTAssertNotNil(MenuBuilder.arguments(c.buttons[1].command!))
    }

    let conversation = "{\"immediately\":true,\"options\":[{\"label\":\"Stop the Conversation\",\"lanes\":false},{\"label\":\"Stop It and Its 3 Lanes\",\"lanes\":true}]}"

    // Once the CLI offers lanes and stopping at once, a stop offers each of
    // its options in the CLI's own words, and always runs immediately.
    func testAStopWithLanesOffersTheCLIsOptionsAndRunsImmediately() throws {
        let c = ConfirmationBuilder.build(try review(conversation))
        XCTAssertEqual(c.buttons.map(\.title), ["Cancel", "Stop the Conversation", "Stop It and Its 3 Lanes"])
        XCTAssertEqual(c.buttons.map(\.destructive), [false, true, true])
        XCTAssertEqual(c.buttons[1].command, .apply(.stop, key: key, expect: expect, lanes: false, immediately: true))
        XCTAssertEqual(c.buttons[2].command, .apply(.stop, key: key, expect: expect, lanes: true, immediately: true))
        XCTAssertEqual(MenuBuilder.arguments(c.buttons[2].command!), ["client", "apply", "stop", "--loop", key, "--expect", expect, "--lanes", "--immediately", "--json"])
        // The words are the CLI's: this app writes none of them.
        let other = ConfirmationBuilder.build(try review("{\"immediately\":true,\"options\":[{\"label\":\"Fictional words A\",\"lanes\":false},{\"label\":\"Fictional words B\",\"lanes\":true}]}"))
        XCTAssertEqual(other.buttons.map(\.title), ["Cancel", "Fictional words A", "Fictional words B"])
        // One option: one stop, immediately.
        let none = ConfirmationBuilder.build(try review("{\"immediately\":true,\"options\":[{\"label\":\"Stop example-api\",\"lanes\":false}]}"))
        XCTAssertEqual(none.buttons.map(\.title), ["Cancel", "Stop example-api"])
        XCTAssertEqual(none.buttons[1].command, .apply(.stop, key: key, expect: expect, lanes: false, immediately: true))
    }

    // #209's third option: it also deletes the messages queued in lanes that
    // have not started. Only that option passes --drop-queued, and always
    // with --lanes.
    func testTheDropQueuedOptionAloneDropsQueuedMessages() throws {
        let three = "{\"lanes\":2,\"queued\":4,\"immediately\":true,\"options\":[{\"label\":\"Stop the Conversation\",\"lanes\":false},{\"label\":\"Stop It and Its 2 Lanes\",\"lanes\":true},{\"label\":\"Stop It, Its Lanes and Their 4 Queued Messages\",\"lanes\":true,\"dropQueued\":true}]}"
        let c = ConfirmationBuilder.build(try review(three))
        XCTAssertEqual(c.buttons.map(\.title), ["Cancel", "Stop the Conversation", "Stop It and Its 2 Lanes", "Stop It, Its Lanes and Their 4 Queued Messages"])
        XCTAssertEqual(c.buttons.map(\.destructive), [false, true, true, true])
        XCTAssertNil(c.defaultButton.command)
        let args = c.buttons.dropFirst().map { MenuBuilder.arguments($0.command!)! }
        XCTAssertEqual(args[0], ["client", "apply", "stop", "--loop", key, "--expect", expect, "--immediately", "--json"])
        XCTAssertEqual(args[1], ["client", "apply", "stop", "--loop", key, "--expect", expect, "--lanes", "--immediately", "--json"])
        XCTAssertEqual(args[2], ["client", "apply", "stop", "--loop", key, "--expect", expect, "--lanes", "--immediately", "--drop-queued", "--json"])
        XCTAssertEqual(args.filter { $0.contains("--drop-queued") }.count, 1)
        // Never without --lanes, and never for anything but a stop.
        XCTAssertNil(MenuBuilder.arguments(.apply(.stop, key: key, expect: expect, lanes: false, immediately: true, dropQueued: true)))
        XCTAssertNil(MenuBuilder.arguments(.apply(.pause, key: key, expect: expect, lanes: false, immediately: false, dropQueued: true)))
    }

    // So that the next additive change cannot break Stop: the review is still
    // shown, and only the options this menu fully understands are offered.
    // An option with a field it does not know is never offered without that
    // field, because its button would then do less than its words say.
    func testAnOptionTheMenuDoesNotUnderstandIsNotOfferedAndTheReviewStillIs() throws {
        func titles(_ stop: String) throws -> [String] { ConfirmationBuilder.build(try review(stop)).buttons.map(\.title) }
        let base = "{\"label\":\"Stop the Conversation\",\"lanes\":false}"
        // A field it does not know.
        XCTAssertEqual(try titles("{\"immediately\":true,\"options\":[\(base),{\"label\":\"Stop It and Forget Its History\",\"lanes\":true,\"dropHistory\":true}]}"), ["Cancel", "Stop the Conversation"])
        // A known field of the wrong type, or missing.
        XCTAssertEqual(try titles("{\"immediately\":true,\"options\":[\(base),{\"label\":\"B\",\"lanes\":\"yes\"},{\"label\":\"C\",\"lanes\":true,\"dropQueued\":1},{\"label\":\"D\"}]}"), ["Cancel", "Stop the Conversation"])
        // Not an object at all.
        XCTAssertEqual(try titles("{\"immediately\":true,\"options\":[\"fictional\",\(base),7]}"), ["Cancel", "Stop the Conversation"])
        // Dropping queued messages without stopping the lanes; a label with
        // nothing to show; two alike: the first is kept.
        XCTAssertEqual(try titles("{\"immediately\":true,\"options\":[{\"label\":\"X\",\"lanes\":false,\"dropQueued\":true},{\"label\":\"\\u0007\",\"lanes\":true},\(base),{\"label\":\"Again\",\"lanes\":false}]}"), ["Cancel", "Stop the Conversation"])
        // Nothing understood, no options, or an unreadable `immediately`: the
        // one plain stop, which waits for the next check unless keepgoing
        // said it can be made at once.
        for stop in ["{\"immediately\":true,\"options\":[{\"label\":\"Z\",\"lanes\":true,\"future\":1}]}", "{\"immediately\":true,\"options\":[]}", "{\"immediately\":true}"] {
            let c = ConfirmationBuilder.build(try review(stop))
            XCTAssertEqual(c.buttons.map(\.title), ["Cancel", "Stop example-api"], stop)
            XCTAssertEqual(c.buttons[1].command, .apply(.stop, key: key, expect: expect, lanes: false, immediately: true))
        }
        let unreadable = ConfirmationBuilder.build(try review("{\"immediately\":\"soon\",\"options\":[\(base)]}"))
        XCTAssertEqual(unreadable.buttons.map(\.title), ["Cancel", "Stop example-api"])
        XCTAssertEqual(unreadable.buttons[1].command, .apply(.stop, key: key, expect: expect, lanes: false, immediately: false))
        // The review's own sentences are always shown.
        XCTAssertEqual(ConfirmationBuilder.build(try review("{\"immediately\":true,\"options\":[{\"x\":1}]}")).informativeText, lines.joined(separator: "\n\n"))
    }

    func testPauseAndResumeAreNotStyledAsDestructive() throws {
        for action in [LoopAction.pause, .resume] {
            let c = ConfirmationBuilder.build(try review(action: action))
            XCTAssertEqual(c.buttons.map(\.destructive), [false, false])
            XCTAssertEqual(c.buttons[1].command, .apply(action, key: key, expect: expect, lanes: false, immediately: false))
        }
    }

    func testAnExpiredReviewIsNotApplied() throws {
        let r = try review()
        XCTAssertTrue(ConfirmationBuilder.current(r, now: r.expiresAt.addingTimeInterval(-1)))
        XCTAssertFalse(ConfirmationBuilder.current(r, now: r.expiresAt))
    }

    func testAppliedContract() throws {
        let done = Data("{\"schemaVersion\":1,\"result\":{\"outcome\":\"done\",\"text\":\"Stop requested: the next check carries it out.\"}}".utf8)
        XCTAssertEqual(try Contract.applied(done), ChangeResult(outcome: "done", text: "Stop requested: the next check carries it out."))
        for bad in ["{\"schemaVersion\":1}", "{\"schemaVersion\":1,\"result\":{\"outcome\":\"ok\",\"text\":\"x\"}}", "{\"schemaVersion\":1,\"result\":{\"outcome\":\"done\",\"text\":\"\\u0007\"}}", "x"] {
            XCTAssertThrowsError(try Contract.applied(Data(bad.utf8)), bad)
        }
    }

    // Starting the panel with controls, or restarting a read-only one, is
    // asked first, with the footer's exposure statement; opening one that
    // already allows changes is not.
    func testOpenWithControlsAsksFirstAndSaysWhoCanMakeChanges() throws {
        let exposure = "While changes are allowed, anyone who can use this browser window can pause, stop and resume loops."
        let reply = "{\"schemaVersion\":1,\"running\":true,\"changesAllowed\":false,\"exposure\":\"\(exposure)\"}"
        let readOnly = try Contract.panelStatus(Data(reply.utf8), exit: 0)
        XCTAssertEqual(readOnly.exposure, exposure)
        let restart = try XCTUnwrap(ConfirmationBuilder.openWithControls(readOnly))
        XCTAssertEqual(restart.messageText, "Restart the panel with controls?")
        XCTAssertTrue(restart.informativeText.hasPrefix("The read-only panel that is running is stopped first"))
        XCTAssertTrue(restart.informativeText.contains(exposure))
        XCTAssertEqual(restart.buttons.map(\.title), ["Cancel", "Restart with Controls"])
        XCTAssertNil(restart.defaultButton.command)
        XCTAssertEqual(restart.buttons[1].command, .openPanelWithChanges)
        var stopped = PanelStatus(schemaVersion: 1, running: false, idleRemainingSeconds: nil)
        stopped.exposure = exposure
        let open = try XCTUnwrap(ConfirmationBuilder.openWithControls(stopped))
        XCTAssertEqual(open.messageText, "Open the panel with controls?")
        XCTAssertTrue(open.informativeText.contains(exposure))
        XCTAssertFalse(open.informativeText.contains("read-only"))
        var allowing = readOnly
        allowing.changesAllowed = true
        XCTAssertNil(ConfirmationBuilder.openWithControls(allowing))
        XCTAssertNil(ConfirmationBuilder.openWithControls(PanelStatus(schemaVersion: 1, running: false, idleRemainingSeconds: nil)))
    }
}
