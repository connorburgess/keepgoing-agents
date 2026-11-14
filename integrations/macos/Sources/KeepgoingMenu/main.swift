import AppKit
import KeepgoingMenuCore
import ServiceManagement

// Development entry point: no installation or automatic executable
// discovery. The user explicitly chooses a trusted CLI. The one thing kept on
// disk is what Open at Login saves when a person ticks it (Login.swift).
let login = LoginItem(service: AppLoginService(), store: AppLoginStore())
guard let launch = login.launch(Array(CommandLine.arguments.dropFirst())) else {
    fputs("Usage: KeepgoingMenu --executable /absolute/path/to/keepgoing [--registry-directory /absolute/path] [--panel-directory /absolute/path] [--no-notifications]\n       KeepgoingMenu ... --render-menu /absolute/path.png [--dark]   draw the menu's rows to a picture and quit\n", stderr)
    exit(2)
}

/// A row's text: its title, then its detail in the secondary colour. For
/// loops the details start at one tab stop, so the states read as a column.
/// The title takes the menu's own colour unless `drawn`, so a real menu can
/// invert it on the highlighted row; the drawing has no highlight and sets it.
/// The line is laid out left to right whatever script the name is in, and the
/// name is isolated so it cannot reorder the state beside it.
func rowText(_ row: MenuRow, column: CGFloat?, drawn: Bool) -> NSAttributedString {
    let style = NSMutableParagraphStyle()
    style.baseWritingDirection = .leftToRight
    style.lineBreakMode = .byTruncatingTail
    if let column { style.tabStops = [NSTextTab(textAlignment: .left, location: column)] }
    var title: [NSAttributedString.Key: Any] = [.font: NSFont.menuFont(ofSize: 0), .paragraphStyle: style]
    if drawn { title[.foregroundColor] = NSColor.labelColor }
    let text = NSMutableAttributedString(string: "\u{2068}" + row.title + "\u{2069}", attributes: title)
    if !row.detail.isEmpty {
        text.append(NSAttributedString(string: (column == nil ? "   " : "\t") + row.detail,
                                       attributes: [.font: NSFont.menuFont(ofSize: 0), .foregroundColor: NSColor.secondaryLabelColor, .paragraphStyle: style]))
    }
    return text
}

/// Where the state column starts: just past the longest loop name.
func stateColumn(_ rows: [MenuRow]) -> CGFloat {
    let widest = rows.filter { $0.kind == .loop }.map { ($0.title as NSString).size(withAttributes: [.font: NSFont.menuFont(ofSize: 0)]).width }.max() ?? 0
    return ceil(widest) + 18
}

/// The background of the drawn picture: the window colour of its appearance.
final class Backdrop: NSView {
    override func draw(_ dirtyRect: NSRect) {
        NSColor.windowBackgroundColor.setFill()
        dirtyRect.fill()
    }
}

final class MenuApp: NSObject, NSApplicationDelegate, NSMenuDelegate {
    let client: LocalClient
    let notifier: Notifier
    let renderPath: String?
    let renderDark: Bool
    let menu = NSMenu()
    var item: NSStatusItem!
    var reading: MenuReading = .notRead
    var panel: PanelStatus?
    var failures = 0
    var timer: Timer?
    var shown: [MenuRow] = []
    var menuOpen = false
    var notice: String?
    var loading = false
    let login: LoginItem
    let launch: LaunchOptions
    init(client: LocalClient, notifier: Notifier, renderPath: String?, renderDark: Bool, login: LoginItem, launch: LaunchOptions) {
        self.login = login
        self.launch = launch
        self.client = client
        self.notifier = notifier
        self.renderPath = renderPath
        self.renderDark = renderDark
    }

