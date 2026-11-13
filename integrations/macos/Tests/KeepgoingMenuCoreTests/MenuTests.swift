import XCTest
@testable import KeepgoingMenuCore

final class MenuTests: XCTestCase {
    let now = Date(timeIntervalSince1970: 1_900_000_000)

    func loop(_ index: Int, _ name: String, _ state: String, _ phrase: String, needsYou: Bool = false) -> PanelLoop {
        PanelLoop(key: String(format: "l%012x", index), name: name, state: state, phrase: phrase, needsYou: needsYou)
    }
    func list(_ loops: [PanelLoop], needsYou: Int = 0, unread: Int = 0, complete: Bool = true, registry: String = "available", omitted: Int = 0) -> PanelList {
        PanelList(schemaVersion: 1, registry: registry, complete: complete, allClear: registry == "available" && complete && needsYou == 0, needsYou: needsYou, unread: unread, omitted: omitted, loops: loops)
    }
    var three: PanelList {
        list([loop(1, "example-api", "waiting_for_you", "Waiting for you", needsYou: true),
              loop(2, "example-docs", "supervising", "Supervising"),
              loop(3, "example-site", "paused", "Paused")], needsYou: 1)
    }
    func text(_ model: MenuModel) -> [String] {
        model.rows.map { row in
            switch row.kind {
            case .separator: return "---"
            case .loop: return "\(row.title) | \(row.detail)"
            default: return row.title
            }
        }
    }

    func testThreeLoopsGiveTheAgreedLayout() {
        let model = MenuBuilder.build(.read(three, at: now.addingTimeInterval(-130)), now: now)
        XCTAssertEqual(text(model), [
            "Open keepgoing Panel",
            "1 loop needs you",
            "---",
            "example-api | Waiting for you",
            "example-docs | Supervising",
            "example-site | Paused",
            "---",
            "Quit keepgoing Menu",
            "---",
            "Read 2 min ago",
        ])
        XCTAssertEqual(model.badge, "1")
        XCTAssertEqual(model.iconAccessibilityLabel, "keepgoing, 1 loop needs you")
        XCTAssertEqual(model.rows.map(\.command), [
            .openPanel, .openPanel, nil,
            .openLoop(key: "l000000000001"), .openLoop(key: "l000000000002"), .openLoop(key: "l000000000003"),
            nil, .quit, nil, nil,
        ])
        // A row says what it is; what choosing it does is offered as help.
        let api = model.rows[3]
        XCTAssertEqual(api.accessibilityLabel, "example-api, Waiting for you")
        XCTAssertEqual(api.accessibilityHelp, "Opens this loop in the panel.")
        XCTAssertEqual(model.rows[0].accessibilityLabel, "Open keepgoing Panel")
        XCTAssertEqual(model.rows[0].accessibilityHelp, "Opens the keepgoing panel in your browser.")
    }

    // Nothing that looks like a menu item is unclickable, except the one
    // status line, which is last.
    func testOnlyTheLastRowCannotBeChosen() {
        let readings: [MenuReading] = [.notRead, .reading, .unsupported, .unavailable, .read(three, at: now), .read(list([]), at: now),
                                       .read(list([], registry: "unavailable"), at: now)]
        for reading in readings {
            for refreshing in [false, true] {
                let model = MenuBuilder.build(reading, now: now, refreshing: refreshing)
                let rows = model.rows.filter { $0.kind != .separator }
                XCTAssertEqual(rows.last?.kind, .status)
                XCTAssertEqual(rows.filter { !$0.isEnabled }.count, 1, "\(reading)")
                XCTAssertEqual(rows.filter { $0.kind == .status }.count, 1)
                XCTAssertEqual(rows.first?.command, .openPanel, "the first row always opens the panel")
                XCTAssertNotEqual(model.rows.first?.kind, .separator)
                // No two separators in a row, and every row is labelled.
                for (a, b) in zip(model.rows, model.rows.dropFirst()) { XCTAssertFalse(a.kind == .separator && b.kind == .separator) }
                XCTAssertTrue(rows.allSatisfy { !$0.accessibilityLabel.isEmpty && !$0.title.isEmpty })
                XCTAssertTrue(rows.filter { $0.isEnabled }.allSatisfy { !$0.accessibilityHelp.isEmpty && !$0.symbol.isEmpty })
            }
        }
    }

