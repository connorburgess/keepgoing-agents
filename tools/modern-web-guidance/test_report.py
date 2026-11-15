import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import report


class ReportTests(unittest.TestCase):
    def sample(self, **overrides):
        data = dict(targetDir=report.TARGET, totalFindings=0, guides=[],
                    activeRuleCount=35, activeGuideCount=33)
        data.update(overrides)
        return json.dumps(data)

    def test_no_patterns_is_limited_result(self):
        self.assertEqual(report.classify(0, self.sample())[0], "no-patterns")

    def test_findings_exit_zero(self):
        self.assertEqual(report.classify(0, self.sample(
            totalFindings=1, guides=[dict(guideId="example-guide", count=1)]))[0], "findings")

    def test_errors_never_become_clean(self):
        cases = [(1, self.sample()), (0, self.sample(error="failed")),
                 (0, ""), (0, "[]"), (0, self.sample(totalFindings=True)),
                 (0, self.sample(activeRuleCount=0)),
                 (0, self.sample(targetDir="../private")),
                 (0, self.sample(totalFindings=1)),
                 (0, self.sample(guides=[{}]))]
        for code, stdout in cases:
            with self.subTest(code=code, stdout=stdout):
                self.assertEqual(report.classify(code, stdout)[0], "error")

    def test_error_artifact_does_not_publish_zero(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory)
            report.write_report(out, 1, self.sample(error="missing target"), "failed")
            data = json.loads((out / "report.json").read_text())
            self.assertEqual(data["status"], "error")
            self.assertIn("No clean result is available", (out / "summary.md").read_text())
            self.assertNotIn("Recorded findings: 0", (out / "summary.md").read_text())
            self.assertEqual((out / "scanner.stderr.txt").read_text(), "failed")

    def test_missing_install_still_creates_artifacts_and_fails(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(
                report, "ROOT", Path(directory)), patch("sys.argv", ["report.py", directory + "/out"]):
            self.assertEqual(report.main(), 1)
            self.assertEqual(json.loads((Path(directory) / "out/report.json").read_text())["status"], "error")

    def test_timeout_still_creates_error_artifact(self):
        with tempfile.TemporaryDirectory() as directory, patch(
                "sys.argv", ["report.py", directory]), patch(
                "subprocess.run", side_effect=report.subprocess.TimeoutExpired("scanner", 65)):
            self.assertEqual(report.main(), 1)
            self.assertEqual(json.loads((Path(directory) / "report.json").read_text())["status"], "error")
