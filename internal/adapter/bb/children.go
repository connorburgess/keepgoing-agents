package bb

import (
	"context"
	"encoding/json"
	"regexp"
)

var childIDPattern = regexp.MustCompile(`^thr_[A-Za-z0-9_-]{1,128}$`)

// childWork observes direct children only. Listing discovers IDs; show verifies
// the current relationship and status. No child is sent a message, stopped,
// spawned, or re-parented. Incomplete discovery must not authorize a wake.
func (a *Adapter) childWork(ctx context.Context) (active int, unverified bool) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()
	out, err := a.run(ctx, "", "thread", "list", "--parent-thread", a.cfg.Thread, "--include-hidden", "--json")
	if err != nil || ctx.Err() != nil {
		return 0, true
	}
	var children []struct {
		ID string `json:"id"`
	}
	// A bare array is required: null, envelopes, and missing data are not an
	// empty inventory. Bound the number of show calls in addition to time.
	if json.Unmarshal(out, &children) != nil || children == nil || len(children) > 200 {
		return 0, true
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
			return active, true
		}
		out, err := a.run(ctx, "", "thread", "show", child.ID, "--json")
		if ctx.Err() != nil {
			return active, true
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
		case "idle", "error", "pending", "starting", "stopping":
			// Known non-active state. Pending child interactions are a
			// separate roadmap item.
		default:
			unverified = true
		}
	}
	return active, unverified
}
