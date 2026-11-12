import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, realpath, rm, mkdir, writeFile, readFile, access } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, delimiter } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
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

test("goal setup requires an explicit flag and verified preview/create goal metadata", async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-goal-host-")));
  try {
    const target = { threadId: "thr_goal_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: root, provider: "codex" };
    const input = { bin: "keepgoing", target, name: "example-loop", task: "Private fictional task", dryRun: true, withGoal: true };
    const base = { schemaVersion: 1, outcome: "preview", loop: input.name, project: root, configPath: join(root, "outside", "keepgoing.yaml"), stateDir: join(root, "outside", "state"), taskPath: join(root, "outside", "task.md"), primary: { agent: { adapter: "bb", bbThread: target.threadId, provider: target.provider } }, fallbacks: [], limits: { wakeEvery: "1h", interval: "5m", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3, failoverOn: [] } };
    const goal = { promptContext: true, titleSource: "loop_name", specificationSource: "task_snapshot" };
    const summary = { id: "goal_0123456789abcdef0123456789abcdef", revision: 1, state: "recorded" };
    let data: unknown = { ...base, goal };
    const run: Execute = async (_bin, args, stdin) => {
      assert.ok(args.includes("--with-goal")); assert.ok(!args.includes(input.task)); assert.equal(stdin, input.task);
      return { code: 0, data };
    };
    const preview = await setupOnHost(input, undefined, run);
    assert.equal(preview.plan.withGoal, true);
    assert.ok(!JSON.stringify(preview).includes(input.task));
    for (const bad of [undefined, { ...goal, promptContext: false }, { ...goal, record: summary }]) {
      data = { ...base, goal: bad };
      await assert.rejects(setupOnHost(input, undefined, run));
    }
    data = { ...base, outcome: "created", goal: { ...goal, record: summary } };
    const created = await setupOnHost({ ...input, dryRun: false }, undefined, run);
    assert.deepEqual(created.plan, preview.plan);
    data = { ...base, outcome: "created", goal };
    await assert.rejects(setupOnHost({ ...input, dryRun: false }, undefined, run));
    await assert.rejects(setupOnHost(input, undefined, async () => ({ code: 2, data: null })), /after v0.1.4/);
  } finally { await rm(root, { recursive: true, force: true }); }
});


test("built CLI creates the goal through the host contract without running BB", { skip: !process.env.KEEPGOING_TEST_BINARY }, async () => {
  const binary = process.env.KEEPGOING_TEST_BINARY!;
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-goal-contract-")));
  const exec = promisify(execFile);
  try {
    const project = join(root, "café 项目"); const bins = join(root, "bin"); const destination = join(root, "private-loop");
    await mkdir(project); await mkdir(bins);
    // A discovered executable is enough for init. Executing this stand-in is a
    // test failure, not a provider status response or a real model invocation.
    const trap = join(root, "unexpected-provider-call");
    await writeFile(join(bins, "bb"), `#!/bin/sh\nprintf called > '${trap}'\nexit 1\n`, { mode: 0o700 });
    const env = { ...process.env, PATH: bins + delimiter + process.env.PATH, GIT_AUTHOR_NAME: "Example", GIT_AUTHOR_EMAIL: "example@example.com", GIT_COMMITTER_NAME: "Example", GIT_COMMITTER_EMAIL: "example@example.com" };
    await exec("git", ["init", "-q", project], { env });
    await exec("git", ["-C", project, "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + join(root, "no-hooks"), "commit", "--allow-empty", "-qm", "test: initialize fixture"], { env });
    // Scope the host's normal OS-default setup destination to this fixture.
    const run: Execute = (bin, args, input, threadId) => new Promise(resolve => {
      const child = execFile(bin, [...args, "--directory", destination], { env: { ...env, BB_THREAD_ID: threadId }, timeout: 20_000, maxBuffer: 1 << 20 }, (error, stdout) => {
        let data: unknown = null; try { data = JSON.parse(stdout); } catch { /* failure remains unavailable */ }
        resolve({ code: error ? 1 : 0, data });
      });
      child.stdin?.on("error", () => {}); child.stdin?.end(input);
    });
    const target = { threadId: "thr_goal_contract", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project, provider: "codex" };
    const input = { bin: binary, target, name: "goal-contract", task: "Fictional task. Acceptance: verify the example.", dryRun: true, withGoal: true };
    const preview = await setupOnHost(input, undefined, run);
    await assert.rejects(access(destination));
    const created = await setupOnHost({ ...input, dryRun: false }, undefined, run);
    assert.deepEqual(created.plan, preview.plan);
    const goal = JSON.parse(await readFile(join(destination, "state", "goal.json"), "utf8"));
    assert.equal(goal.revisions[0].spec.title, "goal-contract");
    assert.equal(goal.revisions[0].spec.objectiveRefs[0], join(destination, "task.md"));
    assert.equal(await readFile(join(destination, "task.md"), "utf8"), input.task + "\n");
    assert.ok(!(await readFile(join(destination, "prompt.md"), "utf8")).includes(input.task));
    await assert.rejects(access(join(destination, "state", "control.json")));
    await assert.rejects(access(trap));
  } finally { await rm(root, { recursive: true, force: true }); }
});
