import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { PLUGIN_VERSION, clientArgs, withClientFlag, forgetRefusals } from "./client-flag.ts";
import { commandOnHost } from "./setup-host.ts";

test("the reported version is package.json's, under a name with no product name", async () => {
  const pkg = JSON.parse(await readFile(new URL("./package.json", import.meta.url), "utf8")) as { version: string };
  assert.equal(PLUGIN_VERSION, pkg.version);
  assert.deepEqual(clientArgs(), ["--client", `bb-plugin@${pkg.version}`]);
});

test("an older CLI that refuses --client is asked again without it, and not given it again", async () => {
  forgetRefusals();
  const calls: string[][] = [];
  const call = async (extra: string[]) => { calls.push(extra); return extra.length ? { code: 2, stdout: "" } : { code: 0, stdout: "{}" }; };
  const refusedFlag = (r: { code: number; stdout: string }) => r.code === 2 && r.stdout === "";
  assert.deepEqual(await withClientFlag("/fictional/older", call, refusedFlag, (r) => r.code === 0), { code: 0, stdout: "{}" });
  await withClientFlag("/fictional/older", call, refusedFlag, (r) => r.code === 0);
  assert.deepEqual(calls, [clientArgs(), [], []]);
  // A CLI that knows the flag is called once, with it.
  calls.length = 0;
  const knows = async (extra: string[]) => { calls.push(extra); return { code: 0, stdout: "{}" }; };
  await withClientFlag("/fictional/newer", knows, refusedFlag);
  assert.deepEqual(calls, [clientArgs()]);
});

test("a status that fails either way is not taken for a refusal", async () => {
  forgetRefusals();
  const calls: string[][] = [];
  const broken = async (extra: string[]) => { calls.push(extra); return { code: 1, data: null }; };
  for (let i = 0; i < 2; i++) await withClientFlag("/fictional/broken", broken, (r) => r.code !== 0 && r.data === null, (r) => r.code === 0);
  assert.deepEqual(calls, [clientArgs(), [], clientArgs(), []]);
});

test("the host's status passes the flag before the configuration path", async () => {
  forgetRefusals();
  const record = { id: "record", threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex", configPath: "/private/example/keepgoing.yaml", loop: "example-loop" };
  const seen: string[][] = [];
  await commandOnHost({ bin: "/fictional/keepgoing", record, operation: "status" }, undefined, async (_bin, args) => { seen.push(args); return { code: 0, data: {} }; });
  assert.deepEqual(seen, [["status", "--json", ...clientArgs(), "-c", record.configPath]]);
  seen.length = 0;
  await commandOnHost({ bin: "/fictional/keepgoing", record, operation: "pause" }, undefined, async (_bin, args) => { seen.push(args); return { code: 0, data: null }; });
  assert.ok(!seen[0].includes("--client"));
});
