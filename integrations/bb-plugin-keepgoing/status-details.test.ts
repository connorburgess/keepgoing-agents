import { test } from "node:test";
import assert from "node:assert/strict";
import { createFakePluginHost } from "@get-bb/plugin-sdk/testing";
import plugin, { rpcContract } from "./server.ts";
import { diagnosticLines, dateLabel } from "./status-details.ts";

test("status RPC projects diagnostic metadata, preserves completion under other holds and supports legacy builds", async () => {
  const record = { id: "example", loop: "example", threadId: "thr_source", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex", configPath: "/private/example/keepgoing.yaml" };
  const agent = { adapter: "bb", bbThread: "thr_source", provider: "codex" };
  const base = { loop: "example", target: "fallback", controlState: "paused", control: { paused: true, completed: true }, window: "open", state: {}, primaryAgent: agent, targetAgent: { ...agent, bbThread: "thr_destination" } };
  let data: unknown = { ...base, task: "PRIVATE TASK", targetConfigured: true,
    runtime: { state: "paused", source: "control", freshness: "stale", reason: "PRIVATE REASON" },
    daemon: { state: "responsive", phase: "waiting", observedAt: "2026-01-01T00:00:00Z", nextCheckAt: "2026-01-01T00:05:00Z", dryRun: true, binaryComparison: "different", message: "PRIVATE OUTPUT" },
    state: { lastWakeAt: "0001-01-01T00:00:00Z", lastCheck: { at: "2025-12-31T23:55:00Z", target: "primary", action: "wake", outcome: "failed", reason: "PRIVATE REASON" } },
  };
  const { bb, harness } = createFakePluginHost({ pluginId: "keepgoing", settings: { configs: "", keepgoingPath: "keepgoing" },
    experimental_callHostRpc: async () => ({ code: 0, data }),
  });
  try {
    await plugin(bb); await bb.storage.kv.set("setups.v1", [record]);
    const read = async () => rpcContract.forThread.output.parse(await harness.behavior.callRpc("forThread", { threadId: record.threadId }));
    const result = await read(), loop = result.loops[0]!;
    assert.deepEqual(result.errors, []); assert.equal(loop.supervising, false);
    assert.equal(loop.controlState, "paused"); assert.equal(loop.details?.completed, true);
    assert.equal(loop.details?.targetThread, "thr_destination");
    assert.ok(!JSON.stringify(result).includes("PRIVATE"));
    const lines = diagnosticLines(loop.details, loop.target, loop.supervising).join("\n");
    assert.match(lines, /declared; not independently verified/);
    assert.match(lines, /thr_destination.*not this thread/);
    assert.match(lines, /stale/); assert.match(lines, /dry run/); assert.match(lines, /build comparison different/);
    assert.match(lines, /Last recorded action: wake · failed · target primary/);
    assert.match(lines, /External scheduler health: not inspected/);
    assert.equal(dateLabel(loop.lastWakeAt), "unavailable");
    for (const state of ["not_recorded", "unavailable", "skipped"]) {
      data = { ...base, daemon: { state } };
      assert.match(diagnosticLines((await read()).loops[0]?.details, "fallback", false).join("\n"), /running supervisor not confirmed/);
    }
    data = base;
    const legacy = (await read()).loops[0]!;
    assert.equal(legacy.details?.lastCheck, null); assert.equal(legacy.details?.runtime, null);
    assert.match(diagnosticLines(legacy.details, "fallback", false).join("\n"), /unavailable from this build/);
    data = { ...base, runtime: { state: 42 } };
    const invalid = await read(); assert.equal(invalid.loops.length, 0); assert.equal(invalid.errors.length, 1);
    assert.deepEqual(harness.inspection.sdk.callsTo("threads.send"), []);
  } finally { await harness.lifecycle.dispose(); }
});

test("historical dry-run, future and missing evidence never imply a successful send or healthy scheduler", () => {
  const lines = diagnosticLines({ readAt: "invalid", completed: false, targetThread: null, targetAdapter: "process", targetConfigured: false,
    runtime: { state: "unknown", source: "unavailable", freshness: "future" }, daemon: null,
    lastCheck: { at: "0001-01-01T00:00:00Z", target: "primary", action: "wake", outcome: "dry_run", dryRun: true },
  }, "primary", false).join("\n");
  assert.match(lines, /missing or disabled/); assert.match(lines, /future/); assert.match(lines, /wake · dry_run · dry run/);
  assert.match(lines, /not confirmed/); assert.match(lines, /unavailable/);
});
