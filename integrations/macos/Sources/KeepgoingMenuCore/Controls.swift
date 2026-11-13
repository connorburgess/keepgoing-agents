import Foundation

// Pause, stop and resume of one loop from the menu. Every sentence a
// confirmation shows comes from `keepgoing client review`, which is built by
// the code that builds the panel page's review, so the menu and the page
// cannot say different things. Confirming runs `keepgoing client apply` with
// the review's `expect` value, so it acts only on the control record that
// was reviewed. Nothing is retried by itself.

public enum LoopAction: String, Equatable, CaseIterable {
    case pause, stop, resume
}

/// How a stop may be made, when the CLI offers a choice. A CLI that offers
/// none (today's) stops the conversation only, at its next check.
public struct StopOptions: Decodable, Equatable {
    /// How many lanes a stop with lanes would stop.
    public let lanes: Int
    /// Whether the stop can be carried out now rather than at the next check.
    public let immediately: Bool
    public init(lanes: Int, immediately: Bool) { self.lanes = lanes; self.immediately = immediately }
}

/// The review `keepgoing client review ACTION --loop KEY --json` prints.
public struct Review: Decodable, Equatable {
    public let action: String
    public let key: String
    public let title: String
    public let lines: [String]
    public let confirm: String
    public let expect: String
    public let expiresAt: Date
    public let lost: String
    public let refused: String
    public let expired: String
    public var stop: StopOptions? = nil
}

/// What a change came to, in the page's words. `outcome` is done, refused,
/// review_again or uncertain.
public struct ChangeResult: Decodable, Equatable {
    public let outcome: String
    public let text: String
    public var retry: Bool? = nil
    public init(outcome: String, text: String, retry: Bool? = nil) { self.outcome = outcome; self.text = text; self.retry = retry }
}

private struct ReviewReply: Decodable {
    let schemaVersion: Int
    var preview: Review? = nil
    var result: ChangeResult? = nil
}

/// Either a review to confirm, or the reason there is none.
public enum ReviewReading: Equatable {
    case review(Review)
    case refused(ChangeResult)
}

extension Contract {
    static let outcomes: Set<String> = ["done", "refused", "review_again", "uncertain"]
    static func validExpect(_ value: String) -> Bool {
        value.range(of: "^[0-9a-f]{64}$", options: .regularExpression) != nil
    }

    private static func reply(_ data: Data) throws -> ReviewReply {
        guard data.count <= 65_536 else { throw ClientFailure.outputLimit }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { d in
            let raw = try d.singleValueContainer().decode(String.self)
            guard let date = Contract.date(raw) else { throw ClientFailure.invalidResponse }
            return date
        }
        let r: ReviewReply
        do { r = try decoder.decode(ReviewReply.self, from: data) } catch { throw ClientFailure.invalidResponse }
        guard r.schemaVersion == 1 else { throw ClientFailure.unsupported }
        return r
    }

    private static func validResult(_ r: ChangeResult) -> Bool {
        outcomes.contains(r.outcome) && !text(r.text).isEmpty
    }

    /// A review for exactly the action and loop asked about, or a refusal.
    public static func review(_ data: Data, exit: Int32, action: LoopAction, key: String) throws -> ReviewReading {
        let r = try reply(data)
        if let p = r.preview, r.result == nil, exit == 0 {
            guard p.action == action.rawValue, p.key == key, validExpect(p.expect), !text(p.title).isEmpty, !text(p.confirm).isEmpty,
                  p.lines.count <= 40, p.lines.allSatisfy({ !text($0).isEmpty }), !text(p.lost).isEmpty, !text(p.refused).isEmpty, !text(p.expired).isEmpty,
                  (p.stop?.lanes ?? 0) >= 0, (p.stop?.lanes ?? 0) <= 10_000, action == .stop || p.stop == nil else {
                throw ClientFailure.invalidResponse
            }
            return .review(p)
        }
        if let res = r.result, r.preview == nil, exit != 0, validResult(res) {
            return .refused(res)
        }
        throw ClientFailure.invalidResponse
    }