    func testStopRowIsThereOnlyWhileThePanelRuns() {
        func rows(_ panel: PanelStatus?) -> [String] { text(MenuBuilder.build(.read(three, at: now), now: now, panel: panel)) }
        let stopped = PanelStatus(schemaVersion: 1, running: false, idleRemainingSeconds: nil)
        XCTAssertEqual(rows(nil), rows(stopped))
        XCTAssertFalse(rows(nil).contains { $0.contains("Stop") })
        let model = MenuBuilder.build(.read(three, at: now), now: now, panel: PanelStatus(schemaVersion: 1, running: true, idleRemainingSeconds: 1380))
        XCTAssertEqual(Array(text(model).suffix(5)), ["---", "Stop the Panel Page", "Quit keepgoing Menu", "---", "Read just now"])
        let stop = model.rows.first { $0.command == .stopPanel }!
        XCTAssertEqual(stop.detail, "stops by itself in 23 min")
        XCTAssertEqual(stop.accessibilityLabel, "Stop the Panel Page, stops by itself in 23 min")
        XCTAssertEqual(stop.accessibilityHelp, "Stops the panel's background process. Your loops keep running.")
        // It does not borrow the symbol of a stopped loop.
        XCTAssertNotEqual(stop.symbol, MenuBuilder.symbols["stopped"])
        XCTAssertEqual(MenuBuilder.stopsIn(59), "stops by itself in under a minute")
        XCTAssertEqual(MenuBuilder.stopsIn(60), "stops by itself in 1 min")
        XCTAssertEqual(MenuBuilder.stopsIn(7200), "stops by itself in 2 h")
        XCTAssertEqual(MenuBuilder.stopsIn(nil), "running")
        // It is offered even when the list could not be read.
        XCTAssertTrue(MenuBuilder.build(.unavailable, now: now, panel: PanelStatus(schemaVersion: 1, running: true, idleRemainingSeconds: 5)).rows.contains { $0.command == .stopPanel })
        // The status line stays the only row that cannot be chosen.
        XCTAssertEqual(model.rows.filter { $0.kind != .separator && !$0.isEnabled }.count, 1)
    }

    func testUnaskedReadsAreSlowAndBackOff() {
        XCTAssertEqual(ReadSchedule.interval(consecutiveFailures: 0, lowPower: false), 60)
        XCTAssertEqual(ReadSchedule.interval(consecutiveFailures: 2, lowPower: false), 60)
        XCTAssertEqual(ReadSchedule.interval(consecutiveFailures: 3, lowPower: false), 300)
        XCTAssertEqual(ReadSchedule.interval(consecutiveFailures: 40, lowPower: false), 300)
        XCTAssertNil(ReadSchedule.interval(consecutiveFailures: 0, lowPower: true))
        XCTAssertNil(ReadSchedule.interval(consecutiveFailures: 5, lowPower: true))
    }

    func testPanelStatusContract() throws {
        XCTAssertEqual(try Contract.panelStatus(Data("{\"schemaVersion\":1,\"running\":false}".utf8), exit: 0), PanelStatus(schemaVersion: 1, running: false, idleRemainingSeconds: nil))
        let running = try Contract.panelStatus(Data("{\"schemaVersion\":1,\"running\":true,\"startedAt\":\"2030-01-01T12:00:00Z\",\"port\":49152,\"idleSeconds\":1800,\"idleRemainingSeconds\":1380}".utf8), exit: 0)
        XCTAssertEqual(running, PanelStatus(schemaVersion: 1, running: true, idleRemainingSeconds: 1380))
        XCTAssertThrowsError(try Contract.panelStatus(Data("{\"schemaVersion\":2,\"running\":true}".utf8), exit: 0)) { XCTAssertEqual(String(describing: $0), "unsupported") }
        for (body, exit) in [("{\"schemaVersion\":1,\"running\":true}", Int32(1)), ("fictional-private-bad-json", 0), ("{\"schemaVersion\":1,\"running\":true,\"idleRemainingSeconds\":-4}", 0), ("{\"schemaVersion\":1}", 0)] {
            XCTAssertThrowsError(try Contract.panelStatus(Data(body.utf8), exit: exit)) { error in
                XCTAssertFalse(String(describing: error).contains("fictional-private"))
            }
        }
    }

    func testCommandsRunOnlyThePanelCommand() {
        XCTAssertEqual(MenuBuilder.arguments(.stopPanel), ["panel", "stop"])
        XCTAssertEqual(MenuBuilder.arguments(.openPanel), ["panel"])
        XCTAssertEqual(MenuBuilder.arguments(.openLoop(key: "l0123456789ab")), ["panel", "--loop", "l0123456789ab"])
        XCTAssertNil(MenuBuilder.arguments(.quit))
        // A key that is not a page key never reaches the command line.
        for bad in ["", "--idle", "l0123", "example-api", "l0123456789ab --no-open", "L0123456789AB"] {
            XCTAssertNil(MenuBuilder.arguments(.openLoop(key: bad)), bad)
        }
    }

