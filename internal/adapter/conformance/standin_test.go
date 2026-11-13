package conformance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
)

// The test binary doubles as the stand-in agent, so the suite runs natively
// on every platform with no shell. CONFORMANCE_STANDIN names the part to
// play and CONFORMANCE_DIR the rig's directory, which holds:
//
//	situation   what the stand-in should do next
//	sent.jsonl  every prompt it received
//	stops       one line per stop command it received

const (
	envPart = "CONFORMANCE_STANDIN"
	envDir  = "CONFORMANCE_DIR"
	// standInSession is the session every stand-in turn reports.
	standInSession = "00000000-0000-4000-8000-0000000000c0"
	// oversize is far more than any real status or turn result.
	oversize = 12 << 20
)

func TestMain(m *testing.M) {
	runner.MaybeRunTurn() // headless turns run through a copy of this binary
	if part := os.Getenv(envPart); part != "" {
		os.Exit(standIn(part, os.Getenv(envDir), os.Args[1:]))
	}
	os.Exit(m.Run())
}

func situationOf(dir string) Situation {
	b, _ := os.ReadFile(filepath.Join(dir, "situation"))
	return Situation(strings.TrimSpace(string(b)))
}

func record(dir string, s Sent) {
	f, err := os.OpenFile(filepath.Join(dir, "sent.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(96)
	}
	defer f.Close()
	b, _ := json.Marshal(s)
	f.Write(append(b, '\n'))
}

func filler(n int) {
	w := bufio.NewWriter(os.Stdout)
	line := strings.Repeat("x", 1023) + "\n"
	for written := 0; written < n; written += len(line) {
		w.WriteString(line)
	}
	w.Flush()
}

func standIn(part, dir string, args []string) int {
	situation := situationOf(dir)
	switch part {
	case "sleep":
		time.Sleep(10 * time.Minute)
		return 0
	case "process":
		return standInProcess(dir, situation, args)
	case "claude":
		return standInClaude(dir, situation, args)
	case "codex":
		return standInCodex(dir, situation, args)
	}
	return 97
}

// standInProcess plays the three commands of a process-adapter wrapper.
func standInProcess(dir string, situation Situation, args []string) int {
	if len(args) == 0 {
		return 97
	}
	switch args[0] {
	case "resume":
		prompt, _ := io.ReadAll(os.Stdin)
		record(dir, Sent{Prompt: string(prompt), Argv: args})
		return 0
	case "stop":
		f, _ := os.OpenFile(filepath.Join(dir, "stops"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString("stop\n")
		f.Close()
		os.WriteFile(filepath.Join(dir, "situation"), []byte(Idle), 0o600)
		return 0
	}
	switch situation {
	case Idle:
		fmt.Println(`{"state":"idle"}`)
	case Active:
		fmt.Println(`{"state":"active"}`)
	case Waiting:
		fmt.Println(`{"state":"idle","awaitingInput":true}`)
	case FailedCoded:
		fmt.Println(`{"state":"error","errorClass":"rate_limit"}`)
	case FailedProse:
		fmt.Println(`{"state":"error","errorClass":"You have hit your usage limit, try again later"}`)
	case Garbage:
		fmt.Println("the agent is doing fine, probably idle")
	case Hanging:
		time.Sleep(10 * time.Minute)
	case Oversized:
		fmt.Print(`{"state":"idle","padding":"`)
		w := bufio.NewWriter(os.Stdout)
		chunk := strings.Repeat("x", 1024)
		for written := 0; written < oversize; written += len(chunk) {
			w.WriteString(chunk)
		}
		w.Flush()
		fmt.Println(`"}`)
	}
	return 0
}

// standInClaude plays `claude -p --output-format json`.
func standInClaude(dir string, situation Situation, args []string) int {
	session := ""
	for i, a := range args {
		if a == "--resume" && i+1 < len(args) {
			session = args[i+1]
		}
	}
	prompt, _ := io.ReadAll(os.Stdin)
	record(dir, Sent{Prompt: string(prompt), Argv: args, Session: session})
	ok := `{"type":"result","subtype":"success","is_error":false,"session_id":"` + standInSession + `","api_error_status":null}`
	switch situation {
	case Idle:
		fmt.Println(ok)
	case FailedCoded:
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` + standInSession + `","api_error_status":429}`)
		return 1
	case FailedProse:
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` + standInSession + `","api_error_status":null,"result":"You have hit your usage limit (429)"}`)
		return 1
	case Garbage:
		fmt.Println("All done! The task is complete.")
	case Active, Hanging:
		time.Sleep(10 * time.Minute)
	case Oversized:
		filler(oversize)
		fmt.Println(ok)
	}
	return 0
}

// standInCodex plays `codex exec --json`, and has no app-server.
func standInCodex(dir string, situation Situation, args []string) int {
	if len(args) > 0 && args[0] == "app-server" {
		return 1
	}
	session := ""
	if len(args) > 2 && args[1] == "resume" {
		session = args[len(args)-2]
	}
	prompt, _ := io.ReadAll(os.Stdin)
	record(dir, Sent{Prompt: string(prompt), Argv: args, Session: session})
	started := `{"type":"thread.started","thread_id":"` + standInSession + `"}`
	switch situation {
	case Idle:
		fmt.Println(started)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
	case FailedCoded:
		fmt.Println(started)
		fmt.Println(`{"type":"turn.failed","error":{"code":"usage_limit_reached"}}`)
		return 1
	case FailedProse:
		fmt.Println(started)
		fmt.Println(`{"type":"turn.failed","error":{"message":"You have hit your usage limit"}}`)
		return 1
	case Garbage:
		fmt.Println("All done! The task is complete.")
	case Active, Hanging:
		time.Sleep(10 * time.Minute)
	case Oversized:
		fmt.Println(started)
		filler(oversize)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
	}
	return 0
}
