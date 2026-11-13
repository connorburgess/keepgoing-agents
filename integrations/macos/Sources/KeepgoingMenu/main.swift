import AppKit
import KeepgoingMenuCore

// Development entry point: no installation, persistence, startup registration or
// automatic executable discovery. The user explicitly chooses a trusted CLI.
let args = Array(CommandLine.arguments.dropFirst())
let options = ["--executable", "--registry-directory", "--panel-directory", "--render-menu"]
var values: [String: String] = [:]
var dark = false
var index = 0
var valid = true
while index < args.count {
    let name = args[index]
    if name == "--dark", !dark {
        dark = true
        index += 1
    } else if options.contains(name), values[name] == nil, args.indices.contains(index + 1), args[index + 1].hasPrefix("/") {
        values[name] = args[index + 1]
        index += 2
    } else {
        valid = false
        break
    }
}
guard valid, let path = values["--executable"] else {
    fputs("Usage: KeepgoingMenu --executable /absolute/path/to/keepgoing [--registry-directory /absolute/path] [--panel-directory /absolute/path]\n       KeepgoingMenu ... --render-menu /absolute/path.png [--dark]   draw the menu's rows to a picture and quit\n", stderr)
    exit(2)
}

/// A loop's row: its name, then its state in the secondary colour, with the
/// states of every loop starting at the same place so they read as a column.
/// Both colours follow light and dark.
func loopText(_ row: MenuRow, column: CGFloat) -> NSAttributedString {
    let style = NSMutableParagraphStyle()
    style.tabStops = [NSTextTab(textAlignment: .left, location: column)]
    style.lineBreakMode = .byTruncatingTail
    let text = NSMutableAttributedString(string: row.title, attributes: [.font: NSFont.menuFont(ofSize: 0), .foregroundColor: NSColor.labelColor, .paragraphStyle: style])
    text.append(NSAttributedString(string: "\t" + row.detail, attributes: [.font: NSFont.menuFont(ofSize: 0), .foregroundColor: NSColor.secondaryLabelColor, .paragraphStyle: style]))
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
    let renderPath: String?
    let renderDark: Bool
    let menu = NSMenu()
    var item: NSStatusItem!
    var reading: MenuReading = .notRead
    var commands: [MenuCommand] = []
    var notice: String?
    var loading = false
    var revision = 0
    init(client: LocalClient, renderPath: String?, renderDark: Bool) {
        self.client = client
        self.renderPath = renderPath
        self.renderDark = renderDark
    }

    // Verification aid: once the first read has finished, draw the menu bar
    // item and the menu's rows into a picture, with the fonts, symbols and
    // colours the real menu uses, and quit. It never opens the menu or looks
    // at the screen, so it is a drawing of the rows and not a screen capture.
    func renderMenu(to path: String) {
        let model = MenuBuilder.build(reading, now: Date())
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
                stack.addArrangedSubview(line(image: NSImage(systemSymbolName: row.symbol, accessibilityDescription: nil), text: loopText(row, column: column)))
            case .action:
                let text = NSAttributedString(string: row.title, attributes: [.font: NSFont.menuFont(ofSize: 0), .foregroundColor: NSColor.labelColor])
                stack.addArrangedSubview(line(image: row.symbol.isEmpty ? nil : NSImage(systemSymbolName: row.symbol, accessibilityDescription: nil), text: text))
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
        // menu is first opened. After that it reads only when the menu opens
        // or Refresh is chosen: there is no timer.
        refresh()
    }
    func menuWillOpen(_ menu: NSMenu) { refresh() }
    func menuDidClose(_ menu: NSMenu) {
        // A notice is shown for one opening of the menu.
        if notice != nil { notice = nil; render() }
    }

    func render() {
        let model = MenuBuilder.build(reading, now: Date(), notice: notice, refreshing: loading)
        if let button = item.button {
            button.title = model.badge.isEmpty ? "" : " " + model.badge
            button.toolTip = model.iconAccessibilityLabel
            button.setAccessibilityLabel(model.iconAccessibilityLabel)
        }
        menu.removeAllItems()
        commands = []
        let column = stateColumn(model.rows)
        for row in model.rows {
            if row.kind == .separator {
                menu.addItem(.separator())
                continue
            }
            let entry = NSMenuItem(title: row.title, action: nil, keyEquivalent: "")
            if let command = row.command {
                entry.action = #selector(choose(_:))
                entry.target = self
                entry.tag = commands.count
                commands.append(command)
            }
            entry.isEnabled = row.isEnabled
            if !row.symbol.isEmpty, let image = NSImage(systemSymbolName: row.symbol, accessibilityDescription: nil) {
                image.isTemplate = true
                entry.image = image
            }
            switch row.kind {
            case .loop:
                entry.attributedTitle = loopText(row, column: column)
            case .status:
                entry.attributedTitle = NSAttributedString(string: row.title, attributes: [.font: NSFont.menuFont(ofSize: NSFont.smallSystemFontSize), .foregroundColor: NSColor.secondaryLabelColor])
            default:
                break
            }
            entry.setAccessibilityLabel(row.accessibilityLabel)
            if !row.toolTip.isEmpty { entry.toolTip = row.toolTip }
            menu.addItem(entry)
        }
    }

    @objc func choose(_ sender: NSMenuItem) {
        guard commands.indices.contains(sender.tag) else { return }
        let command = commands[sender.tag]
        switch command {
        case .refresh:
            refresh()
        case .quit:
            NSApplication.shared.terminate(nil)
        case .openPanel, .openLoop:
            DispatchQueue.global(qos: .userInitiated).async { [self] in
                let failed = (try? client.open(command)) == nil
                DispatchQueue.main.async { [self] in
                    notice = failed ? "The panel could not be opened. Run keepgoing panel in a terminal." : nil
                    render()
                }
            }
        }
    }

    func refresh() {
        guard !loading else { return }
        loading = true
        revision += 1
        let current = revision
        render()
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            let result: MenuReading
            do {
                result = .read(try client.panelList(), at: Date())
            } catch ClientFailure.unsupported {
                result = .unsupported
            } catch {
                result = .unavailable
            }
            DispatchQueue.main.async { [self] in
                guard revision == current else { return }
                reading = result
                loading = false
                render()
                if let path = renderPath { renderMenu(to: path) }
            }
        }
    }
}
let app = NSApplication.shared
let delegate = MenuApp(client: LocalClient(executable: URL(fileURLWithPath: path), directory: values["--registry-directory"], panelDirectory: values["--panel-directory"]), renderPath: values["--render-menu"], renderDark: dark)
app.setActivationPolicy(.accessory)
app.delegate = delegate
app.run()
