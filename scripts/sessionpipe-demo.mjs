// Local fixture only. No provider, account, GUI or external receiver is used.
// Usage: node scripts/sessionpipe-demo.mjs /path/to/keepgoing /path/to/sessionpipe/packages/core
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createServer } from 'node:http';
import { promisify } from 'node:util';
import { execFile } from 'node:child_process';
const [binaryArg, coreArg] = process.argv.slice(2);
if (!binaryArg || !coreArg) throw new Error('Pass a built keepgoing binary and an existing built Sessionpipe core package; this demo installs nothing.');
const binary = resolve(binaryArg), core = resolve(coreArg);
const { Batch, BatchResponse, EventData } = await import(pathToFileURL(join(core, 'dist/schema.js')));
const { filterEvent } = await import(pathToFileURL(join(core, 'dist/index.js')));
const dir = await mkdtemp(join(tmpdir(), 'sessionpipe-fixture-'));
let lastSeq = 0, identity, posts = 0;
const seen = new Set();
const server = createServer(async (req, res) => {
  try {
    if (req.method === 'GET' && req.url === '/.well-known/sessionpipe') {
      assert.equal(req.headers.authorization, undefined);
      res.end(JSON.stringify({ protocol: [1], max_tier: 0, capabilities: ['events'], auth: ['bearer'], endpoints: { events: '/events' }, batch: { max_events: 2, max_bytes: 262144 } })); return;
    }
    assert.equal(req.method, 'POST'); assert.equal(req.url, '/events');
    assert.equal(req.headers.authorization, 'Bearer fictional-token');
    assert.equal(req.headers['sessionpipe-protocol'], '1');
    let body = ''; for await (const chunk of req) { body += chunk; assert.ok(body.length < 262144); }
    const batch = Batch.parse(JSON.parse(body)); assert.equal(batch.events.length, 2);
    for (const e of batch.events) {
      assert.equal(e.harness.name, 'looptender'); assert.equal(e.tier, 0);
      identity ??= e.session.id; assert.equal(e.session.id, identity);
      assert.ok(e.session.seq > lastSeq); lastSeq = e.session.seq;
      assert.ok(!seen.has(e.id)); seen.add(e.id);
      assert.deepEqual(filterEvent(e, 0, false), e);
      if (EventData.shape[e.type]) EventData.shape[e.type].parse(e.data);
      assert.deepEqual(Object.keys(e.session).sort(), ['id', 'seq']);
    }
    assert.equal(batch.events[0].type, 'session.started');
    assert.equal(batch.events[1].type, 'looptender.test');
    for (const privateValue of ['fictional-token', 'private-demo-loop', 'private-demo-prompt', dir]) assert.ok(!body.includes(privateValue));
    posts++;
    const ack = BatchResponse.parse({ accepted: 2, duplicates: 0, rejected: [], max_tier: 0 });
    res.writeHead(202, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(ack));
  } catch (err) { res.writeHead(400); res.end('{}'); server.fixtureError = err; }
});
try {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const config = join(dir, 'looptender.yaml');
  await writeFile(config, `version: 1\nloop:\n  name: private-demo-loop\n  state_dir: ${JSON.stringify(join(dir, 'state'))}\n  adapter: bb\n  bb: {thread: thr_example123}\n  prompt: private-demo-prompt\n  report:\n    format: sessionpipe\n    scheme: bearer\n    url_env: EXAMPLE_SESSIONPIPE_ORIGIN\n    secret_env: EXAMPLE_SESSIONPIPE_TOKEN\n`, {mode:0o600});
  const env = { ...process.env, EXAMPLE_SESSIONPIPE_ORIGIN: `http://127.0.0.1:${server.address().port}`, EXAMPLE_SESSIONPIPE_TOKEN: 'fictional-token' };
  const run = promisify(execFile);
  // Separate CLI processes prove persistence, rather than one in-memory reporter.
  for (let i = 0; i < 2; i++) {
    let result; try { result = await run(binary, ['report', 'test', '-c', config], {env, timeout:20000, maxBuffer:65536}); } catch (err) { throw server.fixtureError ?? err; }
    assert.match(result.stdout, /HTTP accepted; receipt unverified/);
  }
  if (server.fixtureError) throw server.fixtureError;
  assert.equal(posts, 2); assert.equal(lastSeq, 4);
  console.log('PASS: two restarted CLI reports; real Sessionpipe core schema/data/filter; opaque identity, sequences 1–4, no private report content.');
} finally {
  server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  await rm(dir, { recursive:true, force:true });
}
