package prbacklog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// ExecGH runs only the supplied argument vector, without a shell, with bounded
// stdout and no stderr retention. Collect supplies the fixed read-only query.
func ExecGH(path string) Query {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.WaitDelay = time.Second
		cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_PAGER=cat")
		var b limitedBuffer
		b.remaining = MaxBytes
		cmd.Stdout = &b
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if b.exceeded {
			return nil, errors.New("output limit")
		}
		return b.buffer.Bytes(), err
	}
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	exceeded  bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		b.exceeded = true
		return 0, errors.New("output limit")
	}
	b.remaining -= len(p)
	return b.buffer.Write(p)
}
