package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func acpExchangeStream(saved bool, after ...string) string {
	open := `{"jsonrpc":"2.0","id":"keepgoing-open","result":{"sessionId":"` + acpFixtureSession + `"}}`
	if saved {
		open = `{"jsonrpc":"2.0","id":"keepgoing-open","result":{}}`
	}
	return strings.Join(append([]string{acpInitResponse, open}, after...), "\n") + "\n"
}

type acpExchangeWriter struct {
	frames [][]byte
	before func([]byte)
	failAt int
	short  bool
}

func (w *acpExchangeWriter) Write(p []byte) (int, error) {
	if w.before != nil {
		w.before(p)
	}
	w.frames = append(w.frames, append([]byte(nil), p...))
	if len(w.frames) == w.failAt {
		if w.short {
			return len(p) / 2, nil
		}
		return len(p) / 2, errors.New("fictional-private-writer-detail")
	}
	return len(p), nil
}

func acpExchangeFrame(t *testing.T, p []byte) (string, string) {
	t.Helper()
	var f struct {
		Method string
		ID     json.RawMessage
		Result struct {
			Outcome struct {
				Outcome  string
				OptionID *string
			}
		}
	}
	if err := json.Unmarshal(p, &f); err != nil {
		t.Fatal(err)
	}
	if f.Method != "" {
		return f.Method, ""
	}
	if f.Result.Outcome.Outcome != "cancelled" || f.Result.Outcome.OptionID != nil {
		t.Fatalf("non-cancel permission reply: %s", p)
	}
	return "permission-cancelled", string(f.ID)
}

func TestAntigravityACPExchangeFragmentedAndCheckpointOrder(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", saved), func(t *testing.T) {
			var checkpoints []AntigravityACPCheckpoint
			var events []string
			w := &acpExchangeWriter{before: func(p []byte) {
				method, id := acpExchangeFrame(t, p)
				events = append(events, method+id)
				if method == "session/prompt" && (len(checkpoints) != 1 || !checkpoints[0].BeforePrompt) {
					t.Fatal("prompt before checkpoint")
				}
				if method == "session/cancel" || method == "permission-cancelled" {
					want := 2
					if id == `"next"` {
						want = 3
					}
					if len(checkpoints) != want || !checkpoints[want-1].NeedsUser {
						t.Fatal("permission response before checkpoint")
					}
				}
			}}
			input := acpExchangeStream(saved, acpPermission(`42`, acpFixtureSession), acpPermission(`"next"`, acpFixtureSession), acpResult("end_turn"))
			session := ""
			if saved {
				session = acpFixtureSession
			}
			out, err := ExchangeAntigravityACP(context.Background(), iotest.OneByteReader(strings.NewReader(input)), w, session, t.TempDir(), "fictional café\n项目", func(_ context.Context, c AntigravityACPCheckpoint) error {
				if c.SessionID != acpFixtureSession || c.BeforePrompt == c.NeedsUser {
					t.Fatalf("checkpoint: %+v", c)
				}
				checkpoints = append(checkpoints, c)
				events = append(events, "checkpoint")
				return nil
			})
			if err != nil || !out.NeedsUser || out.TurnReturned || out.Failed {
				t.Fatalf("outcome %+v %v", out, err)
			}
			open := "session/new"
			if saved {
				open = "session/resume"
			}
			want := strings.Join([]string{"initialize", open, "checkpoint", "session/prompt", "checkpoint", "session/cancel", "permission-cancelled42", "checkpoint", `permission-cancelled"next"`}, ",")
			if strings.Join(events, ",") != want {
				t.Fatalf("ordering %v", events)
			}
			var prompt struct {
				Params struct {
					SessionID string
					Prompt    []struct{ Text string }
				}
			}
			if err := json.Unmarshal(w.frames[2], &prompt); err != nil {
				t.Fatal(err)
			}
			if prompt.Params.SessionID != acpFixtureSession || len(prompt.Params.Prompt) != 1 || prompt.Params.Prompt[0].Text != "fictional café\n项目" {
				t.Fatalf("private transport: %s", w.frames[2])
			}
		})
	}
}

