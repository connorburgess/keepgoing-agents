package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

// AntigravityACPCheckpoint is minimal evidence for the caller's private durable
// record. BeforePrompt reserves an attempt, not proof it was delivered. NeedsUser
// requires a hold; it never grants permission. The caller must bind this record
// to its own target/turn identity and return only after durable persistence.
// Fields assert evidence, never clear it: false fields must preserve earlier
// reservations and holds. The callback must not discard a previous checkpoint.
type AntigravityACPCheckpoint struct {
	SessionID    string
	BeforePrompt bool
	NeedsUser    bool
}

// ExchangeAntigravityACP drives one prompt over an exclusively owned ACP v1
// stream. It creates no process, closes neither stream, and registers no adapter.
// The checkpoint callback is required before the prompt and before every set of
// permission cancellation/reply frames. No write, prompt or checkpoint is retried.
//
// Framing and retained evidence are size/count bounded, not wall-time bounded.
// There are no I/O goroutines. Context is checked between operations; callers
// must arrange deadlines or close their own transport to interrupt blocked reads,
// writes or checkpoint callbacks. The connection is not reusable by this API:
// buffered bytes beyond the terminal prompt response may have been read ahead.
// The caller must close/terminate its transport on failure; no failed exchange
// may be replayed automatically. Returning turn evidence does not prove process
// exit or task completion.
//
// This is an internal transport foundation. No durable storage implementation,
// owned process lifecycle, question handoff or unattended eligibility is supplied.
func ExchangeAntigravityACP(ctx context.Context, r io.Reader, w io.Writer, saved, cwd, prompt string, checkpoint func(context.Context, AntigravityACPCheckpoint) error) (AntigravityACPOutcome, error) {
	return ExchangeACP(ctx, r, w, ACPOptions{RequireResume: true}, saved, cwd, prompt, checkpoint)
}

// ExchangeACP is the exchange for any agent that speaks ACP v1, with the
// options that differ between agents. Everything said of
// ExchangeAntigravityACP holds for it.
func ExchangeACP(ctx context.Context, r io.Reader, w io.Writer, opts ACPOptions, saved, cwd, prompt string, checkpoint func(context.Context, AntigravityACPCheckpoint) error) (ACPOutcome, error) {
	g, err := NewACPGate(saved, opts)
	if err != nil {
		return AntigravityACPOutcome{Failed: true}, errors.New("acp: exchange configuration failed")
	}
	fail := func(stage string) (AntigravityACPOutcome, error) {
		_ = g.fail()
		// Never expose reader, writer, callback or provider error text.
		if stage == "cancelled" {
			return g.Outcome(), errors.New("acp: exchange cancelled")
		}
		return g.Outcome(), errors.New("acp: exchange " + stage + " failed")
	}
	if ctx == nil || r == nil || w == nil || checkpoint == nil {
		return fail("configuration")
	}
	// Validate local inputs before writing even initialization or creating a remote
	// session. For a new session, reserve the largest encoded bounded ID overhead.
	if _, err := acpOpenSessionFrame(saved, cwd); err != nil {
		return fail("configuration")
	}
	promptSession := saved
	if promptSession == "" {
		promptSession = strings.Repeat("<", 256)
	}
	if _, err := acpPromptFrame(promptSession, prompt); err != nil {
		return fail("configuration")
	}
	write := func(frame []byte) string {
		if ctx.Err() != nil {
			return "cancelled"
		}
		n, err := w.Write(frame)
		if err != nil || n != len(frame) {
			return "write"
		}
		return ""
	}
	save := func(record AntigravityACPCheckpoint) string {
		if ctx.Err() != nil {
			return "cancelled"
		}
		if err := checkpoint(ctx, record); err != nil {
			return "checkpoint"
		}
		if ctx.Err() != nil {
			return "cancelled"
		}
		return ""
	}
	initial, err := g.Initialize()
	if err != nil {
		return fail("protocol")
	}
	if stage := write(initial); stage != "" {
		return fail(stage)
	}
	reader := bufio.NewReaderSize(r, 4096)
	for {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		if g.frames >= antigravityACPFrameCount {
			return fail("read")
		}
		limit := min(antigravityACPFrameLimit, antigravityACPTotalLimit-g.bytes)
		frame, err := readAntigravityACPFrame(ctx, reader, limit)
		if err != nil {
			return fail("read")
		}
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		outgoing, err := g.Receive(frame)
		if err != nil {
			return fail("protocol")
		}
		if len(outgoing) > 0 {
			// Gate output can only be permission cancellation/replies. Persist the hold
			// before even the cancellation notification can leave this process.
			if !g.out.NeedsUser {
				return fail("protocol")
			}
			if stage := save(AntigravityACPCheckpoint{SessionID: g.out.SessionID, NeedsUser: true}); stage != "" {
				return fail(stage)
			}
			for _, reply := range outgoing {
				if stage := write(reply); stage != "" {
					return fail(stage)
				}
			}
		}
		switch g.phase {
		case "initialized":
			frame, err := g.OpenSession(cwd)
			if err != nil {
				return fail("configuration")
			}
			if stage := write(frame); stage != "" {
				return fail(stage)
			}
		case "ready":
			frame, err := g.Prompt(prompt)
			if err != nil {
				return fail("configuration")
			}
			if stage := save(AntigravityACPCheckpoint{SessionID: g.out.SessionID, BeforePrompt: true}); stage != "" {
				return fail(stage)
			}
			if stage := write(frame); stage != "" {
				return fail(stage)
			}
		case "done":
			return g.Outcome(), nil
		}
	}
}

// readAntigravityACPFrame requires LF even for the last frame. EOF without LF
// is truncated evidence. ReadSlice bounds its own buffer; fragments are copied
// only within the remaining per-frame/connection allowance.
func readAntigravityACPFrame(ctx context.Context, r *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("acp: frame limit")
	}
	var frame []byte
	for {
		if ctx.Err() != nil {
			return nil, errors.New("acp: read cancelled")
		}
		part, err := r.ReadSlice('\n')
		if len(part) > limit-len(frame) {
			return nil, errors.New("acp: frame limit")
		}
		frame = append(frame, part...)
		if err == nil {
			return frame, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, errors.New("acp: incomplete frame")
		}
	}
}
