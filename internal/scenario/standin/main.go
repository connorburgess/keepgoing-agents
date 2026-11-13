// Command standin is a test-only stand-in for the bb CLI, used by the
// scenario suite in internal/scenario. It is compiled once per test run and
// installed as bb (bb.exe on Windows), so it runs natively on every platform
// without a POSIX shell.
//
// All state lives in the directory named by SCENARIO_WORLD, which the test
// writes and reads:
//
//	<thread>.show          JSON returned by `thread show`
//	<thread>.log           JSON array of log events, filtered by --after-seq
//	<thread>.children      JSON returned by `thread list --parent-thread`
//	<thread>.interactions  JSON returned by `thread interactions list`
//	calls.jsonl            every invocation, one JSON array of arguments per line
//	sends.jsonl            every `thread tell` (thread, mode, message) and every
//	                       accepted `thread retry`
//
// `thread retry` re-submits a failed turn: it is accepted only while the
// thread's show file says "error", and answers no_failed_turn otherwise.
//
// Any command it does not know is recorded and answered with {"ok":true}.
// It never contacts a network or a real agent.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	world := os.Getenv("SCENARIO_WORLD")
	if world == "" {
		fmt.Fprintln(os.Stderr, "standin: SCENARIO_WORLD is not set")
		return 97
	}
	appendLine(filepath.Join(world, "calls.jsonl"), args)
	if len(args) < 2 || args[0] != "thread" {
		fmt.Println(`{"ok":true}`)
		return 0
	}
	switch args[1] {
	case "show":
		return emit(world, arg(args, 2)+".show", `{"thread":{"status":"idle"}}`)
	case "list":
		return emit(world, flagValue(args, "--parent-thread")+".children", `[]`)
	case "interactions":
		return emit(world, arg(args, 3)+".interactions", `[]`)
	case "log":
		return emitLog(world, arg(args, 2), flagValue(args, "--after-seq"))
	case "tell":
		msg, _ := io.ReadAll(os.Stdin)
		appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{
			"kind": "tell", "thread": arg(args, 2), "mode": flagValue(args, "--mode"), "message": string(msg),
		})
		fmt.Println(`{"ok":true}`)
		return 0
	case "retry":
		return retry(world, arg(args, 2))
	}
	fmt.Println(`{"ok":true}`)
	return 0
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func emit(world, name, fallback string) int {
	b, err := os.ReadFile(filepath.Join(world, name))
	if err != nil {
		b = []byte(fallback)
	}
	os.Stdout.Write(b)
	fmt.Println()
	return 0
}

// emitLog returns the events newer than --after-seq, oldest first, as bb does.
func emitLog(world, thread, after string) int {
	var events []map[string]any
	if b, err := os.ReadFile(filepath.Join(world, thread+".log")); err == nil {
		if err := json.Unmarshal(b, &events); err != nil {
			fmt.Fprintln(os.Stderr, "standin: bad log fixture:", err)
			return 98
		}
	}
	min, _ := strconv.ParseFloat(after, 64)
	out := []map[string]any{}
	for _, e := range events {
		if seq, _ := e["seq"].(float64); seq > min {
			out = append(out, e)
		}
	}
	b, _ := json.Marshal(out)
	os.Stdout.Write(b)
	fmt.Println()
	return 0
}

func appendLine(path string, v any) {
	b, _ := json.Marshal(v)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func retry(world, thread string) int {
	var show struct {
		Thread struct {
			Status string `json:"status"`
		} `json:"thread"`
	}
	b, _ := os.ReadFile(filepath.Join(world, thread+".show"))
	if json.Unmarshal(b, &show) != nil || show.Thread.Status != "error" {
		fmt.Println(`{"ok":false,"error":{"code":"no_failed_turn"}}`)
		return 1
	}
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{"kind": "retry", "thread": thread})
	fmt.Println(`{"ok":true,"delivery":"sent","attempt":1}`)
	return 0
}
