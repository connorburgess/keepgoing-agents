import XCTest
@testable import KeepgoingMenuCore

final class ClientTests: XCTestCase {
    func fixture(_ body: String) throws -> (URL, LocalClient) {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let binary = dir.appendingPathComponent("fictional keepgoing")
        try ("#!/bin/sh\n" + body).write(to: binary, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        return (binary, LocalClient(executable: binary, directory: dir.appendingPathComponent("registry with spaces").path))
    }
    func testOnlyReadArgumentsAndNoShellExpansion() throws {
        let (binary, client) = try fixture("printf '%s\\n' \"$@\" > \"$0.args\"\nprintf '%s' '{\"schemaVersion\":1,\"operation\":\"list\",\"entries\":[]}'\n")
        XCTAssertEqual(try client.registry(), [])
        let args = try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(Array(args.prefix(4)), ["registry", "list", "--json", "--directory"])
        XCTAssertEqual(args.count, 5); XCTAssertTrue(args[4].hasSuffix("registry with spaces"))
    }
    func testStatusPrivateFlagIsExplicit() throws {
        let id = "loop_0123456789abcdef0123456789abcdef"
        let entry = Registration(id: id, loop: "example-loop")
        let body = "printf '%s\\n' \"$@\" > \"$0.args\"\n" +
            "printf '%s' '{\"schemaVersion\":1,\"registrationId\":\"\(id)\",\"readAt\":\"2030-01-01T12:00:00Z\",\"outcome\":\"unavailable\",\"issues\":[]}'\nexit 1\n"
        let (binary, client) = try fixture(body)
        for privateText in [false, true] {
            _ = try client.status(entry, privateText: privateText)
            let args = try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init)
            XCTAssertEqual(Array(args.prefix(5)), ["client", "status", "--id", id, "--json"])
            XCTAssertEqual(args.contains("--private"), privateText)
        }
    }
    func testPanelCommandsPassOnlyFixedArgumentsAndAValidatedKey() throws {
        let reply = "{\"schemaVersion\":1,\"readAt\":\"2030-01-01T12:00:00Z\",\"registry\":\"available\",\"complete\":true,\"allClear\":true,\"needsYou\":0,\"unread\":0,\"omitted\":0,\"loops\":[]}"
        let (binary, base) = try fixture("printf '%s\\n' \"$@\" > \"$0.args\"\nprintf '%s' '\(reply)'\n")
        let client = LocalClient(executable: base.executable, directory: base.directory, panelDirectory: "/private/example panel")
        func args() throws -> [String] { try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init) }
        XCTAssertEqual(try client.panelList().loops, [])
        XCTAssertEqual(Array(try args().prefix(4)), ["panel", "list", "--json", "--directory"])
        XCTAssertFalse(try args().contains("--panel-directory"), "a list starts no server and needs no panel directory")
        try client.open(.openPanel)
        XCTAssertEqual(Array(try args().prefix(4)), ["panel", "--panel-directory", "/private/example panel", "--directory"])
        XCTAssertEqual(try args().count, 5)
        try client.open(.openLoop(key: "l0123456789ab"))
        XCTAssertEqual(Array(try args().prefix(3)), ["panel", "--loop", "l0123456789ab"])
        // Stop and status go to the panel's own directory and take no registry.
        try client.open(.stopPanel)
        XCTAssertEqual(try args(), ["panel", "stop", "--panel-directory", "/private/example panel"])
        _ = try? client.panelStatus()
        XCTAssertEqual(try args(), ["panel", "status", "--json", "--panel-directory", "/private/example panel"])
        // Nothing runs for a key that is not a page key, or for quit.
        try FileManager.default.removeItem(atPath: binary.path + ".args")
        for command in [MenuCommand.openLoop(key: "example; rm -rf /"), .openLoop(key: "--no-open"), .quit] {
            XCTAssertThrowsError(try client.open(command))
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: binary.path + ".args"))
    }
    func testAFailedOpenIsReportedAndNeverShowsOutput() throws {
        let (_, client) = try fixture("printf 'fictional-private-error' >&2\nexit 1\n")
        XCTAssertThrowsError(try client.open(.openPanel)) { error in
            XCTAssertFalse(String(describing: error).contains("fictional-private"))
        }
    }
    func testMalformedAndFailedRepliesAreNotEmptyRegistries() throws {
        for body in ["printf 'fictional-private-bad-json'", "printf '%s' '{\"schemaVersion\":1,\"operation\":\"list\",\"entries\":[]}'; exit 1"] {
            let (_, client) = try fixture(body)
            XCTAssertThrowsError(try client.registry()) { error in
                XCTAssertFalse(String(describing: error).contains("fictional-private"))
            }
        }
    }
    func testLargeOutputIsBounded() throws {
        for redirect in ["", " >&2"] {
            let (_, client) = try fixture("exec /usr/bin/yes fictional-output" + redirect)
            XCTAssertThrowsError(try client.registry()) { error in
                XCTAssertEqual(String(describing: error), "outputLimit")
            }
        }
    }
    func testStalledReadTerminatesWithoutProviderWork() throws {
        let (_, client) = try fixture("exec /bin/sleep 30")
        let start = Date()
        XCTAssertThrowsError(try client.registry()) { error in
            XCTAssertEqual(String(describing: error), "timedOut")
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 10)
    }
}
