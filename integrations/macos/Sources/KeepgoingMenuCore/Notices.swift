import Foundation

/// A notification that a loop's checks have stopped.
public struct OverdueNotice: Equatable {
    /// Names the gap: the same loop and last check give the same identifier.
    public let identifier: String
    public let title: String
    public let body: String
}

/// Decides when to tell a person that a loop's checks have stopped, from
/// the reads the menu makes anyway. A loop is told about once per gap: the
/// gap is named by the loop and the time of its last check, as
/// `keepgoing panel list --json` gives it in `overdueSince`, so a new gap
/// after checks ran again is told again. What has been told is kept in
/// memory only: the preview keeps nothing on disk, so a restart during a
/// gap tells it once more.
///
/// The text names the loop and when it was last checked, and nothing else:
/// the list carries no identifiers, notes or reasons, and neither does this.
public struct OverdueNotices {
    /// More new gaps than this in one read are told in one notification.
    public static let maxSeparate = 3
    private var told: [String: Date] = [:]
    public init() {}

    /// The notifications to post after this read. Only a read that gave a
    /// list counts; a loop missing from a list that was cut off keeps what
    /// was told about it.
    public mutating func next(_ reading: MenuReading, format: (Date) -> String = OverdueNotices.when) -> [OverdueNotice] {
        guard case .read(let list, _) = reading else { return [] }
        var fresh: [(PanelLoop, Date)] = []
        for loop in list.loops {
            guard let since = Contract.date(loop.overdueSince) else {
                told[loop.key] = nil // checks are running again, or it was never overdue
                continue
            }
            if told[loop.key] != since { fresh.append((loop, since)) }
            told[loop.key] = since
        }
        func name(_ loop: PanelLoop) -> String { MenuBuilder.shortened(Contract.text(loop.name), to: MenuBuilder.maxName) }
        func id(_ loop: PanelLoop, _ since: Date) -> String { "keepgoing.overdue.\(loop.key).\(Int(since.timeIntervalSince1970))" }
        if fresh.count <= Self.maxSeparate {
            return fresh.map { loop, since in
                OverdueNotice(identifier: id(loop, since), title: "Checks have stopped: \(name(loop))",
                              body: "No check has run since \(format(since)). The computer may have slept, or the scheduler that runs this loop may have stopped. Open the keepgoing panel to see what to do.")
            }
        }
        let names = fresh.prefix(2).map { name($0.0) }.joined(separator: ", ")
        return [OverdueNotice(identifier: "keepgoing.overdue.several." + fresh.map { id($0.0, $0.1) }.joined(separator: "+"),
                              title: "Checks have stopped for \(fresh.count) loops",
                              body: "\(names) and \(fresh.count - 2) more have had no check for longer than usual. The computer may have slept, or their schedulers may have stopped. Open the keepgoing panel to see what to do.")]
    }

    /// The last check's time in the person's own date and time format.
    public static func when(_ date: Date) -> String {
        DateFormatter.localizedString(from: date, dateStyle: .short, timeStyle: .short)
    }
}
