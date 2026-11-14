import Foundation
import KeepgoingMenuCore
import UserNotifications

/// Posts the notifications OverdueNotices decides on, from the reads the
/// menu already makes (when it starts, when it is opened, and on its timer).
///
/// macOS posts notifications only for an app bundle; asking from a bare
/// executable ends the process. So nothing here touches the notification
/// API unless the menu runs from a bundle (scripts/make-app.sh builds one).
/// Without one, or with --no-notifications, the menu works as before.
final class Notifier {
    private var rule = OverdueNotices()
    private let center: UNUserNotificationCenter?

    init(enabled: Bool) {
        guard enabled else {
            center = nil
            return
        }
        guard Bundle.main.bundleIdentifier != nil else {
            center = nil
            fputs("KeepgoingMenu: not running from an app bundle, so it cannot post notifications; build one with integrations/macos/scripts/make-app.sh\n", stderr)
            return
        }
        center = UNUserNotificationCenter.current()
        // macOS asks the person once; if they decline, nothing is posted.
        center?.requestAuthorization(options: [.alert]) { _, _ in }
    }

    /// Called on the main queue after each read.
    func update(_ reading: MenuReading) {
        guard let center else { return }
        for notice in rule.next(reading) {
            let content = UNMutableNotificationContent()
            content.title = notice.title
            content.body = notice.body
            center.add(UNNotificationRequest(identifier: notice.identifier, content: content, trigger: nil))
        }
    }
}
