#!/usr/bin/env python3
"""Prove the held-reader regression detects the original Windows implementation."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

if os.name != "nt":
    raise SystemExit("This negative control requires native Windows")
root = Path(__file__).resolve().parents[1]
source = root / "internal/adapter/runner/antigravity_runner.go"
original = source.read_text(encoding="utf-8")
changes = {
    '\t"github.com/looptender/looptender/internal/snapshot"\n': '',
    'stat, open := snapshot.Lstat, snapshot.Open': 'stat, open := os.Lstat, os.Open',
    'stat, open = snapshot.LstatRecord, snapshot.OpenRecord': 'stat, open = os.Lstat, os.Open',
    'return snapshot.Replace(f.Name(), d.path(antigravityTurnName))':
        'return os.Rename(f.Name(), d.path(antigravityTurnName))',
}
for before, after in changes.items():
    if original.count(before) != 1:
        raise SystemExit("Negative-control source no longer matches; review the overlay")
    original = original.replace(before, after)
with tempfile.TemporaryDirectory(prefix="antigravity-snapshot-negative-") as directory:
    directory = Path(directory)
    replacement = directory / "antigravity_runner.go"
    replacement.write_text(original, encoding="utf-8")
    overlay = directory / "overlay.json"
    overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}), encoding="utf-8")
    result = subprocess.run(
        ["go", "test", "-race", "-overlay", str(overlay), "./internal/adapter/runner",
         "-run", "^TestAntigravityTurnSnapshotReplacement$", "-count=1"],
        cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        timeout=120, check=False,
    )
    print(result.stdout, end="")
    expected = "replacement while production reader is open:"
    if result.returncode == 0 or expected not in result.stdout or "[build failed]" in result.stdout:
        raise SystemExit("Original reader/writer pair did not fail at the intended replacement assertion")
    print("Original reader/writer pair failed at the intended held-reader replacement assertion.")