    func testBadgeAndNeedsLine() {
        func model(_ needs: Int, _ unread: Int) -> MenuModel { MenuBuilder.build(.read(list([], needsYou: needs, unread: unread), at: now), now: now) }
        XCTAssertEqual(model(0, 0).badge, "")
        XCTAssertEqual(model(0, 0).iconAccessibilityLabel, "keepgoing")
        XCTAssertFalse(text(model(0, 0)).contains { $0.contains("need") })
        XCTAssertEqual(text(model(3, 0))[1], "3 loops need you")
        XCTAssertEqual(text(model(1, 1))[1], "1 loop can't be read")
        XCTAssertEqual(text(model(3, 2))[1], "1 loop needs you, 2 can't be read")
        // The symbol says whether a person is needed or only that a read failed.
        XCTAssertEqual(model(1, 1).rows[1].symbol, "questionmark.circle")
        XCTAssertEqual(model(3, 2).rows[1].symbol, "exclamationmark.circle.fill")
        XCTAssertEqual(model(3, 2).badge, "3")
        XCTAssertEqual(model(250, 0).badge, "99+")
        // The icon is plain only when a complete read found nothing: the CLI's
        // allClear, never a count of zero alone.
        func badge(_ l: PanelList) -> String { MenuBuilder.build(.read(l, at: now), now: now).badge }
        XCTAssertEqual(badge(list(three.loops)), "")
        XCTAssertEqual(badge(list([])), "", "no loops registered, read completely, is all clear")
        XCTAssertEqual(badge(list([], registry: "unavailable")), "?")
        XCTAssertEqual(MenuBuilder.build(.read(list([], registry: "unavailable"), at: now), now: now).iconAccessibilityLabel, "keepgoing, the list of loops can't be read")
        XCTAssertEqual(badge(list(three.loops, complete: false)), "?")
        XCTAssertEqual(MenuBuilder.build(.read(list(three.loops, complete: false), at: now), now: now).iconAccessibilityLabel, "keepgoing, not every loop was read")
        XCTAssertEqual(badge(PanelList(schemaVersion: 1, registry: "available", complete: true, allClear: false, needsYou: 0, unread: 0, omitted: 3, loops: three.loops)), "?",
                       "a list that leaves loops out cannot say all clear unless the CLI does")
        XCTAssertEqual(badge(list(three.loops, needsYou: 2, complete: false)), "2", "a count is shown whenever there is one")
        // Nothing is claimed before a read. A failed read shows on the icon,
        // so it cannot be taken for "nothing needs you".
        for reading in [MenuReading.notRead, .reading] {
            XCTAssertEqual(MenuBuilder.build(reading, now: now).badge, "")
        }
        for reading in [MenuReading.unsupported, .unavailable] {
            XCTAssertEqual(MenuBuilder.build(reading, now: now).badge, "?")
            XCTAssertEqual(MenuBuilder.build(reading, now: now).iconAccessibilityLabel, "keepgoing, can't be read")
        }
    }

    func testStatusLineSaysWhatWasRead() {
        func last(_ reading: MenuReading, notice: String? = nil, refreshing: Bool = false) -> String {
            MenuBuilder.build(reading, now: now, notice: notice, refreshing: refreshing).rows.last!.title
        }
        XCTAssertEqual(last(.read(three, at: now)), "Read just now")
        XCTAssertEqual(last(.read(three, at: now.addingTimeInterval(-59))), "Read just now")
        XCTAssertEqual(last(.read(three, at: now.addingTimeInterval(-3599))), "Read 59 min ago")
        XCTAssertEqual(last(.read(three, at: now.addingTimeInterval(-7300))), "Read 2 h ago")
        XCTAssertEqual(last(.read(three, at: now.addingTimeInterval(-200_000))), "Read more than a day ago")
        XCTAssertEqual(last(.read(three, at: now.addingTimeInterval(30))), "Read just now", "a clock that moved back is not a negative age")
        XCTAssertEqual(last(.read(list([]), at: now)), "No loops are registered. Read just now.")
        XCTAssertEqual(last(.read(list([], registry: "unavailable"), at: now)), "The list of loops could not be read. Read just now.")
        XCTAssertEqual(last(.read(list(three.loops, needsYou: 1, complete: false), at: now)), "Not every loop was read. Read just now.")
        // Loops the CLI left out are reachable through one row, not a sentence.
        XCTAssertEqual(last(.read(list(three.loops, omitted: 4), at: now)), "Read just now")
        XCTAssertTrue(MenuBuilder.build(.read(list(three.loops, omitted: 4), at: now), now: now).rows.contains { $0.title == "All 7 Loops in the Panel" && $0.command == .openPanel })
        XCTAssertEqual(last(.unsupported), "The menu and keepgoing are different versions. Use the panel.")
        XCTAssertEqual(last(.unavailable), "Can't reach keepgoing. Run keepgoing doctor.")
        XCTAssertEqual(last(.read(three, at: now), refreshing: true), "Reading…")
        XCTAssertEqual(last(.read(three, at: now), notice: "The panel could not be opened. Run keepgoing panel in a terminal."), "The panel could not be opened. Run keepgoing panel in a terminal.")
        // While a read is under way the last read's rows stay in place.
        XCTAssertEqual(MenuBuilder.build(.read(three, at: now), now: now, refreshing: true).rows.filter { $0.kind == .loop }.count, 3)
    }

