import Foundation
import Darwin

private final class Capture {
    private let lock = NSLock()
    private var data = Data()
    private var bytes = 0
    private var overflow = false
    private let retain: Bool
    init(retain: Bool) { self.retain = retain }
    func append(_ chunk: Data) -> Bool {
        lock.lock(); defer { lock.unlock() }
        bytes += chunk.count
        if bytes > 1_048_576 { overflow = true; return false }
        if retain { data.append(chunk) }
        return true
    }
    func result() -> (Data, Bool) {
        lock.lock(); defer { lock.unlock() }
        return (data, overflow)
    }
}

public struct LocalClient {
    public let executable: URL
    public let directory: String?
    /// A non-default private directory for the panel's runtime files.
    public let panelDirectory: String?
    public init(executable: URL, directory: String? = nil, panelDirectory: String? = nil) {
        self.executable = executable
        self.directory = directory
        self.panelDirectory = panelDirectory
    }
    /// Every loop and its state in the panel's words. The read is bounded by
    /// the CLI at 20 seconds, so this waits a little longer than that.
    public func panelList() throws -> PanelList {
        let (data, code) = try run(["panel", "list", "--json"], timeout: 25)
        return try Contract.panelList(data, exit: code)
    }
    /// Whether the panel's background server is running, and when it will
    /// stop by itself. Asking does not start it or keep it alive.
    public func panelStatus() throws -> PanelStatus {
        var arguments = ["panel", "status", "--json"]
        if let panelDirectory { arguments += ["--panel-directory", panelDirectory] }
        let (data, code) = try run(arguments, registry: false)
        return try Contract.panelStatus(data, exit: code)
    }
    /// Opens the panel in the browser through `keepgoing panel`, which starts
    /// its background server when none is running, or stops that server.
    /// Nothing but the fixed arguments and a validated page key is passed.
    public func open(_ command: MenuCommand) throws {
        guard var arguments = MenuBuilder.arguments(command) else { throw ClientFailure.unavailable }
        if let panelDirectory { arguments += ["--panel-directory", panelDirectory] }
        let (_, code) = try run(arguments, timeout: 20, registry: command != .stopPanel)
        guard code == 0 else { throw ClientFailure.unavailable }
    }
    public func registry() throws -> [Registration] {
        let (data, code) = try run(["registry", "list", "--json"])
        return try Contract.registry(data, exit: code)
    }
    public func status(_ entry: Registration, privateText: Bool) throws -> Status {
        guard Contract.validID(entry.id) else { throw ClientFailure.invalidResponse }
        var args = ["client", "status", "--id", entry.id, "--json"]
        if privateText { args.append("--private") }
        let (data, code) = try run(args)
        return try Contract.status(data, exit: code, entry: entry)
    }

    // Only the read operations and the panel opener above are exposed. No shell
    // or commands from registry/goal metadata are executed; stderr is never
    // displayed or retained.
    private func run(_ arguments: [String], timeout: TimeInterval = 5, registry: Bool = true) throws -> (Data, Int32) {
        guard executable.isFileURL, executable.path.hasPrefix("/"),
              FileManager.default.isExecutableFile(atPath: executable.path) else { throw ClientFailure.unavailable }
        let process = Process()
        process.executableURL = executable
        // panel status and panel stop take no registry directory.
        process.arguments = arguments + (registry ? (directory.map { ["--directory", $0] } ?? []) : [])
        let output = Pipe(), errors = Pipe()
        process.standardOutput = output
        process.standardError = errors
        process.standardInput = FileHandle.nullDevice
        let stdout = Capture(retain: true), stderr = Capture(retain: false)
        let finished = DispatchSemaphore(value: 0)
        let drained = DispatchGroup()
        for (pipe, capture) in [(output, stdout), (errors, stderr)] {
            drained.enter()
            pipe.fileHandleForReading.readabilityHandler = { handle in
                let chunk = (try? handle.read(upToCount: 8192)) ?? Data()
                if chunk.isEmpty {
                    handle.readabilityHandler = nil
                    drained.leave()
                } else if !capture.append(chunk), process.isRunning {
                    kill(process.processIdentifier, SIGKILL)
                }
            }
        }
        defer {
            output.fileHandleForReading.readabilityHandler = nil
            errors.fileHandleForReading.readabilityHandler = nil
            try? output.fileHandleForReading.close()
            try? errors.fileHandleForReading.close()
        }
        process.terminationHandler = { _ in finished.signal() }
        do { try process.run() } catch { throw ClientFailure.unavailable }
        let deadline = DispatchTime.now() + timeout
        if finished.wait(timeout: deadline) != .success {
            if process.isRunning { kill(process.processIdentifier, SIGKILL) }
            _ = finished.wait(timeout: .now() + 1)
            _ = drained.wait(timeout: .now() + 1)
            throw ClientFailure.timedOut
        }
        guard drained.wait(timeout: deadline) == .success else { throw ClientFailure.timedOut }
        let (data, tooLarge) = stdout.result()
        guard !tooLarge, !stderr.result().1 else { throw ClientFailure.outputLimit }
        return (data, process.terminationStatus)
    }
}
