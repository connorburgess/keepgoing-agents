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

/// How a stop may be made, when the CLI offers a choice: `stop` in the
/// review. A CLI that offers none (today's) stops the conversation only, at
/// its next check. The words on each button come from the CLI, never from
/// this app.
///
/// Read so that the next additive change cannot break Stop: an option this
/// menu does not fully understand (a field it does not know, or a known
/// field of the wrong type) is not offered, and the rest of the review is
/// still shown. It is never offered without the field, because its button
/// would then do less than its words say.
public struct StopOptions: Decodable, Equatable {
    /// Whether the stop is carried out at once (`--immediately`) rather than
    /// at the loop's next check. When it is not a boolean, no option is
    /// offered and the one plain stop is.
    public let immediately: Bool
    /// Every option the CLI gave, understood or not, in order.
    public let options: [StopOption]
    public init(immediately: Bool, options: [StopOption]) { self.immediately = immediately; self.options = options }

    private enum Keys: String, CodingKey { case immediately, options }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        let now = try? c.decode(Bool.self, forKey: .immediately)
        immediately = now ?? false
        var list: [StopOption] = []
        // Without a readable `immediately` no option is offered: its words
        // may promise a stop at once that would then wait.
        if now != nil, var items = try? c.nestedUnkeyedContainer(forKey: .options) {
            while !items.isAtEnd && list.count < 16 {
                // An entry that is not even an object is skipped like any
                // option this menu does not understand.
                if let option = try? items.decode(StopOption.self) {
                    list.append(option)
                } else if (try? items.decode(Skipped.self)) == nil {
                    break
                }
            }
        }
        options = list
    }

    /// The options offered, in order, at most four: those understood, with
    /// words, that drop queued messages only together with the lanes, and
    /// not alike in what they do as an earlier one.
    public var offered: [StopOption] {
        var seen = Set<String>(), out: [StopOption] = []
        for o in options where o.understood && !Contract.text(o.label).isEmpty && Contract.text(o.label).count <= 60 && (!o.dropsQueued || o.lanes) {
            if out.count < 4, seen.insert("\(o.lanes) \(o.dropsQueued)").inserted { out.append(o) }
        }
        return out
    }
}

/// Anything, read only to step past it.
private struct Skipped: Decodable {
    init(from decoder: Decoder) throws {}
}

private struct AnyKey: CodingKey {
    let stringValue: String
    var intValue: Int? { nil }
    init(stringValue: String) { self.stringValue = stringValue }
    init?(intValue: Int) { nil }
}

public struct StopOption: Decodable, Equatable {
    public let label: String
    public let lanes: Bool
    /// Also deletes the messages queued in lanes that have not started
    /// (`--drop-queued`), whose text is then lost. Absent means false; only
    /// the option that says so passes it.
    public let dropQueued: Bool?
    /// False when the option has a field this menu does not know, or a
    /// known field of the wrong type: it is then not offered.
    public let understood: Bool
    public var dropsQueued: Bool { dropQueued == true }
    public init(label: String, lanes: Bool, dropQueued: Bool? = nil) {
        self.label = label; self.lanes = lanes; self.dropQueued = dropQueued; understood = true
    }

    static let known: Set<String> = ["label", "lanes", "dropQueued"]
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: AnyKey.self)
        var ok = c.allKeys.allSatisfy { StopOption.known.contains($0.stringValue) }
        let label = try? c.decode(String.self, forKey: AnyKey(stringValue: "label"))
        let lanes = try? c.decode(Bool.self, forKey: AnyKey(stringValue: "lanes"))
        var drop: Bool? = nil
        if c.contains(AnyKey(stringValue: "dropQueued")) {
            drop = try? c.decode(Bool.self, forKey: AnyKey(stringValue: "dropQueued"))
            ok = ok && drop != nil
        }
        ok = ok && label != nil && lanes != nil
        self.label = label ?? ""
        self.lanes = lanes ?? false
        self.dropQueued = drop
        self.understood = ok
    }
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
    /// A review's expect value: its expiry, the record's digest and a check
    /// over the loop and the action. Passed back only when it matches.
    static func validExpect(_ value: String) -> Bool {
        value.range(of: "^r1\\.[0-9]{1,12}\\.[0-9a-f]{64}\\.[0-9a-f]{32}$", options: .regularExpression) != nil
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
                  action == .stop || p.stop == nil else {
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
    /// The buttons for a stop: one per option the CLI offers, in its words
    /// (the conversation, or it and its lanes), carried out at once when the
    /// CLI says it can be; otherwise the one stop, which waits for the loop's
    /// next check and stops the conversation only, as the review itself says.
    public static func build(_ review: Review) -> Confirmation {
        let name = Contract.text(review.confirm)
        var buttons = [ConfirmButton(title: "Cancel", destructive: false, command: nil)]
        guard let action = LoopAction(rawValue: review.action) else {
            return Confirmation(messageText: Contract.text(review.title), informativeText: "", buttons: buttons)
        }
        let lines = review.lines.map(Contract.text)
        if action == .stop {
            let immediately = review.stop?.immediately ?? false
            if let options = review.stop?.offered, !options.isEmpty {
                for option in options {
                    buttons.append(ConfirmButton(title: Contract.text(option.label), destructive: true,
                                                 command: .apply(.stop, key: review.key, expect: review.expect, lanes: option.lanes, immediately: immediately, dropQueued: option.dropsQueued)))
                }
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
