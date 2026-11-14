// bb-plugin-keepgoing: shows keepgoing's state beside the threads it
// supervises, and offers pause, resume, and stop. It only calls the keepgoing
// CLI; every decision stays in keepgoing.
import { checkSchema, runtimeSchema, daemonSchema, detailsSchema, goalDetails, scheduleSchema } from "./status-details.ts";
import { execFile } from "node:child_process";
import { defineRpcContract, type BbPluginApi } from "@get-bb/plugin-sdk";
import { z } from "zod";
import { hostContract } from "./host-contract.ts";
import { recordSchema, setupInput, setupResult, targetSchema } from "./setup-contract.ts";
import { createTransferController } from "./transfer-controller.ts";
import { transferInput, transferResult, moveIntentSchema } from "./transfer-contract.ts";
import { createSetupController, MAX_SETUPS } from "./setup-controller.ts";
import { createNewThreadController } from "./new-thread-controller.ts";
import { newThreadInput, newThreadResult, startModeSchema, type Bounds } from "./new-thread-contract.ts";
import { randomUUID } from "node:crypto";
import { withClientFlag } from "./client-flag.ts";

/** The subset of `keepgoing status --json` this plugin reads. */
const agentRef = z.object({ adapter: z.string(), bbThread: z.string().optional(), provider: z.string().optional(), model: z.string().optional() });
const statusSchema = z.object({
  goal: z.unknown().optional(), goalError: z.unknown().optional(),
  loop: z.string(),
  target: z.string(),
  controlState: z.string(),
  control: z.object({ note: z.string().optional(), completed: z.boolean().optional() }).passthrough(),
  window: z.string(),
  state: z
    .object({
      lastCheck: checkSchema.optional(),
      lastState: z.string().optional(),
      lastWakeAt: z.string().optional(),
      checkedAt: z.string().optional(),
      handoffs: z.number().optional(),
    })
    .passthrough(),
  redundancy: z.object({ healthy: z.array(z.string()), distinct: z.number(), min: z.number(), meets: z.boolean() }).optional(),
  runtime: runtimeSchema.optional(),
  schedule: scheduleSchema.optional(),
  daemon: daemonSchema.optional(),
  targetConfigured: z.boolean().optional(),
  primaryAgent: agentRef,
  targetAgent: agentRef,
});

const loopSchema = z.object({
  details: detailsSchema.optional(),
  loop: z.string(),
  configPath: z.string(),
  managedId: z.string().optional(),
  managedThreadId: z.string().optional(),
  controlState: z.string(),
  note: z.string().nullable(),
  target: z.string(),
  supervising: z.boolean(), // this thread is the loop's current target
  lastState: z.string().nullable(),
  lastWakeAt: z.string().nullable(),
  checkedAt: z.string().nullable(),
  handoffs: z.number(),
  fallbacks: z.string().nullable(),
});
export type LoopView = z.infer<typeof loopSchema>;

const actionSchema = z.enum(["pause", "resume", "stop"]);

export const rpcContract = defineRpcContract({
  transfer: { input: transferInput, output: transferResult },
  setup: { input: setupInput, output: setupResult },
  newThread: { input: newThreadInput, output: newThreadResult },
  forThread: {
    input: z.object({ threadId: z.string().min(1).max(200) }),
    output: z.object({ loops: z.array(loopSchema), errors: z.array(z.string()) }),
  },
  control: {
    input: z.object({ configPath: z.string().min(1).max(1000), action: actionSchema, managedId: z.string().optional(), expectedThreadId: z.string().min(1).max(200).optional() }),
    output: z.object({ ok: z.boolean(), message: z.string() }),
  },
});

const CHANGED = "changed";

function run(bin: string, args: string[]): Promise<{ code: number; stdout: string; stderr: string; timedOut: boolean }> {
  return new Promise((resolve) => {
    execFile(bin, args, { timeout: 15_000, maxBuffer: 1 << 20 }, (error, stdout, stderr) => {
      const e = error as { code?: unknown; killed?: boolean; signal?: unknown } | null;
      const timedOut = !!e && e.killed === true;
      const code = e ? (typeof e.code === "number" ? e.code : 1) : 0;
      resolve({ code, stdout: String(stdout), stderr: String(stderr), timedOut });
    });
  });
}