    /// What client apply said. Its exit code is not needed: the outcome says it.
    public static func applied(_ data: Data) throws -> ChangeResult {
        let r = try reply(data)
        guard let res = r.result, r.preview == nil, validResult(res) else { throw ClientFailure.invalidResponse }
        return res
    }
}

/// One button of a confirmation, in the order NSAlert is given them: the
/// first is the default and the rightmost.
public struct ConfirmButton: Equatable {
    public let title: String
    /// Styled as destructive. Only stopping is.
    public let destructive: Bool
    /// What confirming runs; nil for Cancel.
    public let command: MenuCommand?
}

/// The native confirmation for one change: the review's title, its lines,
/// and Cancel as the default.
public struct Confirmation: Equatable {
    public let messageText: String
    public let informativeText: String
    public let buttons: [ConfirmButton]
    public var defaultButton: ConfirmButton { buttons[0] }
}

public enum ConfirmationBuilder {
    /// The buttons for a stop: the conversation only, and, when the CLI
    /// offers it, the conversation and its lanes. A stop is carried out now
    /// whenever the CLI can; otherwise it waits for the loop's next check and
    /// stops the conversation only, which the review itself says.
    public static func build(_ review: Review) -> Confirmation {
        let name = Contract.text(review.confirm)
        var buttons = [ConfirmButton(title: "Cancel", destructive: false, command: nil)]
        guard let action = LoopAction(rawValue: review.action) else {
            return Confirmation(messageText: Contract.text(review.title), informativeText: "", buttons: buttons)
        }
        let lines = review.lines.map(Contract.text)
        if action == .stop {
            let immediately = review.stop?.immediately ?? false
            if let lanes = review.stop?.lanes, lanes > 0 {
                buttons.append(ConfirmButton(title: "Stop the Conversation", destructive: true,
                                             command: .apply(.stop, key: review.key, expect: review.expect, lanes: false, immediately: immediately)))
                buttons.append(ConfirmButton(title: lanes == 1 ? "Stop It and Its Lane" : "Stop It and Its \(lanes) Lanes", destructive: true,
                                             command: .apply(.stop, key: review.key, expect: review.expect, lanes: true, immediately: immediately)))
            } else {
                buttons.append(ConfirmButton(title: name, destructive: true,
                                             command: .apply(.stop, key: review.key, expect: review.expect, lanes: false, immediately: immediately)))
            }
        } else {
            buttons.append(ConfirmButton(title: name, destructive: false,
                                         command: .apply(action, key: review.key, expect: review.expect, lanes: false, immediately: false)))
        }
        return Confirmation(messageText: Contract.text(review.title), informativeText: lines.joined(separator: "\n\n"), buttons: buttons)
    }

    /// Whether a review may still be confirmed. Past its time the change is
    /// not run, and the review's own words say to review again.
    public static func current(_ review: Review, now: Date) -> Bool { now < review.expiresAt }

    /// The confirmation for opening the panel with controls. A read-only panel
    /// that is running is stopped and started again, which the text says;
    /// one that already allows changes needs no confirmation (nil).
    public static func openWithControls(_ status: PanelStatus) -> Confirmation? {
        guard let exposure = status.exposure, !Contract.text(exposure).isEmpty else { return nil }
        if status.running && status.changesAllowed == true { return nil }
        let restart = status.running
        let what = "The panel's page will be able to pause, stop and resume loops, each after a review. " + Contract.text(exposure) + " Stop the Panel Page turns them off."
        return Confirmation(
            messageText: restart ? "Restart the panel with controls?" : "Open the panel with controls?",
            informativeText: restart ? "The read-only panel that is running is stopped first; a page open on it stops working. " + what : what,
            buttons: [ConfirmButton(title: "Cancel", destructive: false, command: nil),
                      ConfirmButton(title: restart ? "Restart with Controls" : "Open with Controls", destructive: false, command: .openPanelWithChanges)])
    }
}
