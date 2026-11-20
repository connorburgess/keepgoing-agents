"""Fictional Go-projected saved quota; never reads live accounts or providers."""
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
import argparse,json,time
from urllib.parse import urlsplit,parse_qs
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--report',type=Path,required=True);a=p.parse_args()
report=json.loads(a.report.read_text());assert [c['label'] for c in report['accounts']['cards']][:2]==['Example Codex','Example Claude']
assets=Path(__file__).resolve().parents[2]/'cmd/looptender/dockui'
class H(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_GET(self):
  path=self.path.split('?')[0]
  if path.endswith('/api/v1/dock') or path.endswith('/api/v1/saved-accounts'):
   data=json.loads(json.dumps(report))
   if '/empty/' in path:data['accounts']['cards']=[]
   if '/unavailable/' in path:data['accounts']={'state':'unavailable','complete':False,'cards':[]}
   reference='fictional-saved-'+path.split('/')[1]
   if path.endswith('/api/v1/saved-accounts'):
    if '/error/' in path:self.send_error(503,'Fictional saved read unavailable');return
    if '/loading/' in path:time.sleep(2)
    if parse_qs(urlsplit(self.path).query).get('reference')!=[reference]:self.send_error(409);return
    data={'schemaVersion':1,'reference':reference,'readAt':data['readAt'],'accounts':data['accounts']}
   elif '/legacy/' not in path:
    data['accountsReference']=reference
    data['selectedAccounts']=len(data['accounts']['cards'])
    data['accounts']['cards']=[]
   body=json.dumps(data).encode();mime='application/json'
  elif path.endswith('/api/v1/server'):
   body=json.dumps(dict(schemaVersion=1,servedBy='panel',changes='on' if '/owner/' in path else 'off',accountQuota=True,routeChanges='person',form='fictional-form')).encode();mime='application/json'
  elif path.endswith('/api/v1/usage'):body=b'{"schemaVersion":1,"loops":[]}';mime='application/json'
  else:
   name=path.rsplit('/',1)[-1] or 'index.html'
   if name not in ['index.html','dock.js','dock.css']:self.send_error(404);return
   body=(assets/name).read_bytes();mime={'index.html':'text/html','dock.js':'application/javascript','dock.css':'text/css'}[name]
   if name=='index.html':body=body.replace(b'<body',b'<body data-fictional-quota-fixture="true"')
  self.send_response(200);self.send_header('Content-Type',mime);self.send_header('Cache-Control','no-store');self.end_headers();self.wfile.write(body)
 def do_POST(self):self.send_error(405,'Fictional read-only fixture; no acquisition')
s=ThreadingHTTPServer(('127.0.0.1',0),H);print('Fictional quota fixture: http://127.0.0.1:'+str(s.server_port)+'/owner/#/accounts',flush=True);s.serve_forever()
