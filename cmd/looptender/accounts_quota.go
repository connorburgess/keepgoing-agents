package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

type accountQuotaRun func(context.Context, string, []string, string) ([]byte, error)
type accountQuotaSelection struct {
	Entry                   accounts.Entry
	Bound                   string
	CLI, Workdir, Directory os.FileInfo
}

func accountQuotaCapture(dir, id string, env []string) (accountQuotaSelection, error) {
	f, err := accounts.Load(dir)
	if err != nil {
		return accountQuotaSelection{}, err
	}
	e := accounts.Find(&f, id)
	if e == nil {
		return accountQuotaSelection{}, accounts.ErrChanged
	}
	bound, ok := accounts.Bound(f, *e, env)
	if !ok {
		return accountQuotaSelection{}, accounts.ErrChanged
	}
	s := accountQuotaSelection{Entry: *e, Bound: bound}
	if e.Provider != "codex" {
		return s, nil
	}
	// Selection paths must remain canonical: a replacement symlink is not the
	// executable or directory reviewed, even when it has the same spelling.
	check := func(path string, wantDir bool) (os.FileInfo, error) {
		p, err := filepath.EvalSymlinks(path)
		if err != nil || p != path {
			return nil, accounts.ErrChanged
		}
		// Snapshot metadata captures Windows file IDs through a handle now.
		// Path-based os.Stat defers identity lookup until SameFile, which can
		// otherwise inspect a replacement at the old spelling.
		i, err := snapshot.Lstat(path)
		if err != nil || i.Mode()&os.ModeSymlink != 0 || i.IsDir() != wantDir || (!wantDir && !i.Mode().IsRegular()) {
			return nil, accounts.ErrChanged
		}
		return i, nil
	}
	if s.CLI, err = check(e.CLI, false); err != nil {
		return s, err
	}
	if s.Workdir, err = check(e.Workdir, true); err != nil {
		return s, err
	}
	if e.Mode == "explicit_directory" {
		if s.Directory, err = check(e.Directory, true); err != nil {
			return s, err
		}
	}
	return s, nil
}
func (s accountQuotaSelection) same(t accountQuotaSelection) bool {
	if !accounts.SameSelection(s.Entry, t.Entry) || s.Bound != t.Bound {
		return false
	}
	for _, p := range [][2]os.FileInfo{{s.CLI, t.CLI}, {s.Workdir, t.Workdir}, {s.Directory, t.Directory}} {
		if (p[0] == nil) != (p[1] == nil) {
			return false
		}
		if p[0] != nil && !os.SameFile(p[0], p[1]) {
			return false
		}
	}
	return s.CLI == nil || (s.CLI.Size() == t.CLI.Size() && s.CLI.ModTime().Equal(t.CLI.ModTime()) && s.CLI.Mode() == t.CLI.Mode())
}
func accountQuotaRead(ctx context.Context, cli string, env []string, workdir string) ([]byte, error) {
	return accountCodexRPC(ctx, cli, env, workdir, "account/rateLimits/read", map[string]bool{"excludeResetCreditDetails": true})
}

// accountQuotaRefresh never runs a route probe. CheckGuard prevents parallel
// same-selection reads; publication independently fences edits during the RPC.
func accountQuotaRefresh(ctx context.Context, dir, id string, expected *accountQuotaSelection, run accountQuotaRun, env []string, now func() time.Time) (accounts.QuotaView, error) {
	unavailable := accounts.QuotaView{State: "unavailable", Reason: "selection_unavailable", Freshness: "unavailable", Windows: []accounts.QuotaWindow{}}
	g, err := accounts.CheckGuard(dir, id)
	if err != nil {
		return unavailable, err
	}
	defer g.Release()
	s, err := accountQuotaCapture(dir, id, env)
	if err != nil {
		return unavailable, err
	}
	if expected != nil && !expected.same(s) {
		return unavailable, accounts.ErrChanged
	}
	if _, err := accounts.LoadQuota(dir); err != nil {
		return unavailable, err
	}
	a := accounts.QuotaAttempt{}
	if s.Entry.Provider != "codex" {
		a.Reason = "unsupported_provider"
	} else {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := run(c, s.Entry.CLI, accounts.ExecutionEnvironment(s.Entry, env), s.Entry.Workdir)
		cancel()
		a.Details, a.Reason = accountQuotaDetailsParse(raw, err)
		if a.Reason == "" {
			a.Windows, _ = accountQuotaParse(raw, nil)
		}
	}
	a.At = now().UTC()
	verify := func() error {
		fresh, err := accountQuotaCapture(dir, id, env)
		if err != nil || !s.same(fresh) {
			return accounts.ErrChanged
		}
		return nil
	}
	if err := accounts.PublishQuota(dir, s.Entry, s.Bound, a, env, verify); err != nil {
		return unavailable, err
	}
	f, err := accounts.Load(dir)
	if err != nil {
		return unavailable, err
	}
	fresh, err := accountQuotaCapture(dir, id, env)
	if err != nil || !s.same(fresh) {
		return unavailable, accounts.ErrChanged
	}
	return accounts.QuotaViews(dir, f, env, now())[id], nil
}

