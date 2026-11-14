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
}

const (
	clientReviewUsage   = "usage: keepgoing client review pause|stop|resume --loop KEY [--directory private-registry] [--json]; keepgoing client apply pause|stop|resume --loop KEY --expect VALUE [--directory private-registry] [--json]; keepgoing client review pause --all [--json]; keepgoing client apply pause --all --expect KEY=VALUE... [--json]"
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
	var expects []string
	fs.Func("expect", "apply: the expect value of the review confirmed; with --all, KEY=VALUE for each loop, repeated", func(v string) error {
		expects = append(expects, v)
		return nil
	})
	all := fs.Bool("all", false, "every loop at once (pause only)")
	asJSON := fs.Bool("json", false, "print JSON")
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
	if *all {
		return clientGroupCommand(e, command, action, *directory, expects, *asJSON, *key != "")
	}
	expect := ""
	if len(expects) == 1 {
		expect = expects[0]
	}
	if fs.NArg() != 0 || !panelKeyPattern.MatchString(*key) || len(expects) > 1 || (command == "review" && expect != "") || (command == "apply" && !clientExpectPattern.MatchString(expect)) {
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
		reply, code = clientApply(e.ctx, *directory, *key, action, expect, os.Getenv("BB_THREAD_ID"), now)
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
		fmt.Fprintf(e.stdout, "To confirm before it expires: keepgoing client apply %s --loop %s --expect %s%s\n", p.Action, p.Key, p.Expect, dir)
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
		Expect: clientExpect(key, action, p.ExpiresAt, r.digest), ExpiresAt: p.ExpiresAt, Lost: p.Lost, Refused: p.Refused, Expired: clientReviewExpired}
	for _, l := range p.Lines {
		out.Lines = append(out.Lines, l.renderLocal(now, loc))
	}
	return clientReviewReply{Preview: out}, exitOK
}

func clientApply(ctx context.Context, directory, key, action, expect, caller string, now time.Time) (clientReviewReply, int) {
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
	res := panelOutcome(action, panelApply(ctx, directory, entry.ID, action, "", caller, digest))
	switch {
	case res.Outcome == "done":
		return clientReviewReply{Result: &res}, exitOK
	case res.Outcome == "refused" && res.Retry:
		return clientReviewReply{Result: &res}, exitSkipped
	}
	return clientReviewReply{Result: &res}, exitFailed
}

// clientGroupPreview is the review of one change to every loop: a line per
// loop (up to eight), the total and what the change does, and each loop's
// own expect value for the loops it changes.
type clientGroupPreview struct {
	Action    string            `json:"action"`
	Title     string            `json:"title"`
	Lines     []string          `json:"lines"`
	Confirm   string            `json:"confirm"`
	ExpiresAt time.Time         `json:"expiresAt"`
	Lost      string            `json:"lost"`
	Refused   string            `json:"refused"`
	Expired   string            `json:"expired"`
	Loops     []clientGroupLoop `json:"loops"`
}

type clientGroupLoop struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Expect is set for each loop the change is made to: client apply
	// --all --expect KEY=VALUE takes it back.
	Expect string `json:"expect,omitempty"`
}

type clientGroupReply struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Group         *clientGroupPreview `json:"group,omitempty"`
	Result        *panelResult        `json:"result,omitempty"`
}

