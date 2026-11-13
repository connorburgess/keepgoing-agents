import XCTest
@testable import KeepgoingMenuCore

final class ClientTests: XCTestCase {
    func fixture(_ body: String) throws -> (URL, LocalClient) {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        let binary = dir.appendingPathComponent("fictional keepgoing")
        // The first line after the interpreter lets the script be run once,
        // below, without doing anything.
        try ("#!/bin/sh\n[ -z \"${KEEPGOING_TESTEXEC_WARM:-}\" ] || exit 0\n" + body).write(to: binary, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: binary.path)
        try Self.runOnce(binary)
        return (binary, LocalClient(executable: binary, directory: dir.appendingPathComponent("registry with spaces").path))
    }
    /// Runs a new executable once before a test depends on it. macOS checks a
    /// file the first time it is run, and on a busy machine can refuse it: the
    /// process is then ended by a signal as it starts. Such a launch is tried
    /// again; one refused every time is reported as the host's failure, not as
    /// an assertion about the client. See "Executables that tests run" in
    /// docs/testing.md; internal/testexec does the same for the Go tests.
    static func runOnce(_ executable: URL) throws {
        struct EnvironmentFailure: Error, CustomStringConvertible { let description: String }
        let launches = 20
        for _ in 0..<launches {
            let process = Process()
            process.executableURL = executable
            process.environment = ["KEEPGOING_TESTEXEC_WARM": "1"]
            try process.run()
            process.waitUntilExit()
            if process.terminationReason == .exit { return }
        }
        throw EnvironmentFailure(description: "ENVIRONMENT FAILURE: the host ended \(executable.path) by a signal on each of \(launches) launches. Run the tests again.")
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
