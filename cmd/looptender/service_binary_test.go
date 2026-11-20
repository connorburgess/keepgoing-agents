package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestServiceBinaryHealth(t *testing.T) {
	for _, tt := range []struct{ state, comparison, status, code string }{
		{"available", "match", "ok", ""},
		{"available", "different", "warning", "executable_differs"},
		{"available", "unknown", "unavailable", "executable_comparison_unknown"},
		{"missing", "unknown", "warning", "executable_missing"},
		{"non_regular", "unknown", "warning", "executable_non_regular"},
		{"no_execute_bits", "unknown", "warning", "executable_no_execute_bits"},
		{"unavailable", "unknown", "unavailable", "executable_unavailable"},
	} {
		t.Run(tt.state+tt.comparison, func(t *testing.T) {
			r := serviceReport{Backend: "launchd", Recorded: true, DefinitionPresent: true, Registration: "loaded", Daemon: &daemonStatus{State: "responsive", BinaryComparison: "match"}, Executable: &serviceBinary{State: tt.state, Comparison: tt.comparison}}
			h := assessServiceHealth(r)
			if h.Status != tt.status {
				t.Fatalf("%+v", h)
			}
			if tt.code == "" {
				if len(h.Findings) != 0 {
					t.Fatalf("%+v", h)
				}
			} else if len(h.Findings) != 1 || h.Findings[0].Code != tt.code {
				t.Fatalf("%+v", h)
			}
		})
	}
}

type cancelBinaryReader struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (r cancelBinaryReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.cancel()
	return n, err
}

func TestServiceBinaryHashIsBoundedAndCancellable(t *testing.T) {
	ctx := context.Background()
	for _, size := range []int64{-1, serviceBinaryLimit + 1} {
		if _, err := hashServiceBinary(ctx, strings.NewReader(""), size); err == nil {
			t.Fatal("accepted invalid size")
		}
	}
	contents := strings.Repeat("fictional binary data", 10000)
	digest, err := hashServiceBinary(ctx, strings.NewReader(contents), int64(len(contents)))
	if err != nil || digest != sha256.Sum256([]byte(contents)) {
		t.Fatal("wrong digest", err)
	}
	if _, err := hashServiceBinary(ctx, strings.NewReader("short"), 100); err == nil {
		t.Fatal("accepted truncated file")
	}
	buf := bytes.NewBufferString("prefixextra")
	if _, err := hashServiceBinary(ctx, buf, 6); err != nil || buf.String() != "extra" {
		t.Fatal("read beyond snapshot size", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	r := cancelBinaryReader{strings.NewReader(contents), cancel}
	if _, err := hashServiceBinary(cancelled, r, int64(len(contents))); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
}
