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
    /// The menu's own version, reported to the CLI with `--client` so the
    /// panel's Versions view can say which build the menu runs. nil when the
    /// menu does not run from an app bundle (`swift run`): nothing is reported.
    public let menuVersion: String?
    public init(executable: URL, directory: String? = nil, panelDirectory: String? = nil, menuVersion: String? = LocalClient.bundleVersion()) {
        self.executable = executable
        self.directory = directory
        self.panelDirectory = panelDirectory
        self.menuVersion = menuVersion.flatMap { LocalClient.validVersion($0) ? $0 : nil }
    }

    /// The app's CFBundleShortVersionString, which make-app.sh sets from the
    /// package's VERSION file.
    public static func bundleVersion(_ bundle: Bundle = .main) -> String? {
        bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
    }

    /// A version the CLI accepts: a short label of letters, digits and . + -.
    public static func validVersion(_ v: String) -> Bool {
        guard let first = v.unicodeScalars.first, v.unicodeScalars.count <= 32,
              CharacterSet.alphanumerics.contains(first) else { return false }
        return v.unicodeScalars.allSatisfy { $0.isASCII && (CharacterSet.alphanumerics.contains($0) || ".+-".unicodeScalars.contains($0)) }
    }

    /// Executables that refused `--client` as an unknown flag (an older CLI),
    /// by path, size and modification time, so a CLI upgraded in place is
    /// given the flag again. A refusal is remembered only when the same call
    /// without the flag then succeeded.
    private static let noClientFlag = NSLock()
    private static var refusedClientFlag = Set<String>()
    static func executableIdentity(_ url: URL) -> String? {
        // The file a symlink points to, so an upgrade that moves the link is noticed.
        let url = url.resolvingSymlinksInPath()
        guard let a = try? FileManager.default.attributesOfItem(atPath: url.path),
              let size = a[.size] as? NSNumber, let modified = a[.modificationDate] as? Date else { return nil }
        return "\(url.path)|\(size)|\(modified.timeIntervalSince1970)"
    }
    private static func refusesClientFlag(_ id: String?) -> Bool {
        guard let id else { return false }
        noClientFlag.lock(); defer { noClientFlag.unlock() }
        return refusedClientFlag.contains(id)
    }
    private static func markRefusesClientFlag(_ id: String?) {
        guard let id else { return }
        noClientFlag.lock(); defer { noClientFlag.unlock() }
        refusedClientFlag.insert(id)
    }
    /// Every loop and its state in the panel's words. The read is bounded by
    /// the CLI at 20 seconds, so this waits a little longer than that.
    ///
    /// The read carries `--client macos-menu@VERSION` so the panel's Versions
    /// view can say which menu runs. A CLI too old to know the flag answers
    /// with a usage error (exit 2) and no output; then the same read runs
    /// without it, and only if that succeeds is the CLI remembered as one
    /// that refuses the flag. A timeout is never retried.
    public func panelList() throws -> PanelList {
        let arguments = MenuBuilder.arguments(.readList)!
        let id = LocalClient.executableIdentity(executable)
        if let menuVersion, !LocalClient.refusesClientFlag(id) {
            let (data, code) = try run(arguments + ["--client", "macos-menu@" + menuVersion], timeout: 25)
            guard code == 2 && data.isEmpty else { return try Contract.panelList(data, exit: code) }
            let (again, againCode) = try run(arguments, timeout: 25)
            let list = try Contract.panelList(again, exit: againCode)
            LocalClient.markRefusesClientFlag(id)
            return list
        }
        let (data, code) = try run(arguments, timeout: 25)
        return try Contract.panelList(data, exit: code)
    }
    /// Whether the panel's background server is running, and when it will
    /// stop by itself. Asking does not start it or keep it alive.
    public func panelStatus() throws -> PanelStatus {
        var arguments = MenuBuilder.arguments(.readStatus)!
        if let panelDirectory { arguments += ["--panel-directory", panelDirectory] }
        let (data, code) = try run(arguments, registry: false)
        return try Contract.panelStatus(data, exit: code)
    }
    /// Opens the panel in the browser through `keepgoing panel`, which starts
    /// its background server when none is running, or stops that server.
    /// Nothing but the fixed arguments and a validated page key is passed.
    public func open(_ command: MenuCommand) throws {
        switch command {
        case .openPanel, .openLoop, .openPanelWithChanges, .stopPanel: break
        default: throw ClientFailure.unavailable
        }
        guard var arguments = MenuBuilder.arguments(command) else { throw ClientFailure.unavailable }
        if let panelDirectory { arguments += ["--panel-directory", panelDirectory] }
        let (_, code) = try run(arguments, timeout: 20, registry: command != .stopPanel)
        guard code == 0 else { throw ClientFailure.unavailable }
    }
    /// The review of one change, read by `keepgoing client review`, which
    /// changes nothing.
    public func review(_ action: LoopAction, key: String) throws -> ReviewReading {
        guard let arguments = MenuBuilder.arguments(.review(action, key: key)) else { throw ClientFailure.invalidResponse }
        let (data, code) = try run(arguments, timeout: 25)
        return try Contract.review(data, exit: code, action: action, key: key)
    }
    /// Makes one confirmed change through `keepgoing client apply`, which
    /// refuses it when the loop's control record is no longer the one
    /// reviewed. It is never run twice: when the reply is lost or cannot be
    /// read, whether it was saved is not known, and the review's own words
    /// say so. A command that could not be started did nothing.
    public func apply(_ command: MenuCommand, review: Review) -> ChangeResult {
        guard case .apply = command, let arguments = MenuBuilder.arguments(command) else {
            return ChangeResult(outcome: "refused", text: LocalClient.notAccepted)
        }
        do {
            let (data, code) = try run(arguments, timeout: 25)
            if let result = try? Contract.applied(data) { return result }
            // A usage error is refused before anything runs: this keepgoing
            // does not take these arguments.
            if code == 2 && data.isEmpty { return ChangeResult(outcome: "refused", text: LocalClient.notAccepted) }
            return ChangeResult(outcome: "uncertain", text: Contract.text(review.lost))
        } catch ClientFailure.unavailable {
            return ChangeResult(outcome: "refused", text: LocalClient.notStarted)
        } catch {
            return ChangeResult(outcome: "uncertain", text: Contract.text(review.lost))
        }
    }
    static let notStarted = "keepgoing could not be started, so nothing was changed. Check the keepgoing executable this menu was started with."
    static let notAccepted = "keepgoing did not accept this request, so nothing ran and nothing was changed. The menu and keepgoing may be different versions."
    /// The review of one change to every loop, by `client review --all`,
    /// which changes nothing.
    public func reviewAll(_ action: LoopAction) throws -> GroupReading {
        guard let arguments = MenuBuilder.arguments(.reviewAll(action)) else { throw ClientFailure.invalidResponse }
        let (data, code) = try run(arguments, timeout: 25)
        return try Contract.groupReview(data, exit: code, action: action)
    }
    /// Makes one confirmed change to every loop reviewed, by `client apply
    /// --all`, once. Each loop is changed only if its record is the one
    /// reviewed; the result names each loop's outcome.
    public func applyAll(_ command: MenuCommand, review: GroupReview) -> ChangeResult {
        guard case .applyAll = command, let arguments = MenuBuilder.arguments(command) else {
            return ChangeResult(outcome: "refused", text: LocalClient.notAccepted)
        }
        do {
            // A stop of every loop saves every stop, then carries out those
            // that can be at once in the time keepgoing gives a change; this
            // waits a little longer than that.
            let (data, code) = try run(arguments, timeout: 120)
            if let result = try? Contract.groupApplied(data) { return result }
            if code == 2 && data.isEmpty { return ChangeResult(outcome: "refused", text: LocalClient.notAccepted) }
            return ChangeResult(outcome: "uncertain", text: Contract.text(review.lost))
        } catch ClientFailure.unavailable {
            return ChangeResult(outcome: "refused", text: LocalClient.notStarted)
        } catch {
            return ChangeResult(outcome: "uncertain", text: Contract.text(review.lost))
        }
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

    /// The environment every command is given, the one the panel gives its
    /// own server: where the user's files are, a fixed search path, and the
    /// locale and time zone. Nothing else reaches it, so the menu acts as no
    /// BB thread (BB_THREAD_ID is never passed) wherever it was started.
    public static func environment(_ from: [String: String]) -> [String: String] {
        var env = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin"]
        for name in ["HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"] {
            if let value = from[name] { env[name] = value }
        }
        return env
    }

    // Only the commands in MenuBuilder.arguments are run. No shell or
    // commands from registry/goal metadata are executed; stderr is never
    // displayed or retained.
    private func run(_ arguments: [String], timeout: TimeInterval = 5, registry: Bool = true) throws -> (Data, Int32) {
        guard executable.isFileURL, executable.path.hasPrefix("/"),
              FileManager.default.isExecutableFile(atPath: executable.path) else { throw ClientFailure.unavailable }
        let process = Process()
        process.executableURL = executable
        // panel status and panel stop take no registry directory.
        process.arguments = arguments + (registry ? (directory.map { ["--directory", $0] } ?? []) : [])
        process.environment = LocalClient.environment(ProcessInfo.processInfo.environment)
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
        // A process ended by a signal has no exit code; terminationStatus is
        // then the signal's number, which must not be read as one.
        return (data, process.terminationReason == .exit ? process.terminationStatus : -1)
    }
}
