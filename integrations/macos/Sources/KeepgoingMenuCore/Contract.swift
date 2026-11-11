import Foundation

public enum ClientFailure: Error { case unavailable, unsupported, invalidResponse, timedOut, outputLimit }

public struct Registration: Decodable, Equatable {
    public let id: String
    public let loop: String
}

private struct Registry: Decodable {
    let schemaVersion: Int
    let operation: String
    let entries: [Registration]
}

public struct Status: Decodable {
    let schemaVersion: Int
    let registrationId: String
    let readAt: String
    let outcome: String
    let issues: [String]
    fileprivate let snapshot: Snapshot?
}

private struct Snapshot: Decodable {
    let loop: String
    let controls: Controls?
    let completionEvidence: String
    let target: Target?
    let runtime: Runtime?
    let lastCheck: Check?
    let goalState: String
    let goal: Goal?
    let privateGoal: PrivateGoal?
    let daemon: Daemon
    let externalScheduler: String
}
private struct Controls: Decodable {
    let enabled, paused, stop, awaitingUser, completed: Bool
}
private struct Target: Decodable { let alias, adapter, identityEvidence: String; let bbThread: String? }
private struct Runtime: Decodable { let state, source, freshness, binaryComparison: String; let checkedAt: String? }
private struct Check: Decodable { let at: String?; let action, outcome: String; let dryRun: Bool }
private struct Goal: Decodable { let id: String; let revision: UInt64; let state: String }
private struct PrivateGoal: Decodable { let title: String; let nextAction: String? }
private struct Daemon: Decodable {
    let state, binaryComparison: String
    let phase, observedAt, nextCheckAt: String?
    let dryRun: Bool
}

public enum Contract {
    public static func validID(_ id: String) -> Bool {
        id.count == 37 && id.range(of: "^loop_[0-9a-f]{32}$", options: .regularExpression) != nil
    }

    public static func registry(_ data: Data, exit: Int32) throws -> [Registration] {
        guard exit == 0, data.count <= 1_048_576 else { throw ClientFailure.unavailable }
        let result: Registry
        do { result = try JSONDecoder().decode(Registry.self, from: data) }
        catch { throw ClientFailure.invalidResponse }
        guard result.schemaVersion == 1 else { throw ClientFailure.unsupported }
        guard result.operation == "list", result.entries.count <= 128,
              Set(result.entries.map(\.id)).count == result.entries.count,
              result.entries.allSatisfy({ validID($0.id) && !$0.loop.isEmpty }) else {
            throw ClientFailure.invalidResponse
        }
        return result.entries
    }

    public static func status(_ data: Data, exit: Int32, entry: Registration) throws -> Status {
        guard data.count <= 1_048_576 else { throw ClientFailure.outputLimit }
        let result: Status
        do { result = try JSONDecoder().decode(Status.self, from: data) }
        catch { throw ClientFailure.invalidResponse }
        guard result.schemaVersion == 1 else { throw ClientFailure.unsupported }
        guard result.registrationId == entry.id, date(result.readAt) != nil,
              (result.outcome == "available" && exit == 0 && result.snapshot != nil) ||
              (result.outcome == "partial" && exit == 1 && result.snapshot != nil) ||
              (result.outcome == "unavailable" && exit == 1 && result.snapshot == nil),
              result.snapshot == nil || result.snapshot?.loop == entry.loop else {
            throw ClientFailure.invalidResponse
        }
        return result
    }

    // Metadata is always inert text. Keep control characters out of native menus.
    public static func text(_ value: String) -> String {
        String(String.UnicodeScalarView(value.unicodeScalars.filter { !CharacterSet.controlCharacters.contains($0) })).prefix(180).description
    }

