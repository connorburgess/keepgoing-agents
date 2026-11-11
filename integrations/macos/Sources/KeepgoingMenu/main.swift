import AppKit
import KeepgoingMenuCore

// Development entry point: no installation, persistence, startup registration or
// automatic executable discovery. The user explicitly chooses a trusted CLI.
let args = Array(CommandLine.arguments.dropFirst())
func option(_ name: String) -> String? {
    guard let i = args.firstIndex(of: name), args.indices.contains(i + 1) else { return nil }
    return args[i + 1]
}
let names = stride(from: 0, to: args.count, by: 2).map { args[$0] }
guard [2, 4].contains(args.count), Set(names).count == names.count,
      names.allSatisfy({ ["--executable", "--registry-directory"].contains($0) }),
      let path = option("--executable"), path.hasPrefix("/") else {
    fputs("Usage: KeepgoingMenu --executable /absolute/path/to/keepgoing [--registry-directory /absolute/path]\n", stderr)
    exit(2)
}
let directory = option("--registry-directory")
if let directory, !directory.hasPrefix("/") { exit(2) }

final class MenuApp: NSObject, NSApplicationDelegate, NSMenuDelegate {
    let client: LocalClient
    let menu = NSMenu()
    var item: NSStatusItem!
    var entries: [Registration] = []
    var selected: Registration?
    var details = ["Open the menu to read registered loops"]
    var privateText = false
    var loading = false
    var revision = 0
    init(client: LocalClient) { self.client = client }

    func applicationDidFinishLaunching(_ notification: Notification) {
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.button?.image = NSImage(systemSymbolName: "arrow.triangle.2.circlepath", accessibilityDescription: "Keepgoing")
        item.button?.toolTip = "Keepgoing — read-only loop status"
        item.menu = menu
        menu.autoenablesItems = false
        menu.delegate = self
        render()
    }
    func menuWillOpen(_ menu: NSMenu) { refresh() }
    func line(_ text: String) {
        let row = NSMenuItem(title: Contract.text(text), action: nil, keyEquivalent: "")
        row.isEnabled = false
        menu.addItem(row)
    }
    @discardableResult func action(_ title: String, _ selector: Selector) -> NSMenuItem {
        let row = NSMenuItem(title: title, action: selector, keyEquivalent: "")
        row.target = self
        menu.addItem(row)
        return row
    }
    func render() {
        menu.removeAllItems()
        line(selected.map { "Keepgoing: \($0.loop)" } ?? "Keepgoing")
        details.forEach(line)
        menu.addItem(.separator())
        if !entries.isEmpty {
            let chooser = NSMenu()
            chooser.autoenablesItems = false
            for entry in entries {
                let row = NSMenuItem(title: Contract.text(entry.loop) + " · " + String(entry.id.suffix(8)), action: #selector(selectLoop(_:)), keyEquivalent: "")
                row.target = self
                row.representedObject = entry.id
                row.state = selected?.id == entry.id ? .on : .off
                row.isEnabled = !loading
                chooser.addItem(row)
            }
            let row = NSMenuItem(title: "Choose loop", action: nil, keyEquivalent: "")
            row.submenu = chooser
            menu.addItem(row)
        }
        let privacy = action("Show private goal text", #selector(togglePrivacy))
        privacy.state = privateText ? .on : .off
        privacy.isEnabled = !loading
        action("Refresh", #selector(refresh)).isEnabled = !loading
        line("Read-only preview; controls and installation are planned")
        action("Quit Keepgoing Menu", #selector(quit))
    }
    @objc func selectLoop(_ sender: NSMenuItem) {
        guard !loading else { return }
        selected = entries.first { $0.id == sender.representedObject as? String }
        refresh()
    }
    @objc func togglePrivacy() {
        guard !loading else { return }
        privateText.toggle()
        details = ["Refreshing…"] // Never retain private text after hiding it.
        refresh()
    }
    @objc func quit() { NSApplication.shared.terminate(nil) }
    @objc func refresh() {
        guard !loading else { return }
        loading = true
        revision += 1
        let current = revision
        let id = selected?.id
        let showPrivate = privateText
        details = ["Reading local evidence…"]
        render()
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            var found: [Registration] = []
            var choice: Registration?
            var lines: [String]
            do {
                found = try client.registry()
                choice = found.first { $0.id == id } ?? (id == nil ? found.first : nil)
                if let entry = choice {
                    let report = try client.status(entry, privateText: showPrivate)
                    lines = Contract.lines(report, privateText: showPrivate)
                } else {
                    lines = [id == nil ? "No registered loops" : "Selected registration is no longer available", "Register loops explicitly with keepgoing registry add"]
                }
            } catch ClientFailure.unsupported {
                lines = ["Unsupported CLI response version", "Use a compatible keepgoing development build"]
            } catch {
                lines = ["Status unavailable; refresh to try again", "Check the selected CLI and registry with keepgoing doctor"]
            }
            DispatchQueue.main.async { [self] in
                guard revision == current else { return }
                entries = found
                selected = choice
                details = lines
                loading = false
                render()
            }
        }
    }
}
let app = NSApplication.shared
let delegate = MenuApp(client: LocalClient(executable: URL(fileURLWithPath: path), directory: directory))
app.setActivationPolicy(.accessory)
app.delegate = delegate
app.run()
