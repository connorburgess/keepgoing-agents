import Foundation

// The menu's contents, decided here so they can be tested without AppKit.
// Every row except the last opens the panel, opens a loop's choices, stops
// the panel's page or quits; the last row is the single dimmed status line.

/// One loop as `keepgoing panel list --json` reports it. The phrase is the
/// panel page's own wording; this file never invents a state.
public struct PanelLoop: Decodable, Equatable {
    public let key: String
    public let name: String
    public let state: String
    public let phrase: String
    public let needsYou: Bool
    /// The panel's no-progress sentence when that flag is raised, absent
    /// otherwise. It informs and is never a need: it changes no badge.
    public let noProgress: String?
    /// Which changes fit the loop's control flags, as the page offers them:
    /// pause, stop, resume. Absent from a CLI that has no client review.
    public var can: [String]? = nil
    /// The time of the loop's last check, when it needs a person because its
    /// checks have stopped. See OverdueNotices.
    public let overdueSince: String?

    public init(key: String, name: String, state: String, phrase: String, needsYou: Bool, noProgress: String? = nil, can: [String]? = nil, overdueSince: String? = nil) {
        self.key = key
        self.name = name
        self.state = state
        self.phrase = phrase
        self.needsYou = needsYou
        self.noProgress = noProgress
        self.can = can
        self.overdueSince = overdueSince
    }
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
    /// Changes that can be made to every loop at once (`client review|apply
    /// --all`). Absent from a CLI that cannot.
    public var canAll: [String]? = nil
}

/// What `keepgoing panel status --json` says about the panel's background
/// server. It never carries an address, a code or a cookie.
public struct PanelStatus: Decodable, Equatable {
    let schemaVersion: Int
    public let running: Bool
    public let idleRemainingSeconds: Int?
    /// Started with --allow-changes.
    public var changesAllowed: Bool? = nil
    /// The page footer's statement about allowing changes. Absent from a CLI
    /// that predates it, and then the menu does not offer to allow them.
    public var exposure: String? = nil
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

/// Every command the menu can run, and what a row does when chosen. `nil`
/// on a row means it cannot be chosen. `MenuBuilder.arguments` is the
/// allow-list: nothing else is ever run.
public enum MenuCommand: Equatable {
    /// Runs `keepgoing panel list --json`. Never on a row.
    case readList
    /// Runs `keepgoing panel status --json`. Never on a row.
    case readStatus
    /// Runs `keepgoing panel`: the page opens on what needs a person.
    case openPanel
    /// Runs `keepgoing panel --loop <key>`.
    case openLoop(key: String)
    /// Runs `keepgoing panel --allow-changes`, after a confirmation.
    case openPanelWithChanges
    /// Runs `keepgoing panel stop`. Supervision is not affected.
    case stopPanel
    /// Runs `keepgoing client review <action> --loop <key> --json`, which
    /// reads and changes nothing; its review is then shown for confirmation.
    case review(LoopAction, key: String)
    /// Runs `keepgoing client apply <action> --loop <key> --expect <digest>
    /// [--lanes] [--immediately] --json`, only from a confirmed review.
    /// `--drop-queued` deletes messages queued in lanes that have not
    /// started; it is passed only with `--lanes`, and only for the stop
    /// option that says so.
    case apply(LoopAction, key: String, expect: String, lanes: Bool, immediately: Bool, dropQueued: Bool = false)
    /// Runs `keepgoing client review pause --all --json`, which reads and
    /// changes nothing; its review is then shown for confirmation.
    case reviewAll(LoopAction)
    /// Runs `keepgoing client apply pause --all --expect KEY=VALUE… --json`,
    /// only from a confirmed review of every loop.
    case applyAll(LoopAction, expects: [LoopExpect])
    /// Turns Open at Login on or off. Runs no keepgoing command.
    case toggleLogin
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
    /// A loop's choices, shown as its submenu.
    public var submenu: [MenuRow] = []
    /// Shown with a check mark: Open at Login while it is on.
    public var checked = false
    public var isEnabled: Bool { command != nil || !submenu.isEmpty }
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
    /// `login` is whether the menu opens at login; nil leaves the row out.
    public static func build(_ reading: MenuReading, now: Date, notice: String? = nil, refreshing: Bool = false, panel: PanelStatus? = nil, login: LoginStatus? = nil) -> MenuModel {
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
                let name = Contract.text(loop.name), short = shortened(name, to: maxName)
                let flag = loop.noProgress.map(Contract.text) ?? ""
                // The panel's own words: its phrase, and "no progress" as the
                // panel's picker row says it when that flag is raised.
                let phrase = shortened(Contract.text(loop.phrase) + (flag.isEmpty ? "" : " · no progress"), to: maxPhrase)
                let choices = loopChoices(loop)
                rows.append(MenuRow(kind: .loop, title: short, detail: phrase, symbol: symbols[loop.state] ?? "questionmark.circle",
                                    accessibilityLabel: flag.isEmpty ? "\(name), \(phrase)" : "\(name), \(Contract.text(loop.phrase)). \(flag)",
                                    accessibilityHelp: choices.count > 1 ? "Shows what you can do with this loop." : "Opens this loop in the panel.",
                                    toolTip: short == name ? "" : name, command: choices.count > 1 ? nil : .openLoop(key: loop.key), submenu: choices.count > 1 ? choices : []))
            }
            if list.canAll?.contains("pause") == true {
                rows.append(action("Pause All Loops…", .reviewAll(.pause), symbol: "pause.circle",
                                   help: "Shows which loops pausing every loop changes, then asks you to confirm."))
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
        if let panel, panel.exposure != nil {
            rows.append(action("Open Panel with Controls…", .openPanelWithChanges, symbol: "slider.horizontal.3",
                               help: "Opens the panel so its page can pause, stop and resume loops. Asks first."))
        }
        if let panel, panel.running {
            // Named for what it stops, the page, so it is not taken for
            // stopping a loop; what happens to the loops is in the help.
            rows.append(action("Stop the Panel Page", .stopPanel, symbol: "xmark.circle", help: "Stops the panel's background process. Your loops keep running.", detail: stopsIn(panel.idleRemainingSeconds)))
        }
        if let login {
            rows.append(loginRow(login))
        }
        rows.append(action("Quit keepgoing Menu", .quit, symbol: "power", help: "Quits this menu. Supervision keeps running."))
        rows.append(separator)
        rows.append(status(notice ?? (refreshing ? "Reading…" : line)))
        return MenuModel(rows: rows, badge: badge, iconAccessibilityLabel: iconLabel)
    }

