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
        print("Native client read the real CLI contract for a disabled fixture loop")
    }
}
