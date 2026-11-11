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
    subprocess.run([str(native), str(cli), str(registry)], env=env, check=True, timeout=15)
    assert snapshot() == before, "Read-only client changed fixture files or invoked the provider trap"
    print("Fixture files unchanged; no provider commands executed")
