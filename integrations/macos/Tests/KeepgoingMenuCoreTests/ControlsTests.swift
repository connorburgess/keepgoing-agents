import XCTest
@testable import KeepgoingMenuCore

final class ControlsTests: XCTestCase {
    let key = "l0123456789ab"
    let expect = String(repeating: "c1", count: 32)
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
            (reviewJSON(action: "pause", stop: "{\"lanes\":2,\"now\":true}"), 0, .pause), (reviewJSON(stop: "{\"lanes\":-1,\"now\":true}"), 0, .stop),
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
            ConfirmButton(title: "Stop example-api", destructive: true, command: .apply(.stop, key: key, expect: expect, lanes: false, now: false)),
        ])
        XCTAssertNil(c.defaultButton.command)
        XCTAssertNotNil(MenuBuilder.arguments(c.buttons[1].command!))
    }

    // Once the CLI offers lanes and stopping now, a stop offers the
    // conversation alone, or with its lanes, and always runs now.
    func testAStopWithLanesOffersBothAndRunsNow() throws {
        let c = ConfirmationBuilder.build(try review("{\"lanes\":3,\"now\":true}"))
        XCTAssertEqual(c.buttons.map(\.title), ["Cancel", "Stop the Conversation", "Stop It and Its 3 Lanes"])
        XCTAssertEqual(c.buttons.map(\.destructive), [false, true, true])
        XCTAssertEqual(c.buttons[1].command, .apply(.stop, key: key, expect: expect, lanes: false, now: true))
        XCTAssertEqual(c.buttons[2].command, .apply(.stop, key: key, expect: expect, lanes: true, now: true))
        XCTAssertEqual(MenuBuilder.arguments(c.buttons[2].command!), ["client", "apply", "stop", "--loop", key, "--expect", expect, "--lanes", "--now", "--json"])
        XCTAssertEqual(ConfirmationBuilder.build(try review("{\"lanes\":1,\"now\":true}")).buttons.map(\.title), ["Cancel", "Stop the Conversation", "Stop It and Its Lane"])
        // No lanes: one stop, now.
        let none = ConfirmationBuilder.build(try review("{\"lanes\":0,\"now\":true}"))
        XCTAssertEqual(none.buttons.map(\.title), ["Cancel", "Stop example-api"])
        XCTAssertEqual(none.buttons[1].command, .apply(.stop, key: key, expect: expect, lanes: false, now: true))
    }

    func testPauseAndResumeAreNotStyledAsDestructive() throws {
        for action in [LoopAction.pause, .resume] {
            let c = ConfirmationBuilder.build(try review(action: action))
            XCTAssertEqual(c.buttons.map(\.destructive), [false, false])
            XCTAssertEqual(c.buttons[1].command, .apply(action, key: key, expect: expect, lanes: false, now: false))
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
