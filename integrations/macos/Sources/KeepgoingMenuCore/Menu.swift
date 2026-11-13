import Foundation

// The menu's contents, decided here so they can be tested without AppKit.
// Every row except the last either opens the panel or is Refresh or Quit;
// the last row is the single dimmed status line.

/// One loop as `keepgoing panel list --json` reports it. The phrase is the
/// panel page's own wording; this file never invents a state.
public struct PanelLoop: Decodable, Equatable {
    public let key: String
    public let name: String
    public let state: String
    public let phrase: String
    public let needsYou: Bool
}

public struct PanelList: Decodable, Equatable {
    let schemaVersion: Int
    public let registry: String
    public let complete: Bool
    public let allClear: Bool
    public let needsYou: Int
    public let unread: Int
    public let omitted: Int
    public let loops: [PanelLoop]
}

/// What `keepgoing panel status --json` says about the panel's background
/// server. It never carries an address, a code or a cookie.
public struct PanelStatus: Decodable, Equatable {
    let schemaVersion: Int
    public let running: Bool
    public let idleRemainingSeconds: Int?
}

/// When the app reads without being asked. One list read a minute keeps the
/// count beside the icon current; after three failures in a row it slows to
/// one every five minutes; in Low Power Mode it does not read on a timer at
/// all, and the menu still reads when it is opened.
public enum ReadSchedule {
    public static let normal: TimeInterval = 60
    public static let afterFailures: TimeInterval = 300
    public static let failuresBeforeSlowing = 3
    public static func interval(consecutiveFailures: Int, lowPower: Bool) -> TimeInterval? {
        if lowPower { return nil }
        return consecutiveFailures >= failuresBeforeSlowing ? afterFailures : normal
    }
}

extension Contract {
    public static func panelStatus(_ data: Data, exit: Int32) throws -> PanelStatus {
        guard exit == 0, data.count <= 4096 else { throw ClientFailure.unavailable }
        guard let result = try? JSONDecoder().decode(PanelStatus.self, from: data) else { throw ClientFailure.invalidResponse }
        guard result.schemaVersion == 1 else { throw ClientFailure.unsupported }
        guard (result.idleRemainingSeconds ?? 0) >= 0 else { throw ClientFailure.invalidResponse }
        return result
    }

    static func validKey(_ key: String) -> Bool {
        key.range(of: "^l[0-9a-f]{12}$", options: .regularExpression) != nil
    }

    public static func panelList(_ data: Data, exit: Int32) throws -> PanelList {
        guard data.count <= 1_048_576 else { throw ClientFailure.outputLimit }
        let result: PanelList
        do { result = try JSONDecoder().decode(PanelList.self, from: data) }
        catch { throw ClientFailure.invalidResponse }
        guard result.schemaVersion == 1 else { throw ClientFailure.unsupported }
        // An unreadable registry exits 1 and still says so in a valid reply.
        guard (result.registry == "available" && exit == 0) || (result.registry == "unavailable" && exit == 1),
              result.loops.count <= 128, result.needsYou >= 0, result.unread >= 0, result.unread <= result.needsYou, result.omitted >= 0,
              Set(result.loops.map(\.key)).count == result.loops.count,
              // Checked on what will be shown: a name of control characters only is empty.
              result.loops.allSatisfy({ validKey($0.key) && !text($0.name).isEmpty && !text($0.phrase).isEmpty }) else {
            throw ClientFailure.invalidResponse
        }
        return result
    }
}

/// What a row does when chosen. `nil` on a row means it cannot be chosen.
public enum MenuCommand: Equatable {
    /// Runs `keepgoing panel`: the page opens on what needs a person.
    case openPanel
    /// Runs `keepgoing panel --loop <key>`.
    case openLoop(key: String)
    /// Runs `keepgoing panel stop`. Supervision is not affected.
    case stopPanel
    case quit
}

public struct MenuRow: Equatable {
    public enum Kind: Equatable { case action, loop, status, separator }
    public let kind: Kind
    /// The main text. For a loop this is its name.
    public let title: String
    /// Secondary text on the same row: a loop's state in the panel's words.
    public let detail: String
    /// An SF Symbol name, or empty for none.
    public let symbol: String
    /// What a screen reader says for the row: what it is.
    public let accessibilityLabel: String
    /// What choosing it does, offered as help so it is not read on every row.
    public let accessibilityHelp: String
    /// The full text when the title was shortened; empty otherwise.
    public let toolTip: String
    public let command: MenuCommand?
    public var isEnabled: Bool { command != nil }
}

