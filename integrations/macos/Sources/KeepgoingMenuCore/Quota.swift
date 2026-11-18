import Foundation

/// Saved selected-context evidence only. Menu polling never refreshes providers.
public struct AccountQuotaList: Decodable, Equatable {
    public let state: String
    public let complete: Bool
    public let accounts: [AccountQuota]
    public let omitted: Int
}
public struct AccountQuota: Decodable, Equatable {
    public let label: String
    public let provider: String
    public let summary: AccountQuotaSummary
}
public struct AccountQuotaSummary: Decodable, Equatable {
    public let state: String
    public let text: String
    public let source: String?
    public let observedAt: String?
    public let freshness: String
    public let rows: [AccountQuotaRow]
    public let partial: Bool?
    public let omitted: Int?
    public let failedRefreshAt: String?
    public let failedRefreshText: String?
}
public struct AccountQuotaRow: Decodable, Equatable {
    public let name: String
    public let text: String
    public let usedPercent: Double?
    public let remainingPercent: Double?
    public let resetsAt: String?
    public let ended: Bool?
}
extension Contract {
    static func quotaText(_ value: String, bytes: Int = 512) -> Bool {
        !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && value.utf8.count <= bytes && value.unicodeScalars.allSatisfy { [0x200C, 0x200D].contains($0.value) || !(CharacterSet.controlCharacters.contains($0) || CharacterSet.newlines.contains($0)) }
    }
    static func validQuotas(_ value: AccountQuotaList) -> Bool {
        guard ["available", "unavailable"].contains(value.state), value.accounts.count <= 16,
              value.omitted >= 0, value.omitted <= 64 else { return false }
        for a in value.accounts {
            let q = a.summary
            guard quotaText(a.label), ["codex", "claude-code"].contains(a.provider),
                  ["reported", "unavailable"].contains(q.state), quotaText(q.text),
                  ["fresh", "stale", "unavailable"].contains(q.freshness), q.rows.count <= 41,
                  (q.omitted ?? 0) >= 0, (q.omitted ?? 0) <= 256,
                  q.observedAt == nil || date(q.observedAt) != nil,
                  q.failedRefreshAt == nil || date(q.failedRefreshAt) != nil,
                  q.failedRefreshText == nil || quotaText(q.failedRefreshText!),
                  q.source == nil || q.source == "codex account/rateLimits/read" else { return false }
            if q.state == "reported" {
                guard a.provider == "codex", q.observedAt != nil, q.source != nil,
                      q.freshness != "unavailable" else { return false }
            } else if !q.rows.isEmpty || q.observedAt != nil || q.source != nil { return false }
            for r in q.rows {
                guard quotaText(r.name, bytes: 160), quotaText(r.text),
                      r.resetsAt == nil || date(r.resetsAt) != nil else { return false }
                for p in [r.usedPercent, r.remainingPercent].compactMap({ $0 }) {
                    guard p.isFinite && p >= 0 && p <= 100 else { return false }
                }
                if let used = r.usedPercent, let remaining = r.remainingPercent,
                   abs(used + remaining - 100) > 0.000001 { return false }
            }
        }
        return true
    }
}
extension MenuBuilder {
    static func quotaRows(_ list: AccountQuotaList?) -> [MenuRow] {
        guard let list else {
            return [quotaStatus("Account quota is not reported by this CLI.")]
        }
        guard list.state == "available" else { return [quotaStatus("Account quota evidence unavailable.")] }
        if list.accounts.isEmpty { return [quotaStatus("No selected Claude or Codex accounts.")] }
        var result = [quotaStatus("Account quota · saved selections, not live thread identity")]
        for a in list.accounts.prefix(6) {
            let q = a.summary
            var children = [quotaStatus(q.text)]
            if let at = q.observedAt { children.append(quotaStatus("Read \(at) · \(q.freshness == "stale" ? "older reading" : "saved snapshot")")) }
            for r in q.rows.prefix(8) {
                var value = "\(r.name): \(r.text)"
                if let reset = r.resetsAt { value += " · \(r.ended == true ? "Ended" : "Resets") \(reset)" }
                else if r.usedPercent != nil || r.remainingPercent != nil { value += " · Reset unavailable" }
                children.append(quotaStatus(value))
            }
            if q.rows.count > 8 { children.append(quotaStatus("\(q.rows.count - 8) more quota details in the panel.")) }
            if q.partial == true || (q.omitted ?? 0) > 0 { children.append(quotaStatus("Partial quota evidence; missing values are not zero.")) }
            if let source = q.source { children.append(quotaStatus("Source: \(source)")) }
            if let failed = q.failedRefreshAt { children.append(quotaStatus("Refresh failed \(failed). \(q.failedRefreshText ?? "Earlier evidence retains its time.")")) }
            children.append(MenuRow(kind: .action, title: "Open Panel", detail: "", symbol: "macwindow", accessibilityLabel: "Open panel for quota details", accessibilityHelp: "Choose Accounts for saved quota details; explicit refresh is reviewed there.", toolTip: "", command: .openPanel))
            let first = q.rows.first(where: { $0.usedPercent != nil })
            let detail = q.state == "reported" ? (first.map { $0.ended == true ? "Window ended" : "\($0.usedPercent!)% used" } ?? "Quota details recorded") : "Quota unavailable"
            result.append(MenuRow(kind: .action, title: shortened(a.label, to: maxName), detail: detail, symbol: "gauge.with.dots.needle.33percent", accessibilityLabel: "\(a.label), \(a.provider), \(detail). Selected account identity not verified.", accessibilityHelp: "Opens saved quota details; reads no provider.", toolTip: a.label, command: nil, submenu: children))
        }
        let more = list.omitted + max(0, list.accounts.count - 6)
        if more > 0 { result.append(quotaStatus("\(more) more selected accounts in the panel.")) }
        if !list.complete { result.append(quotaStatus("Account selection evidence is incomplete.")) }
        return result
    }
    private static func quotaStatus(_ full: String) -> MenuRow {
        MenuRow(kind: .status, title: shortened(full, to: 110), detail: "", symbol: "", accessibilityLabel: full, accessibilityHelp: "", toolTip: full, command: nil)
    }
}