    /// Open at Login: checked while it is on. A bare executable cannot open
    /// at login; choosing the row then says it needs the app bundle.
    static func loginRow(_ status: LoginStatus) -> MenuRow {
        var row: MenuRow
        switch status {
        case .needsApp:
            row = action("Open at Login", .toggleLogin, symbol: "arrow.up.forward.app", help: "Needs the menu built as an app. Choosing it says how.", detail: "needs the app")
        case .needsApproval:
            row = action("Open at Login", .toggleLogin, symbol: "arrow.up.forward.app", help: "Allow keepgoing Menu in System Settings, Login Items, or choose this to turn it off.", detail: "allow in System Settings")
        case .on:
            row = action("Open at Login", .toggleLogin, symbol: "arrow.up.forward.app", help: "Opens this menu when you log in. Choose it to turn that off.")
        case .off:
            row = action("Open at Login", .toggleLogin, symbol: "arrow.up.forward.app", help: "Opens this menu when you log in, with the same keepgoing and directories. Off until you choose it.")
        }
        row.checked = status == .on || status == .needsApproval
        return row
    }

    /// A loop's submenu: open it in the panel and, when the CLI says which
    /// changes fit its flags, each of those, Stop last. With nothing to
    /// change it has only the one choice, and the row opens the loop itself.
    static func loopChoices(_ loop: PanelLoop) -> [MenuRow] {
        let rows = [action("Open in Panel", .openLoop(key: loop.key), symbol: "macwindow", help: "Opens this loop in the panel.")]
        let can = Set(loop.can ?? [])
        var changes: [MenuRow] = []
        if can.contains("pause") {
            changes.append(action("Pause…", .review(.pause, key: loop.key), symbol: "pause.circle", help: "Shows what pausing does, then asks you to confirm."))
        }
        if can.contains("resume") {
            changes.append(action("Resume…", .review(.resume, key: loop.key), symbol: "play.circle", help: "Shows what resuming clears, then asks you to confirm."))
        }
        if can.contains("stop") {
            changes.append(action("Stop…", .review(.stop, key: loop.key), symbol: "stop.circle", help: "Shows what stopping does, then asks you to confirm."))
        }
        return changes.isEmpty ? rows : rows + [separator] + changes
    }

    /// The arguments each command runs, before the registry and panel
    /// directories are appended: the menu's whole allow-list. A value taken
    /// from a reply is passed back only after it matches its pattern. Quit
    /// runs nothing.
    public static func arguments(_ command: MenuCommand) -> [String]? {
        switch command {
        case .readList: return ["panel", "list", "--json"]
        case .readStatus: return ["panel", "status", "--json"]
        case .openPanel: return ["panel"]
        case .openLoop(let key): return Contract.validKey(key) ? ["panel", "--loop", key] : nil
        case .openPanelWithChanges: return ["panel", "--allow-changes"]
        case .stopPanel: return ["panel", "stop"]
        case .review(let action, let key): return Contract.validKey(key) ? ["client", "review", action.rawValue, "--loop", key, "--json"] : nil
        case .apply(let action, let key, let expect, let lanes, let immediately, let dropQueued):
            guard Contract.validKey(key), Contract.validExpect(expect), action == .stop || (!lanes && !immediately && !dropQueued), !dropQueued || lanes else { return nil }
            return ["client", "apply", action.rawValue, "--loop", key, "--expect", expect] + (lanes ? ["--lanes"] : []) + (immediately ? ["--immediately"] : [])
                + (dropQueued ? ["--drop-queued"] : []) + ["--json"]
        case .reviewAll(let action): return action == .pause ? ["client", "review", action.rawValue, "--all", "--json"] : nil
        case .applyAll(let action, let expects):
            let keys = expects.map(\.key)
            guard action == .pause, (1...128).contains(expects.count), Set(keys).count == keys.count,
                  expects.allSatisfy({ Contract.validKey($0.key) && Contract.validExpect($0.expect) }) else { return nil }
            return ["client", "apply", action.rawValue, "--all"] + expects.flatMap { ["--expect", $0.key + "=" + $0.expect] } + ["--json"]
        case .toggleLogin, .quit: return nil
        }
    }

    /// Two models have the same shape when their rows are the same kinds
    /// running the same commands, with submenus of the same shape, so an open
    /// menu can be updated in place.
    public static func sameShape(_ a: [MenuRow], _ b: [MenuRow]) -> Bool {
        a.count == b.count && zip(a, b).allSatisfy { $0.kind == $1.kind && $0.command == $1.command && sameShape($0.submenu, $1.submenu) }
    }
}
