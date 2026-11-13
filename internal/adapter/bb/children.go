package bb

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
)

var childIDPattern = regexp.MustCompile(`^thr_[A-Za-z0-9_-]{1,128}$`)

// childWork observes direct children only. Listing discovers IDs; show verifies
// the current relationship and status. No child is sent a message, stopped,
// spawned, or re-parented. Incomplete discovery must not authorize a wake.
//
// errored lists verified direct children whose status is error. They are not
// running work. Only the status field decides this; no child log, message or
// title is read.
//
// healthy lists verified direct children with a known status other than error.
// It lets a caller tell a child that is verifiably no longer in error from one
// an incomplete listing simply did not show.
func (a *Adapter) childWork(ctx context.Context) (active int, unverified bool, awaiting, errored, healthy []string) {
	a.activeChildIDs = nil
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	out, err := a.run(ctx, "", "thread", "list", "--parent-thread", a.cfg.Thread, "--include-hidden", "--json")
	if err != nil || ctx.Err() != nil {
		return 0, true, nil, nil, nil
	}
	var children []struct {
		ID string `json:"id"`
	}
	// A bare array is required: null, envelopes, and missing data are not an
	// empty inventory. Bound the number of show calls in addition to time.
	if json.Unmarshal(out, &children) != nil || children == nil || len(children) > 200 {
		return 0, true, nil, nil, nil
	}
	seen := make(map[string]bool)
	for _, child := range children {
		if !childIDPattern.MatchString(child.ID) || child.ID == a.cfg.Thread {
			unverified = true
			continue
		}
		if seen[child.ID] {
			continue
		}
		seen[child.ID] = true
		if ctx.Err() != nil {
			return active, true, awaiting, errored, healthy
		}
		out, err := a.run(ctx, "", "thread", "show", child.ID, "--json")
		if ctx.Err() != nil {
			return active, true, awaiting, errored, healthy
		}
		if code, ok := envelopeError(out); ok && code == "thread_not_found" {
			continue // Child disappeared after discovery.
		}
		if err != nil || ctx.Err() != nil {
			unverified = true
			continue
		}
		var resp struct {
			OK     *bool           `json:"ok"`
			Error  json.RawMessage `json:"error"`
			Thread *struct {
				ID         string          `json:"id"`
				Parent     json.RawMessage `json:"parentThreadId"`
				Status     string          `json:"status"`
				ArchivedAt *int64          `json:"archivedAt"`
				DeletedAt  *int64          `json:"deletedAt"`
			} `json:"thread"`
		}
		if json.Unmarshal(out, &resp) != nil || (resp.OK != nil && !*resp.OK) || (len(resp.Error) > 0 && string(resp.Error) != "null") || resp.Thread == nil || resp.Thread.ID != child.ID {
			unverified = true
			continue
		}
		t := resp.Thread
		if (t.ArchivedAt != nil && *t.ArchivedAt <= 0) || (t.DeletedAt != nil && *t.DeletedAt <= 0) {
			unverified = true
			continue
		}
		var parent *string
		if len(t.Parent) == 0 || json.Unmarshal(t.Parent, &parent) != nil {
			unverified = true
			continue
		}
		if parent == nil || *parent != a.cfg.Thread || t.ArchivedAt != nil || t.DeletedAt != nil {
			continue
		}
		switch t.Status {
		case "active":
			active++
			healthy = append(healthy, child.ID)
			a.activeChildIDs = append(a.activeChildIDs, child.ID)
		case "error":
			errored = append(errored, child.ID)
		case "idle", "pending", "starting", "stopping":
			healthy = append(healthy, child.ID)
			// Known non-active state; it may still be waiting on a person.
		default:
			unverified = true
		}
		pending, unknown := a.childPending(ctx, child.ID)
		unverified = unverified || unknown
		if pending {
			awaiting = append(awaiting, child.ID)
		}
	}
	sort.Strings(awaiting)
	sort.Strings(errored)
	sort.Strings(healthy)
	return active, unverified, awaiting, errored, healthy
}

// childPending inspects only identity and status, never interaction contents.
// Resolving means a response is in flight: withhold a wake without asking the
// person to answer it again. Unknown data cannot establish an input-free child.
func (a *Adapter) childPending(ctx context.Context, id string) (pending, unverified bool) {
	out, err := a.run(ctx, "", "thread", "interactions", "list", id, "--json")
	if err != nil || ctx.Err() != nil {
		return false, true
	}
	var items []struct {
		Thread string `json:"threadId"`
		Status string `json:"status"`
	}
	if json.Unmarshal(out, &items) != nil || items == nil || len(items) > 500 {
		return false, true
	}
	for _, item := range items {
		if item.Thread != id {
			unverified = true
			continue
		}
		switch item.Status {
		case "pending":
			pending = true
		case "resolved", "interrupted":
		default:
			unverified = true
		}
	}
	return pending, unverified
}
