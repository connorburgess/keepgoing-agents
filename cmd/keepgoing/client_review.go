package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

// client review and client apply are the panel's pause, stop and resume for a
// local program such as the macOS menu, which cannot hold the panel server's
// single-use forms. client review is read-only: it reads the loop the way the
// page does and prints the review the page would show, built by the same
// code (panelReview). client apply makes the change only for the loop, the
// action and the control record reviewed, before the review expires, through
// the same functions as the page's confirmation (panelApply), and words the
// outcome as the page does (panelOutcome). Both take the loop by its page
// key. Pause and stop act as anyone may; a resume acts as the BB thread in
// BB_THREAD_ID, or as no thread outside BB, exactly as keepgoing resume does.

// A review's expect value: when it expires, the control record reviewed, and
// a check over both with the loop's key and the action, so it is one review
// for one action on one loop. It is not a secret: whoever can run client
// apply can read the record and change the loop anyway.
var clientExpectPattern = regexp.MustCompile(`^r1\.([0-9]{1,12})\.([0-9a-f]{64})\.([0-9a-f]{32})$`)

const clientExpectContext = "keepgoing-client-review\x00"

// clientReviewMenuCap is how many characters of a line the macOS menu shows
// (Contract.text); a review line longer than that would be cut.
const clientReviewMenuCap = 180

func clientExpect(key, action string, expires time.Time, digest string) string {
	at := strconv.FormatInt(expires.Unix(), 10)
	return "r1." + at + "." + digest + "." + clientExpectCheck(key, action, at, digest)
}

func clientExpectCheck(key, action, at, digest string) string {
	sum := sha256.Sum256([]byte(clientExpectContext + key + "\x00" + action + "\x00" + at + "\x00" + digest))
	return hex.EncodeToString(sum[:16])
}

// parseClientExpect returns the record digest and expiry of a value made for
// this key and action, or ok false.
func parseClientExpect(value, key, action string) (digest string, expires time.Time, ok bool) {
	m := clientExpectPattern.FindStringSubmatch(value)
	if m == nil || clientExpectCheck(key, action, m[1], m[2]) != m[3] {
		return "", time.Time{}, false
	}
	at, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return m[2], time.Unix(at, 0), true
}

// clientReviewReply is what both commands print. A review carries Preview; a
// refused review, and every apply, carry Result.
type clientReviewReply struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Preview       *clientReviewPreview `json:"preview,omitempty"`
	Result        *panelResult         `json:"result,omitempty"`
}

