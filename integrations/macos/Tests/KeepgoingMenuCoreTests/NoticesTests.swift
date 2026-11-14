import XCTest
@testable import KeepgoingMenuCore

final class NoticesTests: XCTestCase {
    let at = Date(timeIntervalSince1970: 1_900_000_000)

    func loop(_ index: Int, _ name: String, overdueSince: String? = nil) -> PanelLoop {
        PanelLoop(key: String(format: "l%012x", index), name: name, state: overdueSince == nil ? "supervising" : "waiting_for_you",
                  phrase: overdueSince == nil ? "Supervising" : "Waiting for you", needsYou: overdueSince != nil, overdueSince: overdueSince)
    }
    func read(_ loops: [PanelLoop], complete: Bool = true) -> MenuReading {
        let needs = loops.filter(\.needsYou).count
        return .read(PanelList(schemaVersion: 1, registry: "available", complete: complete, allClear: complete && needs == 0, needsYou: needs, unread: 0, omitted: 0, loops: loops), at: at)
    }
    func stamp(_ s: String) -> String { "at " + s }
    func format(_ d: Date) -> String { ISO8601DateFormatter().string(from: d) }

    func testOneNotificationPerGap() {
        var rule = OverdueNotices()
        let gap = "2030-01-01T09:00:00Z"
        let first = rule.next(read([loop(1, "example-api", overdueSince: gap), loop(2, "example-docs")]), format: format)
        XCTAssertEqual(first, [OverdueNotice(identifier: "keepgoing.overdue.l000000000001.1893488400", title: "Checks have stopped: example-api",
                                             body: "No check has run since 2030-01-01T09:00:00Z. The computer may have slept, or the scheduler that runs this loop may have stopped. Open the keepgoing panel to see what to do.")])
        // The next reads of the same gap, and a failed read, tell nothing.
        XCTAssertEqual(rule.next(read([loop(1, "example-api", overdueSince: gap), loop(2, "example-docs")]), format: format), [])
        XCTAssertEqual(rule.next(.unavailable, format: format), [])
        XCTAssertEqual(rule.next(read([loop(1, "example-api", overdueSince: gap)]), format: format), [])
        // Checks ran again, then stopped again: a new gap is told.
        XCTAssertEqual(rule.next(read([loop(1, "example-api")]), format: format), [])
        let second = rule.next(read([loop(1, "example-api", overdueSince: "2030-01-02T09:00:00Z")]), format: format)
        XCTAssertEqual(second.map(\.identifier), ["keepgoing.overdue.l000000000001.1893574800"])
    }

    func testALoopLeftOutOfACutOffListKeepsWhatWasTold() {
        var rule = OverdueNotices()
        let gap = "2030-01-01T09:00:00Z"
        XCTAssertEqual(rule.next(read([loop(1, "example-api", overdueSince: gap)]), format: format).count, 1)
        XCTAssertEqual(rule.next(read([loop(2, "example-docs")], complete: false), format: format), [])
        XCTAssertEqual(rule.next(read([loop(1, "example-api", overdueSince: gap)]), format: format), [])
    }

    func testManyGapsAtOnceAreOneNotification() {
        var rule = OverdueNotices()
        let loops = (1...5).map { loop($0, "example-\($0)", overdueSince: "2030-01-01T09:00:00Z") }
        let told = rule.next(read(loops), format: format)
        XCTAssertEqual(told.count, 1)
        XCTAssertEqual(told.first?.title, "Checks have stopped for 5 loops")
        XCTAssertEqual(told.first?.body, "example-1, example-2 and 3 more have had no check for longer than usual. The computer may have slept, or their schedulers may have stopped. Open the keepgoing panel to see what to do.")
        XCTAssertEqual(rule.next(read(loops), format: format), [])
    }

    func testTheTextIsTheLoopsNameAndNothingElse() {
        var rule = OverdueNotices()
        let long = "example-a-very-long-loop-name-that-goes-on\u{0007}"
        let told = rule.next(read([loop(1, long, overdueSince: "2030-01-01T09:00:00Z")]), format: format)
        XCTAssertEqual(told.first?.title, "Checks have stopped: example-a-very…-that-goes-on")
        XCTAssertFalse(told.first?.body.contains("\u{0007}") ?? true)
    }

    func testAnUnreadableTimeIsNotAGap() {
        var rule = OverdueNotices()
        XCTAssertEqual(rule.next(read([loop(1, "example-api", overdueSince: "not a time")]), format: format), [])
    }

    func testTheListDecodesTheField() throws {
        let data = Data(#"{"schemaVersion":1,"registry":"available","complete":true,"allClear":false,"needsYou":1,"unread":0,"omitted":0,"loops":[{"key":"l0123456789ab","name":"example-api","state":"waiting_for_you","phrase":"Waiting for you","needsYou":true,"overdueSince":"2030-01-01T09:00:00Z"}]}"#.utf8)
        XCTAssertEqual(try Contract.panelList(data, exit: 0).loops.first?.overdueSince, "2030-01-01T09:00:00Z")
    }
}
