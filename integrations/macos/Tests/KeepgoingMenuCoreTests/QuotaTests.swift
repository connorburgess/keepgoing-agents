import XCTest
@testable import KeepgoingMenuCore

final class QuotaTests: XCTestCase {
    let base = "\"schemaVersion\":1,\"registry\":\"available\",\"complete\":true,\"allClear\":true,\"needsYou\":0,\"unread\":0,\"omitted\":0,\"loops\":[]"
    let summary = #"{"state":"reported","text":"Saved selected context; identity not verified.","source":"codex account/rateLimits/read","observedAt":"2030-03-04T12:00:00Z","freshness":"stale","partial":true,"rows":[{"name":"Codex · 5-hour","text":"37% used · 63% remaining","usedPercent":37,"remainingPercent":63,"resetsAt":"2030-03-04T17:00:00Z"},{"name":"Codex · Credits","text":"Balance 12.75 provider units; currency not reported"}],"failedRefreshAt":"2030-03-04T13:00:00Z","failedRefreshText":"Timed out; earlier evidence retains its time."}"#
    func payload(_ q: String) -> Data { Data(("{" + base + ",\"accountQuotas\":{\"state\":\"available\",\"complete\":true,\"omitted\":0,\"accounts\":[{\"label\":\"Example Codex\",\"provider\":\"codex\",\"summary\":" + q + "}]}}").utf8) }
    func testSavedQuotaMenuAndNoNewRefreshAuthority() throws {
        let list = try Contract.panelList(payload(summary), exit: 0)
        let model = MenuBuilder.build(.read(list, at: Date()), now: Date())
        let account = try XCTUnwrap(model.rows.first { $0.title == "Example Codex" })
        XCTAssertTrue(account.detail.contains("37"))
        XCTAssertTrue(account.submenu.contains { $0.accessibilityLabel.contains("currency not reported") })
        XCTAssertTrue(account.submenu.contains { $0.accessibilityLabel.contains("Partial quota evidence") })
        XCTAssertTrue(account.submenu.contains { $0.accessibilityLabel.contains("2030-03-04T17:00:00Z") })
        XCTAssertEqual(account.submenu.compactMap(\.command), [.openPanel])
        XCTAssertEqual(model.badge, "") // quota never changes supervision attention
    }
    func testQuotaDecoderRejectsMalformedEvidence() {
        for bad in [summary.replacingOccurrences(of: "\"usedPercent\":37", with: "\"usedPercent\":101"),
                    summary.replacingOccurrences(of: "\"remainingPercent\":63", with: "\"remainingPercent\":0"),
                    summary.replacingOccurrences(of: "2030-03-04T12:00:00Z", with: "not-a-date"),
                    summary.replacingOccurrences(of: "Codex · Credits", with: "bad\\nname"),
                    summary.replacingOccurrences(of: "codex account/rateLimits/read", with: "unsupported-source")] {
            XCTAssertThrowsError(try Contract.panelList(payload(bad), exit: 0))
        }
        XCTAssertThrowsError(try Contract.panelList(payload(summary).dropLast(), exit: 0))
    }
    func testOldCLIAndUnknownQuotaDoNotBecomeZero() throws {
        let old = try Contract.panelList(Data(("{" + base + "}").utf8), exit: 0)
        XCTAssertNil(old.accountQuotas)
        let no = #"{"state":"unavailable","text":"No supported model-free source.","freshness":"unavailable","rows":[]}"#
        let list = try Contract.panelList(payload(no), exit: 0)
        let account = try XCTUnwrap(MenuBuilder.build(.read(list, at: Date()), now: Date()).rows.first { $0.title == "Example Codex" })
        XCTAssertEqual(account.detail, "Quota unavailable")
        XCTAssertFalse(account.submenu.contains { $0.title.contains("0%") })
    }
    func testQuotaBoundsAndAuthorityLimitsRemainSeparate() throws {
        let long = summary.replacingOccurrences(of: "Codex · Credits", with: String(repeating: "A", count: 161))
        XCTAssertThrowsError(try Contract.panelList(payload(long), exit: 0))
        XCTAssertEqual(Contract.text(String(repeating: "x", count: 200)).count, 180)
        XCTAssertNotNil(Contract.panelAuthority(String(repeating: "x", count: 231)))
    }
    func testClaudeRetrievalNeverBecomesFreshProviderEvidence() throws {
        let q = #"{"state":"reported","text":"Provider freshness unknown; may be cached or seeded.","source":"claude:get_usage","retrievedAt":"2030-03-04T12:00:00Z","freshness":"provider_unknown","rows":[{"name":"5-hour","text":"0% used · 100% remaining","usedPercent":0,"remainingPercent":100}]}"#
        func claude(_ value: String) -> Data {
            Data(String(decoding: payload(value), as: UTF8.self).replacingOccurrences(of: "Example Codex", with: "Example Claude").replacingOccurrences(of: #""provider":"codex""#, with: #""provider":"claude-code""#).utf8)
        }
        let list = try Contract.panelList(claude(q), exit: 0)
        let row = try XCTUnwrap(MenuBuilder.build(.read(list, at: Date()), now: Date()).rows.first { $0.title == "Example Claude" })
        XCTAssertTrue(row.detail.contains("0.0% used")) // Explicit zero only.
        XCTAssertTrue(row.detail.contains("freshness unknown"))
        XCTAssertTrue(row.submenu.contains { $0.accessibilityLabel.contains("Retrieved 2030-03-04T12:00:00Z") && $0.accessibilityLabel.contains("cached or seeded") })
        XCTAssertFalse(row.submenu.contains { $0.accessibilityLabel.contains("saved snapshot") || $0.accessibilityLabel.hasPrefix("Read ") })
        XCTAssertEqual(row.submenu.compactMap(\.command), [.openPanel])
        for bad in [q.replacingOccurrences(of: "provider_unknown", with: "fresh"),
                    q.replacingOccurrences(of: "retrievedAt", with: "observedAt"),
                    q.replacingOccurrences(of: "2030-03-04T12:00:00Z", with: "not-a-date"),
                    q.replacingOccurrences(of: "claude:get_usage", with: "codex account/rateLimits/read"),
                    q.replacingOccurrences(of: #""retrievedAt":"2030-03-04T12:00:00Z",""#, with: "")] {
            XCTAssertThrowsError(try Contract.panelList(claude(bad), exit: 0))
        }
        XCTAssertThrowsError(try Contract.panelList(payload(q), exit: 0)) // Wrong provider.
        let mixedTimes = q.replacingOccurrences(of: #""freshness":"provider_unknown""#, with: #""observedAt":"2030-03-04T11:00:00Z","freshness":"provider_unknown""#)
        XCTAssertThrowsError(try Contract.panelList(claude(mixedTimes), exit: 0))
    }
    func testConnectOpensOnlyTheSelectedAccountAndOldCLIRemainsReadable() throws {
        let id = String(repeating: "a", count: 32)
        let no = #"{"state":"unavailable","text":"Usage could not be read.","freshness":"unavailable","rows":[]}"#
        let raw = String(decoding: payload(no), as: UTF8.self).replacingOccurrences(of: #""label":"Example Codex""#, with: #""id":""# + id + #"","label":"Example Codex""#)
        let list = try Contract.panelList(Data(raw.utf8), exit: 0)
        let row = try XCTUnwrap(MenuBuilder.build(.read(list, at: Date()), now: Date()).rows.first { $0.title == "Example Codex" })
        XCTAssertEqual(row.detail, "Connect Codex")
        XCTAssertEqual(row.command, .connectAccount(id: id))
        XCTAssertEqual(MenuBuilder.arguments(.openAccount(id: id)), ["panel", "--account", id])
        XCTAssertNil(MenuBuilder.arguments(.openAccount(id: "../other")))
        XCTAssertThrowsError(try Contract.panelList(Data(raw.replacingOccurrences(of: id, with: "../other").utf8), exit: 0))
        XCTAssertEqual(list.accountQuotas?.accounts.first?.summary.state, "unavailable") // Connect is an action, not an auth diagnosis.
    }

}