    // Verification aid: once the first read has finished, draw the menu bar
    // item and the menu's rows into a picture, with the fonts, symbols and
    // colours the real menu uses, and quit. It never opens the menu or looks
    // at the screen, so it is a drawing of the rows and not a screen capture.
    func renderMenu(to path: String) {
        let model = MenuBuilder.build(reading, now: Date(), panel: panel)
        let appearance = NSAppearance(named: renderDark ? .darkAqua : .aqua)!
        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 0
        stack.edgeInsets = NSEdgeInsets(top: 6, left: 0, bottom: 6, right: 0)
        func label(_ text: NSAttributedString) -> NSTextField {
            let field = NSTextField(labelWithAttributedString: text)
            field.lineBreakMode = .byTruncatingTail
            return field
        }
        func line(image: NSImage?, text: NSAttributedString, height: CGFloat = 24) -> NSView {
            let icon = NSImageView()
            icon.image = image
            icon.contentTintColor = .labelColor
            icon.widthAnchor.constraint(equalToConstant: 18).isActive = true
            let row = NSStackView(views: [icon, label(text)])
            row.spacing = 7
            row.edgeInsets = NSEdgeInsets(top: 0, left: 14, bottom: 0, right: 18)
            row.heightAnchor.constraint(equalToConstant: height).isActive = true
            return row
        }
        // The menu bar item: the icon and, when something needs a person, the count.
        let bar = NSMutableAttributedString(string: model.badge.isEmpty ? "" : model.badge, attributes: [.font: NSFont.menuBarFont(ofSize: 0), .foregroundColor: NSColor.labelColor])
        bar.append(NSAttributedString(string: "      menu bar item", attributes: [.font: NSFont.systemFont(ofSize: 10), .foregroundColor: NSColor.tertiaryLabelColor]))
        stack.addArrangedSubview(line(image: NSImage(systemSymbolName: "arrow.triangle.2.circlepath", accessibilityDescription: nil), text: bar, height: 26))
        func rule() {
            let box = NSBox()
            box.boxType = .separator
            stack.addArrangedSubview(box)
            box.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
            stack.setCustomSpacing(5, after: box)
            if let before = stack.arrangedSubviews.dropLast().last { stack.setCustomSpacing(5, after: before) }
        }
        rule()
        let column = stateColumn(model.rows)
        for row in model.rows {
            switch row.kind {
            case .separator:
                rule()
            case .status:
                stack.addArrangedSubview(line(image: nil, text: NSAttributedString(string: row.title, attributes: [.font: NSFont.menuFont(ofSize: NSFont.smallSystemFontSize), .foregroundColor: NSColor.secondaryLabelColor])))
            case .loop:
                stack.addArrangedSubview(line(image: NSImage(systemSymbolName: row.symbol, accessibilityDescription: nil), text: rowText(row, column: column, drawn: true)))
            case .action:
                stack.addArrangedSubview(line(image: row.symbol.isEmpty ? nil : NSImage(systemSymbolName: row.symbol, accessibilityDescription: nil), text: rowText(row, column: nil, drawn: true)))
            }
        }
        let backdrop = Backdrop()
        backdrop.appearance = appearance
        backdrop.addSubview(stack)
        stack.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([stack.leadingAnchor.constraint(equalTo: backdrop.leadingAnchor), stack.trailingAnchor.constraint(equalTo: backdrop.trailingAnchor),
                                     stack.topAnchor.constraint(equalTo: backdrop.topAnchor), stack.bottomAnchor.constraint(equalTo: backdrop.bottomAnchor),
                                     stack.widthAnchor.constraint(greaterThanOrEqualToConstant: 300)])
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 300, height: 100), styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = appearance
        window.contentView = backdrop
        backdrop.layoutSubtreeIfNeeded()
        let size = backdrop.fittingSize
        window.setContentSize(size)
        backdrop.layoutSubtreeIfNeeded()
        if let rep = backdrop.bitmapImageRepForCachingDisplay(in: backdrop.bounds) {
            backdrop.cacheDisplay(in: backdrop.bounds, to: rep)
            try? rep.representation(using: .png, properties: [:])?.write(to: URL(fileURLWithPath: path))
        }
        NSApplication.shared.terminate(nil)
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        let image = NSImage(systemSymbolName: "arrow.triangle.2.circlepath", accessibilityDescription: nil)
        image?.isTemplate = true // follows the menu bar in light and dark
        item.button?.image = image
        item.button?.imagePosition = .imageLeading
        item.menu = menu
        menu.autoenablesItems = false
        menu.delegate = self
        render()
        // One read when the app is started, so the count is there before the
        // menu is first opened. After that it reads when the menu opens, when
        // Refresh is chosen, and on the slow schedule in ReadSchedule.
        let workspace = NSWorkspace.shared.notificationCenter
        workspace.addObserver(forName: NSWorkspace.willSleepNotification, object: nil, queue: .main) { [weak self] _ in self?.timer?.invalidate() }
        workspace.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in self?.refresh() }
        NotificationCenter.default.addObserver(forName: .NSProcessInfoPowerStateDidChange, object: nil, queue: .main) { [weak self] _ in self?.scheduleNextRead() }
        refresh()
    }
    func menuWillOpen(_ menu: NSMenu) {
        menuOpen = true
        refresh()
    }
    func menuDidClose(_ menu: NSMenu) {
        menuOpen = false
        // A notice is shown for one opening of the menu.
        notice = nil
        render()
    }

    /// Arms the one timer for the next unasked read, or none in Low Power Mode.
    func scheduleNextRead() {
        timer?.invalidate()
        timer = nil
        guard renderPath == nil, let interval = ReadSchedule.interval(consecutiveFailures: failures, lowPower: ProcessInfo.processInfo.isLowPowerModeEnabled) else { return }
        let next = Timer(timeInterval: interval, repeats: false) { [weak self] _ in self?.refresh() }
        next.tolerance = interval / 10
        // Common modes: the count keeps up while a menu is open too.
        RunLoop.main.add(next, forMode: .common)
        timer = next
    }

    /// Shows the current model. The count beside the icon is always brought
    /// up to date. An open menu is never torn down under the pointer: its rows
    /// are updated in place when they are the same rows, and otherwise the
    /// rebuild waits until nothing is highlighted or the menu has closed.
    func render() {
        let model = MenuBuilder.build(reading, now: Date(), notice: notice, refreshing: loading, panel: panel, login: login.status)
        if let button = item.button {
            button.title = model.badge.isEmpty ? "" : " " + model.badge
            button.setAccessibilityLabel(model.iconAccessibilityLabel)
        }
        let column = stateColumn(model.rows)
        if MenuBuilder.sameShape(model.rows, shown), menu.items.count == model.rows.count {
            for (entry, row) in zip(menu.items, model.rows) where row.kind != .separator { fill(entry, row, column: column) }
        } else if menuOpen, menu.highlightedItem != nil {
            return // menuDidClose renders again
        } else {
            build(menu, model.rows, column: column)
        }
        shown = model.rows
    }

    func build(_ target: NSMenu, _ rows: [MenuRow], column: CGFloat?) {
        target.removeAllItems()
        target.autoenablesItems = false
        for row in rows {
            if row.kind == .separator {
                target.addItem(.separator())
                continue
            }
            let entry = NSMenuItem(title: "", action: nil, keyEquivalent: "")
            fill(entry, row, column: column ?? 0)
            target.addItem(entry)
        }
    }

    func fill(_ entry: NSMenuItem, _ row: MenuRow, column: CGFloat) {
        // The plain title is what type-to-select matches.
        entry.title = row.title
        entry.representedObject = row.command.map(Command.init)
        entry.action = row.command == nil ? nil : #selector(choose(_:))
        entry.target = self
        entry.isEnabled = row.isEnabled
        entry.state = row.checked ? .on : .off
        if row.symbol.isEmpty {
            entry.image = nil
        } else if let image = NSImage(systemSymbolName: row.symbol, accessibilityDescription: nil) {
            image.isTemplate = true // decorative: the words carry the meaning
            entry.image = image
        }
        switch row.kind {
        case .loop:
            entry.attributedTitle = rowText(row, column: column, drawn: false)
        case .action:
            entry.attributedTitle = row.detail.isEmpty ? nil : rowText(row, column: nil, drawn: false)
        case .status:
            entry.attributedTitle = NSAttributedString(string: row.title, attributes: [.font: NSFont.menuFont(ofSize: NSFont.smallSystemFontSize), .foregroundColor: NSColor.secondaryLabelColor])
        case .separator:
            break
        }
        entry.setAccessibilityLabel(row.accessibilityLabel)
        entry.setAccessibilityHelp(row.accessibilityHelp.isEmpty ? nil : row.accessibilityHelp)
        entry.toolTip = row.toolTip.isEmpty ? nil : row.toolTip
        if row.submenu.isEmpty {
            entry.submenu = nil
        } else if let open = entry.submenu, open.items.count == row.submenu.count {
            // Same shape (render checked): updated in place, never rebuilt
            // under the pointer.
            for (item, sub) in zip(open.items, row.submenu) where sub.kind != .separator { fill(item, sub, column: 0) }
        } else {
            let sub = NSMenu()
            build(sub, row.submenu, column: nil)
            entry.submenu = sub
        }
    }

    @objc func choose(_ sender: NSMenuItem) {
        guard let command = (sender.representedObject as? Command)?.value else { return }
        switch command {
        case .quit:
            NSApplication.shared.terminate(nil)
            return
        case .review(let action, let key):
            review(action, key: key)
            return
        case .reviewAll(let action):
            reviewAll(action)
            return
        case .openPanelWithChanges:
            openWithControls()
            return
        case .toggleLogin:
            toggleLogin()
            return
        default:
            break
        }
        run(command)
    }

    /// Asks a native question and returns the command of the button chosen,
    /// or nil for Cancel. Cancel is first, so it is the default and Return
    /// chooses it; Escape chooses it too. A stop is styled as destructive.
    func ask(_ confirmation: Confirmation) -> MenuCommand? {
        let alert = NSAlert()
        alert.messageText = confirmation.messageText
        alert.informativeText = confirmation.informativeText
        alert.alertStyle = confirmation.buttons.contains { $0.destructive } ? .warning : .informational
        for b in confirmation.buttons {
            let button = alert.addButton(withTitle: b.title)
            button.hasDestructiveAction = b.destructive
            if b.command == nil {
                button.keyEquivalent = "\r"
            } else {
                button.keyEquivalent = ""
            }
        }
        // Escape cancels, as in every native sheet.
        let escape = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
            if event.keyCode == 53 { NSApp.abortModal(); return nil }
            return event
        }
        NSApp.activate(ignoringOtherApps: true)
        let response = alert.runModal()
        if let escape { NSEvent.removeMonitor(escape) }
        let index = response.rawValue - NSApplication.ModalResponse.alertFirstButtonReturn.rawValue
        guard confirmation.buttons.indices.contains(index) else { return nil }
        return confirmation.buttons[index].command
    }

    func tell(_ title: String, _ text: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = text
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }

    /// Reads the review, shows it, and applies the change only if it is
    /// confirmed while the review is current. The result is said in the
    /// page's words; nothing is retried by itself.
    func review(_ action: LoopAction, key: String) {
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let reading = try? client.review(action, key: key)
            DispatchQueue.main.async { [self] in
                switch reading {
                case .none:
                    tell("keepgoing could not prepare this review", "Nothing was changed. Run keepgoing client review \(action.rawValue) --loop \(key) in a terminal to see why.")
                case .refused(let result):
                    tell("Nothing was changed", Contract.text(result.text))
                case .review(let review):
                    guard let command = ask(ConfirmationBuilder.build(review)) else { return }
                    guard ConfirmationBuilder.current(review, now: Date()) else {
                        tell("Nothing was changed", Contract.text(review.expired))
                        return
                    }
                    DispatchQueue.global(qos: .userInitiated).async { [self] in
                        let result = client.apply(command, review: review)
                        DispatchQueue.main.async { [self] in
                            if result.outcome == "done" {
                                // Seen on the next look at the menu.
                                notice = Contract.text(result.text)
                            } else {
                                tell(result.outcome == "uncertain" ? "keepgoing could not confirm the change" : "Nothing was changed", Contract.text(result.text))
                            }
                            refresh()
                        }
                    }
                }
            }
        }
    }

    /// Reads the review of a change to every loop, shows it, and applies it
    /// only if confirmed while the review is current. The result always
    /// names each loop's outcome, so a partial result is never taken for
    /// success; nothing is retried by itself.
    func reviewAll(_ action: LoopAction) {
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let reading = try? client.reviewAll(action)
            DispatchQueue.main.async { [self] in
                switch reading {
                case .none:
                    tell("keepgoing could not prepare this review", "Nothing was changed. Run keepgoing client review \(action.rawValue) --all in a terminal to see why.")
                case .refused(let result):
                    tell("Nothing was changed", Contract.text(result.text))
                case .review(let review):
                    guard let command = ask(ConfirmationBuilder.build(review)) else { return }
                    guard ConfirmationBuilder.current(review, now: Date()) else {
                        tell("Nothing was changed", Contract.text(review.expired))
                        return
                    }
                    DispatchQueue.global(qos: .userInitiated).async { [self] in
                        let result = client.applyAll(command, review: review)
                        DispatchQueue.main.async { [self] in
                            tell(Contract.text(result.text), (result.lines ?? []).map(Contract.text).joined(separator: "\n"))
                            refresh()
                        }
                    }
                }
            }
        }
    }

    /// Open at Login, as the person chose it. Nothing is registered or saved
    /// until they choose it; choosing it again removes both.
    func toggleLogin() {
        switch login.toggle(launch) {
        case .needsApp:
            tell("Open at Login needs the app", "macOS opens only an app bundle at login. Build one with integrations/macos/scripts/make-app.sh and start the menu from it; then choose Open at Login again.")
        case .turnedOn:
            notice = "Opens at login, with the same keepgoing and directories."
        case .needsApproval:
            tell("Allow keepgoing Menu to open at login", "macOS asks you to allow it first: open System Settings, General, Login Items, and turn on keepgoing Menu. Choose Open at Login again to turn it off.")
        case .turnedOff:
            notice = "No longer opens at login."
        case .failed:
            tell("Open at Login could not be changed", "macOS refused the change, so nothing was saved. You can turn keepgoing Menu on or off in System Settings, General, Login Items.")
        }
        render()
    }

    /// Opens the panel so its page can make changes. Starting one, or
    /// restarting a read-only one, is asked first, with the footer's own
    /// statement of who can then make changes. Whether the panel runs, and in
    /// which mode, is read again first, off the main thread: the menu's last
    /// read can be minutes old, and a panel may have stopped by itself since.
    func openWithControls() {
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let fresh = try? client.panelStatus()
            DispatchQueue.main.async { [self] in
                panel = fresh
                render()
                // Offered only with the footer's statement, which is always shown.
                guard let status = fresh, status.exposure != nil else {
                    tell("The keepgoing panel could not be read", "Nothing was started. Run keepgoing panel status in a terminal to see why. Your loops are not affected.")
                    return
                }
                if let confirmation = ConfirmationBuilder.openWithControls(status) {
                    guard ask(confirmation) != nil else { return }
                }
                let restart = status.running && status.changesAllowed != true
                DispatchQueue.global(qos: .userInitiated).async { [self] in
                    if restart, (try? client.open(.stopPanel)) == nil {
                        DispatchQueue.main.async { [self] in
                            tell("The keepgoing panel could not be stopped", "Run keepgoing panel stop in a terminal to see why, then keepgoing panel --allow-changes. Your loops are not affected.")
                            refresh()
                        }
                        return
                    }
                    DispatchQueue.main.async { [self] in run(.openPanelWithChanges) }
                }
            }
        }
    }

    func run(_ command: MenuCommand) {
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let failed = (try? client.open(command)) == nil
            DispatchQueue.main.async { [self] in
                if failed {
                    // The menu has closed by now, so say it where it will be
                    // seen and heard, and leave it in the menu for the next look.
                    let stopping = command == .stopPanel
                    let opener = command == .openPanelWithChanges ? "keepgoing panel --allow-changes" : "keepgoing panel"
                    notice = stopping ? "The panel could not be stopped. Run keepgoing panel stop in a terminal." : "The panel could not be opened. Run \(opener) in a terminal."
                    let alert = NSAlert()
                    alert.messageText = stopping ? "The keepgoing panel could not be stopped" : "The keepgoing panel could not be opened"
                    alert.informativeText = stopping ? "Run keepgoing panel stop in a terminal to see why. Your loops are not affected." : "Run \(opener) in a terminal to see why. Your loops are not affected."
                    NSApp.activate(ignoringOtherApps: true)
                    alert.runModal()
                }
                // The panel has just started or stopped: read its status again.
                refresh()
            }
        }
    }

    func refresh() {
        guard !loading else { return }
        loading = true
        render()
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let result: MenuReading
            var failed = false
            do {
                result = .read(try client.panelList(), at: Date())
            } catch ClientFailure.unsupported {
                result = .unsupported
                failed = true
            } catch {
                result = .unavailable
                failed = true
            }
            // Asked second, and never an error for the menu: without it the
            // stop row is simply absent.
            let status = try? client.panelStatus()
            DispatchQueue.main.async { [self] in
                reading = result
                panel = status
                failures = failed ? failures + 1 : 0
                loading = false
                scheduleNextRead()
                render()
                notifier.update(result)
                if let path = renderPath { renderMenu(to: path) }
            }
        }
    }
}

