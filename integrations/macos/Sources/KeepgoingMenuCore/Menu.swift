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

extension Contract {
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
              result.loops.allSatisfy({ validKey($0.key) && !$0.name.isEmpty && !$0.phrase.isEmpty }) else {
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
    case refresh
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
    /// What a screen reader says for the row.
    public let accessibilityLabel: String
    /// The full text when the title was shortened; empty otherwise.
    public let toolTip: String
    public let command: MenuCommand?
    public var isEnabled: Bool { command != nil }
}

public struct MenuModel: Equatable {
    public let rows: [MenuRow]
    /// The count beside the menu bar icon, or empty when nothing needs a person.
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
    static let maxName = 32

    /// The state symbols mirror the page's status dot: one per state, with
    /// the needs-you symbol filled so it stands out without colour.
    static let symbols: [String: String] = [
        "waiting_for_you": "exclamationmark.circle.fill", "unknown": "questionmark.circle", "checks_failing": "exclamationmark.triangle",
        "not_checked_recently": "clock.badge.questionmark", "waiting_on_limit": "hourglass", "supervising": "arrow.triangle.2.circlepath",
        "outside_hours": "moon", "paused": "pause.circle", "off": "circle", "stopped": "stop.circle", "finished": "checkmark.circle",
    ]

    static func shortened(_ name: String) -> String {
        let clean = Contract.text(name)
        return clean.count <= maxName ? clean : String(clean.prefix(maxName - 1)) + "…"
    }

    static func age(_ seconds: TimeInterval) -> String {
        let s = max(0, Int(seconds))
        if s < 60 { return "Read just now" }
        if s < 3600 { return "Read \(s / 60) min ago" }
        if s < 86_400 { return "Read \(s / 3600) h ago" }
        return "Read more than a day ago"
    }

    /// "1 loop needs you", "3 loops need you", "2 loops could not be read", or
    /// both; nil when nothing needs a person.
    static func needsLine(held: Int, unread: Int) -> String? {
        var parts: [String] = []
        if held > 0 { parts.append(held == 1 ? "1 loop needs you" : "\(held) loops need you") }
        if unread > 0 {
            let could = unread == 1 ? "1 loop could not be read" : "\(unread) loops could not be read"
            parts.append(parts.isEmpty ? could : (unread == 1 ? "1 could not be read" : "\(unread) could not be read"))
        }
        return parts.isEmpty ? nil : parts.joined(separator: ", ")
    }

    static func action(_ title: String, _ command: MenuCommand, symbol: String = "", label: String? = nil) -> MenuRow {
        MenuRow(kind: .action, title: title, detail: "", symbol: symbol, accessibilityLabel: label ?? title, toolTip: "", command: command)
    }
    static let separator = MenuRow(kind: .separator, title: "", detail: "", symbol: "", accessibilityLabel: "", toolTip: "", command: nil)
    static func status(_ text: String) -> MenuRow {
        MenuRow(kind: .status, title: text, detail: "", symbol: "", accessibilityLabel: text, toolTip: "", command: nil)
    }

    /// `notice` replaces the status line once, for example after the panel
    /// could not be opened. While `refreshing`, the rows of the last read stay
    /// in place and the status line says a read is under way.
    public static func build(_ reading: MenuReading, now: Date, notice: String? = nil, refreshing: Bool = false) -> MenuModel {
        // The first row is always there and always tries, whatever was read.
        var rows = [action("Open keepgoing panel", .openPanel, symbol: "macwindow", label: "Open keepgoing panel in your browser")]
        var badge = ""
        var iconLabel = "keepgoing"
        var line: String
        switch reading {
        case .notRead:
            line = "Open the menu to read your loops"
        case .reading:
            line = "Reading…"
        case .unsupported:
            line = "This keepgoing is a different version from the menu. Open the panel instead."
        case .unavailable:
            line = "keepgoing could not be read. Open the panel, or run keepgoing doctor."
        case .read(let list, let at):
            let held = max(0, list.needsYou - list.unread)
            if let needs = needsLine(held: held, unread: list.unread) {
                rows.append(action(needs, .openPanel, symbol: "exclamationmark.circle.fill", label: needs + ". Opens the panel on what needs you."))
                badge = list.needsYou > 99 ? "99+" : String(list.needsYou)
                iconLabel = "keepgoing, " + needs
            }
            if !list.loops.isEmpty { rows.append(separator) }
            for loop in list.loops {
                let name = Contract.text(loop.name), short = shortened(loop.name), phrase = Contract.text(loop.phrase)
                rows.append(MenuRow(kind: .loop, title: short, detail: phrase, symbol: symbols[loop.state] ?? "questionmark.circle",
                                    accessibilityLabel: "\(name), \(phrase). Opens this loop in the panel.",
                                    toolTip: short == name ? "" : name, command: .openLoop(key: loop.key)))
            }
            var facts: [String] = []
            if list.registry != "available" {
                facts.append("The list of loops could not be read.")
            } else if list.loops.isEmpty {
                facts.append("No loops are registered.")
            } else if !list.complete {
                facts.append("Not every loop was read.")
            }
            if list.omitted > 0 { facts.append(list.omitted == 1 ? "1 more loop is not shown." : "\(list.omitted) more loops are not shown.") }
            // Alone, the age reads as a label; after a sentence it is one too.
            let read = age(now.timeIntervalSince(at))
            line = facts.isEmpty ? read : (facts + [read + "."]).joined(separator: " ")
        }
        rows.append(separator)
        rows.append(action("Refresh", .refresh, symbol: "arrow.clockwise", label: "Refresh: read your loops again"))
        rows.append(action("Quit keepgoing menu", .quit, label: "Quit keepgoing menu. Supervision keeps running."))
        rows.append(separator)
        rows.append(status(notice ?? (refreshing ? "Reading…" : line)))
        return MenuModel(rows: rows, badge: badge, iconAccessibilityLabel: iconLabel)
    }

    /// The arguments each command runs, before the registry and panel
    /// directories are appended. Refresh and quit run nothing.
    public static func arguments(_ command: MenuCommand) -> [String]? {
        switch command {
        case .openPanel: return ["panel"]
        case .openLoop(let key): return Contract.validKey(key) ? ["panel", "--loop", key] : nil
        case .refresh, .quit: return nil
        }
    }
}
