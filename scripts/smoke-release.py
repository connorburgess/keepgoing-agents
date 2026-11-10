#!/usr/bin/env python3
"""Exercise a built CLI on macOS/Linux without invoking installed providers."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path, help="built or unpacked keepgoing binary")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    require(os.name == "posix", "This smoke check requires macOS/Linux, Git and Python 3.")

    with tempfile.TemporaryDirectory(prefix="keepgoing-release-") as temporary:
        root = Path(temporary).resolve()
        commands = root / "bin"
        commands.mkdir()
        marker = root / "unexpected-provider-call"
        # Discovery may find these executables; execution must never reach them.
        for name in ("bb", "claude", "codex"):
            stub = commands / name
            stub.write_text('#!/bin/sh\n: > "$SMOKE_PROVIDER_MARKER"\nexit 99\n')
            stub.chmod(0o700)
        environment = os.environ.copy()
        # Do not inherit the caller's Git repository or configuration overrides.
        for key in list(environment):
            if key.startswith("GIT_"):
                del environment[key]
        environment.update({
            "PATH": str(commands) + os.pathsep + os.environ.get("PATH", ""),
            "SMOKE_PROVIDER_MARKER": str(marker),
            "BB_THREAD_ID": "thr_smoke_owner",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1",
        })

        def run(command, *, owner="thr_smoke_owner", expected=0, stdin=None):
            result = subprocess.run(
                [str(item) for item in command], cwd=root,
                env=dict(environment, BB_THREAD_ID=owner), input=stdin,
                capture_output=True, text=True, timeout=30,
            )
            require(not marker.exists(), "A provider command was unexpectedly invoked.")
            require(result.returncode == expected,
                    f"{command[0:2]}: exit {result.returncode}, expected {expected}: {result.stderr}")
            return result.stdout

        project = root / "project"
        run(["git", "init", "--quiet", project])
        run(["git", "-C", project, "-c", "user.name=Example Contributor",
             "-c", "user.email=contributor@example.com", "-c", "commit.gpgsign=false",
             "-c", "core.hooksPath=" + os.devnull,
             "commit", "--quiet", "--allow-empty", "-m", "Example project"])
        print(run([binary, "version"]).strip())

        for agent in ("bb", "claude", "codex"):
            destination = root / (agent + "-loop")
            setup = [binary, "init", "--non-interactive", "--json",
                     "--name", agent + "-smoke", "--agent", agent,
                     "--project", project, "--directory", destination, "--task-file", "-"]
            if agent == "bb":
                setup += ["--thread", "thr_smoke_target", "--provider", "codex"]
            task = "Verify the fictional example task, then declare completion.\n"
            preview = json.loads(run(setup + ["--dry-run"], stdin=task))
            require(preview["outcome"] == "preview" and not destination.exists(),
                    "Preview created a loop or did not report a preview.")
            created = json.loads(run(setup, stdin=task))
            require(created["outcome"] == "created", "Setup did not report creation.")
            config = destination / "keepgoing.yaml"

            def cli(command, *options, **kwargs):
                return run([binary, command, "-c", config, *options], **kwargs)

            def status():
                return json.loads(cli("status", "--json"))

            def held(reason):
                # Every call is a new process, reading persisted control state.
                for _ in range(2):
                    tick = json.loads(cli("tick"))
                    require(tick["action"] == "none" and tick["reason"] == reason,
                            f"{agent}: expected {reason} hold, got {tick}")

            require(status()["controlState"] == "disabled", "Setup enabled supervision.")
            cli("doctor", "--offline", "--json")
            held("disabled")
            cli("enable")
            cli("pause")
            held("paused")
            cli("resume")
            cli("await-user", "--note", "Example input needed")
            held("awaiting user")
            cli("resume")
            cli("complete", "--note", "Example completion declaration")
            cli("disable")
            cli("enable")
            held("completed")
            require(status()["control"]["completed"], "Completion was lost.")

            before = status()["control"]
            cli("enable", owner="thr_smoke_other", expected=1)
            require(status()["control"] == before, "Refused ownership claim changed controls.")
            cli("enable", "--take-over", owner="thr_smoke_other")
            transferred = json.loads(cli("status", "--json", owner="thr_smoke_other"))["control"]
            require(transferred["completed"] and
                    transferred["ownership"]["threadId"] == "thr_smoke_other" and
                    transferred["ownership"]["previous"]["status"] == "handed_off",
                    "Takeover lost completion or ownership history.")
            cli("disable", owner="thr_smoke_other")
            cli("resume", owner="thr_smoke_other")
            final = json.loads(cli("status", "--json", owner="thr_smoke_other"))["control"]
            require(not final["enabled"] and not final.get("completed", False),
                    "Explicit resume did not clear completion while staying disabled.")
            print(f"PASS {agent}: setup, offline diagnostics, holds, completion, ownership")
        print("PASS: zero provider commands; temporary loops removed on exit.")


if __name__ == "__main__":
    main()
