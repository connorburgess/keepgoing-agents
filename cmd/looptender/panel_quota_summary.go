package main

import "github.com/looptender/looptender/internal/accounts"

type panelQuotaList struct {
	State    string              `json:"state"`
	Complete bool                `json:"complete"`
	Accounts []panelQuotaAccount `json:"accounts"`
	Omitted  int                 `json:"omitted"`
}
type panelQuotaAccount struct {
	ID       string                `json:"id,omitempty"`
	Label    string                `json:"label"`
	Provider string                `json:"provider"`
	Summary  accounts.QuotaSummary `json:"summary"`
}

func panelQuotaSummary(r dockReport) *panelQuotaList {
	out := &panelQuotaList{State: "unavailable", Accounts: []panelQuotaAccount{}}
	if r.Accounts == nil {
		return out
	}
	out.State = r.Accounts.State
	out.Complete = r.Accounts.Complete
	for _, c := range r.Accounts.Cards {
		if c.Provider != "codex" && c.Provider != "claude-code" && c.Provider != "antigravity" {
			continue
		}
		if len(out.Accounts) >= 16 {
			out.Omitted++
			continue
		}
		q := c.Quota
		if q == nil {
			q = &accounts.QuotaView{State: "unavailable", Reason: "unsupported_provider", Freshness: "unavailable"}
		}
		out.Accounts = append(out.Accounts, panelQuotaAccount{ID: c.ID, Label: clip(c.Label, 120), Provider: c.Provider, Summary: accounts.SummarizeQuota(*q, r.ReadAt)})
	}
	return out
}