    func testLongNamesHundredLoopsAndUnknownStates() {
        let long = "example-monorepo-migration-of-the-billing-service-phase-one"
        let sibling = "example-monorepo-migration-of-the-billing-service-phase-two"
        let hostile = "example\u{0007}-loop\nsecond\u{2028}line"
        var loops = [loop(1, long, "supervising", "Supervising"), loop(2, sibling, "supervising", "Supervising"),
                     loop(3, hostile, "a_state_from_a_newer_build", String(repeating: "A very long phrase from a newer build ", count: 4))]
        for i in 4...100 { loops.append(loop(i, "example-loop-\(i)", "off", "Off")) }
        let model = MenuBuilder.build(.read(list(loops), at: now), now: now)
        let rows = model.rows.filter { $0.kind == .loop }
        // The list is capped; one more row leads to the rest.
        XCTAssertEqual(rows.count, MenuBuilder.maxLoops)
        let more = model.rows.first { $0.title == "All 100 Loops in the Panel" }
        XCTAssertEqual(more?.command, .openPanel)
        XCTAssertNil(MenuBuilder.build(.read(list(Array(loops.prefix(25))), at: now), now: now).rows.first { $0.title.hasPrefix("All ") })
        XCTAssertEqual(MenuBuilder.build(.read(list(Array(loops.prefix(10)), omitted: 30), at: now), now: now).rows.first { $0.title.hasPrefix("All ") }?.title, "All 40 Loops in the Panel")
        // Shortened in the middle, so two names with the same beginning differ.
        XCTAssertEqual(rows[0].title.count, MenuBuilder.maxName)
        XCTAssertTrue(rows[0].title.contains("…"))
        XCTAssertTrue(rows[0].title.hasPrefix("example-") && rows[0].title.hasSuffix("phase-one"))
        XCTAssertNotEqual(rows[0].title, rows[1].title)
        XCTAssertEqual(rows[0].toolTip, long, "the full name is still reachable")
        XCTAssertEqual(rows[0].accessibilityLabel, long + ", Supervising")
        XCTAssertEqual(rows[2].title, "example-loopsecondline", "control characters and line separators never reach the menu")
        XCTAssertEqual(rows[2].symbol, "questionmark.circle", "a state this build does not know gets the unknown symbol")
        XCTAssertEqual(rows[2].detail.count, MenuBuilder.maxPhrase, "a phrase cannot set the menu's width")
        XCTAssertEqual(rows[3].toolTip, "")
        XCTAssertTrue(rows.allSatisfy { $0.isEnabled && !$0.symbol.isEmpty })
        // Joiners that scripts and emoji sequences need are kept.
        XCTAssertEqual(Contract.text("a\u{200D}b\u{200C}c\u{200B}\u{0000}"), "a\u{200D}b\u{200C}c")
        // Every state the panel has gets a symbol, and only needs-you is filled.
        for state in ["waiting_for_you", "unknown", "checks_failing", "not_checked_recently", "waiting_on_limit", "supervising", "outside_hours", "paused", "off", "stopped", "finished"] {
            XCTAssertNotNil(MenuBuilder.symbols[state], state)
        }
        XCTAssertEqual(MenuBuilder.symbols.values.filter { $0.hasSuffix(".fill") }, ["exclamationmark.circle.fill"])
    }

