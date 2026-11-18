import { test } from "node:test";
import assert from "node:assert/strict";
import { publishReference } from "./registry-host.ts";
import { commandOnHost, type Execute } from "./setup-host.ts";
import { transferOnHost } from "./transfer-host.ts";

const binding = "a".repeat(64), id = "loop_" + "b".repeat(32);
const record = { id: "example", threadId: "thr_example", provider: "codex", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", configPath: "/private/example/looptender.yaml", loop: "example-loop" };
const reply = (state: "preview" | "registered", extra = {}) => ({ schemaVersion: 1, registration: { state, source: "cli_registry", binding, ...(state === "registered" ? { id } : {}), ...extra } });

test("publication has fixed bounded read/apply protocol and stable reply identity", async () => {
  let attempts = 0;
  const run: Execute = async (bin, args, input, caller) => {
    assert.equal(bin, "example-cli"); assert.equal(input, ""); assert.equal(caller, record.threadId);
    assert.deepEqual(args.slice(0, 9), ["registry", "reconcile", "-c", record.configPath, "--thread", record.threadId, "--provider", record.provider, "--json"]);
    assert.deepEqual(args.slice(9), attempts++ % 2 === 0 ? [] : ["--binding", binding]);
    return { code: 0, data: reply(args.includes("--binding") ? "registered" : "preview") };
  };
  for (let i = 0; i < 2; i++) assert.deepEqual(await publishReference("example-cli", record.configPath, record.threadId, record.provider, undefined, run), { state: "registered", source: "cli_registry", id });
  assert.equal(attempts, 4);
});

test("unavailable, malformed, changed-correlation and lost publication replies remain repair-needed without lifecycle retry", async () => {
  for (const failure of ["old_cli", "malformed", "changed_binding", "lost_reply", "cancelled"] as const) {
    let calls = 0, starts = 0;
    const abort = new AbortController();
    const run: Execute = async (_bin, args) => {
      calls++;
      if (args[0] === "start") { starts++; return { code: 0, data: { schemaVersion: 1, outcome: "started" } }; }
      if (!args.includes("--binding")) {
        if (failure === "old_cli") return { code: 2, data: null };
        if (failure === "malformed") return { code: 0, data: reply("registered") };
        if (failure === "cancelled") abort.abort();
        return { code: 0, data: reply("preview") };
      }
      if (failure === "lost_reply") throw new Error("PRIVATE HOST FAILURE");
      return { code: 0, data: reply("registered", { binding: "c".repeat(64) }) };
    };
    const r = await commandOnHost({ bin: "example-cli", record, operation: "start", json: true }, abort.signal, run);
    assert.equal(r.code, 0); assert.deepEqual(r.data, { schemaVersion: 1, outcome: "started" });
    assert.ok("registration" in r); assert.deepEqual(r.registration, { state: "repair_needed", source: "cli_registry" });
    assert.equal(starts, 1); assert.ok(calls <= 3); assert.ok(!JSON.stringify(r).includes("PRIVATE"));
  }
});

test("status/doctor/control reads and failed or unconfirmed lifecycle operations never publish", async () => {
  for (const operation of ["status", "doctor", "pause", "resume", "stop", "start"] as const) {
    let calls = 0;
    await commandOnHost({ bin: "example-cli", record, operation, json: true }, undefined, async (_bin, args) => {
      assert.equal(args[0], operation); calls++; return { code: operation === "start" ? 1 : 0, data: null };
    });
    assert.equal(calls, 1);
  }
  let calls = 0;
  await commandOnHost({ bin: "example-cli", record, operation: "start", json: true }, undefined, async () => { calls++; return { code: 0, data: { outcome: "unknown" } }; });
  assert.equal(calls, 1);
});

test("only a matching committed BB transfer publishes; a publication failure never repeats apply", async () => {
  const destination = { ...record, threadId: "thr_destination" };
  for (const matches of [true, false]) {
    const calls: string[] = [];
    const r = await transferOnHost({ bin: "example-cli", record, destination, operation: "apply" }, undefined, async (_bin, args, input, caller) => {
      calls.push(args[0]!); assert.equal(caller, destination.threadId); assert.equal(input, "");
      if (args[0] === "transfer") return { code: 0, data: { outcome: "applied", id: "move_" + "d".repeat(32), from: record.threadId, to: matches ? destination.threadId : "thr_other", awaitingUser: true, ownerGeneration: 2 } };
      return { code: 1, data: null };
    });
    assert.deepEqual(calls, matches ? ["transfer", "registry"] : ["transfer"]);
    assert.equal(r.code, 0);
    if (matches) { assert.ok("registration" in r); assert.equal(r.registration?.state, "repair_needed"); }
  }
});

test("real CLI setup publishes or reports a partial commit without controls/provider calls", { skip: !process.env.KEEPGOING_TEST_BINARY }, async () => {
  const { mkdtemp, realpath, mkdir, writeFile, readFile, access, rm } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join, delimiter } = await import("node:path");
  const { execFile } = await import("node:child_process");
  const { promisify } = await import("node:util");
  const { setupOnHost } = await import("./setup-host.ts");
  const exec = promisify(execFile);
  for (const corrupt of [false, true]) {
    const root = await realpath(await mkdtemp(join(tmpdir(), "registry-lifecycle-")));
    try {
      const project = join(root, "project"), destination = join(root, "private-loop"), registry = join(root, "registry"), bins = join(root, "bin"), trap = join(root, "unexpected-provider");
      await mkdir(project); await mkdir(bins); await mkdir(registry, { mode: 0o700 });
      await writeFile(join(bins, "bb"), `#!/bin/sh\nprintf called > '${trap}'\nexit 1\n`, { mode: 0o700 });
      const env = { ...process.env, HOME: root, USERPROFILE: root, XDG_CONFIG_HOME: root, APPDATA: root, PATH: bins + delimiter + process.env.PATH };
      await exec("git", ["init", "-q", project], { env });
      await exec("git", ["-C", project, "-c", "user.name=Example", "-c", "user.email=example@example.com", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + join(root, "no-hooks"), "commit", "--allow-empty", "-qm", "test: initialize fixture"], { env });
      if (corrupt) await writeFile(join(registry, "registry.json"), "PRIVATE CORRUPT REGISTRY", { mode: 0o600 });
      const calls: string[] = [];
      const run: Execute = (bin, args, input, threadId, signal) => new Promise(resolve => {
        calls.push(args[0]!);
        const child = execFile(bin, [...args, "--directory", args[0] === "registry" ? registry : destination], { env: { ...env, BB_THREAD_ID: threadId }, timeout: 20_000, maxBuffer: 1 << 20, signal }, (error, stdout) => {
          let data: unknown = null; try { data = JSON.parse(stdout); } catch { /* raw output withheld */ }
          resolve({ code: error ? 1 : 0, data });
        });
        child.stdin?.on("error", () => {}); child.stdin?.end(input);
      });
      const target = { ...record, project };
      const r = await setupOnHost({ bin: process.env.KEEPGOING_TEST_BINARY!, target, name: record.loop, task: "Fictional private task", dryRun: false }, undefined, run);
      assert.ok("plan" in r); assert.equal(r.registration?.state, corrupt ? "repair_needed" : "registered");
      assert.deepEqual(calls, ["init", "registry", "registry"]);
      assert.match(await readFile(r.plan.configPath, "utf8"), /failover_on: \[\]/);
      await assert.rejects(access(join(destination, "state", "control.json")));
      await assert.rejects(access(trap));
      if (corrupt) assert.equal(await readFile(join(registry, "registry.json"), "utf8"), "PRIVATE CORRUPT REGISTRY");
      else {
        const first = await readFile(join(registry, "registry.json"), "utf8");
        const repeated = await publishReference(process.env.KEEPGOING_TEST_BINARY!, r.plan.configPath, record.threadId, record.provider, undefined, run);
        assert.equal(repeated.state, "registered"); assert.deepEqual(repeated, r.registration);
        assert.equal(await readFile(join(registry, "registry.json"), "utf8"), first);
      }
    } finally { await rm(root, { recursive: true, force: true }); }
  }
});