func accountQuotaParse(raw []byte, err error) ([]accounts.QuotaWindow, string) {
	if err != nil {
		_, reason := accountCodexPlan(nil, err)
		return nil, reason
	}
	if !accounts.QuotaJSONUnique(raw) {
		return nil, "schema_unavailable"
	}
	var response struct {
		RateLimits json.RawMessage            `json:"rateLimits"`
		ByID       map[string]json.RawMessage `json:"rateLimitsByLimitId"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, "schema_unavailable"
	}
	bucket := response.RateLimits
	if len(response.ByID) > 0 {
		var ok bool
		bucket, ok = response.ByID["codex"]
		if !ok {
			return nil, "unknown_bucket"
		}
	}
	if len(bucket) == 0 || string(bucket) == "null" {
		return nil, "no_windows"
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(bucket, &fields) != nil || fields == nil {
		return nil, "schema_unavailable"
	}
	if id := fields["limitId"]; len(id) > 0 && string(id) != "null" {
		var name string
		if json.Unmarshal(id, &name) != nil {
			return nil, "schema_unavailable"
		}
		if name != "" && name != "codex" {
			return nil, "unknown_bucket"
		}
	}
	// These documented metadata fields are discarded, including all credits,
	// plan/model labels and identifiers. Only known quota windows survive.
	for k := range fields {
		switch k {
		case "primary", "secondary", "limitId", "limitName", "credits", "individualLimit", "normalModelSlug", "planType", "rateLimitReachedType", "spendControlReached":
		default:
			return nil, "unknown_windows"
		}
	}
	var out []accounts.QuotaWindow
	for _, name := range []string{"primary", "secondary"} {
		b := fields[name]
		if len(b) == 0 || string(b) == "null" {
			continue
		}
		var w struct {
			Used     *int   `json:"usedPercent"`
			Duration *int64 `json:"windowDurationMins"`
			Reset    *int64 `json:"resetsAt"`
		}
		var keys map[string]json.RawMessage
		if json.Unmarshal(b, &keys) != nil || keys == nil {
			return nil, "schema_unavailable"
		}
		for k := range keys {
			if k != "usedPercent" && k != "windowDurationMins" && k != "resetsAt" {
				return nil, "unknown_windows"
			}
		}
		if json.Unmarshal(b, &w) != nil || w.Used == nil || *w.Used < 0 || *w.Used > 100 {
			return nil, "schema_unavailable"
		}
		q := accounts.QuotaWindow{Name: name, UsedPercent: *w.Used, DurationMinutes: w.Duration}
		if w.Duration != nil && (*w.Duration <= 0 || *w.Duration > 525600) {
			return nil, "schema_unavailable"
		}
		if w.Reset != nil {
			at := time.Unix(*w.Reset, 0).UTC()
			if at.Year() < 2000 || at.Year() > 9999 {
				return nil, "schema_unavailable"
			}
			q.ResetsAt = &at
		}
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, "no_windows"
	}
	return out, ""
}

func accountsUsage(e env, dir, id string, asJSON bool) int {
	if id == "" {
		e.errf("accounts usage: select an account with --id")
		return exitUsage
	}
	v, err := accountQuotaRefresh(e.ctx, dir, id, nil, accountQuotaRead, os.Environ(), time.Now)
	if err != nil {
		e.errf("accounts usage: selection changed, refresh busy, or private evidence unavailable; nothing published")
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		return exitFailed
	}
	if asJSON {
		_ = json.NewEncoder(e.stdout).Encode(struct {
			SchemaVersion int                `json:"schemaVersion"`
			ID            string             `json:"id"`
			Quota         accounts.QuotaView `json:"quota"`
		}{1, id, v})
	} else {
		fmt.Fprintf(e.stdout, "Selected-context subscription quota: %s", v.State)
		if v.Reason != "" {
			fmt.Fprintf(e.stdout, " (%s)", v.Reason)
		}
		fmt.Fprintln(e.stdout)
		if v.ObservedAt != nil {
			fmt.Fprintf(e.stdout, "Read %s (%s); source: %s\n", v.ObservedAt.Format(time.RFC3339), v.Freshness, v.Source)
		}
		summary := accounts.SummarizeQuota(v, time.Now())
		for _, row := range summary.Rows {
			fmt.Fprintf(e.stdout, "%s: %s", row.Name, row.Text)
			if row.ResetsAt != nil {
				fmt.Fprintf(e.stdout, "; reset %s", row.ResetsAt.Format(time.RFC3339))
			}
			fmt.Fprintln(e.stdout)
		}
		if summary.Partial {
			fmt.Fprintln(e.stdout, "Partial quota evidence; missing values are not zero.")
		}
		if summary.Omitted > 0 {
			fmt.Fprintf(e.stdout, "%d additional buckets not shown.\n", summary.Omitted)
		}
		if v.FailedRefreshReason != "" {
			fmt.Fprintf(e.stdout, "Refresh failed %s (%s); any retained reading has its original timestamp.\n", v.FailedRefreshAt.Format(time.RFC3339), v.FailedRefreshReason)
		}
	}
	if v.State != "reported" || v.FailedRefreshAt != nil {
		return exitFailed
	}
	return exitOK
}
