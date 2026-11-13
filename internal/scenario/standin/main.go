// Command standin is a test-only stand-in for the bb, claude and codex CLIs, used by
// the scenario suite in internal/scenario. It is compiled once per test run
// and installed under each name it plays (bb, claude; with .exe on Windows),
// so it runs natively on every platform without a POSIX shell. It picks its
// part from the name it was run as.
//
// As claude or codex it answers probes and runs headless turns. A turn's
// answer follows net.mode (online, offline or portal), unless headless.mode
// says otherwise: online succeeds and reports the session; offline (or
// "structured") fails with the CLI's structured error; portal prints a
// sign-in page; silent exits 1 with no output, before any session exists.
//
// All state lives in the directory named by SCENARIO_WORLD, which the test
// writes and reads:
//
//	<thread>.show          JSON returned by `thread show`
//	<thread>.log           JSON array of log events, filtered by --after-seq
//	<thread>.children      JSON returned by `thread list --parent-thread`
//	<thread>.interactions  JSON returned by `thread interactions list`
//	place.list             JSON array of {id, title} returned by
//	                       `thread list --project ... --environment ...`
//	spawn.modes            one outcome per line for successive `thread spawn`
//	                       calls, consumed in order (default ok); see spawn
//	probe-limited.<model>  if present, a claude probe of that model is rate limited
//	kill.<command>         if present, after `thread <command>` has answered, the
//	                       stand-in kills the keepgoing process that ran it
//	killbefore.<command>   if present, the stand-in kills the keepgoing process
//	                       that runs `thread <command>` before doing anything
//	show.gate              if present, holds a number N: each `thread show`
//	                       records its arrival in show.arrived and waits until N
//	                       have arrived or show.open exists, then answers
//	calls.jsonl            every bb invocation, one JSON array of arguments per line
//	probes.jsonl           every probe
//	net.mode               online, offline or portal (default online)
//	headless.mode          overrides net.mode for headless turns
//	sends.jsonl            everything an agent received: `thread tell` (thread,
//	                       mode, message), an accepted `thread retry`, and a
//	                       `thread spawn` (new thread, title, first prompt), and
//	                       every headless turn (thread is the CLI name, mode the
//	                       session it resumed, message the prompt)
//
// `thread retry` re-submits a failed turn: it is accepted only while the
// thread's show file says "error" and answers like BB otherwise: an error
// envelope with no_failed_turn and exit status 1.
//
// As example-agent it plays a fictional agent that keepgoing has no built-in
// runner for, with this documented interface:
//
//	example-agent run --json [--session ID]
//	    Reads the prompt on standard input and prints one JSON line of type
//	    "result" with "status" (ok, failed or needs_input) and "session".
//	    Exits 0 when ok, 1 when failed, 2 when it needs input.
//
// What each successive turn does comes from example-agent.turns, one outcome
// per line consumed in order (default ok). Every turn is recorded in
// sends.jsonl like the other headless stand-ins: kind "turn", the agent's name,
// the session it resumed as the mode, and its prompt.
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
	"strings"
	"time"
)

func main() {
	// A scenario may run keepgoing with no room to write (a zero file-size
	// limit, which is how a full disk looks to a program). The stand-in
	// is not the program under test: it lifts its own limit, so it can
	// still record what it was sent.
	liftFileSizeLimit()
	world := os.Getenv("SCENARIO_WORLD")
	if world == "" {
		fmt.Fprintln(os.Stderr, "standin: SCENARIO_WORLD is not set")
		os.Exit(97)
	}
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	switch name {
	case "claude":
		if isClaudeTurn(os.Args[1:]) {
			os.Exit(turn(world, name, os.Args[1:]))
		}
		os.Exit(claude(world, os.Args[1:]))
	case "codex":
		if len(os.Args) > 1 && os.Args[1] == "app-server" {
			os.Exit(1) // no structured limit snapshot
		}
		if isCodexProbe(os.Args[1:]) {
			os.Exit(codexProbe(world, os.Args[1:]))
		}
		os.Exit(turn(world, name, os.Args[1:]))
	}
	if name == "example-agent" {
		os.Exit(exampleAgent(world, os.Args[1:]))
	}
	args := os.Args[1:]
	// killbefore.<command>: the stand-in kills the keepgoing process that
	// runs `thread <command>` before doing anything, as a crash just before
	// the call would. The command is not recorded or done. The file is used up.
	if len(args) > 1 && args[0] == "thread" && os.Remove(filepath.Join(world, "killbefore."+args[1])) == nil {
		killParent()
		os.Exit(1)
	}
	code := run(world, args)
	// kill.<command>: once the command has done its work and answered, the
	// stand-in kills the keepgoing process that ran it, as a crash or a
	// power cut would at that moment. The file is used up.
	if len(args) > 1 && args[0] == "thread" {
		if os.Remove(filepath.Join(world, "kill."+args[1])) == nil {
			os.Stdout.Sync()
			killParent()
		}
	}
	os.Exit(code)
}