    static func date(_ value: String?) -> Date? {
        guard let value else { return nil }
        let parser = ISO8601DateFormatter()
        parser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = parser.date(from: value) { return d }
        parser.formatOptions = [.withInternetDateTime]
        return parser.date(from: value)
    }
    static func stamp(_ value: String?) -> String {
        guard let d = date(value) else { return "unavailable" }
        let formatter = ISO8601DateFormatter()
        return formatter.string(from: d)
    }
    static func known(_ value: String, _ choices: [String]) -> String {
        choices.contains(value) ? value : "unknown"
    }

    public static func lines(_ report: Status, privateText: Bool) -> [String] {
        var lines = ["Evidence: \(report.outcome)", "Read: \(stamp(report.readAt))"]
        if !report.issues.isEmpty { lines.append("Some evidence is unavailable (\(report.issues.count) issues)") }
        guard let s = report.snapshot else { return lines + ["Loop status unavailable"] }
        if let c = s.controls {
            lines.append("Supervision: \(c.enabled ? "enabled" : "disabled")")
            var holds: [String] = []
            if c.stop { holds.append("stop requested") }
            if c.paused { holds.append("paused") }
            if c.awaitingUser { holds.append("awaiting user") }
            if c.completed { holds.append("completion declared") }
            lines.append("Holds: \(holds.isEmpty ? "none recorded" : holds.joined(separator: ", "))")
            if c.completed { lines.append("Completion is a declaration, not verification") }
        } else { lines.append("Controls: unavailable") }
        if let t = s.target {
            lines.append("Configured target: \(text(t.alias)) (\(text(t.adapter)))")
            if let thread = t.bbThread { lines.append("Configured thread: \(text(thread))") }
        } else { lines.append("Configured target: unavailable") }
        if let r = s.runtime {
            let states = ["recorded_active": "active at last check", "recorded_idle": "idle at last check", "recorded_error": "error at last check", "recorded_pending": "pending at last check", "recorded_stopping": "stopping at last check", "recorded_awaiting_input": "awaiting input at last check", "stop_requested": "stop requested", "disabled": "disabled", "paused": "paused", "awaiting_user": "awaiting user", "completed": "completion declared", "outside_window": "outside operating window", "check_failed": "last check failed", "dry_run": "dry run only", "not_observed": "target not observed"]
            lines.append("Loop status: \(states[r.state] ?? "unknown")")
            lines.append("Freshness when read: \(known(r.freshness, ["recent", "stale", "never", "unknown", "future"]))")
            lines.append("Checked: \(stamp(r.checkedAt))")
            lines.append("Recorded build: \(known(r.binaryComparison, ["match", "different", "unknown"]))")
        } else { lines.append("Recorded activity: unavailable") }
        if let check = s.lastCheck {
            lines.append("Last action: \(known(check.action, ["none", "wake", "nudge", "reprompt", "await_user", "stop", "handoff", "switch_back"])) / \(known(check.outcome, ["succeeded", "failed", "skipped", "dry_run", "unknown"]))\(check.dryRun ? " (dry run)" : "")")
        }
        if let goal = s.goal {
            lines.append("Goal revision: \(goal.revision) (metadata only)")
            if privateText, let detail = s.privateGoal {
                lines.append("Goal: \(text(detail.title))")
                if let next = detail.nextAction { lines.append("Next action: \(text(next))") }
            } else { lines.append("Goal text: hidden") }
        } else { lines.append("Goal: \(s.goalState == "not_recorded" ? "not recorded" : "unavailable")") }
        lines.append("Stage: unavailable")
        let daemon = s.daemon
        lines.append("Daemon at read: \(known(daemon.state, ["responsive", "not_recorded", "unavailable", "skipped"]))\(daemon.dryRun ? " (dry run)" : "")")
        if daemon.state == "responsive" {
            lines.append("Daemon observed: \(stamp(daemon.observedAt))")
            lines.append("Next normal check: \(stamp(daemon.nextCheckAt))")
        } else { lines.append("Next normal check: unknown") }
        lines.append("External scheduler: not inspected")
        lines.append("Daemon response does not prove agent health")
        return lines
    }
}
