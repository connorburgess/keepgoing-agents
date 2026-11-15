#!/usr/bin/env python3
"""Advisory report. Scanner errors are never interpreted as zero findings."""
import datetime
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
TARGET = "cmd/looptender/dockui"
VERSION = "1.0.0"


def classify(code, stdout):
    try:
        data = json.loads(stdout)
    except (ValueError, TypeError):
        return "error", None, "Scanner did not return valid JSON."
    if not isinstance(data, dict):
        return "error", data, "Scanner JSON is not an object."
    if code != 0 or "error" in data:
        return "error", data, "Scanner failed; any zero count is not a clean result."
    count = data.get("totalFindings")
    rules, guides = data.get("activeRuleCount"), data.get("activeGuideCount")
    groups = data.get("guides")
    if (type(count) is not int or count < 0 or not isinstance(groups, list)
            or type(rules) is not int or rules <= 0
            or type(guides) is not int or guides <= 0
            or data.get("targetDir") != TARGET):
        return "error", data, "Scanner returned an unexpected report shape or target."
    if any(not isinstance(g, dict) or type(g.get("count")) is not int
           or g["count"] < 0 or not isinstance(g.get("guideId"), str) for g in groups):
        return "error", data, "Scanner returned an unexpected guide shape."
    if sum(g["count"] for g in groups) != count:
        return "error", data, "Scanner finding totals disagree."
    return ("findings" if count else "no-patterns"), data, ""


def write_report(out, code, stdout, stderr, failure=""):
    status, data, reason = classify(code, stdout)
    if failure:
        status, reason = "error", failure
    out.mkdir(parents=True, exist_ok=True)
    (out / "scanner.stdout.json").write_text(stdout)
    (out / "scanner.stderr.txt").write_text(stderr)
    report = {
        "status": status, "package": "modern-web-guidance", "version": VERSION,
        "target": TARGET, "scannerExitCode": code,
        "commit": os.environ.get("GITHUB_SHA", "local"),
        "recordedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "reason": reason, "scanner": data,
        "limits": "Limited deterministic patterns, not browser compatibility or completeness. "
                  "Unsupported, oversized, test/docs or unreadable files may be skipped.",
    }
    (out / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    count = data.get("totalFindings") if isinstance(data, dict) and status != "error" else None
    text = f"# Modern Web Guidance advisory\n\nStatus: **{status}**\n\n"
    text += f"Package: modern-web-guidance@{VERSION}. Target: {TARGET}.\n\n"
    if status == "error":
        text += f"{reason} No clean result is available.\n\n"
    else:
        text += f"Recorded findings: {count}. Review each guide's keepWhen/dropWhen context before changes.\n\n"
    text += report["limits"] + "\n"
    (out / "summary.md").write_text(text)
    return status


def main():
    out = Path(sys.argv[1]) if len(sys.argv) == 2 else ROOT / "tools/modern-web-guidance/report"
    code, stdout, stderr, failure = None, "", "", ""
    try:
        package = json.loads((ROOT / "tools/modern-web-guidance/node_modules/modern-web-guidance/package.json").read_text())
        if package["version"] != VERSION:
            raise ValueError("Installed scanner version differs from the pin.")
        env = dict(os.environ, DISABLE_TELEMETRY="1", ENABLE_FILE_LOGGING="false")
        env.pop("NODE_OPTIONS", None)
        env.pop("NODE_PATH", None)
        proc = subprocess.run(["node", "tools/modern-web-guidance/cli.mjs", "legacy-scan"],
                              cwd=ROOT, env=env, capture_output=True, text=True, timeout=65)
        code, stdout, stderr = proc.returncode, proc.stdout, proc.stderr
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired) as error:
        failure = f"Scanner setup/execution failed ({type(error).__name__})."
    status = write_report(out, code, stdout, stderr, failure)
    print(f"Modern Web Guidance advisory: {status}; {out / 'report.json'}")
    return 1 if status == "error" else 0


if __name__ == "__main__":
    sys.exit(main())
