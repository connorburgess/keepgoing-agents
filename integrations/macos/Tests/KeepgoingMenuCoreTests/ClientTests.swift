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
    /// The menu reports its version with --client, from its app bundle. An
    /// older CLI that refuses the flag is asked again without it, and is not
    /// given it again.
    func testStatusReportsTheMenuVersionAndFallsBackForAnOlderCLI() throws {
        let id = "loop_0123456789abcdef0123456789abcdef"
        let entry = Registration(id: id, loop: "example-loop")
        let reply = "{\"schemaVersion\":1,\"registrationId\":\"\(id)\",\"readAt\":\"2030-01-01T12:00:00Z\",\"outcome\":\"unavailable\",\"issues\":[]}"
        // A CLI that knows the flag.
        let (binary, base) = try fixture("printf '%s\\n' \"$@\" > \"$0.args\"\nprintf '%s' '\(reply)'\nexit 1\n")
        let client = LocalClient(executable: base.executable, directory: base.directory, menuVersion: "0.1.0")
        _ = try client.status(entry, privateText: false)
        var args = try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(Array(args.prefix(7)), ["client", "status", "--id", id, "--json", "--client", "macos-menu@0.1.0"])
        // An older CLI: a usage error with no output for the flag, then the
        // same read without it.
        let older = "printf '%s\\n' \"$@\" >> \"$0.calls\"\ncase \" $* \" in *\" --client \"*) exit 2;; esac\nprintf '%s' '\(reply)'\nexit 1\n"
        let (oldBinary, oldBase) = try fixture(older)
        let oldClient = LocalClient(executable: oldBase.executable, directory: oldBase.directory, menuVersion: "0.1.0")
        XCTAssertEqual(try oldClient.status(entry, privateText: false).outcome, "unavailable")
        _ = try oldClient.status(entry, privateText: false)
        args = try String(contentsOfFile: oldBinary.path + ".calls", encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertEqual(args.filter { $0 == "--client" }.count, 1, "the flag is tried once, then never again")
        XCTAssertEqual(args.filter { $0 == "status" }.count, 3)
        // Without an app bundle version nothing is reported.
        let plain = LocalClient(executable: base.executable, directory: base.directory, menuVersion: nil)
        _ = try plain.status(entry, privateText: false)
        args = try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init)
        XCTAssertFalse(args.contains("--client"))
    }
    func testOnlyAPlainVersionIsReported() {
        for good in ["0.1.0", "1.2.3-beta.1", "2026.11+local"] { XCTAssertTrue(LocalClient.validVersion(good), good) }
        for bad in ["", "-1", "0.1.0 ; rm", "v\u{0007}", String(repeating: "1", count: 33), "0.1.0\n"] { XCTAssertFalse(LocalClient.validVersion(bad), bad) }
        XCTAssertNil(LocalClient(executable: URL(fileURLWithPath: "/bin/sh"), menuVersion: "bad version").menuVersion)
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
    // A review and its confirmation run only their allowed arguments, and a
    // lost or unreadable reply to a confirmation is uncertain, never retried.
    func testReviewAndApplyPassOnlyAllowedArguments() throws {
        let key = "l0123456789ab", expect = "r1.1893499320." + String(repeating: "c1", count: 32) + "." + String(repeating: "d2", count: 16)
        let review = "{\"schemaVersion\":1,\"preview\":{\"action\":\"stop\",\"key\":\"\(key)\",\"title\":\"Stop example-api?\",\"lines\":[\"Now: Supervising.\"],\"confirm\":\"Stop example-api\",\"expect\":\"\(expect)\",\"expiresAt\":\"2030-01-01T12:02:00Z\",\"lost\":\"fictional lost\",\"refused\":\"fictional refused\",\"expired\":\"fictional expired\"}}"
        let (binary, client) = try fixture("printf '%s\\n' \"$@\" >> \"$0.args\"\ncat \"$0.reply\"\n")
        func args() throws -> [String] { try String(contentsOfFile: binary.path + ".args", encoding: .utf8).split(separator: "\n").map(String.init) }
        try review.write(toFile: binary.path + ".reply", atomically: true, encoding: .utf8)
        guard case .review(let r) = try client.review(.stop, key: key) else { return XCTFail("no review") }
        XCTAssertEqual(Array(try args().prefix(7)), ["client", "review", "stop", "--loop", key, "--json", "--directory"])
        try FileManager.default.removeItem(atPath: binary.path + ".args")
        let command = ConfirmationBuilder.build(r).buttons[1].command!
        try "{\"schemaVersion\":1,\"result\":{\"outcome\":\"done\",\"text\":\"Stop requested: the next check carries it out.\"}}".write(toFile: binary.path + ".reply", atomically: true, encoding: .utf8)
        XCTAssertEqual(client.apply(command, review: r), ChangeResult(outcome: "done", text: "Stop requested: the next check carries it out."))
        XCTAssertEqual(Array(try args().prefix(9)), ["client", "apply", "stop", "--loop", key, "--expect", expect, "--json", "--directory"])
        // Run once per confirmation, whatever the reply.
        try FileManager.default.removeItem(atPath: binary.path + ".args")
        try "fictional-private-bad-json".write(toFile: binary.path + ".reply", atomically: true, encoding: .utf8)
        XCTAssertEqual(client.apply(command, review: r), ChangeResult(outcome: "uncertain", text: "fictional lost"))
        XCTAssertEqual(try args().filter { $0 == "apply" }.count, 1)
        // Only an apply command is run as one.
        try FileManager.default.removeItem(atPath: binary.path + ".args")
        XCTAssertEqual(client.apply(.openPanel, review: r).outcome, "refused")
        XCTAssertEqual(client.apply(.apply(.stop, key: key, expect: "--immediately", lanes: false, immediately: false), review: r).outcome, "refused")
        XCTAssertThrowsError(try client.open(.review(.stop, key: key)))
        XCTAssertFalse(FileManager.default.fileExists(atPath: binary.path + ".args"))
        // A CLI that cannot be started did nothing.
        let missing = LocalClient(executable: URL(fileURLWithPath: "/nonexistent/keepgoing"))
        XCTAssertEqual(missing.apply(command, review: r), ChangeResult(outcome: "refused", text: "keepgoing could not be started, so nothing was changed. Check the keepgoing executable this menu was started with."))
        // A keepgoing that refuses the arguments (exit 2, no reply) ran nothing.
        let (_, usage) = try fixture("exit 2\n")
        XCTAssertEqual(usage.apply(command, review: r), ChangeResult(outcome: "refused", text: "keepgoing did not accept this request, so nothing ran and nothing was changed. The menu and keepgoing may be different versions."))
    }

    // Every command is given the panel server's environment and nothing
    // else: the menu acts as no BB thread wherever it was started.
    func testCommandsGetAScrubbedEnvironment() throws {
        let scrubbed = LocalClient.environment(["HOME": "/private/example", "BB_THREAD_ID": "thr_example0001", "PATH": "/opt/example/bin", "KEEPGOING_REGISTRY_DIR": "/private/other", "LANG": "en_US.UTF-8"])
        XCTAssertEqual(scrubbed, ["HOME": "/private/example", "PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LANG": "en_US.UTF-8"])
        setenv("BB_THREAD_ID", "thr_example0001", 1)
        defer { unsetenv("BB_THREAD_ID") }
        let reply = "{\"schemaVersion\":1,\"readAt\":\"2030-01-01T12:00:00Z\",\"registry\":\"available\",\"complete\":true,\"allClear\":true,\"needsYou\":0,\"unread\":0,\"omitted\":0,\"loops\":[]}"
        let (binary, client) = try fixture("/usr/bin/env > \"$0.env\"\nprintf '%s' '\(reply)'\n")
        XCTAssertEqual(try client.panelList().loops, [])
        let env = try String(contentsOfFile: binary.path + ".env", encoding: .utf8)
        XCTAssertFalse(env.contains("BB_THREAD_ID"))
        XCTAssertTrue(env.contains("PATH=/usr/bin:/bin:/usr/sbin:/sbin"))
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