func clientGroupCommand(e env, command, action, directory string, expects []string, asJSON, withLoop bool) int {
	if !panelGroupActions[action] || withLoop || (command == "review" && len(expects) > 0) || (command == "apply" && (len(expects) == 0 || len(expects) > 128)) {
		e.errf("client: %s", clientReviewUsage)
		return exitUsage
	}
	targets := map[string]string{}
	for _, x := range expects {
		key, value, ok := strings.Cut(x, "=")
		if _, dup := targets[key]; !ok || dup || !panelKeyPattern.MatchString(key) || !clientExpectPattern.MatchString(value) {
			e.errf("client: %s", clientReviewUsage)
			return exitUsage
		}
		targets[key] = value
	}
	if _, err := registryDirectory(directory); err != nil {
		e.errf("client %s: registry directory unavailable", command)
		return exitUsage
	}
	var reply clientGroupReply
	code := exitOK
	if command == "review" {
		reply, code = clientGroupReview(e.ctx, directory, action, time.Now(), time.Local)
	} else {
		reply, code = clientGroupApply(e.ctx, directory, action, expects, os.Getenv("BB_THREAD_ID"), time.Now())
	}
	reply.SchemaVersion = 1
	if asJSON {
		if json.NewEncoder(e.stdout).Encode(reply) != nil {
			return exitFailed
		}
		return code
	}
	if g := reply.Group; g != nil {
		fmt.Fprintln(e.stdout, g.Title)
		for _, l := range g.Lines {
			fmt.Fprintln(e.stdout, l)
		}
	} else if r := reply.Result; r != nil {
		fmt.Fprintln(e.stdout, r.Text)
		for _, l := range r.Lines {
			fmt.Fprintln(e.stdout, l)
		}
	}
	return code
}

func clientGroupReview(ctx context.Context, directory, action string, now time.Time, loc *time.Location) (clientGroupReply, int) {
	reader := &dockReader{ctx: ctx, directory: directory, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}
	ctx, cancel := context.WithTimeout(ctx, panelChangeTimeout)
	defer cancel()
	now = now.UTC()
	g, refused := panelGroupReview(ctx, directory, reader, action, now)
	if refused != nil {
		return clientGroupReply{Result: refused}, exitFailed
	}
	p := g.preview
	out := &clientGroupPreview{Action: action, Title: p.Title, Lines: []string{}, Confirm: p.Confirm, ExpiresAt: p.ExpiresAt,
		Lost: p.Lost, Refused: p.Refused, Expired: clientReviewExpired, Loops: []clientGroupLoop{}}
	for _, l := range p.Lines {
		out.Lines = append(out.Lines, l.renderLocal(now, loc))
	}
	for _, it := range g.items {
		l := clientGroupLoop{Key: it.Key, Name: it.Name}
		if it.Fits {
			l.Expect = clientExpect(it.Key, action, p.ExpiresAt, it.digest)
		}
		out.Loops = append(out.Loops, l)
	}
	return clientGroupReply{Group: out}, exitOK
}

// clientGroupApply changes each loop named, with its own expect value: a
// value for another loop or change, or past its time, refuses that loop
// alone, and the rest go ahead.
func clientGroupApply(ctx context.Context, directory, action string, expects []string, caller string, now time.Time) (clientGroupReply, int) {
	var targets []panelGroupTarget
	names := map[string]string{}
	var refused []panelLoopTo
	for _, x := range expects {
		key, value, _ := strings.Cut(x, "=")
		name := ""
		if entry, _, _, ok := panelEntry(directory, key); ok {
			name = clip(entry.Loop, dockMaxText)
		} else {
			refused = append(refused, panelLoopTo{Key: key, Name: key, Outcome: "refused", Text: key + ": not " + panelDone[action] + ". This loop is no longer registered. Nothing was changed."})
			continue
		}
		digest, expires, ok := parseClientExpect(value, key, action)
		switch {
		case !ok:
			refused = append(refused, panelLoopTo{Key: key, Name: name, Outcome: "review_again", Text: panelGroupLoopText(name, action, panelResult{Outcome: "review_again"})})
		case !now.Before(expires):
			refused = append(refused, panelLoopTo{Key: key, Name: name, Outcome: "review_again", Text: name + ": the review expired, so it was not " + panelDone[action] + "; review it again."})
		default:
			targets = append(targets, panelGroupTarget{Key: key, Digest: digest})
			names[key] = name
		}
	}
	ctx, cancel := context.WithTimeout(ctx, panelChangeTimeout)
	defer cancel()
	g := panelGroupApply(ctx, directory, action, "", caller, targets, names)
	g.Loops = append(refused, g.Loops...)
	g = panelGroupTotal(action, g)
	res := &panelResult{Outcome: g.Outcome, Text: g.Text, Retry: g.Retry, Lines: g.Lines}
	if g.Outcome == "done" {
		return clientGroupReply{Result: res}, exitOK
	}
	return clientGroupReply{Result: res}, exitFailed
}
