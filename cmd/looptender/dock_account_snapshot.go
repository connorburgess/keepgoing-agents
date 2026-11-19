package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

const dockAccountsPath = "api/v1/saved-accounts"
const dockMaxAccountsResponse = 1 << 20
const dockAccountsLifetime = 30 * time.Second

type dockAccountSnapshot struct {
	SchemaVersion int            `json:"schemaVersion"`
	Reference     string         `json:"reference"`
	ReadAt        time.Time      `json:"readAt"`
	Accounts      *accounts.View `json:"accounts"`
}
type dockAccountCached struct {
	at        time.Time
	reference string
	body      []byte
}

// Split only the web serialization. Native and command projections retain the
// complete Go evidence. A reference binds one saved catalog/quota snapshot to
// the report that advertised it; it is not an account identity or authority.
func dockSplitAccounts(report dockReport, reference string) (dockReport, []byte, error) {
	snapshot := dockAccountSnapshot{SchemaVersion: 1, Reference: reference, ReadAt: report.ReadAt, Accounts: report.Accounts}
	body, err := json.Marshal(snapshot)
	if err != nil || len(body) > dockMaxAccountsResponse {
		return dockReport{}, nil, errors.New("saved accounts exceed their bound")
	}
	report.AccountsReference = reference
	if report.Accounts != nil {
		report.SelectedAccounts = len(report.Accounts.Cards)
		report.Accounts = &accounts.View{State: report.Accounts.State, Complete: report.Accounts.Complete, Cards: []accounts.Card{}}
	}
	return report, body, nil
}

// No filesystem/provider read, mutation or refresh: only a bounded snapshot
// already produced by a main read. Superseded/expired references fail closed.
func (d *dockReader) accountsJSON(reference string) ([]byte, error) {
	decoded, err := hex.DecodeString(reference)
	if err != nil || len(decoded) != 16 || reference != hex.EncodeToString(decoded) {
		return nil, errors.New("invalid saved account reference")
	}
	d.accountsMu.Lock()
	defer d.accountsMu.Unlock()
	for _, c := range d.accountBodies {
		if c.reference == reference && time.Since(c.at) < dockAccountsLifetime {
			return c.body, nil
		}
	}
	return nil, errors.New("saved account reference expired")
}
