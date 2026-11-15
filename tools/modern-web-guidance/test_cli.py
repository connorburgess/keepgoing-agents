"""Fictional probes of the actual pinned scanner, not private runtime data."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
PACKAGE = ROOT / "tools/modern-web-guidance/node_modules/modern-web-guidance"
CLI = PACKAGE / "skills/modern-web-guidance/modern-web.mjs"


class CliTests(unittest.TestCase):
    def scan(self, directory, target):
        env = dict(os.environ, DISABLE_TELEMETRY="1", ENABLE_FILE_LOGGING="false")
        env.pop("NODE_OPTIONS", None)
        env.pop("NODE_PATH", None)
        result = subprocess.run([
            "node", "--experimental-permission", "--allow-fs-read=" + str(PACKAGE),
            "--allow-fs-read=" + directory, str(CLI), "legacy-scan", target, "--json",
        ], env=env, capture_output=True, text=True, timeout=60)
        return result.returncode, json.loads(result.stdout)

    def test_actual_zero_positive_determinism_and_missing_target(self):
        with tempfile.TemporaryDirectory() as directory:
            clean = Path(directory) / "clean.html"
            clean.write_text("<!doctype html><title>Example</title><p>Hello</p>")
            legacy = Path(directory) / "input.js"
            legacy.write_text(
                "document.querySelector('input').addEventListener('keydown', (event) => {\n"
                "if (event.key === 'Enter') document.querySelector('form').requestSubmit();\n"
                "});")
            code, data = self.scan(directory, str(clean))
            self.assertEqual(code, 0)
            self.assertEqual(data["totalFindings"], 0)
            code, data = self.scan(directory, str(legacy))
            self.assertEqual(code, 0)
            self.assertGreater(data["totalFindings"], 0)
            self.assertIn("ime-safe-enter-submit", [g["guideId"] for g in data["guides"]])
            self.assertEqual((code, data), self.scan(directory, str(legacy)))
            code, data = self.scan(directory, str(Path(directory) / "missing.html"))
            self.assertEqual(code, 1)
            self.assertIn("error", data)
            self.assertEqual(data["totalFindings"], 0)

    def test_local_wrapper_refuses_install_and_arbitrary_scan_target(self):
        for args in [("install",), ("legacy-scan", "../private"), ("update",)]:
            result = subprocess.run(["node", str(Path(__file__).with_name("cli.mjs")), *args],
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 2)