/// Boxes a command so a menu item can carry it.
final class Command: NSObject {
    let value: MenuCommand
    init(_ value: MenuCommand) { self.value = value }
}

let app = NSApplication.shared
let delegate = MenuApp(client: LocalClient(executable: URL(fileURLWithPath: launch.executable), directory: launch.registryDirectory, panelDirectory: launch.panelDirectory),
                       notifier: Notifier(enabled: launch.notifications && launch.renderMenu == nil), renderPath: launch.renderMenu, renderDark: launch.dark,
                       login: login, launch: launch)
app.setActivationPolicy(.accessory)
app.delegate = delegate
app.run()

/// The login item: this app bundle, through SMAppService. A bare executable
/// is never registered.
struct AppLoginService: LoginService {
    var bundled: Bool { Bundle.main.bundleURL.pathExtension == "app" && Bundle.main.bundleIdentifier != nil }
    var status: LoginStatus {
        guard bundled else { return .needsApp }
        switch SMAppService.mainApp.status {
        case .enabled: return .on
        case .requiresApproval: return .needsApproval
        default: return .off
        }
    }
    func register() throws { try SMAppService.mainApp.register() }
    func unregister() throws { try SMAppService.mainApp.unregister() }
}

/// The arguments a login launch is given, in this app's own preferences
/// (`defaults read local.keepgoing.menu-preview`). Written only when Open at
/// Login is ticked; deleted when it is unticked.
struct AppLoginStore: LoginStore {
    static let key = "loginArguments"
    func save(_ arguments: [String]) { UserDefaults.standard.set(arguments, forKey: AppLoginStore.key) }
    func load() -> [String]? { UserDefaults.standard.stringArray(forKey: AppLoginStore.key) }
    func clear() { UserDefaults.standard.removeObject(forKey: AppLoginStore.key) }
}
