from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
from pathlib import Path
import argparse,json,copy
parser=argparse.ArgumentParser(description='Fictional route-editor API; never reads runtime state.')
parser.add_argument('--artifacts',type=Path,required=True)
args=parser.parse_args()
root=args.artifacts; root.mkdir(parents=True,exist_ok=True); wt=Path(__file__).resolve().parents[2]
rows=[dict(id='example-route-a',provider='claude-code',model='example-large',effort='medium',group='owner-limit-a',enter='same_conversation',disabled=False),dict(id='example-route-b',provider='codex',model='example-codex',group='owner-limit-b',enter='new_conversation',disabled=True)]
loops=[dict(key='r'+str(i)*32,view=dict(loop='example-loop-'+str(i),list='own',handoff='off',can=['follow_standard'],routes=[],skipped=[],warnings=[])) for i in range(1,4)]
accounts=[dict(id='a'*32,label='Example selected Claude',provider='claude-code',selection='externally_managed'),dict(id='b'*32,label='Example selected Codex',provider='codex',selection='inherited')]
pending={}
class H(BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def reply(self,data):
  self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps(dict(schemaVersion=1,**data)).encode())
 def do_GET(self):
  if self.path.endswith('/api/v1/server'):
   mode='bb_caller' if '/agent/' in self.path else 'person'
   self.reply(dict(servedBy='panel',changes='off' if '/readonly/' in self.path else 'on',routeChanges=mode,form='fictional-session-form'));return
  if self.path.endswith('/api/v1/dock'):
   self.reply(dict(readAt='2026-11-18T01:00:00Z',complete=True,registry=dict(availability='available'),loops=[dict(key='r'+'1'*32,name='Example loop',state='paused',chain=dict(entries=[]))],routes=[],routesSummary=dict(picker='Configured routes'),needsYou=[],needsLine='Nothing needs you'));return
  name=self.path.split('/')[-1] or 'index.html'
  if name not in ['dock.js','dock.css','routes-help.html']:name='index.html'
  self.send_response(200); self.send_header('Content-Type',{'dock.js':'application/javascript','dock.css':'text/css','index.html':'text/html','routes-help.html':'text/html'}[name]);self.end_headers();body=(wt/'cmd/looptender/dockui'/name).read_bytes();body=body.replace(b'<body',b'<body data-fictional-route-fixture="true"') if name=='index.html' else body;self.wfile.write(body)
 def do_POST(self):
  step=self.path.rsplit('/',1)[-1]; data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  with (root/'routes-browser-posts.jsonl').open('a') as f:f.write(json.dumps(dict(step=step,data=data))+'\n')
  if step=='read':self.reply(dict(editor=dict(standard=dict(state='ok',routes=rows,can=['add','move','off','on','remove']),accounts=accounts,loops=loops,accountLabels={'example-route-a':'Example selected Claude'})));return
  if step=='models':self.reply(dict(models=[dict(model='example-listed',displayName='Example supported model',supportedReasoningEfforts=[dict(reasoningEffort='medium'),dict(reasoningEffort='high')]),dict(model='example-other',displayName='Example other model',supportedReasoningEfforts=[dict(reasoningEffort='low')])],text='Models reported by BB; no access check.'));return
  if step=='prepare':
   after=copy.deepcopy(rows)
   if data['op']=='add':after.append(dict(id='route-generated',provider='claude-code',model=data['model'],effort=data.get('effort',''),group=data['limit'],enter=data['behavior'],disabled=False))
   change=dict(outcome='preview',op=data['op'],before=[r['id'] for r in rows],after=[r['id'] for r in after],routes=after,loops=[dict(loop='example-loop-1',before=[],after=[r['id'] for r in after],newlySkipped=[],belowMinHealthy=True)],warnings=[])
   changes=[change]
   if data['op']=='follow_standard': changes=[dict(change,loop='example-loop-'+str(i+1)) for i in range(len(data['loops']))]
   pending['changes']=changes
   pending['accountLabels']={'route-generated':next((a['label'] for a in accounts if a['id']==data.get('account')),'')} if data['op']=='add' else {}
   self.reply(dict(form='fictional-review',outcome='preview',changes=changes,accountLabels=pending['accountLabels'],text='Review before saving. Each loop saves separately; earlier successful saves remain.'));return
  if step=='confirm':
   changes=copy.deepcopy(pending.get('changes',[]))
   for i,c in enumerate(changes):c['outcome']=['changed','refused','not_attempted'][min(i,2)]
   self.reply(dict(outcome='partial' if len(changes)>1 else 'changed',changes=changes,accountLabels=pending.get('accountLabels',{}),text='Saving stopped. Earlier successful saves remain; remaining loops were not changed.' if len(changes)>1 else 'Saved the reviewed change.'));return
s=ThreadingHTTPServer(('127.0.0.1',0),H)
print('Fictional fixture: http://127.0.0.1:'+str(s.server_port)+'/owner/#/routes',flush=True)
s.serve_forever()
