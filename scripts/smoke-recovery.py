#!/usr/bin/env python3
"""Verify a native keepgoing binary's recovery path using only local fixtures."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import platform
import shlex
import signal
import subprocess
import sys
import tempfile
import threading
import time


PRIVATE = "FICTIONAL_PRIVATE_RECOVERY_TASK"
SCRIPT = Path(__file__).resolve()
CURSOR_SESSION = "00000000-0000-4000-8000-0000000000c1"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def save(path, value):
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value))
    temporary.replace(path)


def load(path):
    return json.loads(path.read_text())


def fixture(kind, args):
    """Only these stand-ins are reachable; never discover a real provider."""
    root = Path(os.environ["RECOVERY_FIXTURE"])
    with (root / "calls.jsonl").open("a") as stream:
        stream.write(json.dumps([kind, *args]) + "\n")
    if kind == "bb":
        command = args[:2]
        if command == ["thread", "show"]:
            value = {"thread": {"status": load(root / "observation.json")["state"]}}
        elif command == ["thread", "log"]:
            value = load(root / "events.json")
        elif command in (["thread", "list"], ["thread", "interactions"]):
            value = []
        elif command == ["thread", "tell"]:
            require(sys.stdin.read() == PRIVATE, "Unexpected fixture task.")
            value = {"ok": True}
        elif command == ["thread", "retry"]:
            # As BB answers for a failed turn it dispatched again by reference.
            require(sys.stdin.read() == "", "A turn retry carried text.")
            value = {"ok": True, "threadId": args[2], "delivery": "sent", "turnRequestId": "req_smoke1", "attempt": 2}
        elif command == ["provider-retry", "explain"]:
            # BB's optional retry plugin, with no decision recorded for the thread.
            value = {"threadId": args[2], "diagnostic": None}
        elif command == ["provider-retry", "status"]:
            value = {"retries": []}
        elif command == ["thread", "stop"]:
            save(root / "observation.json", {"state": "idle", "errorClass": "none"})
            value = {"ok": True}
        else:
            (root / "unexpected").touch()
            return 99
        print(json.dumps(value))
        return 0
    if kind == "process-status":
        print(json.dumps(load(root / "observation.json")))
        return 0
    if kind == "process-resume":
        require(sys.stdin.read() == PRIVATE, "Unexpected fixture task.")
        return 0
    if kind == "cursor":
        expected = ["-p", "--output-format", "stream-json"]
        invocations = (root / "calls.jsonl").read_text().splitlines()
        if len(invocations) > 1:
            expected.append("--resume=" + CURSOR_SESSION)
        require(args == expected, "Cursor fixture did not receive exact, permission-preserving arguments.")
        require(sys.stdin.read() == PRIVATE, "Unexpected fixture task.")
        print(json.dumps({"type": "system", "subtype": "init", "session_id": CURSOR_SESSION}))
        if (root / "recovered").exists():
            print(json.dumps({"type": "result", "subtype": "success", "is_error": False,
                              "session_id": CURSOR_SESSION, "result": PRIVATE}))
            return 0
        # A complete initialization followed by nonzero exit retains identity,
        # but stderr is never a taxonomy or proof of quota/auth/network state.
        print("fictional 429 rate limit authentication network failure", file=sys.stderr)
        return 1
    if kind in ("claude", "codex"):
        if (kind == "codex" and args[:1] != ["exec"]) or (kind == "claude" and "--output-format" not in args):
            (root / "unexpected").touch()
            return 99
        require(sys.stdin.read() == PRIVATE, "Unexpected fixture task.")
        good = (root / "recovered").exists()
        if kind == "claude":
            print(json.dumps({"type": "result", "is_error": not good,
                              "subtype": "success" if good else "connection_error",
                              "session_id": "session_example", "result": PRIVATE}))
        else:
            print(json.dumps({"type": "thread.started", "thread_id": "session_example"}))
            print(json.dumps({"type": "turn.completed"} if good else
                             {"type": "turn.failed", "error": {"code": "connection_error", "message": PRIVATE}}))
        return 0 if good else 1
    (root / "unexpected").touch()
    return 99


class Receiver:
    def __init__(self):
        self.events = []
        self.lock = threading.Lock()
        receiver = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                size = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(size))
                with receiver.lock:
                    receiver.events.append(body["type"])
                self.send_response(503)  # Only a local fixture rejects reports.
                self.end_headers()

            def log_message(self, *_args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def count(self):
        with self.lock:
            return len(self.events)


class Loop:
    def __init__(self, binary, root, kind, receiver):
        self.binary, self.root, self.kind, self.receiver = binary, root, kind, receiver
        root.mkdir(mode=0o700)
        self.state = root / "state"
        self.config = root / "keepgoing.yaml"
        self.commands = root / "bin"
        self.commands.mkdir()
        # Deliberately do not inherit credentials, account homes, provider hooks,
        # Git configuration, reporting overrides or BB caller identity.
        self.environment = {
            "PATH": str(self.commands) + os.pathsep + os.defpath,
            "LANG": "C", "BB_THREAD_ID": "thr_example_owner",
            "RECOVERY_FIXTURE": str(root),
            "RECOVERY_URL": f"http://127.0.0.1:{receiver.server.server_port}",
            "RECOVERY_SECRET": "fictional-recovery-key",
        }
        for name, adapter in (("bb", "bb"), ("claude", "claude"), ("codex", "codex"), ("agent", "cursor")):
            stub = self.commands / name
            stub.write_text("#!/bin/sh\nexec " + shlex.join([sys.executable, str(SCRIPT), "--fixture", adapter]) + ' "$@"\n')
            stub.chmod(0o700)
        self.observation("idle")
        save(root / "events.json", [])
        self.write_config(1)

    def write_config(self, retries):
        loop = {
            "name": "example-recovery-" + self.kind, "state_dir": str(self.state),
            "adapter": self.kind, "prompt": PRIVATE,
            "progress": {"stall_after": "1m", "min_wake_interval": "1m", "max_nudges": 1},
            "errors": {"max_reprompts": retries, "backoff": "1s", "failover_on": []},
            "limits": {"max_sends": 6},
            "daemon": {"interval": "10s", "control_poll": "100ms"},
            "report": {"url_env": "RECOVERY_URL", "secret_env": "RECOVERY_SECRET",
                       "scheme": "bearer", "events": ["await_user", "complete"], "timeout": "2s"},
        }
        if self.kind == "bb":
            loop["bb"] = {"thread": "thr_example_target", "cli": str(self.commands / "bb")}
        elif self.kind == "process":
            loop["process"] = {
                "status_command": [sys.executable, str(SCRIPT), "--fixture", "process-status"],
                "resume_command": [sys.executable, str(SCRIPT), "--fixture", "process-resume"],
            }
        else:
            executable = "agent" if self.kind == "cursor" else self.kind
            loop[self.kind] = {"cli": str(self.commands / executable), "workdir": str(self.root)}
        save(self.config, {"version": 1, "loop": loop})  # JSON is a YAML subset.

    def cli(self, command, *args, expected=0, owner="thr_example_owner"):
        result = subprocess.run([str(self.binary), command, "-c", str(self.config), *args],
                                cwd=self.root, env=dict(self.environment, BB_THREAD_ID=owner),
                                capture_output=True, text=True, timeout=30)
        require(not (self.root / "unexpected").exists(), "Unexpected fixture/provider command.")
        require(result.returncode == expected,
                f"{self.kind}/{command}: exit {result.returncode}, expected {expected}: {result.stderr}")
        return result.stdout

    def observation(self, state, error="none"):
        save(self.root / "observation.json", {"state": state, "errorClass": error, "message": PRIVATE})

    def calls(self):
        path = self.root / "calls.jsonl"
        return path.read_bytes() if path.exists() else b""

    def tick(self, minute, action):
        now = datetime(2030, 1, 1, tzinfo=timezone.utc) + timedelta(minutes=minute)
        result = json.loads(self.cli("tick", "--now", now.isoformat()))
        require(result["action"] == action, f"{self.kind}: expected {action}, got {result}")
        return result

    def wait_turn(self, expected):
        path = self.state / "targets" / "primary" / "exit"
        until = time.monotonic() + 15
        while time.monotonic() < until:
            if path.exists():
                require(int(path.read_text()) == expected, "Unexpected fixture exit code.")
                return
            time.sleep(0.05)
        raise RuntimeError("Local headless fixture did not finish.")

    @contextmanager
    def daemon(self, exit_signal=signal.SIGTERM):
        with (self.root / "daemon-output").open("wb") as output:
            child = subprocess.Popen([str(self.binary), "daemon", "-c", str(self.config)],
                                     cwd=self.root, env=self.environment, stdout=output,
                                     stderr=subprocess.STDOUT, start_new_session=True)
            try:
                yield child
            finally:
                child.send_signal(exit_signal)
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait(timeout=5)
                    raise RuntimeError("Fixture supervisor did not exit cleanly.")
                expected = -signal.SIGKILL if exit_signal == signal.SIGKILL else 0
                require(child.returncode == expected, "Fixture supervisor did not exit as requested.")

    def restart_held(self, flag, ordinary_intervals=False):
        before, requests = self.calls(), self.receiver.count()
        control_before = load(self.state / "control.json")
        sends_before = load(self.state / "sends.json")
        session = self.state / "targets" / "primary" / "session"
        session_before = session.read_bytes() if self.kind == "cursor" else None
        # Crash the first held daemon after a recorded check, then start a new
        # process against the same files and stale endpoint. BB and Cursor cases
        # wait for an additional ordinary interval in each process.
        endpoint = self.state / "daemon-endpoint.json"
        crashed_endpoint = None
        for exit_signal in (signal.SIGKILL, signal.SIGTERM):
            sequence = load(self.state / "incidents.json")["nextSequence"]
            wanted = sequence + (2 if ordinary_intervals else 1)
            with self.daemon(exit_signal) as child:
                until = time.monotonic() + 25
                while time.monotonic() < until:
                    require(child.poll() is None, "Daemon exited before recording held checks.")
                    if load(self.state / "incidents.json")["nextSequence"] >= wanted:
                        break
                    time.sleep(0.05)
                else:
                    raise RuntimeError("Daemon did not record held checks before deadline.")
                current_endpoint = endpoint.read_bytes()
                if crashed_endpoint is not None:
                    require(current_endpoint != crashed_endpoint, "Restart reused the crashed daemon endpoint.")
            if exit_signal == signal.SIGKILL:
                require(endpoint.read_bytes() == current_endpoint, "Crash fixture did not leave a stale endpoint.")
                crashed_endpoint = current_endpoint
            else:
                require(not endpoint.exists(), "Orderly shutdown left its endpoint behind.")
            require(load(self.state / "control.json") == control_before, "Restart changed controls/owner.")
            if session_before is not None:
                require(session.read_bytes() == session_before, "Held restart changed saved Cursor session identity.")
        require(control_before[flag], "Expected persistent hold is absent.")
        require(self.calls() == before and self.receiver.count() == requests,
                "Held daemon queried a provider or replayed a notification.")
        require(load(self.state / "sends.json") == sends_before, "Held restart changed send allowance.")

    def verify_timeline(self):
        before = {p.name: p.read_bytes() for p in self.state.iterdir() if p.is_file()}
        calls, requests = self.calls(), self.receiver.count()
        output = self.cli("timeline", "--json", "--limit", "320")
        view = json.loads(output)
        require(view["control"]["state"] == "awaiting_user", "Timeline lost current hold.")
        require(view["summary"]["heldChecks"] >= 2 and view["summary"]["notificationWarnings"] == 1,
                "Timeline lost held checks or failed alert.")
        expected_class = "unknown" if self.kind == "cursor" else "network"
        require(any(e.get("incident", {}).get("observation", {}).get("errorClass") == expected_class
                    for e in view["events"]), "Timeline lost expected failure-class evidence.")
        if self.kind == "cursor":
            observations = [e.get("incident", {}).get("observation", {}) for e in view["events"]]
            require(not any(o.get("errorClass") in ("network", "auth", "rate_limit") for o in observations),
                    "Cursor stderr became classified evidence.")
        require(PRIVATE not in output and str(self.root) not in output and "fictional-recovery-key" not in output,
                "Timeline exposed private fixture content.")
        after = {p.name: p.read_bytes() for p in self.state.iterdir() if p.is_file()}
        require(before == after and calls == self.calls() and requests == self.receiver.count(),
                "Offline diagnosis changed state or performed provider/report work.")
        return view


def exercise(loop):
    headless = loop.kind in ("claude", "codex", "cursor")
    loop.cli("enable")
    if loop.kind == "bb":
        loop.observation("active")
        events = [
            {"seq": 1, "type": "turn/started", "data": {"providerThreadId": "provider_example"}},
            {"seq": 2, "type": "provider/error", "data": {"willRetry": True, "message": PRIVATE,
             "errorInfo": {"category": "stream-disconnected", "providerCode": "responseStreamDisconnected", "httpStatusCode": 503}}},
        ]
        save(loop.root / "events.json", events)
        for minute in (0, 2, 4):
            tick = loop.tick(minute, "none")
            require(tick["reason"] == "provider is retrying the turn", "Active retry was treated as a productive stall.")
        require(not any(json.loads(line)[1:3] == ["thread", "tell"] for line in loop.calls().splitlines()),
                "Active retry sent a prompt.")
        events.append({"seq": 3, "type": "provider/error", "data": {"willRetry": False,
                       "errorInfo": {"category": "stream-disconnected", "providerCode": "responseStreamDisconnected"}}})
        save(loop.root / "events.json", events)
        loop.observation("error", "network")
    elif headless:
        loop.tick(0, "wake")
        loop.wait_turn(1)
    else:
        loop.observation("error", "network")
    failed = loop.tick(5, "reprompt")
    if loop.kind == "cursor":
        require(failed.get("errorClass") == "unknown" and not failed.get("handoff") and not failed.get("probes"),
                "Cursor failure was classified or triggered fallback work.")
    if headless:
        loop.wait_turn(1)
    requests = loop.receiver.count()
    loop.tick(6, "await_user")
    require(loop.receiver.count() == requests + 2, "Ordinary escalation retry policy changed.")
    # A different caller may inspect but cannot claim the already-owned loop.
    saved = load(loop.state / "control.json")
    loop.cli("enable", expected=1, owner="thr_example_other")
    require(load(loop.state / "control.json") == saved, "Conflicting owner changed hold.")
    loop.restart_held("awaitingUser", ordinary_intervals=(loop.kind in ("bb", "cursor")))
    loop.verify_timeline()
    spent = load(loop.state / "sends.json")["used"]
    # Repair only the stand-in. Explicitly increase its synthetic retry allowance
    # before resume; spent sends and failed delivery must never be refunded.
    if headless:
        (loop.root / "recovered").touch()
        loop.write_config(2)
    elif loop.kind == "bb":
        events.append({"seq": 4, "type": "item/agentMessage/delta", "data": {
            "providerThreadId": "provider_example", "itemId": "item_example", "delta": PRIVATE}})
        save(loop.root / "events.json", events)
        loop.observation("active")
    else:
        loop.observation("active")
    loop.tick(7, "none")
    require(load(loop.state / "control.json")["awaitingUser"], "Provider repair cleared the hold.")
    loop.cli("resume")
    require(load(loop.state / "sends.json")["used"] == spent, "Resume refunded send allowance.")
    loop.tick(8, "reprompt" if headless else "none")
    if headless:
        loop.wait_turn(0)
        loop.tick(9, "wake")  # Observe clean result, then one more resumed turn.
        loop.wait_turn(0)
        invocations = [json.loads(line) for line in loop.calls().splitlines()]
        if loop.kind == "cursor":
            first = ["cursor", "-p", "--output-format", "stream-json"]
            require(invocations == [first] + [first + ["--resume=" + CURSOR_SESSION]] * 3,
                    "Cursor session was not reused exactly across failure, hold and restart.")
        else:
            require(len(invocations) == 4 and "session_example" in invocations[-1] and
                    ("resume" in invocations[-1] or "--resume" in invocations[-1]), "Saved headless session was not reused.")
    else:
        require(load(loop.state / "state.json")["lastState"] == "active", "Recovery was not observed.")
    require(load(loop.state / "sends.json")["used"] == spent + (2 if headless else 0), "Unexpected send accounting after recovery.")
    requests = loop.receiver.count()
    loop.cli("complete", "--note", "Fictional recovery verified")
    loop.cli("complete")
    require(loop.receiver.count() == requests + 1, "Completion was retried or repeated.")
    loop.restart_held("completed")
    require(loop.receiver.count() == requests + 1, "Completion replayed on restart.")
    print(f"PASS {loop.kind}: failure, alert rejection, held daemon SIGKILL/restart, explicit recovery, completion, allowance and owner preservation")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path, help="native built or unpacked keepgoing binary")
    parser.add_argument("--include-cursor", action="store_true",
                        help="also verify the unreleased Cursor adapter; requires a build containing it")
    args = parser.parse_args()
    require(os.name == "posix", "This fixture requires macOS/Linux and Python 3.")
    binary = args.binary.resolve(strict=True)
    print(f"artifact sha256={hashlib.sha256(binary.read_bytes()).hexdigest()} platform={platform.system()}/{platform.machine()}")
    with tempfile.TemporaryDirectory(prefix="keepgoing-recovery-") as temporary, Receiver() as receiver:
        root = Path(temporary).resolve()
        kinds = ("bb", "process", "claude", "codex") + (("cursor",) if args.include_cursor else ())
        for kind in kinds:
            loop = Loop(binary, root / kind, kind, receiver)
            print("binary version=" + loop.cli("version").strip())
            exercise(loop)
    print("PASS: local fixtures only; temporary supervisors stopped and state removed. No live provider or migration certification.")


if __name__ == "__main__":
    if len(sys.argv) > 2 and sys.argv[1] == "--fixture":
        sys.exit(fixture(sys.argv[2], sys.argv[3:]))
    main()
