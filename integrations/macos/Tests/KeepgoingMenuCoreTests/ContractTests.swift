import XCTest
@testable import KeepgoingMenuCore

final class ContractTests: XCTestCase {
    let id = "loop_0123456789abcdef0123456789abcdef"
    var entry: Registration { Registration(id: id, loop: "example-loop") }
    func data(_ value: Any) -> Data { try! JSONSerialization.data(withJSONObject: value) }
    func registry(_ entries: [[String: String]]) -> Data {
        data(["schemaVersion": 1, "operation": "list", "entries": entries])
    }
    func report() -> [String: Any] {
        ["schemaVersion": 1, "registrationId": id, "readAt": "2030-01-01T12:00:00Z", "outcome": "available", "issues": [], "snapshot": [
            "loop": "example-loop", "controls": ["enabled": true, "paused": false, "stop": false, "awaitingUser": false, "completed": false], "completionEvidence": "declaration_only",
            "target": ["alias": "primary", "adapter": "bb", "identityEvidence": "configuration_only", "bbThread": "thr_example"],
            "runtime": ["state": "recorded_active", "source": "last_check", "freshness": "recent", "binaryComparison": "match", "checkedAt": "2030-01-01T11:59:00.123Z"],
            "lastCheck": ["action": "none", "outcome": "succeeded", "dryRun": false],
            "goalState": "recorded", "goal": ["id": "goal_example", "revision": 2, "state": "recorded"],
            "privateGoal": ["title": "Fictional private goal", "nextAction": "Fictional private next action"],
            "daemon": ["state": "responsive", "binaryComparison": "match", "phase": "waiting", "dryRun": false, "observedAt": "2030-01-01T12:00:00Z", "nextCheckAt": "2030-01-01T12:05:00Z"], "externalScheduler": "not_inspected"
        ]]
    }
    func testRegistryRejectsFailureAndBadBindingsButAllowsAdditiveFields() throws {
        let good = registry([["id": id, "loop": "example-loop", "futureField": "ignored"]])
        XCTAssertEqual(try Contract.registry(good, exit: 0), [entry])
        XCTAssertThrowsError(try Contract.registry(good, exit: 1))
        XCTAssertThrowsError(try Contract.registry(registry([["id": id, "loop": "a"], ["id": id, "loop": "b"]]), exit: 0))
        XCTAssertThrowsError(try Contract.registry(registry([["id": "--stop", "loop": "a"]]), exit: 0))
        XCTAssertEqual(try Contract.registry(registry([]), exit: 0), [])
        XCTAssertFalse(Contract.validID(id + "\n"))
        XCTAssertThrowsError(try Contract.registry(data(["schemaVersion": 2, "operation": "list", "entries": []]), exit: 0))
    }
    func testPrivateMetadataRequiresExplicitDisplay() throws {
        let status = try Contract.status(data(report()), exit: 0, entry: entry)
        let hidden = Contract.lines(status, privateText: false).joined(separator: "\n")
        XCTAssertFalse(hidden.contains("Fictional private"))
        XCTAssertTrue(hidden.contains("active at last check"))
        XCTAssertTrue(hidden.contains("Freshness when read: recent"))
        XCTAssertTrue(hidden.contains("Recorded build: match"))
        XCTAssertTrue(hidden.contains("does not prove agent health"))
        XCTAssertTrue(hidden.contains("Stage: unavailable"))
        XCTAssertTrue(Contract.lines(status, privateText: true).joined().contains("Fictional private goal"))
    }
    func testExitSchemaIdentityAndSnapshotMismatchAreRejected() {
        for mode in ["exit", "schema", "identity", "loop", "readAt", "outcome", "missing snapshot"] {
            var r = report()
            switch mode {
            case "schema": r["schemaVersion"] = 2
            case "identity": r["registrationId"] = "loop_ffffffffffffffffffffffffffffffff"
            case "loop": var s = r["snapshot"] as! [String: Any]; s["loop"] = "other-loop"; r["snapshot"] = s
            case "readAt": r["readAt"] = "not a date"
            case "outcome": r["outcome"] = "healthy"
            case "missing snapshot": r.removeValue(forKey: "snapshot")
            default: break
            }
            XCTAssertThrowsError(try Contract.status(data(r), exit: mode == "exit" ? 1 : 0, entry: entry), mode)
        }
    }
    func testPartialAndUnavailablePreserveUncertainty() throws {
        var r = report()
        r["outcome"] = "partial"; r["issues"] = ["control_unavailable", "new_issue"]
        var s = r["snapshot"] as! [String: Any]
        s.removeValue(forKey: "controls"); s.removeValue(forKey: "runtime")
        s["daemon"] = ["state": "not_recorded", "binaryComparison": "unknown", "dryRun": false]
        r["snapshot"] = s
        let lines = Contract.lines(try Contract.status(data(r), exit: 1, entry: entry), privateText: false).joined(separator: "\n")
        XCTAssertTrue(lines.contains("Evidence: partial")); XCTAssertTrue(lines.contains("Controls: unavailable"))
        XCTAssertTrue(lines.contains("Next normal check: unknown")); XCTAssertFalse(lines.contains("Supervision: enabled"))
        r["outcome"] = "unavailable"; r.removeValue(forKey: "snapshot")
        XCTAssertTrue(Contract.lines(try Contract.status(data(r), exit: 1, entry: entry), privateText: false).contains("Loop status unavailable"))
        XCTAssertThrowsError(try Contract.status(data(r), exit: 0, entry: entry))
    }
    func testUnrecognizedStateAndControlCharactersStayInert() throws {
        var r = report(); var s = r["snapshot"] as! [String: Any]
        s["runtime"] = ["state": "fictional-private-provider-output", "source": "unknown", "freshness": "new", "binaryComparison": "new"]
        r["snapshot"] = s
        let lines = Contract.lines(try Contract.status(data(r), exit: 0, entry: entry), privateText: false).joined()
        XCTAssertTrue(lines.contains("Loop status: unknown")); XCTAssertFalse(lines.contains("fictional-private-provider-output"))
        XCTAssertEqual(Contract.text("one\n\t\u{1b}two"), "onetwo")
        XCTAssertLessThanOrEqual(Contract.text(String(repeating: "a", count: 500)).count, 180)
        // A cut ends in an ellipsis; a text that fits is whole.
        XCTAssertEqual(Contract.text(String(repeating: "a", count: 181)), String(repeating: "a", count: 179) + "…")
        XCTAssertEqual(Contract.text(String(repeating: "a", count: 180)), String(repeating: "a", count: 180))
    }
}
