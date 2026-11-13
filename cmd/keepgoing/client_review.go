package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"time"
)

// client review and client apply are the panel's pause, stop and resume for a
// local program such as the macOS menu, which cannot hold the panel server's
// single-use forms. client review is read-only: it reads the loop the way the
// page does and prints the review the page would show, built by the same
// code (panelReview). client apply makes the change only if the loop's
// control record is still the one reviewed, through the same functions as the
// page's confirmation (panelApply), and words the outcome as the page does
// (panelOutcome). Both act as no BB thread and take the loop by its page key.

var clientDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// clientReviewReply is what both commands print. A review carries Preview; a
// refused review, and every apply, carry Result.
type clientReviewReply struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Preview       *clientReviewPreview `json:"preview,omitempty"`
	Result        *panelResult         `json:"result,omitempty"`
}

// clientReviewPreview is the page's review as plain sentences.
type clientReviewPreview struct {
	Action string   `json:"action"`
	Key    string   `json:"key"`
	Title  string   `json:"title"`
	Lines  []string `json:"lines"`
	// Confirm is the confirming button's words.
	Confirm string `json:"confirm"`
	// Expect identifies the control record reviewed: client apply --expect
	// takes it back and refuses the change when the record has changed. It
	// is a hash of a file this user can already read.
	Expect    string    `json:"expect"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Lost: what to say when apply's reply never arrives. Refused: what to
	// say when apply refuses before doing anything. Expired: what to say when
	// the review is past ExpiresAt; apply is then not run.
	Lost    string `json:"lost"`
	Refused string `json:"refused"`
	Expired string `json:"expired"`
}

const clientReviewUsage = "usage: keepgoing client review pause|stop|resume --loop KEY [--directory private-registry] --json; keepgoing client apply pause|stop|resume --loop KEY --expect DIGEST [--directory private-registry] --json"

func clientReviewCommand(e env, args []string) int {
	if len(args) < 2 || panelActions[args[1]] == "" {
		e.errf("client: %s", clientReviewUsage)
		return exitUsage
	}
	command, action := args[0], args[1]
	fs := flag.NewFlagSet("client "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	key := fs.String("loop", "", "the loop's key, as panel list --json gives it")
	directory := fs.String("directory", "", "private registry directory")
	expect := fs.String("expect", "", "apply: the expect value of the review confirmed")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 0 || !panelKeyPattern.MatchString(*key) ||
		(command == "review" && *expect != "") || (command == "apply" && !clientDigestPattern.MatchString(*expect)) {
		e.errf("client: %s", clientReviewUsage)
		return exitUsage
	}
	if _, err := registryDirectory(*directory); err != nil {
		e.errf("client %s: registry directory unavailable", command)
		return exitUsage
	}
	var reply clientReviewReply
	code := exitOK
	if command == "review" {
		reply, code = clientReview(e.ctx, *directory, *key, action, time.Now().UTC())
	} else {
		reply, code = clientApply(e.ctx, *directory, *key, action, *expect)
	}
	reply.SchemaVersion = 1
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(reply) != nil {
			return exitFailed
		}
		return code
	}
	if p := reply.Preview; p != nil {
		fmt.Fprintln(e.stdout, p.Title)
		for _, l := range p.Lines {
			fmt.Fprintln(e.stdout, l)
		}
		fmt.Fprintf(e.stdout, "To confirm: keepgoing client apply %s --loop %s --expect %s\n", p.Action, p.Key, p.Expect)
	} else if reply.Result != nil {
		fmt.Fprintln(e.stdout, reply.Result.Text)
	}
	return code
}

func clientReview(ctx context.Context, directory, key, action string, now time.Time) (clientReviewReply, int) {
	reader := &dockReader{ctx: ctx, directory: directory, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}
	ctx, cancel := context.WithTimeout(ctx, panelChangeTimeout)
	defer cancel()
	r, refused := panelReview(ctx, directory, reader, key, action, now)
	if refused != nil {
		return clientReviewReply{Result: refused}, exitFailed
	}
	p := r.preview
	out := &clientReviewPreview{Action: p.Action, Key: p.Key, Title: p.Title, Lines: []string{}, Confirm: p.Confirm, Expect: r.digest,
		ExpiresAt: p.ExpiresAt, Lost: p.Lost, Refused: p.Refused, Expired: "This review expired. Nothing was changed. Review it again."}
	for _, l := range p.Lines {
		out.Lines = append(out.Lines, l.render(now))
	}
	return clientReviewReply{Preview: out}, exitOK
}

func clientApply(ctx context.Context, directory, key, action, expect string) (clientReviewReply, int) {
	entry, _, _, ok := panelEntry(directory, key)
	if !ok {
		return clientReviewReply{Result: &panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}}, exitFailed
	}
	ctx, cancel := context.WithTimeout(ctx, panelChangeTimeout)
	defer cancel()
	res := panelOutcome(action, panelApply(ctx, directory, entry.ID, action, "", expect))
	switch {
	case res.Outcome == "done":
		return clientReviewReply{Result: &res}, exitOK
	case res.Outcome == "refused" && res.Retry:
		return clientReviewReply{Result: &res}, exitSkipped
	}
	return clientReviewReply{Result: &res}, exitFailed
}
