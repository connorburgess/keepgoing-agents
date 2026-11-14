import XCTest
@testable import KeepgoingMenuCore

// Fakes only: these tests never touch the real login items or preferences.
final class FakeLoginService: LoginService {
    var status: LoginStatus
    var registered = 0, unregistered = 0
    var failRegister = false, failUnregister = false
    var approvalNeeded = false
    init(_ status: LoginStatus) { self.status = status }
    struct Refused: Error {}
    func register() throws {
        registered += 1
        if failRegister { throw Refused() }
        status = approvalNeeded ? .needsApproval : .on
    }
    func unregister() throws {
        unregistered += 1
        if failUnregister { throw Refused() }
        status = .off
    }
}

final class FakeLoginStore: LoginStore {
    var saved: [String]?
    func save(_ arguments: [String]) { saved = arguments }
    func load() -> [String]? { saved }
    func clear() { saved = nil }
}

final class LoginTests: XCTestCase {
    let options = LaunchOptions.parse(["--executable", "/private/example/keepgoing", "--registry-directory", "/private/example registry", "--no-notifications"])!

    func testOptionsParseAsTheyAlwaysHave() {
        XCTAssertEqual(options.executable, "/private/example/keepgoing")
        XCTAssertEqual(options.registryDirectory, "/private/example registry")
        XCTAssertFalse(options.notifications)
        XCTAssertEqual(options.loginArguments, ["--executable", "/private/example/keepgoing", "--registry-directory", "/private/example registry", "--no-notifications"])
        for bad in [[], ["--executable"], ["--executable", "relative/keepgoing"], ["--executable", "/a", "--executable", "/b"], ["--executable", "/a", "--unknown"],
                    ["--registry-directory", "/a"], ["--executable", "/a", "--dark", "--dark"]] {
            XCTAssertNil(LaunchOptions.parse(bad), "\(bad)")
        }
        // Drawing a picture of the menu is never part of a login launch.
        let render = LaunchOptions.parse(["--executable", "/a", "--render-menu", "/tmp/m.png", "--dark"])!
        XCTAssertEqual(render.loginArguments, ["--executable", "/a"])
    }

    // Off by default: nothing is registered or saved until a person ticks it,
    // and ticking saves exactly the arguments the menu was started with.
    func testTickingRegistersAndSavesUntickingRemovesBoth() {
        let service = FakeLoginService(.off), store = FakeLoginStore()
        let item = LoginItem(service: service, store: store)
        XCTAssertEqual(item.status, .off)
        XCTAssertEqual(service.registered, 0)
        XCTAssertNil(store.saved)
        XCTAssertEqual(item.toggle(options), .turnedOn)
        XCTAssertEqual(service.registered, 1)
        XCTAssertEqual(store.saved, options.loginArguments)
        XCTAssertEqual(item.toggle(options), .turnedOff)
        XCTAssertEqual(service.unregistered, 1)
        XCTAssertNil(store.saved)
        XCTAssertEqual(item.status, .off)
    }

    func testABareExecutableNeverRegisters() {
        let service = FakeLoginService(.needsApp), store = FakeLoginStore()
        let item = LoginItem(service: service, store: store)
        XCTAssertEqual(item.toggle(options), .needsApp)
        XCTAssertEqual(service.registered + service.unregistered, 0)
        XCTAssertNil(store.saved)
        XCTAssertNil(item.launch([]), "a bare executable started with nothing prints its usage")
    }

    func testRefusalsLeaveNothingBehind() {
        let service = FakeLoginService(.off), store = FakeLoginStore()
        service.failRegister = true
        let item = LoginItem(service: service, store: store)
        if case .failed = item.toggle(options) {} else { XCTFail("not failed") }
        XCTAssertNil(store.saved, "a refused registration keeps nothing")
        // Unregistering refused: it is still registered, so its arguments stay.
        let on = FakeLoginService(.on), kept = FakeLoginStore()
        kept.saved = options.loginArguments
        on.failUnregister = true
        if case .failed = LoginItem(service: on, store: kept).toggle(options) {} else { XCTFail("not failed") }
        XCTAssertEqual(kept.saved, options.loginArguments)
    }

    // macOS may ask the person to allow it first; choosing it again turns it off.
    func testApprovalInSystemSettings() {
        let service = FakeLoginService(.off), store = FakeLoginStore()
        service.approvalNeeded = true
        let item = LoginItem(service: service, store: store)
        XCTAssertEqual(item.toggle(options), .needsApproval)
        XCTAssertNotNil(store.saved)
        XCTAssertEqual(item.toggle(options), .turnedOff)
        XCTAssertNil(store.saved)
    }

    // A launch with no arguments uses the saved ones only while the login
    // item is on, and only if they still parse; given arguments always win.
    func testALoginLaunchUsesTheSavedArgumentsOnlyWhileOn() {
        let service = FakeLoginService(.on), store = FakeLoginStore()
        store.saved = options.loginArguments
        let item = LoginItem(service: service, store: store)
        XCTAssertEqual(item.launch([]), options)
        XCTAssertEqual(item.launch(["--executable", "/other"])?.executable, "/other")
        service.status = .off
        XCTAssertNil(item.launch([]))
        service.status = .needsApproval
        XCTAssertNil(item.launch([]))
        service.status = .on
        store.saved = ["--executable", "relative"]
        XCTAssertNil(item.launch([]))
        store.saved = ["--executable", "/a", "--render-menu", "/tmp/m.png"]
        XCTAssertNil(item.launch([]), "a login launch never draws a picture and quits")
        store.saved = nil
        XCTAssertNil(item.launch([]))
    }

    // The row: checked while on, said plainly otherwise, and never a row that
    // looks choosable but is not.
    func testTheRow() {
        let now = Date(timeIntervalSince1970: 1_900_000_000)
        func row(_ status: LoginStatus?) -> MenuRow? { MenuBuilder.build(.unavailable, now: now, login: status).rows.first { $0.command == .toggleLogin } }
        XCTAssertNil(row(nil))
        XCTAssertEqual(row(.off)?.checked, false)
        XCTAssertEqual(row(.on)?.checked, true)
        XCTAssertEqual(row(.needsApproval)?.checked, true)
        XCTAssertEqual(row(.needsApproval)?.detail, "allow in System Settings")
        XCTAssertEqual(row(.needsApp)?.checked, false)
        XCTAssertEqual(row(.needsApp)?.detail, "needs the app")
        XCTAssertEqual(row(.off)?.title, "Open at Login")
        XCTAssertEqual(row(.off)?.accessibilityHelp, "Opens this menu when you log in, with the same keepgoing and directories. Off until you choose it.")
        XCTAssertNil(MenuBuilder.arguments(.toggleLogin), "it runs no keepgoing command")
        for status in [LoginStatus.needsApp, .off, .on, .needsApproval] {
            let rows = MenuBuilder.build(.unavailable, now: now, login: status).rows.filter { $0.kind != .separator }
            XCTAssertEqual(rows.filter { !$0.isEnabled }.count, 1, "only the status line cannot be chosen")
            // Just before Quit.
            let titles = rows.map(\.title)
            XCTAssertEqual(titles[titles.firstIndex(of: "Quit keepgoing Menu")! - 1], "Open at Login")
        }
    }
}
