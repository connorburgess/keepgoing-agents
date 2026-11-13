import Foundation

@main struct ContractSmoke {
    static func main() throws {
        let args = CommandLine.arguments
        guard args.count == 3 else { fatalError("expected CLI and registry directory") }
        let client = LocalClient(executable: URL(fileURLWithPath: args[1]), directory: args[2])
        let entries = try client.registry()
        guard entries.count == 1, entries[0].loop == "example-menu-loop" else { fatalError("registry contract mismatch") }
        let report = try client.status(entries[0], privateText: false)
        let lines = Contract.lines(report, privateText: false)
        guard lines.contains("Supervision: disabled"), lines.contains("Goal: not recorded"),
              lines.contains("Next normal check: unknown"), lines.contains("External scheduler: not inspected") else {
            fatalError("status projection mismatch")
        }
        // The menu's own read: one loop, in the panel's words, with a key the
        // opener accepts. Nothing is opened here: that would start a server.
        let list = try client.panelList()
        guard list.loops.count == 1, list.loops[0].name == "example-menu-loop", list.loops[0].phrase == "Off", list.loops[0].state == "off",
              list.needsYou == 0, MenuBuilder.arguments(.openLoop(key: list.loops[0].key)) != nil else {
            fatalError("panel list contract mismatch")
        }
        let model = MenuBuilder.build(.read(list, at: Date()), now: Date())
        guard model.badge.isEmpty, model.rows.contains(where: { $0.kind == .loop && $0.title == "example-menu-loop" && $0.detail == "Off" }) else {
            fatalError("menu model mismatch")
        }
        print("Native client read the real CLI contract for a disabled fixture loop")
    }
}
