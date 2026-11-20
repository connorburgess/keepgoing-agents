import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile, writeFile, mkdtemp, rm, symlink, unlink, realpath } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PLUGIN_VERSION, clientArgs, withClientFlag, forgetRefusals, cliIdentity, type CallOutcome } from "./client-flag.ts";
import { commandOnHost } from "./setup-host.ts";

type R = CallOutcome & { out: string };
const outcome = (r: R): CallOutcome => r;
const ok: R = { code: 0, emptyStdout: false, timedOut: false, out: "{}" };
const refusal: R = { code: 2, emptyStdout: true, timedOut: false, out: "" };

test("the reported version is package.json's, under a name with no product name", async () => {
  const pkg = JSON.parse(await readFile(new URL("./package.json", import.meta.url), "utf8")) as { version: string };
  assert.equal(PLUGIN_VERSION, pkg.version);
  assert.deepEqual(clientArgs(), ["--client", `bb-plugin@${pkg.version}`]);
});

test("an older CLI that refuses --client is asked again without it, and not given it again", async () => {
  forgetRefusals();
  const calls: string[][] = [];
  const older = async (extra: string[]) => { calls.push(extra); return extra.length ? refusal : ok; };
  assert.deepEqual(await withClientFlag("/fictional/older", older, outcome), ok);
  await withClientFlag("/fictional/older", older, outcome);
  assert.deepEqual(calls, [clientArgs(), [], []]);
  calls.length = 0;
  const knows = async (extra: string[]) => { calls.push(extra); return ok; };
  await withClientFlag("/fictional/newer", knows, outcome);
  assert.deepEqual(calls, [clientArgs()]);
});

test("only a usage error with nothing printed, followed by a success, counts as a refusal", async () => {
  for (const [name, first, second, calls] of [
    ["a timeout is never retried", { code: 1, emptyStdout: true, timedOut: true, out: "" }, ok, 1],
    ["exit 1 is not a refusal", { code: 1, emptyStdout: true, timedOut: false, out: "" }, ok, 1],
    ["exit 2 with output is not a refusal", { code: 2, emptyStdout: false, timedOut: false, out: "usage" }, ok, 1],
    ["a missing configuration fails both ways, so nothing is remembered", refusal, refusal, 4],
    ["a retry that times out is not remembered", refusal, { code: 1, emptyStdout: true, timedOut: true, out: "" }, 4],
  ] as const) {
    forgetRefusals();
    const seen: string[][] = [];
    const call = async (extra: string[]) => { seen.push(extra); return extra.length ? first : second; };
    await withClientFlag("/fictional/" + name, call, outcome);
    await withClientFlag("/fictional/" + name, call, outcome);
    assert.equal(seen.length, calls * (calls === 1 ? 2 : 1), name);
    assert.ok(seen.filter((x) => x.length).length === 2, `${name}: the flag is still sent`);
  }
});

test("a CLI upgraded in place is given the flag again", async () => {
  forgetRefusals();
  const dir = await mkdtemp(join(tmpdir(), "kgp-"));
  try {
    const bin = join(dir, "keepgoing");
    await writeFile(bin, "old", { mode: 0o700 });
    const before = await cliIdentity(bin);
    const seen: string[][] = [];
    const older = async (extra: string[]) => { seen.push(extra); return extra.length ? refusal : ok; };
    await withClientFlag(bin, older, outcome);
    await writeFile(bin, "a newer, longer build");
    assert.notEqual(await cliIdentity(bin), before);
    await withClientFlag(bin, async (extra) => { seen.push(extra); return ok; }, outcome);
    assert.deepEqual(seen, [clientArgs(), [], clientArgs()]);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("a bare command name and a symlink are known by the file they run", async () => {
  const dir = await mkdtemp(join(tmpdir(), "kgp-"));
  try {
    const first = join(dir, "keepgoing-0.2.0"), second = join(dir, "keepgoing-0.2.1"), link = join(dir, "keepgoing");
    await writeFile(first, "old", { mode: 0o700 });
    await writeFile(second, "a newer, longer build", { mode: 0o700 });
    await symlink(first, link);
    const byName = await cliIdentity("keepgoing", dir);
    assert.ok(byName.startsWith(await realpath(first)), byName);
    assert.equal(await cliIdentity(link), byName);
    // The link moved to a new file: a new identity.
    await unlink(link);
    await symlink(second, link);
    assert.notEqual(await cliIdentity("keepgoing", dir), byName);
    assert.notEqual(await cliIdentity(link), byName);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("the host's status passes the flag before the configuration path, and only on status", async () => {
  forgetRefusals();
  const record = { id: "record", threadId: "thr_example", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex", configPath: "/private/example/keepgoing.yaml", loop: "example-loop" };
  const seen: string[][] = [];
  await commandOnHost({ bin: "/fictional/keepgoing", record, operation: "status" }, undefined, async (_bin, args) => { seen.push(args); return { code: 0, data: {} }; });
  assert.deepEqual(seen, [["status", "--json", ...clientArgs(), "-c", record.configPath]]);
  seen.length = 0;
  await commandOnHost({ bin: "/fictional/keepgoing", record, operation: "pause" }, undefined, async (_bin, args) => { seen.push(args); return { code: 0, data: null }; });
  assert.ok(!seen[0].includes("--client"));
});