func TestAntigravityACPExchangeSuccessAndTerminalBoundary(t *testing.T) {
	r := strings.NewReader(acpExchangeStream(false, acpResult("end_turn")) + "unrelated next connection data")
	w := &acpExchangeWriter{}
	calls := 0
	out, err := ExchangeAntigravityACP(context.Background(), r, w, "", t.TempDir(), "fictional task", func(_ context.Context, c AntigravityACPCheckpoint) error { calls++; return nil })
	if err != nil || !out.TurnReturned || out.NeedsUser || out.Failed || calls != 1 || len(w.frames) != 3 {
		t.Fatalf("outcome %+v %v, checkpoints %d, writes %d", out, err, calls, len(w.frames))
	}
}

func TestAntigravityACPExchangeCheckpointFailureStopsWrites(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			w := &acpExchangeWriter{}
			calls := 0
			input := acpExchangeStream(false, acpPermission(`1`, acpFixtureSession), acpPermission(`2`, acpFixtureSession), acpResult("end_turn"))
			out, err := ExchangeAntigravityACP(context.Background(), strings.NewReader(input), w, "", t.TempDir(), "fictional task", func(_ context.Context, c AntigravityACPCheckpoint) error {
				calls++
				if calls == failAt {
					return errors.New("fictional-private-checkpoint-detail")
				}
				return nil
			})
			wantWrites := map[int]int{1: 2, 2: 3, 3: 5}[failAt]
			if err == nil || err.Error() != "acp: exchange checkpoint failed" || !out.Failed || out.TurnReturned || out.NeedsUser != (failAt > 1) || calls != failAt || len(w.frames) != wantWrites {
				t.Fatalf("outcome %+v %v, checkpoints %d, writes %d", out, err, calls, len(w.frames))
			}
		})
	}
}

func TestAntigravityACPExchangeWriteFailuresNeverReplay(t *testing.T) {
	for _, short := range []bool{false, true} {
		for failAt := 1; failAt <= 5; failAt++ {
			t.Run(fmt.Sprintf("short=%t/write=%d", short, failAt), func(t *testing.T) {
				w := &acpExchangeWriter{failAt: failAt, short: short}
				input := acpExchangeStream(false, acpPermission(`1`, acpFixtureSession), acpResult("end_turn"))
				calls := 0
				out, err := ExchangeAntigravityACP(context.Background(), strings.NewReader(input), w, "", t.TempDir(), "fictional task", func(context.Context, AntigravityACPCheckpoint) error { calls++; return nil })
				wantCalls := 0
				if failAt >= 3 {
					wantCalls = 1
				}
				if failAt >= 4 {
					wantCalls = 2
				}
				if err == nil || err.Error() != "acp: exchange write failed" || len(w.frames) != failAt || calls != wantCalls || !out.Failed || out.TurnReturned || out.NeedsUser != (failAt >= 4) {
					t.Fatalf("outcome %+v %v, calls %d, writes %d", out, err, calls, len(w.frames))
				}
			})
		}
	}
}

type acpInjectedReadError struct{}

func (acpInjectedReadError) Read([]byte) (int, error) {
	return 0, errors.New("fictional-private-reader-detail")
}

type acpCountReader struct {
	r io.Reader
	n int
}

func (r *acpCountReader) Read(p []byte) (int, error) { n, err := r.r.Read(p); r.n += n; return n, err }

