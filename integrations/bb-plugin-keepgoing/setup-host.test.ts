import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, realpath, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setupOnHost, commandOnHost, execute, type Execute } from "./setup-host.ts";

test("host setup uses stdin, fixed flags and verified CLI output", async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-plugin-")));
  try {
    const target = { threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: root, provider: "codex" };
    const input = { bin: "keepgoing", target, name: "example-loop", task: "Fictional private task", dryRun: true };
    const reply = { schemaVersion: 1, outcome: "preview", loop: input.name, project: root, configPath: join(root, "outside", "keepgoing.yaml"), stateDir: join(root, "outside", "state"), primary: { agent: { adapter: "bb", bbThread: target.threadId, provider: target.provider } }, fallbacks: [], limits: { wakeEvery: "1h", interval: "5m", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3, failoverOn: [] } };
    const run: Execute = async (bin, args, stdin, thread) => {
      assert.equal(bin, input.bin); assert.ok(!args.includes(input.task)); assert.equal(stdin, input.task); assert.equal(thread, target.threadId);
      assert.deepEqual(args.slice(-3), ["--task-file", "-", "--dry-run"]);
      assert.ok(!args.includes("--directory")); assert.ok(!args.includes("--fallback"));
      return { code: 0, data: reply };
    };
    const r = await setupOnHost(input, undefined, run); assert.equal(r.plan.threadId, target.threadId);
    reply.primary.agent.bbThread = "thr_wrong";
    await assert.rejects(setupOnHost(input, undefined, run));
    reply.primary.agent.bbThread = target.threadId; reply.schemaVersion = 99;
    await assert.rejects(setupOnHost(input, undefined, run));
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("host commands route to the recorded file and set the owner; no shell or takeover", async () => {
  const record = { id: "record", threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex", configPath: "/private/path with spaces/keepgoing.yaml", loop: "example-loop" };
  for (const operation of ["status", "doctor", "start", "pause", "resume", "stop"] as const) {
    await commandOnHost({ bin: "keepgoing", record, operation }, undefined, async (_bin, args, input, thread) => {
      assert.equal(input, ""); assert.equal(thread, record.threadId); assert.equal(args[0], operation);
      assert.deepEqual(args.slice(-2), ["-c", record.configPath]); assert.ok(!args.includes("--take-over"));
      assert.equal(args.includes("--background"), operation === "start"); return { code: 0, data: null };
    });
  }
});

test("real local subprocess receives stdin and errors are withheld", async () => {
  const r = await execute(process.execPath, ["-e", "let input='';process.stdin.on('data',c=>input+=c);process.stdin.on('end',()=>process.stdout.write(JSON.stringify({bytes:input.length,thread:process.env.BB_THREAD_ID})))"], "Fictional task", "thr_example");
  assert.deepEqual(r, { code: 0, data: { bytes: 14, thread: "thr_example" } });
  const bad = await execute(process.execPath, ["-e", "process.stderr.write('fictional-secret');process.exit(1)"], "", "thr_example");
  assert.deepEqual(bad, { code: 1, data: null });
  const large = await execute(process.execPath, ["-e", "process.stdout.write('x'.repeat(2**21))"], "", "thr_example");
  assert.equal(large.code, 1); assert.equal(large.data, null);
});
