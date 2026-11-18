"""Fictional panel evidence only: no runtime, provider or control operations."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import argparse, json
p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--before', type=Path, required=True, help='Extracted baseline dockui directory')
a = p.parse_args()
after = Path(__file__).resolve().parents[2] / 'cmd/looptender/dockui'
name = 'Example loop with a deliberately long name ending in visible-tail'
subtitle = 'Supervising · on its second route after a usage limit · subtitle-tail'
review = dict(availability='available', text='2 unfinished declarations; dormant work may be intentional.', observedAt='2026-11-18T00:00:00Z', counts=dict(assignments=3, unfinished=2, dormant=1, unresolved=1, directLanes=2, absentAssignments=1))
class H(BaseHTTPRequestHandler):
 def log_message(self, *args): pass
 def send(self, body, mime):
  self.send_response(200); self.send_header('Content-Type', mime); self.end_headers(); self.wfile.write(body)
 def do_GET(self):
  parts=self.path.split('?')[0].strip('/').split('/'); before=parts[0]=='before'; variant=parts[1] if len(parts)>2 else 'normal'
  if self.path.endswith('/api/v1/server'):
   data=dict(schemaVersion=1, servedBy='panel', changes='off', changesText=[dict(t='Read-only: this page cannot pause, resume, stop or start anything.')])
  elif self.path.endswith('/api/v1/usage'):
   data=dict(schemaVersion=1, loops=[])
  elif self.path.endswith('/api/v1/dock'):
   loop=dict(key='exampleloop', name=name if variant!='unbroken' else 'R'*100, state='supervising', phrase='Supervising', pickerState=subtitle, detail='Its last check found no hold.', decision=dict(availability='known', phrase='No message was needed.'), hold=dict(state='none'), children=dict(availability='known',phrase='No direct lanes were recorded.'), lanes=dict(lanes=[]), lastCheck=dict(text='Read at the last check'), nextCheck=dict(availability='unknown'), limits={}, events=dict(availability='known',entries=[],pruned=True,footer='Older ones are no longer kept.'), chain=dict(availability='known',entries=[],short=True,brief='No standby route is ready; its current route is unaffected'),migrationReview=review)
   summary='1 place on another build' if not before else '1 runs a different build; which is newer is not known'
   data=dict(schemaVersion=1,readAt='2026-11-18T01:00:00Z',complete=True,allClear=True,registry=dict(availability='available'),loops=[loop],needsYou=[],needsLine='Nothing needs you',lede='1 loop, 1 supervising. '+('Read from looptender\'s own records; nothing here is a live view of a conversation.' if before else 'Not live: this is what each loop\'s last check recorded.'),versions=dict(summary=summary),accounts=dict(state='available',complete=True,cards=[]),routes=[],routesSummary={},usagePicker='No messages sent')
  else:
   file=parts[-1] if parts[-1] in ['dock.js','dock.css'] else 'index.html'
   body=((a.before if before else after)/file).read_bytes()
   if file=='index.html': body=body.replace(b'<body',b'<body data-fictional-caveats-fixture="true"')
   self.send(body,{'dock.js':'application/javascript','dock.css':'text/css','index.html':'text/html'}[file]); return
  self.send(json.dumps(data).encode(),'application/json')
 def do_POST(self):
  self.send_response(405);self.end_headers()
s=ThreadingHTTPServer(('127.0.0.1',0),H)
print('Fictional caveats fixture: http://127.0.0.1:'+str(s.server_port)+'/after/normal/',flush=True)
s.serve_forever()
