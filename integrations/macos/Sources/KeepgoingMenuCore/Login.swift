import Foundation

// Open at Login: an explicit choice, off by default. Ticking it registers the
// menu's app bundle as a login item (SMAppService.mainApp, in the app) and
// saves the arguments the menu was started with, because macOS starts a login
// item with none. Unticking unregisters it and deletes those arguments.
// Nothing is registered or saved until a person ticks it, and a bare
// executable cannot be a login item: it says it needs the app bundle.
// The decisions are here, behind two small protocols, so tests use fakes and
// never touch the real login items or preferences.

/// Whether the menu opens at login, as macOS reports it.
public enum LoginStatus: Equatable {
    /// A bare executable, not the app bundle make-app.sh builds.
    case needsApp
    case off
    case on
    /// Registered, but the person has to allow it in System Settings, Login
    /// Items, before it opens at login.
    case needsApproval
}

/// The login item itself: SMAppService.mainApp in the app.
public protocol LoginService {
    var status: LoginStatus { get }
    func register() throws
    func unregister() throws
}

/// Where the launch arguments for a login are kept: the app's own
/// preferences in the app.
public protocol LoginStore {
    func save(_ arguments: [String])
    func load() -> [String]?
    func clear()
}

/// The menu's command-line options, parsed in one place so a saved login
/// launch is held to exactly the same rules as one typed by a person.
public struct LaunchOptions: Equatable {
    public var executable: String
    public var registryDirectory: String? = nil
    public var panelDirectory: String? = nil
    public var renderMenu: String? = nil
    public var dark = false
    public var notifications = true

    static let paths = ["--executable", "--registry-directory", "--panel-directory", "--render-menu"]

    /// nil for anything but known options, each at most once, with absolute
    /// paths, and an executable.
    public static func parse(_ args: [String]) -> LaunchOptions? {
        var values: [String: String] = [:]
        var dark = false, notifications = true
        var index = 0
        while index < args.count {
            let name = args[index]
            if name == "--dark", !dark {
                dark = true
                index += 1
            } else if name == "--no-notifications", notifications {
                notifications = false
                index += 1
            } else if paths.contains(name), values[name] == nil, args.indices.contains(index + 1), args[index + 1].hasPrefix("/") {
                values[name] = args[index + 1]
                index += 2
            } else {
                return nil
            }
        }
        guard let executable = values["--executable"] else { return nil }
        return LaunchOptions(executable: executable, registryDirectory: values["--registry-directory"], panelDirectory: values["--panel-directory"],
                             renderMenu: values["--render-menu"], dark: dark, notifications: notifications)
    }

    /// What a login launch is given: the same options, without drawing a
    /// picture of the menu.
    public var loginArguments: [String] {
        var out = ["--executable", executable]
        if let registryDirectory { out += ["--registry-directory", registryDirectory] }
        if let panelDirectory { out += ["--panel-directory", panelDirectory] }
        if !notifications { out.append("--no-notifications") }
        return out
    }
}

/// What choosing Open at Login came to.
public enum LoginOutcome: Equatable {
    /// A bare executable: nothing was registered or saved.
    case needsApp
    case turnedOn
    /// Registered; the person has to allow it in System Settings.
    case needsApproval
    case turnedOff
    /// macOS refused; what was saved for it is removed again.
    case failed(String)
}

public struct LoginItem {
    let service: LoginService
    let store: LoginStore
    public init(service: LoginService, store: LoginStore) { self.service = service; self.store = store }

    public var status: LoginStatus { service.status }

    /// Turns opening at login on or off, as the person chose. `options` are
    /// the ones this menu was started with; they are what a login launch is
    /// given.
    public func toggle(_ options: LaunchOptions) -> LoginOutcome {
        switch service.status {
        case .needsApp:
            return .needsApp
        case .on, .needsApproval:
            do { try service.unregister() } catch { return .failed(String(describing: error)) }
            store.clear()
            return .turnedOff
        case .off:
            store.save(options.loginArguments)
            do { try service.register() } catch {
                store.clear()
                return .failed(String(describing: error))
            }
            return service.status == .needsApproval ? .needsApproval : .turnedOn
        }
    }

    /// The options for a launch with no arguments: those saved when Open at
    /// Login was ticked, only while it is on, and only if they still parse.
    /// A launch with arguments always uses its own.
    public func launch(_ given: [String]) -> LaunchOptions? {
        if !given.isEmpty { return LaunchOptions.parse(given) }
        guard service.status == .on, let saved = store.load(), let options = LaunchOptions.parse(saved), options.renderMenu == nil else { return nil }
        return options
    }
}
