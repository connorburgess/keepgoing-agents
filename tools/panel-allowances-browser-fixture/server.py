"""Serve fictional Go-projected reports and checkout assets; all writes refused."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import argparse, json
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--reports',type=Path,required=True)
p.add_argument('--before',type=Path,required=True,help='Baseline dockui assets')
a=p.parse_args()
after=Path(__file__).resolve().parents[2]/'cmd/looptender/dockui'
class H(BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def do_GET(self):
  parts=self.path.split('?')[0].strip('/').split('/')
  before=parts[0]=='before'
  if self.path.endswith('/api/v1/server'):
   body=json.dumps(dict(schemaVersion=1,servedBy='panel',changes='off',routeChanges='person',accountQuota=False)).encode();mime='application/json'
  elif self.path.endswith('/api/v1/dock'):
   d=json.loads((a.reports/('before.json' if before else 'after.json')).read_text())
   if len(parts)>1 and parts[1] in ['empty','unavailable']:
    d['routes']=[];d['allowances']=[]
    if parts[1]=='unavailable':d['registry']['availability']='unavailable';d['complete']=False;d['allClear']=False
   body=json.dumps(d).encode();mime='application/json'
  elif self.path.endswith('/api/v1/usage'):
   body=b'{"schemaVersion":1,"loops":[]}';mime='application/json'
  else:
   name=parts[-1] if parts[-1] in ['dock.js','dock.css'] else 'index.html'
   body=((a.before if before else after)/name).read_bytes()
   if name=='index.html':body=body.replace(b'<body',b'<body data-fictional-allowances-fixture="true"')
   mime={'dock.js':'application/javascript','dock.css':'text/css','index.html':'text/html'}[name]
  self.send_response(200);self.send_header('Content-Type',mime);self.end_headers();self.wfile.write(body)
 def do_POST(self):self.send_response(405);self.end_headers()
s=ThreadingHTTPServer(('127.0.0.1',0),H)
print('Fictional allowances fixture: http://127.0.0.1:'+str(s.server_port)+'/after/#/routes',flush=True)
s.serve_forever()
