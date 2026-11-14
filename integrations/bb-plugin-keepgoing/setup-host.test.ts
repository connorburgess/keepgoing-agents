import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, realpath, rm, mkdir, writeFile, readFile, access } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, delimiter } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { setupOnHost, commandOnHost, execute, type Execute } from "./setup-host.ts";

// The plan from a setup that keepgoing did not refuse.
function planned<T>(r: { plan: T } | { refused: unknown }): { plan: T } {
  assert.ok("plan" in r, `refused: ${JSON.stringify(r)}`);
  return r;
}

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
    const r = planned(await setupOnHost(input, undefined, run)); assert.equal(r.plan.threadId, target.threadId);
    reply.primary.agent.bbThread = "thr_wrong";
    await assert.rejects(setupOnHost(input, undefined, run));
    reply.primary.agent.bbThread = target.threadId; reply.schemaVersion = 99;
    await assert.rejects(setupOnHost(input, undefined, run));
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("loop-wide bounds travel as fixed flags and must come back in the CLI's own report", async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-plugin-bounds-")));
  try {
    const target = { threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: root, provider: "codex" };
    const input = { bin: "keepgoing", target, name: "example-loop", task: "Fictional private task", dryRun: true, bounds: { maxSends: 40, noProgressTimeout: "24h" as const } };
    const limits: Record<string, unknown> = { wakeEvery: "1h", interval: "5m", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3, failoverOn: [], maxSends: 40, noProgressTimeout: "24h0m0s" };
    const reply = { schemaVersion: 1, outcome: "preview", loop: input.name, project: root, configPath: join(root, "outside", "keepgoing.yaml"), stateDir: join(root, "outside", "state"), primary: { agent: { adapter: "bb", bbThread: target.threadId, provider: target.provider } }, fallbacks: [], limits };
    const run: Execute = async (_bin, args, stdin) => {
      assert.ok(!args.includes(input.task)); assert.equal(stdin, input.task);
      const at = args.indexOf("--max-sends");
      assert.deepEqual(args.slice(at, at + 4), ["--max-sends", "40", "--no-progress-timeout", "24h"]);
      return { code: 0, data: reply };
    };
    const r = planned(await setupOnHost(input, undefined, run));
    assert.equal(r.plan.limits.maxSends, 40); assert.equal(r.plan.limits.noProgressTimeout, "24h0m0s");
    limits.maxSends = 400;
    await assert.rejects(setupOnHost(input, undefined, run), /loop-wide limits/);
    limits.maxSends = 40; limits.noProgressTimeout = "2h0m0s";
    await assert.rejects(setupOnHost(input, undefined, run), /loop-wide limits/);
    delete limits.maxSends; delete limits.noProgressTimeout;
    await assert.rejects(setupOnHost(input, undefined, run), /loop-wide limits/, "a CLI that ignores the flags is not accepted as bounded");
    // Without requested bounds no flag is added.
    await setupOnHost({ ...input, bounds: undefined }, undefined, async (_bin, args) => { assert.ok(!args.includes("--max-sends")); return { code: 0, data: reply }; });
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("keepgoing's structured refusal comes back as a code and a loop name, never as its own text", async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-plugin-refusal-")));
  try {
    const target = { threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: root, provider: "codex" };
    const input = { bin: "keepgoing", target, name: "example-loop", task: "Fictional private task", dryRun: true };
    const refusal = { schemaVersion: 1, outcome: "refused", refusal: { code: "second_loop_on_session", otherLoop: "other-loop" } };
    const r = await setupOnHost(input, undefined, async () => ({ code: 1, data: refusal }));
    assert.deepEqual(r, { refused: { code: "second_loop_on_session", otherLoop: "other-loop" } });
    // Other fields the CLI may add, such as the other loop's configuration path, are dropped.
    const extra = await setupOnHost(input, undefined, async () => ({ code: 1, data: { ...refusal, refusal: { ...refusal.refusal, configPath: "/private/other/keepgoing.yaml" } } }));
    assert.deepEqual(extra, { refused: { code: "second_loop_on_session", otherLoop: "other-loop" } });
    assert.ok(!JSON.stringify(extra).includes("/private"));
    // A name that is not a loop name, an
    // unknown code, or a refusal reported with success is not believed: the
    // ordinary failure stands, and nothing from the CLI is passed on.
    for (const data of [
      { ...refusal, refusal: { ...refusal.refusal, otherLoop: "/private/other/keepgoing.yaml" } },
      { ...refusal, refusal: { ...refusal.refusal, otherLoop: "Other Loop" } },
      { ...refusal, refusal: { code: "something_else", otherLoop: "other-loop" } },
      { ...refusal, schemaVersion: 2 },
      null,
    ]) {
      await assert.rejects(setupOnHost(input, undefined, async () => ({ code: 1, data })), (e: Error) => !e.message.includes("/private") && !e.message.includes("Other Loop"));
    }
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("the preview's start capability is believed only as the literal true", async () => {
  const root = await realpath(await mkdtemp(join(tmpdir(), "keepgoing-plugin-startjson-")));
  try {
    const target = { threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: root, provider: "codex" };
    const input = { bin: "keepgoing", target, name: "example-loop", task: "Fictional private task", dryRun: true };
    const reply = (startJson: unknown) => ({ code: 0, data: { schemaVersion: 1, outcome: "preview", loop: input.name, project: root, configPath: join(root, "outside", "keepgoing.yaml"), stateDir: join(root, "outside", "state"), primary: { agent: { adapter: "bb", bbThread: target.threadId, provider: target.provider } }, fallbacks: [], limits: { wakeEvery: "1h", interval: "5m", stallAfter: "30m", backoff: "10m", maxNudges: 3, maxReprompts: 3, failoverOn: [] }, ...(startJson === undefined ? {} : { startJson }) } });
    assert.equal(planned(await setupOnHost(input, undefined, async () => reply(true))).plan.startJson, true);
    for (const v of [undefined, false, "true", 1, {}]) assert.equal(planned(await setupOnHost(input, undefined, async () => reply(v))).plan.startJson, undefined, String(v));
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("start passes --json only when told to, and no other command ever does from that flag", async () => {
  const record = { id: "record", threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex", configPath: "/private/example/keepgoing.yaml", loop: "example-loop" };
  const argsOf = async (operation: "start" | "pause" | "resume" | "stop", json?: true) => {
    let got: string[] = [];
    await commandOnHost({ bin: "keepgoing", record, operation, json }, undefined, async (_bin, args) => { got = args; return { code: 0, data: null }; });
    return got;
  };
  assert.deepEqual(await argsOf("start"), ["start", "--background", "-c", record.configPath]);
  assert.deepEqual(await argsOf("start", true), ["start", "--background", "--json", "-c", record.configPath]);
  for (const op of ["pause", "resume", "stop"] as const) assert.ok(!(await argsOf(op, true)).includes("--json"), op);
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
  assert.deepEqual(r, { code: 0, data: { bytes: 14, thread: "thr_example" }, emptyStdout: false, timedOut: false });
  const bad = await execute(process.execPath, ["-e", "process.stderr.write('fictional-secret');process.exit(1)"], "", "thr_example");
  assert.deepEqual(bad, { code: 1, data: null, emptyStdout: true, timedOut: false });
  // The real exit code, so a usage error (2) can be told from a failure (1).
  const usage = await execute(process.execPath, ["-e", "process.exit(2)"], "", "thr_example");
  assert.deepEqual(usage, { code: 2, data: null, emptyStdout: true, timedOut: false });
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
    const preview = planned(await setupOnHost(input, undefined, run));
    assert.equal(preview.plan.withGoal, true);
    assert.ok(!JSON.stringify(preview).includes(input.task));
    for (const bad of [undefined, { ...goal, promptContext: false }, { ...goal, record: summary }]) {
      data = { ...base, goal: bad };
      await assert.rejects(setupOnHost(input, undefined, run));
    }
    data = { ...base, outcome: "created", goal: { ...goal, record: summary } };
    const created = planned(await setupOnHost({ ...input, dryRun: false }, undefined, run));
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
    const input = { bin: binary, target, name: "goal-contract", task: "Fictional task. Acceptance: verify the example.", dryRun: true, withGoal: true, bounds: { maxSends: 40, noProgressTimeout: "24h" as const } };
    const preview = planned(await setupOnHost(input, undefined, run));
    await assert.rejects(access(destination));
    const created = planned(await setupOnHost({ ...input, dryRun: false }, undefined, run));
    assert.deepEqual(created.plan, preview.plan);
    // This build advertises that its start accepts --json.
    assert.equal(preview.plan.startJson, true); assert.equal(created.plan.startJson, true);
    // The real CLI accepts the bounds flags, reports them, and writes them.
    assert.equal(created.plan.limits.maxSends, 40); assert.equal(created.plan.limits.noProgressTimeout, "24h0m0s");
    const config = await readFile(join(destination, "keepgoing.yaml"), "utf8");
    assert.match(config, /max_sends: 40/); assert.match(config, /no_progress_timeout: 24h/);
    const goal = JSON.parse(await readFile(join(destination, "state", "goal.json"), "utf8"));
    assert.equal(goal.revisions[0].spec.title, "goal-contract");
    assert.equal(goal.revisions[0].spec.objectiveRefs[0], join(destination, "task.md"));
    assert.equal(await readFile(join(destination, "task.md"), "utf8"), input.task + "\n");
    assert.ok(!(await readFile(join(destination, "prompt.md"), "utf8")).includes(input.task));
    await assert.rejects(access(join(destination, "state", "control.json")));
    await assert.rejects(access(trap));
  } finally { await rm(root, { recursive: true, force: true }); }
});