    func testAnOpenMenuCanBeUpdatedInPlaceOnlyWhenItsRowsAreTheSame() {
        let before = MenuBuilder.build(.read(three, at: now.addingTimeInterval(-600)), now: now)
        let reading = MenuBuilder.build(.read(three, at: now.addingTimeInterval(-600)), now: now, refreshing: true)
        let after = MenuBuilder.build(.read(three, at: now), now: now)
        XCTAssertTrue(MenuBuilder.sameShape(before.rows, reading.rows))
        XCTAssertTrue(MenuBuilder.sameShape(before.rows, after.rows))
        // A loop that starts needing a person, a different set of loops, and a
        // panel that started all change the rows.
        var changed = three.loops
        changed.removeLast()
        XCTAssertFalse(MenuBuilder.sameShape(before.rows, MenuBuilder.build(.read(list(changed, needsYou: 1), at: now), now: now).rows))
        XCTAssertFalse(MenuBuilder.sameShape(before.rows, MenuBuilder.build(.read(list(three.loops), at: now), now: now).rows))
        XCTAssertFalse(MenuBuilder.sameShape(before.rows, MenuBuilder.build(.read(three, at: now), now: now, panel: PanelStatus(schemaVersion: 1, running: true, idleRemainingSeconds: 60)).rows))
    }

    func testPanelListContract() throws {
        func reply(_ loops: String, version: Int = 1, registry: String = "available", needs: Int = 1, unread: Int = 0, extra: String = "") -> Data {
            Data("{\"schemaVersion\":\(version),\"readAt\":\"2030-01-01T12:00:00Z\",\"registry\":\"\(registry)\",\"complete\":true,\"allClear\":false,\"needsYou\":\(needs),\"unread\":\(unread),\"omitted\":0,\"loops\":[\(loops)]\(extra)}".utf8)
        }
        let one = "{\"key\":\"l0123456789ab\",\"name\":\"example-api\",\"state\":\"waiting_for_you\",\"phrase\":\"Waiting for you\",\"needsYou\":true,\"addedLater\":1}"
        let parsed = try Contract.panelList(reply(one, extra: ",\"addedLater\":true"), exit: 0)
        XCTAssertEqual(parsed.loops, [PanelLoop(key: "l0123456789ab", name: "example-api", state: "waiting_for_you", phrase: "Waiting for you", needsYou: true)])
        XCTAssertEqual(try Contract.panelList(reply("", registry: "unavailable", needs: 0), exit: 1).loops, [])
        XCTAssertThrowsError(try Contract.panelList(reply(one, version: 2), exit: 0)) { XCTAssertEqual(String(describing: $0), "unsupported") }
        let invalid: [(Data, Int32)] = [
            (reply(one), 1), (reply("", registry: "unavailable", needs: 0), 0), (reply(one + "," + one), 0),
            (reply(one.replacingOccurrences(of: "l0123456789ab", with: "loop_0123456789abcdef0123456789abcdef")), 0),
            (reply(one.replacingOccurrences(of: "example-api", with: "")), 0), (reply(one.replacingOccurrences(of: "example-api", with: "\\u0007\\u2028")), 0),
            (reply(one, needs: 0, unread: 1), 0),
            (Data("fictional-private-bad-json".utf8), 0), (Data(), 0),
        ]
        for (data, exit) in invalid {
            XCTAssertThrowsError(try Contract.panelList(data, exit: exit)) { error in
                XCTAssertEqual(String(describing: error), "invalidResponse")
            }
        }
    }

    func testNoProgressIsShownOnTheRowAndIsNeverANeed() throws {
        let json = #"{"schemaVersion":1,"registry":"available","complete":true,"allClear":true,"needsYou":0,"unread":0,"omitted":0,"loops":[{"key":"l0123456789ab","name":"example-api","state":"supervising","phrase":"Supervising","needsYou":false,"noProgress":"No progress: 8 messages with no recorded change to its repositories in 3 h."},{"key":"l0123456789ac","name":"example-web","state":"supervising","phrase":"Supervising","needsYou":false}]}"#
        let list = try JSONDecoder().decode(PanelList.self, from: Data(json.utf8))
        XCTAssertEqual(list.loops[0].noProgress, "No progress: 8 messages with no recorded change to its repositories in 3 h.")
        XCTAssertNil(list.loops[1].noProgress)
        let model = MenuBuilder.build(.read(list, at: now), now: now)
        let rows = model.rows.filter { $0.kind == .loop }
        XCTAssertEqual(rows.map { $0.detail }, ["Supervising · no progress", "Supervising"])
        XCTAssertEqual(rows[0].accessibilityLabel, "example-api, Supervising. No progress: 8 messages with no recorded change to its repositories in 3 h.")
        // It is never a need: no badge, and the icon says nothing needs you.
        XCTAssertEqual(model.badge, "")
    }
}
