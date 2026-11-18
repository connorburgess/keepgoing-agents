"""Serve checked-out panel assets and test-generated fictional JSON, never runtime data."""
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--report", type=Path, required=True)
args = parser.parse_args()
report = json.loads(args.report.read_text())
assert report["schemaVersion"] == 1
assert {row["name"] for row in report["loops"]} == {
    "docs-site-refresh", "billing-api-migration", "mobile-onboarding",
    "search-indexer", "release-notes-bot", "nightly-dependency-bumps",
    "data-export-cleanup", "payments-reconciler", "inventory-sync", "report-builder",
}, "Use only TestDockMigrationReviewBrowserFixture output"
assets = Path(__file__).resolve().parents[2] / "cmd/looptender/dockui"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        name = self.path.split("?")[0].rsplit("/", 1)[-1]
        if name in ("dock", "server", "usage"):
            data = report if name == "dock" else (
                dict(schemaVersion=1, servedBy="dock", changes="off") if name == "server"
                else dict(schemaVersion=1, loops=[], total=[])
            )
            body, mime = json.dumps(data).encode(), "application/json"
        elif name in ("", "dock.js", "dock.css"):
            name = name or "index.html"
            body = (assets / name).read_bytes()
            if name == "index.html":
                body = body.replace(b"<body", b'<body data-fictional-migration-fixture="true"')
            mime = {"index.html": "text/html", "dock.js": "application/javascript", "dock.css": "text/css"}[name]
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        self.send_error(405, "Read-only fictional fixture")


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
print("Fictional fixture: http://127.0.0.1:" + str(server.server_port) + "/", flush=True)
server.serve_forever()
