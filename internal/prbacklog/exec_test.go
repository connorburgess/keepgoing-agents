package prbacklog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPRBacklogSubprocess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--backlog-fixture" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "small":
		fmt.Print(`{"fictional":true}`)
	case "large":
		fmt.Print(strings.Repeat("x", MaxBytes+1))
	case "wait":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}
func TestExecGHBoundsAndCancellation(t *testing.T) {
	run := ExecGH(os.Args[0])
	args := []string{"-test.run=^TestPRBacklogSubprocess$", "--", "--backlog-fixture"}
	b, err := run(context.Background(), append(args, "small")...)
	if err != nil || string(b) != `{"fictional":true}` {
		t.Fatalf("%s %v", b, err)
	}
	if _, err = run(context.Background(), append(args, "large")...); err == nil {
		t.Fatal("unbounded stdout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err = run(ctx, append(args, "wait")...); err == nil {
		t.Fatal("ignored cancellation")
	}
}