func run(world string, args []string) int {
	appendLine(filepath.Join(world, "calls.jsonl"), args)
	// BB's optional retry plugin: its last decision for a thread, from
	// <thread>.retry-decision, and its pending retries, from <thread>.retries.
	if len(args) >= 3 && args[0] == "provider-retry" {
		switch args[1] {
		case "explain":
			b, _ := json.Marshal(map[string]any{"threadId": args[2], "diagnostic": nil})
			return emit(world, args[2]+".retry-decision", string(b))
		case "status":
			return emit(world, args[2]+".retries", `{"retries":[]}`)
		}
	}
	if len(args) < 2 || args[0] != "thread" {
		fmt.Println(`{"ok":true}`)
		return 0
	}
	switch args[1] {
	case "show":
		if !gate(world) {
			fmt.Fprintln(os.Stderr, "standin: the show gate was never opened")
			return 1
		}
		return emit(world, arg(args, 2)+".show", `{"thread":{"status":"idle"}}`)
	case "list":
		if p := flagValue(args, "--parent-thread"); p != "" {
			return emit(world, p+".children", `[]`)
		}
		return emit(world, "place.list", `[]`)
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
	case "spawn":
		return spawn(world, args)
	}
	fmt.Println(`{"ok":true}`)
	return 0
}

// spawn creates a thread. The next line of spawn.modes says what happens:
//
//	ok        the thread is created and listed, and its ID is printed
//	lost      the thread is created and listed, but the command prints
//	          nothing and exits 1, as when bb is cut off after BB acted
//	unlisted  the thread is created but not listed under its title (it was
//	          renamed), and bb prints a server error
//	refuse    bb rejects the invocation with unknown_option; nothing exists
func spawn(world string, args []string) int {
	mode := nextMode(filepath.Join(world, "spawn.modes"))
	if mode == "refuse" {
		fmt.Println(`{"ok":false,"error":{"code":"unknown_option"}}`)
		return 1
	}
	prompt, _ := io.ReadAll(os.Stdin)
	var made []string
	readJSON(filepath.Join(world, "spawned.json"), &made)
	id := fmt.Sprintf("thr_example_new%d", len(made)+1)
	made = append(made, id)
	writeJSON(filepath.Join(world, "spawned.json"), made)
	project, environment, title := flagValue(args, "--project"), flagValue(args, "--environment"), flagValue(args, "--title")
	writeJSON(filepath.Join(world, id+".show"), map[string]any{"thread": map[string]any{
		"id": id, "status": "active", "projectId": project, "environmentId": environment,
	}})
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{
		"kind": "spawn", "thread": id, "title": title, "mode": flagValue(args, "--permission-mode"), "message": string(prompt),
	})
	if mode != "unlisted" {
		var listed []map[string]any
		readJSON(filepath.Join(world, "place.list"), &listed)
		writeJSON(filepath.Join(world, "place.list"), append(listed, map[string]any{"id": id, "title": title}))
	}
	switch mode {
	case "lost":
		return 1
	case "unlisted":
		fmt.Println(`{"ok":false,"error":{"code":"internal_error"}}`)
		return 1
	}
	b, _ := json.Marshal(map[string]string{"id": id, "projectId": project, "environmentId": environment})
	fmt.Println(string(b))
	return 0
}

// nextMode pops the first line of a file of modes; "ok" when there is none.
func nextMode(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "ok"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "ok"
	}
	os.WriteFile(path, []byte(strings.Join(lines[1:], "\n")), 0o600)
	return strings.TrimSpace(lines[0])
}

