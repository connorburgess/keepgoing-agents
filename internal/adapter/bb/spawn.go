package bb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"sort"
)

// Creating a thread is the one thing in this package that is not about an
// existing thread. It is used only for a handoff to a fallback candidate that
// opted in with bb.new_thread. Nothing here stops, archives, re-parents or
// messages any other thread.

var placeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ErrSpawnUncertain means bb neither returned a created thread nor a
// structured error, so a thread may or may not exist. The caller must look
// for it by title before trying again.
var ErrSpawnUncertain = errors.New("bb thread spawn: outcome unknown")

// ErrSpawnRefused means the bb CLI said it rejected the invocation, with one
// of its documented invocation-mistake codes, and ended on its own; or the
// request was invalid and bb was never run. It is evidence that nothing was
// created, not proof: the caller still looks for the thread before treating
// the attempt as never made. A command that was killed or timed out is never
// a refusal, whatever it printed, and every other failure, including a
// structured server error, is uncertain: BB may have created the thread and
// failed afterwards.
var ErrSpawnRefused = errors.New("bb thread spawn: refused before creation")

// ErrSpawnNotStarted is an uncertain outcome in which the bb CLI could not be
// started at all (it is missing or not executable). Most likely nothing was
// created, but nothing confirmed it.
var ErrSpawnNotStarted = fmt.Errorf("%w: the bb CLI could not be started", ErrSpawnUncertain)

// endedOnItsOwn reports whether a bb command ran to its own exit: it was not
// killed, did not time out and was started.
func endedOnItsOwn(ctx context.Context, err error) bool {
	if err == nil {
		return true
	}
	if ctx.Err() != nil || errors.Is(err, errStopped) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || notStarted(err) {
		return false
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ProcessState != nil && !exit.Exited() {
		return false // ended by a signal
	}
	return true
}

func notStarted(err error) bool {
	var start *exec.Error
	var path *fs.PathError
	return errors.As(err, &start) || errors.As(err, &path) || errors.Is(err, exec.ErrNotFound)
}

// invocationMistakes are the bb CLI's own codes for a command it did not
// send (bb guide json). They are the only answers that prove nothing exists.
var invocationMistakes = map[string]bool{
	"unknown_command": true, "unknown_option": true, "missing_required": true,
	"unexpected_argument": true, "invalid_value": true, "missing_command": true,
}

// PermissionModes are the values `bb thread spawn --permission-mode` accepts.
var PermissionModes = []string{"accept-edits", "auto", "full"}

// maxListed bounds the threads inspected when looking for a created thread.
const maxListed = 5000

// Place is where a thread lives.
type Place struct {
	Project     string
	Environment string
}

// SpawnRequest describes the one thread to create.
type SpawnRequest struct {
	Place
	Provider  string
	Model     string
	Reasoning string
	// Permission is always passed, so the new thread never inherits a
	// project's remembered default.
	Permission string
	Title      string
	Prompt     string
}

// PlaceOf reads the project and environment of an existing thread with
// `bb thread show`. Only those two identifiers are read.
func (a *Adapter) PlaceOf(ctx context.Context, thread string) (Place, error) {
	if !childIDPattern.MatchString(thread) {
		return Place{}, errors.New("bb thread show: invalid thread id")
	}
	out, err := a.run(ctx, "", "thread", "show", thread, "--json")
	if code, ok := envelopeError(out); ok {
		return Place{}, errors.New("bb thread show: " + code)
	}
	if err != nil {
		return Place{}, errors.New("bb thread show: failed")
	}
	var resp struct {
		Thread *struct {
			ID          string `json:"id"`
			Project     string `json:"projectId"`
			Environment string `json:"environmentId"`
		} `json:"thread"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Thread == nil || resp.Thread.ID != thread ||
		!placeIDPattern.MatchString(resp.Thread.Project) || !placeIDPattern.MatchString(resp.Thread.Environment) {
		return Place{}, errors.New("bb thread show: unexpected JSON")
	}
	return Place{Project: resp.Thread.Project, Environment: resp.Thread.Environment}, nil
}

// FindByTitle lists the threads of a place and returns the IDs of those whose
// title is exactly title, sorted. The title is one keepgoing generated with a
// random token, so a match is a thread keepgoing asked BB to create. An error
// means the listing could not be trusted, which is not the same as no match.
func (a *Adapter) FindByTitle(ctx context.Context, p Place, title string) ([]string, error) {
	if !placeIDPattern.MatchString(p.Project) || !placeIDPattern.MatchString(p.Environment) || title == "" {
		return nil, errors.New("bb thread list: invalid place or title")
	}
	out, err := a.run(ctx, "", "thread", "list", "--project", p.Project, "--environment", p.Environment, "--include-hidden", "--json")
	if code, ok := envelopeError(out); ok {
		return nil, errors.New("bb thread list: " + code)
	}
	if err != nil {
		return nil, errors.New("bb thread list: failed")
	}
	var threads []struct {
		ID    string  `json:"id"`
		Title *string `json:"title"`
	}
	// A bare array is required: null and envelopes are not an empty listing.
	if json.Unmarshal(out, &threads) != nil || threads == nil || len(threads) > maxListed {
		return nil, errors.New("bb thread list: unexpected JSON")
	}
	ids := []string{}
	for _, t := range threads {
		if t.Title == nil || *t.Title != title {
			continue
		}
		if !childIDPattern.MatchString(t.ID) {
			return nil, errors.New("bb thread list: unexpected thread id")
		}
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// Spawn creates one thread with `bb thread spawn`, the prompt on stdin. It
// returns the new thread's ID. ErrSpawnRefused means the bb CLI reported
// rejecting the invocation; ErrSpawnUncertain means a thread may exist.
func (a *Adapter) Spawn(ctx context.Context, r SpawnRequest) (string, error) {
	permitted := false
	for _, m := range PermissionModes {
		permitted = permitted || r.Permission == m
	}
	if !placeIDPattern.MatchString(r.Project) || !placeIDPattern.MatchString(r.Environment) || r.Provider == "" || r.Title == "" || r.Prompt == "" || !permitted {
		return "", ErrSpawnRefused // nothing was run
	}
	args := []string{"thread", "spawn", "--project", r.Project, "--environment", r.Environment, "--provider", r.Provider, "--permission-mode", r.Permission, "--title", r.Title, "--prompt-file", "-", "--json"}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}
	if r.Reasoning != "" {
		args = append(args, "--reasoning-level", r.Reasoning)
	}
	out, err := a.run(ctx, r.Prompt, args...)
	if code, ok := envelopeError(out); ok {
		if invocationMistakes[code] && endedOnItsOwn(ctx, err) {
			return "", ErrSpawnRefused
		}
		return "", ErrSpawnUncertain // a server error or a killed command: the thread may exist
	}
	if err != nil && notStarted(err) {
		return "", ErrSpawnNotStarted
	}
	var created struct {
		ID          string `json:"id"`
		Project     string `json:"projectId"`
		Environment string `json:"environmentId"`
	}
	if err != nil || json.Unmarshal(out, &created) != nil || !childIDPattern.MatchString(created.ID) {
		return "", ErrSpawnUncertain
	}
	return created.ID, nil
}