// clientReviewPreview is the page's review as plain sentences, with times in
// this computer's zone, named.
type clientReviewPreview struct {
	Action string   `json:"action"`
	Key    string   `json:"key"`
	Title  string   `json:"title"`
	Lines  []string `json:"lines"`
	// Confirm is the confirming button's words.
	Confirm string `json:"confirm"`
	// Expect is what client apply --expect takes back.
	Expect    string    `json:"expect"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Lost: what to say when apply's reply never arrives. Refused: what to
	// say when apply refuses before doing anything. Expired: what to say when
	// the review is past ExpiresAt.
	Lost    string `json:"lost"`
	Refused string `json:"refused"`
	Expired string `json:"expired"`
	// Stop is a stop's options; see stopChoices.
	Stop *stopChoices `json:"stop,omitempty"`
}

const (
	clientReviewUsage   = "usage: keepgoing client review pause|stop|resume --loop KEY [--directory private-registry] [--json]; keepgoing client apply pause|stop|resume --loop KEY --expect VALUE [--lanes [--drop-queued]] [--immediately] [--directory private-registry] [--json]"
	clientReviewExpired = "This review expired. Nothing was changed. Review it again."
	clientReviewOther   = "This review is for another loop or another change. Nothing was changed. Review it again."
	clientReviewOwned   = "This loop is supervised for another conversation, so it can be resumed only from that conversation or from outside BB. Nothing was changed."
)

func clientReviewCommand(e env, args []string) int {
	command := args[0]
	fs := flag.NewFlagSet("client "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	key := fs.String("loop", "", "the loop's key, as panel list --json gives it")
	directory := fs.String("directory", "", "private registry directory")
	expect := fs.String("expect", "", "apply: the expect value of the review confirmed")
	asJSON := fs.Bool("json", false, "print JSON")
	lanes := fs.Bool("lanes", false, "apply stop: also stop the lanes")
	immediately := fs.Bool("immediately", false, "apply stop: carry the stop out in this command")
	dropQueued := fs.Bool("drop-queued", false, "apply stop, with --lanes: also delete the messages queued in lanes that have not started; their text is not kept")
	help := func() int {
		fmt.Fprintln(e.stdout, clientReviewUsage)
		fs.SetOutput(e.stdout)
		fs.PrintDefaults()
		return exitOK
	}
	if len(args) < 2 || panelActions[args[1]] == "" {
		if len(args) >= 2 && (args[1] == "-h" || args[1] == "-help" || args[1] == "--help") {
			return help()
		}
		e.errf("client: %s", clientReviewUsage)
		return exitUsage
	}
	action := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return help()
		}
		e.errf("client: %s", clientReviewUsage)
		return exitUsage
	}
	if fs.NArg() != 0 || !panelKeyPattern.MatchString(*key) || (command == "review" && *expect != "") || (command == "apply" && !clientExpectPattern.MatchString(*expect)) ||
		((*lanes || *immediately || *dropQueued) && (command != "apply" || action != "stop")) || (*dropQueued && !*lanes) {
		e.errf("client: %s", clientReviewUsage)
		return exitUsage
	}
	if _, err := registryDirectory(*directory); err != nil {
		e.errf("client %s: registry directory unavailable", command)
		return exitUsage
	}
	var reply clientReviewReply
	code := exitOK
	now := time.Now()
	if command == "review" {
		reply, code = clientReview(e.ctx, *directory, *key, action, now, time.Local)
	} else {
		reply, code = clientApply(e.ctx, *directory, *key, action, *expect, os.Getenv("BB_THREAD_ID"), now, holdOptions{Lanes: *lanes, Now: *immediately, DropQueued: *dropQueued})
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
		dir := ""
		if *directory != "" {
			dir = " --directory " + shellQuote(*directory)
		}
		if p.Stop != nil {
			for _, o := range p.Stop.Options {
				flags := ""
				if o.Lanes {
					flags += " --lanes"
				}
				if o.DropQueued {
					flags += " --drop-queued"
				}
				if p.Stop.Immediately {
					flags += " --immediately"
				}
				fmt.Fprintf(e.stdout, "%s: keepgoing client apply %s --loop %s --expect %s%s%s\n", o.Label, p.Action, p.Key, p.Expect, flags, dir)
			}
		} else {
			fmt.Fprintf(e.stdout, "To confirm before it expires: keepgoing client apply %s --loop %s --expect %s%s\n", p.Action, p.Key, p.Expect, dir)
		}
	} else if reply.Result != nil {
		fmt.Fprintln(e.stdout, reply.Result.Text)
	}
	return code
}

// shellQuote quotes a path for a line a person may paste into a shell.
func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func clientReview(ctx context.Context, directory, key, action string, now time.Time, loc *time.Location) (clientReviewReply, int) {
	reader := &dockReader{ctx: ctx, directory: directory, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}
	ctx, cancel := context.WithTimeout(ctx, panelChangeTimeout)
	defer cancel()
	now = now.UTC()
	r, refused := panelReview(ctx, directory, reader, key, action, now)
	if refused != nil {
		return clientReviewReply{Result: refused}, exitFailed
	}
	p := r.preview
	out := &clientReviewPreview{Action: p.Action, Key: p.Key, Title: p.Title, Lines: []string{}, Confirm: p.Confirm,
		Expect: clientExpect(key, action, p.ExpiresAt, r.digest), ExpiresAt: p.ExpiresAt, Lost: p.Lost, Refused: p.Refused, Expired: clientReviewExpired, Stop: p.Stop}
	for _, l := range p.Lines {
		out.Lines = append(out.Lines, l.renderLocal(now, loc))
	}
	return clientReviewReply{Preview: out}, exitOK
}

func clientApply(ctx context.Context, directory, key, action, expect, caller string, now time.Time, opts holdOptions) (clientReviewReply, int) {
	refuse := func(text string) (clientReviewReply, int) {
		return clientReviewReply{Result: &panelResult{Outcome: "review_again", Text: text, Retry: true}}, exitFailed
	}
	digest, expires, ok := parseClientExpect(expect, key, action)
	switch {
	case !ok:
		return refuse(clientReviewOther)
	case !now.Before(expires):
		return refuse(clientReviewExpired)
	}
	entry, _, _, ok := panelEntry(directory, key)
	if !ok {
		return clientReviewReply{Result: &panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}}, exitFailed
	}
	if action == "resume" {
		// Said plainly before anything is prepared; the resume itself makes
		// the same check again.
		if ctl, why := panelReadControl(entry.StateDir); why == "" && control.CheckOwner(ctl, caller, false) != nil {
			return clientReviewReply{Result: &panelResult{Outcome: "refused", Text: clientReviewOwned}}, exitFailed
		}
	}
	ctx, cancel := context.WithTimeout(ctx, panelChangeTimeout)
	defer cancel()
	res := panelOutcome(action, panelApply(ctx, directory, entry.ID, action, "", caller, digest, opts))
	switch {
	case res.Outcome == "done":
		return clientReviewReply{Result: &res}, exitOK
	case res.Outcome == "refused" && res.Retry:
		return clientReviewReply{Result: &res}, exitSkipped
	}
	return clientReviewReply{Result: &res}, exitFailed
}