func TestAntigravityACPExchangeIncompleteAndBadReads(t *testing.T) {
	cases := map[string]struct {
		r      io.Reader
		hold   bool
		writes int
	}{
		"empty":                {strings.NewReader(""), false, 1},
		"partial init":         {strings.NewReader(`{"jsonrpc":`), false, 1},
		"init only":            {strings.NewReader(acpInitResponse + "\n"), false, 2},
		"prompt incomplete":    {strings.NewReader(acpExchangeStream(false)), false, 3},
		"terminal without LF":  {strings.NewReader(strings.TrimSuffix(acpExchangeStream(false, acpResult("end_turn")), "\n")), false, 3},
		"permission then EOF":  {strings.NewReader(acpExchangeStream(false, acpPermission(`1`, acpFixtureSession))), true, 5},
		"reader error":         {acpInjectedReadError{}, false, 1},
		"invalid utf8":         {strings.NewReader("\xff\n"), false, 1},
		"wrong session":        {strings.NewReader(acpExchangeStream(false, acpPermission(`1`, "wrong"))), false, 3},
		"unsupported question": {strings.NewReader(acpExchangeStream(false, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"elicitation/create","params":{"sessionId":%q,"mode":"form"}}`, acpFixtureSession))), false, 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := &acpExchangeWriter{}
			out, err := ExchangeAntigravityACP(context.Background(), tc.r, w, "", t.TempDir(), "fictional task", func(context.Context, AntigravityACPCheckpoint) error { return nil })
			if err == nil || strings.Contains(err.Error(), "private") || !out.Failed || out.TurnReturned || out.NeedsUser != tc.hold || len(w.frames) != tc.writes {
				t.Fatalf("outcome %+v %v, writes %d", out, err, len(w.frames))
			}
		})
	}
	t.Run("oversize without delimiter", func(t *testing.T) {
		r := &acpCountReader{r: strings.NewReader(strings.Repeat(" ", antigravityACPFrameLimit*3))}
		w := &acpExchangeWriter{}
		out, err := ExchangeAntigravityACP(context.Background(), r, w, "", t.TempDir(), "fictional task", func(context.Context, AntigravityACPCheckpoint) error { return nil })
		if err == nil || !out.Failed || len(w.frames) != 1 || r.n > antigravityACPFrameLimit+4096 {
			t.Fatalf("unbounded read: %+v %v bytes=%d", out, err, r.n)
		}
	})
}

func TestAntigravityACPExchangePreflightHasNoSideEffects(t *testing.T) {
	for _, name := range []string{"saved id", "relative cwd", "cwd utf8", "cwd nul", "cwd encoded limit", "prompt utf8", "prompt raw limit", "prompt encoded limit", "nil checkpoint", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			saved := ""
			cwd := t.TempDir()
			prompt := "fictional task"
			ctx := context.Background()
			calls := 0
			checkpoint := func(context.Context, AntigravityACPCheckpoint) error { calls++; return nil }
			switch name {
			case "saved id":
				saved = "invalid\nidentity"
			case "relative cwd":
				cwd = "relative"
			case "cwd utf8":
				cwd += "\xff"
			case "cwd nul":
				cwd += "\x00"
			case "cwd encoded limit":
				cwd += strings.Repeat("<", antigravityACPFrameLimit/2)
			case "prompt utf8":
				prompt = "\xff"
			case "prompt raw limit":
				prompt = strings.Repeat("x", antigravityACPFrameLimit+1)
			case "prompt encoded limit":
				prompt = strings.Repeat("\x00", antigravityACPFrameLimit/2)
			case "nil checkpoint":
				checkpoint = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			w := &acpExchangeWriter{}
			r := &acpCountReader{r: strings.NewReader(acpExchangeStream(false, acpResult("end_turn")))}
			out, err := ExchangeAntigravityACP(ctx, r, w, saved, cwd, prompt, checkpoint)
			if err == nil || !out.Failed || len(w.frames) != 0 || r.n != 0 || calls != 0 {
				t.Fatalf("preflight side effects: %+v %v reads=%d writes=%d checkpoints=%d", out, err, r.n, len(w.frames), calls)
			}
		})
	}
}

func TestAntigravityACPExchangeCheckpointCancellationStopsWrites(t *testing.T) {
	for _, at := range []int{1, 2} {
		ctx, cancel := context.WithCancel(context.Background())
		w := &acpExchangeWriter{}
		calls := 0
		out, err := ExchangeAntigravityACP(ctx, strings.NewReader(acpExchangeStream(false, acpPermission(`1`, acpFixtureSession))), w, "", t.TempDir(), "fictional task", func(context.Context, AntigravityACPCheckpoint) error {
			calls++
			if calls == at {
				cancel()
			}
			return nil
		})
		cancel()
		want := 2
		if at == 2 {
			want = 3
		}
		if err == nil || err.Error() != "acp: exchange cancelled" || !out.Failed || out.NeedsUser != (at == 2) || len(w.frames) != want {
			t.Fatalf("post-checkpoint cancellation: %+v %v writes=%d", out, err, len(w.frames))
		}
	}
}

func TestAntigravityACPFrameReaderBounds(t *testing.T) {
	for _, size := range []int{1, 4096, 4097, antigravityACPFrameLimit} {
		frame := strings.Repeat("x", size-1) + "\n"
		r := bufio.NewReaderSize(iotest.HalfReader(strings.NewReader(frame)), 4096)
		got, err := readAntigravityACPFrame(context.Background(), r, size)
		if err != nil || !bytes.Equal(got, []byte(frame)) {
			t.Fatalf("size %d: len=%d %v", size, len(got), err)
		}
	}
}

func TestAntigravityACPExchangeBidirectionalPipe(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	deadline := time.Now().Add(5 * time.Second)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := peer.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	checkpoints := make(chan AntigravityACPCheckpoint, 2)
	finished := make(chan error, 1)
	go func() {
		r := bufio.NewReader(peer)
		readMethod := func(want string) error {
			frame, err := r.ReadBytes('\n')
			if err != nil {
				return err
			}
			var f struct {
				Method string
				Result struct {
					Outcome struct {
						Outcome  string
						OptionID *string
					}
				}
			}
			if err := json.Unmarshal(frame, &f); err != nil {
				return err
			}
			if want == "permission-cancelled" {
				if f.Method != "" || f.Result.Outcome.Outcome != "cancelled" || f.Result.Outcome.OptionID != nil {
					return errors.New("unexpected permission reply")
				}
			} else if f.Method != want {
				return fmt.Errorf("method %q, want %q", f.Method, want)
			}
			return nil
		}
		write := func(frame string) error { _, err := io.WriteString(peer, frame+"\n"); return err }
		result := func() error {
			if err := readMethod("initialize"); err != nil {
				return err
			}
			if err := write(acpInitResponse); err != nil {
				return err
			}
			if err := readMethod("session/resume"); err != nil {
				return err
			}
			if err := write(`{"jsonrpc":"2.0","id":"keepgoing-open","result":{}}`); err != nil {
				return err
			}
			if err := readMethod("session/prompt"); err != nil {
				return err
			}
			select {
			case c := <-checkpoints:
				if !c.BeforePrompt {
					return errors.New("prompt reservation absent")
				}
			default:
				return errors.New("prompt preceded checkpoint")
			}
			if err := write(acpPermission(`1`, acpFixtureSession)); err != nil {
				return err
			}
			if err := readMethod("session/cancel"); err != nil {
				return err
			}
			select {
			case c := <-checkpoints:
				if !c.NeedsUser {
					return errors.New("hold absent")
				}
			default:
				return errors.New("cancel preceded checkpoint")
			}
			if err := readMethod("permission-cancelled"); err != nil {
				return err
			}
			return write(acpResult("cancelled"))
		}()
		finished <- result
	}()
	out, err := ExchangeAntigravityACP(context.Background(), client, client, acpFixtureSession, t.TempDir(), "fictional task", func(_ context.Context, c AntigravityACPCheckpoint) error { checkpoints <- c; return nil })
	// Close before joining on failures so the fixture cannot leave a blocked peer.
	_ = client.Close()
	peerErr := <-finished
	if err != nil || peerErr != nil || out.Failed || !out.NeedsUser || out.TurnReturned {
		t.Fatalf("exchange %+v %v, peer %v", out, err, peerErr)
	}
}

func TestAntigravityACPExchangeConnectionLimits(t *testing.T) {
	for _, kind := range []string{"bytes", "frames"} {
		t.Run(kind, func(t *testing.T) {
			content := "x"
			count := antigravityACPFrameCount + 1
			if kind == "bytes" {
				content = strings.Repeat("x", antigravityACPFrameLimit/2)
				count = 10
			}
			frame := fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`, acpFixtureSession, content) + "\n"
			input := acpExchangeStream(false) + strings.Repeat(frame, count)
			r := &acpCountReader{r: strings.NewReader(input)}
			w := &acpExchangeWriter{}
			out, err := ExchangeAntigravityACP(context.Background(), r, w, "", t.TempDir(), "fictional task", func(context.Context, AntigravityACPCheckpoint) error { return nil })
			if err == nil || !out.Failed || len(w.frames) != 3 || r.n > antigravityACPTotalLimit+4096 {
				t.Fatalf("limit: %+v %v reads=%d writes=%d", out, err, r.n, len(w.frames))
			}
		})
	}
}
