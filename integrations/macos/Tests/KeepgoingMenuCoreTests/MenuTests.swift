import XCTest
@testable import KeepgoingMenuCore

final class MenuTests: XCTestCase {
    let now = Date(timeIntervalSince1970: 1_900_000_000)

    func loop(_ index: Int, _ name: String, _ state: String, _ phrase: String, needsYou: Bool = false) -> PanelLoop {
        PanelLoop(key: String(format: "l%012x", index), name: name, state: state, phrase: phrase, needsYou: needsYou)
    }
    func list(_ loops: [PanelLoop], needsYou: Int = 0, unread: Int = 0, complete: Bool = true, registry: String = "available", omitted: Int = 0) -> PanelList {
        PanelList(schemaVersion: 1, registry: registry, complete: complete, allClear: complete && needsYou == 0, needsYou: needsYou, unread: unread, omitted: omitted, loops: loops)
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
            "Open keepgoing panel",
            "1 loop needs you",
            "---",
            "example-api | Waiting for you",
            "example-docs | Supervising",
            "example-site | Paused",
            "---",
            "Refresh",
            "Quit keepgoing menu",
            "---",
            "Read 2 min ago",
        ])
        XCTAssertEqual(model.badge, "1")
        XCTAssertEqual(model.iconAccessibilityLabel, "keepgoing, 1 loop needs you")
        XCTAssertEqual(model.rows.map(\.command), [
            .openPanel, .openPanel, nil,
            .openLoop(key: "l000000000001"), .openLoop(key: "l000000000002"), .openLoop(key: "l000000000003"),
            nil, .refresh, .quit, nil, nil,
        ])
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
            }
        }
    }

    func testCommandsRunOnlyThePanelCommand() {
        XCTAssertEqual(MenuBuilder.arguments(.openPanel), ["panel"])
        XCTAssertEqual(MenuBuilder.arguments(.openLoop(key: "l0123456789ab")), ["panel", "--loop", "l0123456789ab"])
        XCTAssertNil(MenuBuilder.arguments(.refresh))
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
        XCTAssertEqual(text(model(1, 1))[1], "1 loop could not be read")
        XCTAssertEqual(text(model(3, 2))[1], "1 loop needs you, 2 could not be read")
        XCTAssertEqual(model(3, 2).badge, "3")
        XCTAssertEqual(model(250, 0).badge, "99+")
        // Nothing is claimed before a read, or when the read failed.
        for reading in [MenuReading.notRead, .reading, .unsupported, .unavailable] {
            XCTAssertEqual(MenuBuilder.build(reading, now: now).badge, "")
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
        XCTAssertEqual(last(.read(list(three.loops, omitted: 4), at: now)), "4 more loops are not shown. Read just now.")
        XCTAssertEqual(last(.unsupported), "This keepgoing is a different version from the menu. Open the panel instead.")
        XCTAssertEqual(last(.unavailable), "keepgoing could not be read. Open the panel, or run keepgoing doctor.")
        XCTAssertEqual(last(.read(three, at: now), refreshing: true), "Reading…")
        XCTAssertEqual(last(.read(three, at: now), notice: "The panel could not be opened. Run keepgoing panel in a terminal."), "The panel could not be opened. Run keepgoing panel in a terminal.")
        // While a read is under way the last read's rows stay in place.
        XCTAssertEqual(MenuBuilder.build(.read(three, at: now), now: now, refreshing: true).rows.filter { $0.kind == .loop }.count, 3)
    }

    func testLongNamesHundredLoopsAndUnknownStates() {
        let long = String(repeating: "very-long-fictional-loop-name-", count: 6)
        let hostile = "example\u{0007}-loop\nsecond line"
        var loops = [loop(1, long, "supervising", "Supervising"), loop(2, hostile, "a_state_from_a_newer_build", "Something new")]
        for i in 3...100 { loops.append(loop(i, "example-loop-\(i)", "off", "Off")) }
        let model = MenuBuilder.build(.read(list(loops), at: now), now: now)
        let rows = model.rows.filter { $0.kind == .loop }
        XCTAssertEqual(rows.count, 100)
        XCTAssertEqual(rows[0].title.count, MenuBuilder.maxName)
        XCTAssertTrue(rows[0].title.hasSuffix("…"))
        XCTAssertEqual(rows[0].toolTip, long, "the full name is still reachable")
        XCTAssertTrue(rows[0].accessibilityLabel.hasPrefix(long + ", Supervising."))
        XCTAssertEqual(rows[1].title, "example-loopsecond line", "control characters never reach the menu")
        XCTAssertEqual(rows[1].symbol, "questionmark.circle", "a state this build does not know gets the unknown symbol")
        XCTAssertEqual(rows[1].detail, "Something new", "and still shows the phrase the CLI gave")
        XCTAssertEqual(rows[2].toolTip, "")
        XCTAssertTrue(rows.allSatisfy { $0.isEnabled && !$0.symbol.isEmpty })
        // Every state the panel has gets its own symbol.
        for state in ["waiting_for_you", "unknown", "checks_failing", "not_checked_recently", "waiting_on_limit", "supervising", "outside_hours", "paused", "off", "stopped", "finished"] {
            XCTAssertNotNil(MenuBuilder.symbols[state], state)
        }
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
            (reply(one.replacingOccurrences(of: "example-api", with: "")), 0), (reply(one, needs: 0, unread: 1), 0),
            (Data("fictional-private-bad-json".utf8), 0), (Data(), 0),
        ]
        for (data, exit) in invalid {
            XCTAssertThrowsError(try Contract.panelList(data, exit: exit)) { error in
                XCTAssertEqual(String(describing: error), "invalidResponse")
            }
        }
    }
}
