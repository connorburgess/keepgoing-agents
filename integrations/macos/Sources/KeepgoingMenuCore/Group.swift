import Foundation

// Pause all and stop all: one review of every loop from `keepgoing client review pause
// --all --json`, one native confirmation, then `keepgoing client apply pause
// --all --expect KEY=VALUE…`, which changes each loop on its own, only if its
// control record is still the one reviewed. Every sentence comes from the
// CLI, which builds it with the code the panel page uses: a line per loop
// (up to eight, then "And N more."), the total and what pausing does. The
// result names each loop's outcome; a partial result never reads as success.

/// One loop's expect value, passed back to `client apply --all`.
public struct LoopExpect: Equatable {
    public let key: String
    public let expect: String
    public init(key: String, expect: String) { self.key = key; self.expect = expect }
}

/// The review `client review pause --all --json` prints.
public struct GroupReview: Decodable, Equatable {
    public struct Loop: Decodable, Equatable {
        public let key: String
        public let name: String
        /// Set for each loop the change is made to.
        public var expect: String? = nil
    }
    public let action: String
    public let title: String
    public let lines: [String]
    public let confirm: String
    public let expiresAt: Date
    public let lost: String
    public let refused: String
    public let expired: String
    public let loops: [Loop]
    /// For a stop: the options of the whole, read as a single stop's are.
    public var stop: StopOptions? = nil

    /// The loops it changes, with their own expect values.
    public var targets: [LoopExpect] { loops.compactMap { l in l.expect.map { LoopExpect(key: l.key, expect: $0) } } }
}

public enum GroupReading: Equatable {
    case review(GroupReview)
    case refused(ChangeResult)
}

private struct GroupReply: Decodable {
    let schemaVersion: Int
    var group: GroupReview? = nil
    var result: ChangeResult? = nil
}

extension Contract {
    private static func groupReply(_ data: Data) throws -> GroupReply {
        guard data.count <= 262_144 else { throw ClientFailure.outputLimit }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { d in
            let raw = try d.singleValueContainer().decode(String.self)
            guard let date = Contract.date(raw) else { throw ClientFailure.invalidResponse }
            return date
        }
        let r: GroupReply
        do { r = try decoder.decode(GroupReply.self, from: data) } catch { throw ClientFailure.invalidResponse }
        guard r.schemaVersion == 1 else { throw ClientFailure.unsupported }
        return r
    }

    /// A review of every loop for exactly this action, or a refusal.
    public static func groupReview(_ data: Data, exit: Int32, action: LoopAction) throws -> GroupReading {
        let r = try groupReply(data)
        if let g = r.group, r.result == nil, exit == 0 {
            let keys = g.loops.map(\.key)
            guard g.action == action.rawValue, !text(g.title).isEmpty, !text(g.confirm).isEmpty, (1...24).contains(g.lines.count),
                  g.lines.allSatisfy({ !text($0).isEmpty }), !text(g.lost).isEmpty, !text(g.refused).isEmpty, !text(g.expired).isEmpty,
                  (1...128).contains(g.loops.count), Set(keys).count == keys.count, keys.allSatisfy(validKey),
                  g.loops.allSatisfy({ !text($0.name).isEmpty && ($0.expect.map(validExpect) ?? true) }), !g.targets.isEmpty else {
                throw ClientFailure.invalidResponse
            }
            return .review(g)
        }
        if let res = r.result, r.group == nil, exit != 0, outcomes.contains(res.outcome), !text(res.text).isEmpty {
            return .refused(res)
        }
        throw ClientFailure.invalidResponse
    }

    /// What `client apply --all` said: a total and each loop's outcome.
    public static func groupApplied(_ data: Data) throws -> ChangeResult {
        let r = try groupReply(data)
        guard let res = r.result, r.group == nil, outcomes.contains(res.outcome), !text(res.text).isEmpty,
              (res.lines ?? []).count <= 24, (res.lines ?? []).allSatisfy({ !text($0).isEmpty }) else { throw ClientFailure.invalidResponse }
        return res
    }
}

extension ConfirmationBuilder {
    /// The confirmation for a change to every loop: the loops, a line each,
    /// as one block, then the total and what the change does. Cancel is the
    /// default; pausing is not styled as destructive.
    public static func build(_ review: GroupReview) -> Confirmation {
        let lines = review.lines.map(Contract.text)
        // The CLI names up to eight loops, then "And N more.": one block.
        let named = min(review.loops.count, 8) + (review.loops.count > 8 ? 1 : 0)
        let block = lines.prefix(named).joined(separator: "\n")
        let rest = lines.dropFirst(named)
        let text = ([block] + rest).filter { !$0.isEmpty }.joined(separator: "\n\n")
        var buttons = [ConfirmButton(title: "Cancel", destructive: false, command: nil)]
        // A stop's options in keepgoing's words, those this menu fully
        // understands; deleting queued messages is never one for every loop.
        let options = (review.stop?.offered ?? []).filter { !$0.dropsQueued }
        if let action = LoopAction(rawValue: review.action), action == .stop, !options.isEmpty {
            for option in options {
                buttons.append(ConfirmButton(title: Contract.text(option.label), destructive: true,
                                             command: .applyAll(.stop, expects: review.targets, lanes: option.lanes)))
            }
        } else if let action = LoopAction(rawValue: review.action) {
            buttons.append(ConfirmButton(title: Contract.text(review.confirm), destructive: action == .stop,
                                         command: .applyAll(action, expects: review.targets)))
        }
        return Confirmation(messageText: Contract.text(review.title), informativeText: text, buttons: buttons)
    }

    public static func current(_ review: GroupReview, now: Date) -> Bool { now < review.expiresAt }
}
