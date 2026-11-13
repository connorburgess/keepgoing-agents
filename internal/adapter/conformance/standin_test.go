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
//	live        "begin" and "finish" lines of agents that run one turn each
//	quit        tells an agent that is waiting to end

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
	for _, kv := range os.Environ() {
		if strings.Contains(kv, Token) {
			s.InEnv = true
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "sent.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(96)
	}
	defer f.Close()
	b, _ := json.Marshal(s)
	f.Write(append(b, '\n'))
}

func mark(dir, line string) {
	if f, err := os.OpenFile(filepath.Join(dir, "live"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		f.WriteString(line + "\n")
		f.Close()
	}
}

// work is an agent that keeps working until it is stopped or told to quit.
func work(dir string) {
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, "quit")); err == nil {
			return
		}
	}
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
	case "claude", "codex":
		if part == "codex" && len(args) > 0 && args[0] == "app-server" {
			return codexAppServer(situation)
		}
		mark(dir, "begin")
		play := standInClaude
		if part == "codex" {
			play = standInCodex
		}
		code := play(dir, situation, args)
		mark(dir, "finish")
		return code
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
	case ExitNonZero:
		fmt.Println(`{"state":"idle"}`)
		return 3
	case FailedUnmapped:
		fmt.Println(`{"state":"error","errorClass":"E_FICTIONAL_17"}`)
	case FailedStderr:
		fmt.Println(`{"state":"error"}`)
		fmt.Fprintln(os.Stderr, "rate limit exceeded (429), try again later")
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
	case ExitNonZero:
		fmt.Println(ok)
		return 1
	case FailedUnmapped:
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` + standInSession + `","api_error_status":418}`)
		return 1
	case FailedStderr:
		fmt.Fprintln(os.Stderr, "rate limit exceeded (429), try again later")
		return 1
	case Active, Hanging, Crashed:
		work(dir)
	case Oversized:
		filler(oversize)
		fmt.Println(ok)
	}
	return 0
}

// codexLimitStream is the shape of a real Codex usage-limit failure, as
// recorded in internal/adapter/runner's fixtures: the limit is only prose.
// What tells a limit from any other failure is the app-server's answer.
func codexLimitStream() {
	fmt.Println(`{"type":"thread.started","thread_id":"` + standInSession + `"}`)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"error","message":"Example usage limit message, try again later."}`)
	fmt.Println(`{"type":"turn.failed","error":{"message":"Example usage limit message, try again later."}}`)
}

// codexAppServer answers the initialize handshake and account/rateLimits/read
// over stdio. Only in the coded-failure situation does it report a limit.
func codexAppServer(situation Situation) int {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue
		}
		switch {
		case *m.ID == 1:
			fmt.Println(`{"id":1,"result":{"userAgent":"stand-in"}}`)
		case *m.ID == 2 && situation == FailedCoded:
			fmt.Println(`{"id":2,"result":{"ordinaryUsageAllowed":false,"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1794393286}}}}`)
		case *m.ID == 2:
			fmt.Println(`{"id":2,"result":{"ordinaryUsageAllowed":true,"rateLimits":{"primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1794393286}}}}`)
		}
	}
	return 0
}

// standInCodex plays `codex exec --json`.
func standInCodex(dir string, situation Situation, args []string) int {
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
	case FailedCoded, FailedProse:
		codexLimitStream()
		return 1
	case FailedUnmapped:
		fmt.Println(started)
		fmt.Println(`{"type":"turn.failed","error":{"code":"fictional_new_code","message":"Example failure."}}`)
		return 1
	case FailedStderr:
		fmt.Println(started)
		fmt.Fprintln(os.Stderr, "rate limit exceeded (429), try again later")
		return 1
	case ExitNonZero:
		fmt.Println(started)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
		return 1
	case Garbage:
		fmt.Println("All done! The task is complete.")
	case Active, Hanging, Crashed:
		work(dir)
	case Oversized:
		fmt.Println(started)
		filler(oversize)
		fmt.Println(`{"type":"turn.completed","usage":{}}`)
	}
	return 0
}
