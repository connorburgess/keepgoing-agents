package bb

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
)

// This file is everything the BB adapter does for a stop that covers the
// lanes: list a thread's children, re-read each, stop one, and cancel a
// provider retry waiting in one. It sends no message. Only identity, parent,
// status and the queued count are read: no title, log or message.

// LaneRoot implements adapter.LaneStopper.
func (a *Adapter) LaneRoot() string { return a.cfg.Thread }

// laneListMax bounds one listing, as child discovery does.
const laneListMax = 200

// LaneChildren implements adapter.LaneStopper. It lists parent's direct
// children, hidden ones included, and verifies at most max of them with
// `thread show`: the thread must name itself and parent and be neither
// archived nor deleted. A thread that is gone (not found, archived, deleted,
// or now under another parent) is left out and uses none of max, as child
// discovery leaves it out. One that cannot be read is returned unverified.
func (a *Adapter) LaneChildren(ctx context.Context, parent string, max int) ([]adapter.LaneInfo, int, error) {
	if !childIDPattern.MatchString(parent) {
		return nil, 0, errors.New("bb: not a thread ID")
	}
	out, err := a.run(ctx, "", "thread", "list", "--parent-thread", parent, "--include-hidden", "--json")
	if err != nil || ctx.Err() != nil {
		return nil, 0, laneError(out, err, ctx)
	}
	var listed []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out, &listed) != nil || listed == nil || len(listed) > laneListMax {
		return nil, 0, &adapter.LaneStopError{Class: "listing_unreadable", Err: errors.New("bb thread list: not a bounded array")}
	}
	var kids []adapter.LaneInfo
	seen, notChecked := map[string]bool{}, 0
	for _, l := range listed {
		if seen[l.ID] || l.ID == parent || l.ID == a.cfg.Thread {
			continue
		}
		seen[l.ID] = true
		if len(kids) >= max || ctx.Err() != nil {
			notChecked++
			continue
		}
		info, gone := a.showLane(ctx, l.ID, parent)
		if !gone {
			kids = append(kids, info)
		}
	}
	return kids, notChecked, nil
}

// showLane re-reads one listed child. gone: it no longer exists, is
// archived or deleted, or is now another thread's child.
func (a *Adapter) showLane(ctx context.Context, id, parent string) (info adapter.LaneInfo, gone bool) {
	info = adapter.LaneInfo{ID: id}
	if !childIDPattern.MatchString(id) {
		return info, false
	}
	out, err := a.run(ctx, "", "thread", "show", id, "--json")
	if code, bad := envelopeError(out); bad {
		return info, code == "thread_not_found"
	}
	if err != nil || ctx.Err() != nil {
		return info, false
	}
	var resp struct {
		Thread *struct {
			ID         string          `json:"id"`
			Parent     json.RawMessage `json:"parentThreadId"`
			Status     string          `json:"status"`
			Queued     int             `json:"queuedMessageCount"`
			ArchivedAt *int64          `json:"archivedAt"`
			DeletedAt  *int64          `json:"deletedAt"`
		} `json:"thread"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Thread == nil || resp.Thread.ID != id {
		return info, false
	}
	t := resp.Thread
	var p *string
	if len(t.Parent) == 0 || json.Unmarshal(t.Parent, &p) != nil {
		return info, false
	}
	if p == nil || *p != parent || t.ArchivedAt != nil || t.DeletedAt != nil {
		return info, true
	}
	info.Verified, info.Status, info.Queued = true, t.Status, max(0, t.Queued)
	return info, false
}

// StopLane implements adapter.LaneStopper with `bb thread stop`, the command
// the supervised thread's own stop uses.
func (a *Adapter) StopLane(ctx context.Context, id string) error {
	if !childIDPattern.MatchString(id) || id == a.cfg.Thread {
		return &adapter.LaneStopError{Class: "not_a_lane", Err: errors.New("bb: refused to stop a thread that is not a lane")}
	}
	out, err := a.run(ctx, "", "thread", "stop", id, "--json")
	if code, ok := envelopeError(out); ok {
		return &adapter.LaneStopError{Class: safeCode(code), Err: errors.New("bb thread stop: " + safeCode(code))}
	}
	if err != nil || ctx.Err() != nil {
		return laneError(out, err, ctx)
	}
	return nil
}

// CancelLaneRetry implements adapter.LaneStopper with BB's provider-retry
// plugin, whose `cancel` deletes the queued row of a pending automatic
// provider retry:
//
//	{"ok":true,"threadId":...,"queuedMessageId":...}
//	{"ok":false,"error":{"code":"no_pending_retry",...}}
//
// A BB without the plugin answers an unknown command, and that is
// ErrUnsupported: nothing else can cancel it. Other queued rows (a retry a
// person or keepgoing asked for) are not provider retries and stay queued.
func (a *Adapter) CancelLaneRetry(ctx context.Context, id string) (bool, error) {
	if !childIDPattern.MatchString(id) || id == a.cfg.Thread {
		return false, &adapter.LaneStopError{Class: "not_a_lane", Err: errors.New("bb: refused a thread that is not a lane")}
	}
	out, err := a.run(ctx, "", "provider-retry", "cancel", id, "--json")
	if code, ok := envelopeError(out); ok {
		switch code {
		case "no_pending_retry":
			return false, nil
		case "unknown_command", "unknown_plugin_command", "command_not_found":
			return false, adapter.ErrUnsupported{Op: "provider retry cancel"}
		}
		return false, &adapter.LaneStopError{Class: safeCode(code), Err: errors.New("bb provider-retry cancel: " + safeCode(code))}
	}
	if err != nil || ctx.Err() != nil {
		return false, laneError(out, err, ctx)
	}
	var res struct {
		OK     *bool  `json:"ok"`
		Thread string `json:"threadId"`
	}
	if json.Unmarshal(out, &res) != nil || res.OK == nil || !*res.OK || res.Thread != id {
		return false, &adapter.LaneStopError{Class: "unreadable", Err: errors.New("bb provider-retry cancel: unreadable result")}
	}
	return true, nil
}

// laneError gives a failed bb command its class.
func laneError(out []byte, err error, ctx context.Context) error {
	if code, ok := envelopeError(out); ok {
		return &adapter.LaneStopError{Class: safeCode(code), Err: errors.New("bb: " + safeCode(code))}
	}
	if errors.Is(err, errStopped) || ctx.Err() != nil {
		return &adapter.LaneStopError{Class: "timeout", Err: errors.New("bb command stopped at its time limit")}
	}
	if err == nil {
		err = errors.New("no result")
	}
	return &adapter.LaneStopError{Class: "failed", Err: err}
}

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// safeCode keeps an error code only when it is one: a short lower-case
// token. Anything else is not repeated.
func safeCode(code string) string {
	if codePattern.MatchString(code) {
		return code
	}
	return "error"
}

var _ adapter.LaneStopper = (*Adapter)(nil)