export default async function plugin(bb: BbPluginApi) {
  const settings = bb.settings.define({
    keepgoingPath: {
      type: "string",
      // The key and the default keep the tool's earlier name: the key is
      // stored with the setting, and releases up to v0.2.0 install the
      // executable as keepgoing.
      label: "looptender binary",
      description: "Absolute path to the looptender executable, which releases up to v0.2.0 install as keepgoing. A bare name is looked up on the BB server's PATH.",
      default: "keepgoing",
    },
    configs: {
      type: "string",
      label: "Loop configs",
      description: "One keepgoing.yaml path per line. Only these loops are shown and controlled.",
      experimental_multiline: true,
      default: "",
    },
    newThreadStart: {
      type: "select",
      label: "Keep going from the New thread screen",
      description: "What happens after a thread is created with Send and keep going. Start automatically: supervision starts once the thread exists; if anything about the start is uncertain, the setup is left switched off with the reason. Review first: a switched-off setup is created and you start it from the thread. Both create the loop with a loop-wide limit.",
      options: ["automatic", "manual"],
      experimental_optionLabels: { automatic: "Start automatically", manual: "Review first; I start it" },
      default: "automatic",
    },
  });

  const configured = async () => {
    const { keepgoingPath, configs } = await settings.get();
    const paths = configs
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line !== "" && !line.startsWith("#"));
    return { bin: keepgoingPath.trim() || "keepgoing", paths };
  };

  // Serialize setup, moves and resume across clients. Restrictive pause/stop
  // remain independent so they can reach the CLI's short control lock.
  let mutation: Promise<unknown> = Promise.resolve();
  const serialize = <T>(fn: () => Promise<T>): Promise<T> => {
    const next = mutation.then(fn, fn); mutation = next.catch(() => {}); return next;
  };
  const intents = async () => z.array(moveIntentSchema).max(32).parse(await bb.storage.kv.get("moves.v1") ?? []);
  const host = bb.hosts.experimental_client({ contract: hostContract });
  const records = async () => z.array(recordSchema).max(32).parse(await bb.storage.kv.get("setups.v1") ?? []);
  const hostCommand = (bin: string, record: z.infer<typeof recordSchema>, operation: "status" | "doctor" | "start" | "pause" | "resume" | "stop", json?: true) =>
    host.call("command", { bin, record, operation, ...(json ? { json } : {}) }, { hostId: record.hostId, signal: AbortSignal.timeout(30_000) });
  const resolveTarget = async (threadId: string) => {
    const signal = AbortSignal.timeout(10_000);
    const t = await bb.sdk.threads.get({ threadId, signal });
    if (t.id !== threadId || t.archivedAt !== null || t.deletedAt !== null || !t.environmentId) throw new Error("Target unavailable");
    const e = await bb.sdk.environments.get({ environmentId: t.environmentId, signal });
    if (e.id !== t.environmentId || e.projectId !== t.projectId || e.status !== "ready" || e.lifecycle.phase !== "active" || e.hostLifecycle !== "active" || !e.isGitRepo || !e.path) throw new Error("Environment unavailable");
    const h = await bb.sdk.hosts.get({ hostId: e.hostId, signal });
    if (h.id !== e.hostId || h.status !== "connected" || h.lifecycle.phase !== "active") throw new Error("Host unavailable");
    return targetSchema.parse({ threadId, environmentId: e.id, projectId: t.projectId, hostId: e.hostId, project: e.path, provider: t.providerId });
  };

  const status = async (bin: string, configPath: string) => {
    const result = await withClientFlag(bin, (extra) => run(bin, ["status", "--json", ...extra, "-c", configPath]), (r) => ({ code: r.code, emptyStdout: r.stdout === "", timedOut: r.timedOut }));
    if (result.code !== 0) throw new Error(`${configPath}: looptender status exited ${result.code}`);
    let raw: unknown;
    try {
      raw = JSON.parse(result.stdout);
    } catch {
      throw new Error(`${configPath}: looptender status did not print JSON`);
    }
    const parsed = statusSchema.safeParse(raw);
    if (!parsed.success) {
      const old = typeof raw === "object" && raw !== null && !("primaryAgent" in raw);
      throw new Error(old ? `${configPath}: this looptender is too old for the plugin; it needs a build with primaryAgent in status --json (after 0.1.0)` : `${configPath}: unexpected looptender status JSON`);
    }
    return parsed.data;
  };

  const view = (s: z.infer<typeof statusSchema>, configPath: string, threadId: string | null): LoopView => ({
    details: {
      ...goalDetails(s.goal, s.goalError),
      readAt: new Date().toISOString(), completed: s.control.completed ?? false,
      targetThread: s.targetAgent.bbThread ?? null, targetAdapter: s.targetAgent.adapter,
      targetConfigured: s.targetConfigured ?? null, runtime: s.runtime ?? null,
      daemon: s.daemon ?? null, lastCheck: s.state.lastCheck ?? null,
      schedule: s.schedule ?? null,
    },
    loop: s.loop,
    configPath,
    controlState: s.controlState,
    note: s.control.note ?? null,
    target: s.target,
    supervising: threadId !== null && s.targetAgent.bbThread === threadId,
    lastState: s.state.lastState ?? null,
    lastWakeAt: s.state.lastWakeAt ?? null,
    checkedAt: s.state.checkedAt ?? null,
    handoffs: s.state.handoffs ?? 0,
    fallbacks: s.redundancy ? `${s.redundancy.healthy.length} healthy, need ${s.redundancy.min}` : null,
  });

  const allLoops = async (threadId: string | null) => {
    const { bin, paths } = await configured();
    const loops: LoopView[] = [];
    const errors: string[] = [];
    for (const configPath of paths) {
      try {
        const s = await status(bin, configPath);
        const matches = threadId === null || s.primaryAgent.bbThread === threadId || s.targetAgent.bbThread === threadId;
        if (matches) loops.push(view(s, configPath, threadId));
      } catch (error) {
        errors.push(error instanceof Error ? error.message : String(error));
      }
    }
    const moving = await intents();
    for (const record of await records()) {
      const move = moving.find(i => i.record.id === record.id);
      if (threadId !== null && record.threadId !== threadId && move?.destination.threadId !== threadId) continue;
      try {
        const result = await hostCommand(bin, record, "status");
        if (result.code !== 0) throw new Error("status failed");
        const displayed = view(statusSchema.parse(result.data), record.configPath, threadId);
        // A partial move is not a normal Resume action, even if the host has
        // already committed but the plugin reference is not reconciled yet.
        if (move) { displayed.controlState = "move requires inspection"; displayed.note = `Move requested to ${move.destination.threadId}; check move status there before resuming.`; }
        loops.push({ ...displayed, managedId: record.id, managedThreadId: record.threadId });
      } catch { errors.push(`${record.loop}: host status unavailable`); }
    }
    return { loops, errors };
  };

  // Control only loops listed in settings: the path comes from the browser,
  // so it must never choose an arbitrary config file.
  const control = async (configPath: string, action: z.infer<typeof actionSchema>, managedId?: string, expectedThreadId?: string) => {
    const { bin, paths } = await configured();
    if (action === "resume" && (await intents()).some(i => i.record.id === managedId || i.record.configPath === configPath)) return { ok: false, message: "A move requires inspection. Check move status from its destination before resuming." };
    if (managedId) {
      const record = (await records()).find(r => r.id === managedId && r.configPath === configPath);
      if (!record) return { ok: false, message: "That setup is not registered with this plugin." };
      if (action === "resume" && expectedThreadId !== record.threadId) return { ok: false, message: "This setup moved or the view is stale. Open its current thread and refresh before resuming." };
      try {
        if (action === "resume") {
          const current = await resolveTarget(record.threadId);
          const { id, configPath: ignored, loop, startJson: _json, startJsonBin: _jsonBin, ...saved } = record;
          const status = await hostCommand(bin, record, "status");
          const parsed = statusSchema.safeParse(status.data);
          if (JSON.stringify(current) !== JSON.stringify(saved) || status.code !== 0 || !parsed.success || parsed.data.target !== "primary" || parsed.data.primaryAgent.bbThread !== record.threadId || parsed.data.targetAgent.bbThread !== record.threadId || parsed.data.primaryAgent.provider !== record.provider || parsed.data.targetAgent.provider !== record.provider) throw new Error("Target changed");
        }
        const result = await hostCommand(bin, record, action);
        bb.realtime.publish(CHANGED, { at: Date.now() });
        return { ok: result.code === 0, message: result.code === 0 ? `${action} done` : `looptender ${action} failed; inspect host status` };
      } catch { return { ok: false, message: "Host control unavailable or target changed. No transfer was performed." }; }
    }
    if (!paths.includes(configPath)) return { ok: false, message: "That loop is not in the plugin's configured list." };
    const result = await run(bin, [action, "-c", configPath]);
    bb.realtime.publish(CHANGED, { at: Date.now() });
    return result.code === 0
      ? { ok: true, message: result.stdout.trim() || `${action} done` }
      : { ok: false, message: `looptender ${action} exited ${result.code}` };
  };

  const setup = createSetupController({
    target: resolveTarget,
    binary: async () => (await configured()).bin,
    records,
    save: async value => { await bb.storage.kv.set("setups.v1", value); },
    guard: async threadId => {
      const result = await allLoops(threadId);
      if (result.loops.length || result.errors.length) throw new Error("Existing or unverified supervision");
    },
    init: (bin, target, name, task, dryRun, withGoal, bounds) => host.call("init", { bin, target, name, task, dryRun, withGoal, ...(bounds ? { bounds } : {}) }, { hostId: target.hostId, signal: AbortSignal.timeout(30_000) }),
    command: hostCommand,
    changed: () => bb.realtime.publish(CHANGED, { at: Date.now() }),
  });
  const transfer = createTransferController({
    target: resolveTarget,
    binary: async () => (await configured()).bin,
    records, intents,
    save: async value => { await bb.storage.kv.set("setups.v1", value); },
    saveIntents: async value => { await bb.storage.kv.set("moves.v1", value); },
    guard: async (threadId, record) => {
      const { paths } = await configured();
      if (paths.includes(record.configPath)) throw new Error("Duplicate manual reference");
      if ((await records()).some(r => r.id !== record.id && r.threadId === threadId) || (await intents()).some(i => i.record.id !== record.id && i.destination.threadId === threadId)) throw new Error("Destination reserved");
      const result = await allLoops(threadId);
      if (result.errors.length || result.loops.some(l => l.managedId !== record.id)) throw new Error("Existing or unverified supervision");
    },
    command: hostCommand,
    transfer: (intent, operation, recoveryId) => host.call("transfer", { bin: intent.bin, record: intent.record, destination: intent.destination, operation, ...(recoveryId ? { recoveryId } : {}) }, { hostId: intent.record.hostId, signal: AbortSignal.timeout(30_000) }),
    changed: () => bb.realtime.publish(CHANGED, { at: Date.now() }),
  });
  // Browser setup requests never carry loop-wide limits; the New thread flow
  // supplies them from its own closed list.
  const setupCall = (input: z.infer<typeof setupInput>, bounds?: Bounds) => serialize(async () => {
    if (input.action === "start" && (await intents()).some(i => i.record.threadId === input.threadId || i.destination.threadId === input.threadId)) return { ok: false, canStart: false, message: "A saved move requires inspection before starting supervision." };
    return setup.call(input, bounds);
  });
  let hookAvailable = false;
  const newThread = createNewThreadController({
    now: () => Date.now(),
    id: () => randomUUID(),
    // An unreadable setting falls back to the mode that starts nothing.
    mode: async () => {
      try { return startModeSchema.catch("manual").parse((await settings.get()).newThreadStart); }
      catch { return "manual"; }
    },
    unavailable: async () => {
      if (!hookAvailable) return "This version of BB does not offer the hook Keep going needs to learn which thread a message created, so it is unavailable here. Supervise this thread still works on an existing thread.";
      const { bin } = await configured();
      const result = await run(bin, ["version"]);
      return result.code === 0 ? null : "The looptender program (installed as keepgoing up to v0.2.0) was not found or did not run. Install it, or set its path in this plugin's settings, then check again.";
    },
    ready: async threadId => {
      try {
        const signal = AbortSignal.timeout(10_000);
        const t = await bb.sdk.threads.get({ threadId, signal });
        if (t.archivedAt !== null || t.deletedAt !== null) return { reason: "This thread was archived or deleted before looptender could set it up. Nothing was created." };
        // A thread still pending has not started its first turn: BB has not
        // delivered the message, and may never.
        if (t.status === "pending" || !t.environmentId) return null;
        const e = await bb.sdk.environments.get({ environmentId: t.environmentId, signal });
        if (e.status !== "ready") return null;
        if (!e.isGitRepo || !e.path) return { reason: "This thread does not work in a Git repository, which looptender needs to tell whether work is progressing. Nothing was created." };
        const target = await resolveTarget(threadId);
        return { project: target.project };
      } catch { return null; }
    },
    capacity: async () => ({ used: (await records()).length, max: MAX_SETUPS }),
    supervision: async threadId => {
      const result = await allLoops(threadId);
      return result.errors.length ? "unverified" : result.loops.length ? "supervised" : "none";
    },
    setup: (input, bounds) => setupCall(input, bounds),
    changed: () => bb.realtime.publish(CHANGED, { at: Date.now() }),
  });
  // The dispatch hook is how BB reports which thread a marked message
  // created. It never waits for anything and always lets the send proceed.
  // The hook is an experimental BB API: if this BB does not have it, or
  // refuses the registration, only the New thread control is lost. Status,
  // Pause, Stop and setup on existing threads do not depend on it.
  try {
    bb.experimental_hooks.on("message.dispatch", ctx => {
      try {
        const s = ctx.experimental_submission;
        newThread.dispatched({
          data: s && s.pluginId === bb.pluginId ? s.data : null,
          threadId: ctx.thread.id, projectId: ctx.thread.projectId, threadCreatedAt: ctx.thread.createdAt,
          attempt: ctx.attempt, initiator: ctx.initiator, senderThreadId: ctx.senderThreadId, parentThreadId: ctx.thread.parentThreadId, sourceThreadId: ctx.thread.sourceThreadId,
          queued: ctx.queuedMessages.length, text: s && s.pluginId === bb.pluginId ? ctx.input.text : "",
          threadStatus: ctx.thread.status,
        });
      } catch { /* a send is never delayed or failed by this plugin */ }
      return { action: "proceed" };
    });
    hookAvailable = true;
  } catch { hookAvailable = false; }
  // Setup waits for the first turn: a running or finished turn is what shows
  // that BB delivered the message.
  bb.events.on("thread.active", ({ thread }) => { void newThread.advance(thread.id); });
  bb.events.on("thread.idle", ({ thread }) => { void newThread.advance(thread.id); });
  bb.events.on("thread.failed", ({ thread }) => { void newThread.advance(thread.id); });
  bb.events.on("thread.archived", ({ thread }) => { newThread.forget(thread.id); });
  bb.events.on("thread.deleted", ({ thread }) => { newThread.forget(thread.id); });
  bb.onDispose(() => { setup.dispose(); transfer.dispose(); newThread.dispose(); });
  bb.rpc.register(rpcContract, {
    transfer: input => serialize(() => transfer.call(input)),
    setup: input => setupCall(input),
    newThread: input => newThread.call(input),
    forThread: ({ threadId }) => allLoops(threadId),
    control: ({ configPath, action, managedId, expectedThreadId }) => action === "resume" ? serialize(() => control(configPath, action, managedId, expectedThreadId)) : control(configPath, action, managedId, expectedThreadId),
  });

  bb.cli.register({
    name: "keepgoing",
    summary: "Show and control the looptender loops this plugin is configured for",
    commands: [
      { name: "status", summary: "List configured loops with their control state and target", usage: "bb keepgoing status" },
      { name: "pause", summary: "Pause a loop by name", usage: "bb keepgoing pause <loop>" },
      { name: "resume", summary: "Resume a loop by name", usage: "bb keepgoing resume <loop>" },
      { name: "stop", summary: "Stop a loop's running session by name", usage: "bb keepgoing stop <loop>" },
    ],
    async run(argv) {
      const [command, name, ...rest] = argv;
      if (command === "status" && name === undefined) {
        const { loops, errors } = await allLoops(null);
        const lines = loops.map((l) => `${l.loop}\t${l.controlState}\ttarget=${l.target}\tlast=${l.lastState ?? "never"}${l.note ? `\t(${l.note})` : ""}`);
        return { exitCode: errors.length > 0 ? 1 : 0, stdout: `${lines.join("\n")}\n`, stderr: errors.join("\n") };
      }
      const parsed = actionSchema.safeParse(command);
      if (!parsed.success || !name || rest.length > 0) return { exitCode: 2, stderr: "Usage: bb keepgoing status | pause <loop> | resume <loop> | stop <loop>" };
      const { loops } = await allLoops(null);
      const matches = loops.filter((l) => l.loop === name);
      if (matches.length > 1) return { exitCode: 1, stderr: "That loop name is ambiguous across hosts/configs; use the thread's controls or the CLI on its host." };
      const loop = matches[0];
      if (!loop) return { exitCode: 1, stderr: `No configured loop named ${name}` };
      const result = await (parsed.data === "resume" ? serialize(() => control(loop.configPath, parsed.data, loop.managedId, loop.managedThreadId)) : control(loop.configPath, parsed.data, loop.managedId, loop.managedThreadId));
      return { exitCode: result.ok ? 0 : 1, stdout: `${result.message}\n` };
    },
  });
}