// claude answers a probe: a success, a structured rate limit when
// probe-limited.<model> exists, or nothing structured when net.mode says the
// provider is unreachable.
func claude(world string, args []string) int {
	appendLine(filepath.Join(world, "probes.jsonl"), args)
	model := flagValue(args, "--model")
	if model == "" {
		model = "default"
	}
	if readMode(filepath.Join(world, "net.mode"), "online") != "online" {
		fmt.Fprintln(os.Stderr, "fictional: could not connect")
		return 1
	}
	if _, err := os.Stat(filepath.Join(world, "probe-limited."+model)); err == nil {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429,"result":"fictional limit"}`)
		return 1
	}
	fmt.Println(`{"type":"result","subtype":"success","is_error":false,"session_id":"session_example_probe","result":"OK"}`)
	return 0
}

func readJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, v)
	}
}

func writeJSON(path string, v any) {
	b, _ := json.Marshal(v)
	tmp := path + ".tmp"
	os.WriteFile(tmp, b, 0o600)
	os.Rename(tmp, path)
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
		fmt.Println(`{"ok":false,"error":{"code":"no_failed_turn","message":"fictional"}}`)
		return 1
	}
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{"kind": "retry", "thread": thread})
	fmt.Println(`{"ok":true,"delivery":"sent","attempt":1}`)
	return 0
}

// exampleSession is the session the fictional agent reports.
const exampleSession = "sess_example_0001"

func exampleAgent(world string, args []string) int {
	if len(args) < 2 || args[0] != "run" || args[1] != "--json" {
		fmt.Fprintln(os.Stderr, "example-agent: usage: run --json [--session ID]")
		return 64
	}
	prompt, _ := io.ReadAll(os.Stdin)
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{
		"kind": "turn", "thread": "example-agent", "mode": flagValue(args, "--session"), "message": string(prompt),
	})
	outcome := "ok"
	path := filepath.Join(world, "example-agent.turns")
	if b, err := os.ReadFile(path); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 0 && lines[0] != "" {
			outcome = lines[0]
			os.WriteFile(path, []byte(strings.Join(lines[1:], "\n")+"\n"), 0o600)
		}
	}
	// With the network down the agent's turn fails with nothing structured.
	if outcome == "ok" && readMode(filepath.Join(world, "net.mode"), "online") != "online" {
		outcome = "failed"
	}
	status, code := "ok", 0
	switch outcome {
	case "failed":
		status, code = "failed", 1
	case "needs_input":
		status, code = "needs_input", 2
	}
	fmt.Printf(`{"type":"result","status":%q,"session":%q}`+"\n", status, exampleSession)
	return code
}

// HeadlessSession is the session every stand-in headless turn reports.
const HeadlessSession = "00000000-0000-4000-8000-0000000000aa"

// isCodexProbe tells a probe (an ephemeral exec) from a headless turn.
func isCodexProbe(args []string) bool {
	for _, a := range args {
		if a == "--ephemeral" {
			return true
		}
	}
	return false
}

// codexProbe answers a probe: a completed turn, or nothing structured when
// net.mode says the provider is unreachable. It is not a turn of the
// supervised session and is recorded with the probes.
func codexProbe(world string, args []string) int {
	appendLine(filepath.Join(world, "probes.jsonl"), args)
	if readMode(filepath.Join(world, "net.mode"), "online") != "online" {
		fmt.Fprintln(os.Stderr, "fictional: could not connect")
		return 1
	}
	fmt.Println(`{"type":"turn.completed"}`)
	return 0
}

// isClaudeTurn tells a headless turn (prompt on stdin, JSON output) from a
// probe (prompt as the argument after -p).
func isClaudeTurn(args []string) bool {
	return len(args) > 2 && args[0] == "-p" && args[1] == "--output-format" && args[2] == "json"
}

// turn runs one headless turn of claude or codex.
func turn(world, cli string, args []string) int {
	prompt, _ := io.ReadAll(os.Stdin)
	resumed := ""
	switch cli {
	case "claude":
		resumed = flagValue(args, "--resume")
	case "codex":
		if len(args) > 2 && args[0] == "exec" && args[1] == "resume" {
			resumed = args[len(args)-2] // the session comes before the final "-"
		}
	}
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{"kind": "turn", "thread": cli, "mode": resumed, "message": string(prompt)})
	mode := readMode(filepath.Join(world, "headless.mode"), "")
	if mode == "" {
		mode = readMode(filepath.Join(world, "net.mode"), "online")
	}
	switch mode {
	case "silent":
		return 1
	case "portal":
		fmt.Println("<html><body>Fictional sign-in page</body></html>")
		return 1
	case "online":
		if cli == "claude" {
			fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"api_error_status":null}`+"\n", HeadlessSession)
		} else {
			fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n"+`{"type":"turn.completed","usage":{}}`+"\n", HeadlessSession)
		}
		return 0
	}
	// offline or structured: the CLI's structured failure, no recognised code
	// for Claude Code, stream_disconnected for Codex.
	if cli == "claude" {
		fmt.Printf(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":%q,"api_error_status":null}`+"\n", HeadlessSession)
	} else {
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n"+`{"type":"turn.failed","error":{"type":"stream_disconnected"}}`+"\n", HeadlessSession)
	}
	return 1
}

func readMode(path, fallback string) string {
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return fallback
	}
	return strings.TrimSpace(string(b))
}

// gate holds `thread show` while show.gate exists, until as many calls as
// it names have arrived or show.open is created. It lets a test put two
// checks in the same moment, or keep one check in the middle of its work
// while another starts. It gives up after 30 seconds.
func gate(world string) bool {
	b, err := os.ReadFile(filepath.Join(world, "show.gate"))
	if err != nil {
		return true
	}
	want, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	f, err := os.OpenFile(filepath.Join(world, "show.arrived"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		f.Write([]byte{'.'})
		f.Close()
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(world, "show.open")); err == nil {
			return true
		}
		if info, err := os.Stat(filepath.Join(world, "show.arrived")); err == nil && want > 0 && info.Size() >= int64(want) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
