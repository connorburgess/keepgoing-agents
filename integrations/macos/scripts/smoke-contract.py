#!/usr/bin/env python3
"""Compile the native reader and exercise the real Go CLI, without a GUI/provider."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
cli = Path(sys.argv[1]).resolve(strict=True)
with tempfile.TemporaryDirectory(prefix="keepgoing-menu-smoke-") as temporary:
    work = Path(temporary)
    native = work / "contract-smoke"
    subprocess.run(["swiftc", *map(str, sorted((root / "Sources/KeepgoingMenuCore").glob("*.swift"))),
                    str(root / "scripts/ContractSmoke.swift"), "-o", str(native)], check=True)
    trap = work / "provider-trap"
    trap.write_text('#!/bin/sh\n: > "$0.called"\nexit 99\n')
    trap.chmod(0o700)
    config = work / "keepgoing.yaml"
    config.write_text(json.dumps({"version": 1, "loop": {
        "name": "example-menu-loop", "state_dir": str(work / "state"), "adapter": "process",
        "process": {"status_command": [str(trap)], "resume_command": [str(trap)]},
        "prompt": "Fictional local contract fixture"
    }}))
    registry = work / "registry with spaces"
    env = {"PATH": "/usr/bin:/bin", "HOME": str(work)}
    subprocess.run([str(cli), "registry", "add", "-c", str(config), "--directory", str(registry), "--json"],
                   env=env, check=True, stdout=subprocess.DEVNULL)
    def snapshot():
        return {str(p.relative_to(work)): hashlib.sha256(p.read_bytes()).hexdigest()
                for p in work.rglob("*") if p.is_file()}
    before = snapshot()
    # The panel's socket path is bounded, so its directory is short and
    # outside the fixture (it is only read: no panel is started).
    panel = Path(tempfile.mkdtemp(prefix="kgm", dir="/tmp"))
    subprocess.run([str(native), str(cli), str(registry), str(panel / "p")], env=env, check=True, timeout=15)
    after = snapshot()
    # The review reads the control record under its lock, as the page's
    # review does, which leaves the empty lock file. Nothing else may change.
    empty = hashlib.sha256(b"").hexdigest()
    if after.get("state/control.lock") == empty and "state/control.lock" not in before:
        del after["state/control.lock"]
    assert after == before, ("Read-only client changed fixture files or invoked the provider trap", sorted(set(after.items()) ^ set(before.items())))
    print("Fixture files unchanged; no provider commands executed")
    # The menu's environment, as it gives it: a BB thread identity in the
    # caller's environment must not reach the CLI.
    subprocess.run([str(native), str(cli), str(registry), str(panel / "p"), "apply"], env={**env, "BB_THREAD_ID": "thr_example0001"}, check=True, timeout=30)
    control = json.loads((work / "state" / "control.json").read_text())
    assert control.get("stop") is True and not control.get("ownership"), control
    assert not (work / "provider-trap.called").exists(), "a stop from the menu ran a provider command"
    print("Stop saved through review and apply; no owner taken; no provider commands executed")
    for p in sorted(panel.rglob("*"), reverse=True):
        p.rmdir() if p.is_dir() else p.unlink()
    panel.rmdir()