public struct MenuModel: Equatable {
    public let rows: [MenuRow]
    /// Beside the menu bar icon: the count of loops that need a person; "?"
    /// when keepgoing could not be read or not every loop was; and empty only
    /// when a complete read found that nothing needs a person.
    public let badge: String
    public let iconAccessibilityLabel: String
}

/// What the app knows when it draws the menu.
public enum MenuReading: Equatable {
    case notRead
    case reading
    case read(PanelList, at: Date)
    /// The CLI answered in a version this menu does not understand.
    case unsupported
    /// The CLI could not be run, timed out or answered something unreadable.
    case unavailable
}

public enum MenuBuilder {
    // Long enough for a real loop name, short enough that the state column
    // stays close to the short names beside it.
    static let maxName = 28
    static let maxPhrase = 40
    /// The most loops listed. They arrive with the ones that want attention
    /// first; the rest are one choice away in the panel.
    static let maxLoops = 25

    /// One symbol per state, told apart by shape and never by colour. The
    /// needs-you symbol is the only filled one.
    static let symbols: [String: String] = [
        "waiting_for_you": "exclamationmark.circle.fill", "unknown": "questionmark.circle", "checks_failing": "exclamationmark.triangle",
        "not_checked_recently": "questionmark.circle", "waiting_on_limit": "hourglass", "supervising": "circle.inset.filled",
        "outside_hours": "moon", "paused": "pause.circle", "off": "circle", "stopped": "stop.circle", "finished": "checkmark.circle",
    ]

    /// Shortened in the middle: loop names often share a beginning, so the
    /// end is what tells them apart.
    static func shortened(_ text: String, to limit: Int) -> String {
        let characters = Array(text)
        guard characters.count > limit else { return text }
        let head = (limit - 1 + 1) / 2, tail = limit - 1 - head
        return String(characters.prefix(head)) + "…" + String(characters.suffix(tail))
    }

    static func age(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds))
        if s < 60 { return "Read just now" }
        if s < 3600 { return "Read \(s / 60) min ago" }
        if s < 86_400 { return "Read \(s / 3600) h ago" }
        return "Read more than a day ago"
    }

    /// "1 loop needs you", "3 loops need you", "2 loops can't be read", or
    /// both; nil when nothing needs a person. "Can't be read" is the panel's
    /// phrase for such a loop.
    static func needsLine(held: Int, unread: Int) -> String? {
        var parts: [String] = []
        if held > 0 { parts.append(held == 1 ? "1 loop needs you" : "\(held) loops need you") }
        if unread > 0 {
            parts.append(parts.isEmpty ? (unread == 1 ? "1 loop can't be read" : "\(unread) loops can't be read") : "\(unread) can't be read")
        }
        return parts.isEmpty ? nil : parts.joined(separator: ", ")
    }

    static func action(_ title: String, _ command: MenuCommand, symbol: String, help: String, detail: String = "") -> MenuRow {
        MenuRow(kind: .action, title: title, detail: detail, symbol: symbol, accessibilityLabel: detail.isEmpty ? title : title + ", " + detail,
                accessibilityHelp: help, toolTip: "", command: command)
    }
    static let separator = MenuRow(kind: .separator, title: "", detail: "", symbol: "", accessibilityLabel: "", accessibilityHelp: "", toolTip: "", command: nil)
    static func status(_ text: String) -> MenuRow {
        MenuRow(kind: .status, title: text, detail: "", symbol: "", accessibilityLabel: text, accessibilityHelp: "", toolTip: "", command: nil)
    }

    /// "stops by itself in 23 min", from the seconds the panel reports.
    static func stopsIn(_ seconds: Int?) -> String {
        guard let seconds else { return "running" }
        if seconds < 60 { return "stops by itself in under a minute" }
        if seconds < 5400 { return "stops by itself in \(seconds / 60) min" }
        return "stops by itself in \(seconds / 3600) h"
    }

    /// `notice` replaces the status line once, for example after the panel
    /// could not be opened. While `refreshing`, the rows of the last read stay
    /// in place and the status line says a read is under way. `panel` is the
    /// panel server's status when it was read; the row that stops it is there
    /// only while it is running.
    public static func build(_ reading: MenuReading, now: Date, notice: String? = nil, refreshing: Bool = false, panel: PanelStatus? = nil) -> MenuModel {
        // The first row is always there and always tries, whatever was read.
        var rows = [action("Open keepgoing Panel", .openPanel, symbol: "macwindow", help: "Opens the keepgoing panel in your browser.")]
        var badge = ""
        var iconLabel = "keepgoing"
        var line: String
        switch reading {
        case .notRead:
            line = "Not read yet"
        case .reading:
            line = "Reading…"
        case .unsupported:
            line = "The menu and keepgoing are different versions. Use the panel."
            badge = "?"
            iconLabel = "keepgoing, can't be read"
        case .unavailable:
            line = "Can't reach keepgoing. Run keepgoing doctor."
            badge = "?"
            iconLabel = "keepgoing, can't be read"
        case .read(let list, let at):
            let held = max(0, list.needsYou - list.unread)
            if let needs = needsLine(held: held, unread: list.unread) {
                // The symbol says which it is: a person is needed, or only
                // that something could not be read.
                rows.append(action(needs, .openPanel, symbol: held > 0 ? "exclamationmark.circle.fill" : "questionmark.circle", help: "Opens the panel on what needs you."))
                badge = list.needsYou > 99 ? "99+" : String(list.needsYou)
                iconLabel = "keepgoing, " + needs
            } else if !list.allClear {
                // The icon is plain only when the CLI says every loop was
                // read and none needs a person. An unreadable registry or a
                // read that was cut off is not "nothing needs you".
                badge = "?"
                iconLabel = list.registry == "available" ? "keepgoing, not every loop was read" : "keepgoing, the list of loops can't be read"
            }
            if !list.loops.isEmpty { rows.append(separator) }
            for loop in list.loops.prefix(maxLoops) {
                let name = Contract.text(loop.name), short = shortened(name, to: maxName), phrase = shortened(Contract.text(loop.phrase), to: maxPhrase)
                rows.append(MenuRow(kind: .loop, title: short, detail: phrase, symbol: symbols[loop.state] ?? "questionmark.circle",
                                    accessibilityLabel: "\(name), \(phrase)", accessibilityHelp: "Opens this loop in the panel.",
                                    toolTip: short == name ? "" : name, command: .openLoop(key: loop.key)))
            }
            let total = list.loops.count + list.omitted
            // Whenever some loops are not listed here, whether this menu cut
            // them or the CLI did.
            if total > min(list.loops.count, maxLoops) {
                rows.append(action("All \(total) Loops in the Panel", .openPanel, symbol: "ellipsis.circle", help: "Opens the panel, which lists every loop."))
            }
            var facts: [String] = []
            if list.registry != "available" {
                facts.append("The list of loops could not be read.")
            } else if list.loops.isEmpty {
                facts.append("No loops are registered.")
            } else if !list.complete {
                facts.append("Not every loop was read.")
            }
            // Alone, the age reads as a label; after a sentence it is one too.
            let read = age(now.timeIntervalSince(at))
            line = facts.isEmpty ? read : (facts + [read + "."]).joined(separator: " ")
        }
        rows.append(separator)
        if let panel, panel.running {
            // Named for what it stops, the page, so it is not taken for
            // stopping a loop; what happens to the loops is in the help.
            rows.append(action("Stop the Panel Page", .stopPanel, symbol: "xmark.circle", help: "Stops the panel's background server. Your loops keep running.", detail: stopsIn(panel.idleRemainingSeconds)))
        }
        rows.append(action("Quit keepgoing Menu", .quit, symbol: "power", help: "Quits this menu. Supervision keeps running."))
        rows.append(separator)
        rows.append(status(notice ?? (refreshing ? "Reading…" : line)))
        return MenuModel(rows: rows, badge: badge, iconAccessibilityLabel: iconLabel)
    }

    /// The arguments each command runs, before the registry and panel
    /// directories are appended. Quit runs nothing.
    public static func arguments(_ command: MenuCommand) -> [String]? {
        switch command {
        case .openPanel: return ["panel"]
        case .openLoop(let key): return Contract.validKey(key) ? ["panel", "--loop", key] : nil
        case .stopPanel: return ["panel", "stop"]
        case .quit: return nil
        }
    }

    /// Two models have the same shape when their rows are the same kinds
    /// running the same commands, so an open menu can be updated in place.
    public static func sameShape(_ a: [MenuRow], _ b: [MenuRow]) -> Bool {
        a.count == b.count && zip(a, b).allSatisfy { $0.kind == $1.kind && $0.command == $1.command }
    }
}
