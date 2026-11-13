import { createHash, randomUUID } from "node:crypto";
import { z } from "zod";
import { goalDetails } from "./status-details.ts";
import { checkSchema, refusalMessage, refusedReportSchema, setupInput, type Refusal, type SetupTarget, type SetupPlan, type SetupRecord, type SetupResult } from "./setup-contract.ts";
import type { Bounds } from "./new-thread-contract.ts";

export interface SetupDependencies {
  target(threadId: string): Promise<SetupTarget>;
  binary(): Promise<string>;
  records(): Promise<SetupRecord[]>;
  save(records: SetupRecord[]): Promise<void>;
  guard(threadId: string): Promise<void>;
  init(bin: string, target: SetupTarget, name: string, task: string, dryRun: boolean, withGoal: boolean, bounds?: Bounds): Promise<{ plan: SetupPlan } | { refused: Refusal }>;
  command(bin: string, record: SetupRecord, operation: "status" | "doctor" | "start"): Promise<{ code: number; data: unknown }>;
  changed(): void;
}
const agent = z.object({ adapter: z.literal("bb"), bbThread: z.string(), provider: z.string() });
const statusSchema = z.object({
  target: z.literal("primary"), primaryAgent: agent, targetAgent: agent, controlState: z.string(),
  goal: z.unknown().optional(), goalError: z.unknown().optional(),
  redundancy: z.unknown().optional(),
  daemon: z.object({ state: z.string() }).optional(),
});
const doctorSchema = z.object({ schemaVersion: z.literal(1), targetAgent: agent, checks: z.array(checkSchema).max(300) });
const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
const digest = (s: string) => createHash("sha256").update(s).digest("hex");

/** The most setups the plugin keeps a reference to. There is no removal yet. */
export const MAX_SETUPS = 32;

export function createSetupController(d: SetupDependencies) {
  const previews = new Map<string, { target: SetupTarget; name: string; taskHash: string; withGoal: boolean; bounds?: Bounds; bin: string; plan: SetupPlan; expires: number }>();
  let queue: Promise<unknown> = Promise.resolve();
  const serialize = <T>(fn: () => Promise<T>): Promise<T> => {
    const next = queue.then(fn, fn);
    queue = next.catch(() => {});
    return next;
  };
  const sameTarget = async (r: SetupRecord) => {
    const now = await d.target(r.threadId);
    const { id: _id, configPath: _path, loop: _loop, ...saved } = r;
    if (!equal(now, saved)) throw new SetupPolicyError("The thread's environment or provider changed. Setup does not transfer an existing loop.");
  };
  const inspect = async (bin: string, r: SetupRecord): Promise<SetupResult> => {
    await sameTarget(r);
    const status = await d.command(bin, r, "status");
    const s = statusSchema.safeParse(status.data);
    const matches = (a: z.infer<typeof agent>) => a.bbThread === r.threadId && a.provider === r.provider;
    if (status.code !== 0 || !s.success || s.data.redundancy !== undefined || !matches(s.data.primaryAgent) || !matches(s.data.targetAgent)) throw new SetupPolicyError("The saved loop target could not be verified. No start or transfer was performed.");
    const result = await d.command(bin, r, "doctor");
    const report = doctorSchema.safeParse(result.data);
    if (!report.success || !matches(report.data.targetAgent)) return { ok: true, record: r, canStart: false, message: "Setup is saved, but diagnostics could not verify this target. Check host access and retry diagnostics." };
    const checks = report.data.checks;
    const goal = goalDetails(s.data.goal, s.data.goalError);
    const canStart = goal.goalState !== "unavailable" && result.code === 0 && checks.some(c => c.id === "agent.primary.target" && c.status === "ok") && !checks.some(c => c.status === "error") && ["disabled", "enabled"].includes(s.data.controlState) && s.data.daemon?.state !== "responsive";
    return { ok: true, record: r, ...goal, checks, canStart, message: canStart ? "Review the diagnostics, then start background supervision when ready." : "Setup is saved. Existing holds, a responding daemon, or diagnostic failures prevent another start here." };
  };
  const handle = async (raw: z.infer<typeof setupInput>, bounds?: Bounds): Promise<SetupResult> => {
    const input = setupInput.parse(raw);
    const bin = await d.binary();
    const records = await d.records();
    const existing = records.find(r => r.threadId === input.threadId);
    if (input.action === "inspect" || input.action === "start") {
      if (!existing) return { ok: true, message: "Enter a task and preview a new loop for this thread." };
      let result: SetupResult;
      try { result = await inspect(bin, existing); }
      catch { return { ok: false, record: existing, canStart: false, message: "The saved target could not be verified. Refresh diagnostics after checking the host and configuration; no transfer was performed." }; }
      if (input.action === "inspect" || !result.canStart) return result;
      await sameTarget(existing);
      const start = await d.command(bin, existing, "start");
      const startRefused = start.code !== 0 ? refusedReportSchema.safeParse(start.data) : undefined;
      if (startRefused?.success) { d.changed(); return { ...result, ok: false, canStart: false, started: false, refused: startRefused.data.refusal, message: refusalMessage(startRefused.data.refusal, "start") }; }
      d.changed();
      return { ...result, canStart: false, ok: start.code === 0, started: start.code === 0, message: start.code === 0 ? "Background supervision started. Existing timers were not changed." : "Start could not be confirmed. Check loop status before retrying; no holds were cleared." };
    }
    if (existing) throw new SetupPolicyError("This thread already has a saved setup. Use its diagnostics and controls.");
    await d.guard(input.threadId);
    const target = await d.target(input.threadId);
    const task = input.task ?? "";
    const withGoal = input.withGoal ?? false;
    if (!task.trim() || Buffer.byteLength(task) > 262144) throw new SetupPolicyError("Enter a task of at most 256 KiB.");
    if (input.action === "preview") {
      if (!input.name) throw new SetupPolicyError("Enter a loop name using lowercase letters, digits, and hyphens.");
      for (const [key, p] of previews) if (p.expires < Date.now()) previews.delete(key);
      if (previews.size >= 64) throw new SetupPolicyError("Too many pending previews. Wait five minutes and retry.");
      const previewed = await d.init(bin, target, input.name, task, true, withGoal, bounds);
      if ("refused" in previewed) return { ok: false, refused: previewed.refused, message: refusalMessage(previewed.refused, "init") };
      const { plan } = previewed;
      const ticket = randomUUID();
      previews.set(ticket, { target, name: input.name, taskHash: digest(task), withGoal, bounds, bin, plan, expires: Date.now() + 300_000 });
      return { ok: true, ticket, plan, message: "Preview only. Creating this setup leaves supervision disabled; fallbacks and automatic handoffs are off." };
    }
    const preview = previews.get(input.ticket ?? "");
    previews.delete(input.ticket ?? ""); // one attempt, including failure; never replay a creation
    if (!preview || preview.expires < Date.now() || preview.bin !== bin || preview.withGoal !== withGoal || !equal(preview.bounds, bounds) || preview.taskHash !== digest(task) || !equal(preview.target, target)) throw new SetupPolicyError("The preview expired or its task, goal choice, target, or binary changed. Preview again.");
    if (records.length >= MAX_SETUPS) throw new SetupPolicyError(`The plugin already has ${MAX_SETUPS} saved setups; manage existing loops before adding another.`);
    const fresh = await d.init(bin, target, preview.name, task, true, withGoal, bounds);
    if ("refused" in fresh) return { ok: false, refused: fresh.refused, message: refusalMessage(fresh.refused, "init") };
    if (!equal(fresh.plan, preview.plan)) throw new SetupPolicyError("The resolved setup changed. Preview again before creating it.");
    const created = await d.init(bin, target, preview.name, task, false, withGoal, bounds);
    // Refused at creation, after a preview that passed: keepgoing checks
    // before it writes anything, so nothing was created.
    if ("refused" in created) return { ok: false, refused: created.refused, message: refusalMessage(created.refused, "init") };
    if (!equal(created.plan, preview.plan)) throw new SetupPolicyError("Setup result changed. A disabled configuration may exist; inspect the destination before retrying.");
    const record = { ...target, id: randomUUID(), configPath: created.plan.configPath, loop: created.plan.loop };
    try { await d.save([...records, record]); }
    catch { return { ok: false, record, message: "A disabled setup was created, but the plugin could not save its reference. Keep the displayed config path for recovery; do not recreate or start it automatically." }; }
    d.changed();
    try { return await inspect(bin, record); }
    catch { return { ok: true, record, canStart: false, message: "Setup was saved, but target diagnostics are unavailable. Retry diagnostics before starting." }; }
  };
  return {
    // Serialize mutations and previews so concurrent clients cannot create two
    // loops for one thread or overwrite the stored record list.
    // `bounds` are loop-wide limits chosen by a server-side caller. They are
    // not part of the browser's input, and a preview made with one set
    // cannot be created with another.
    call: (input: z.infer<typeof setupInput>, bounds?: Bounds): Promise<SetupResult> => serialize(async () => {
      try { return await handle(input, bounds); }
      catch (error) {
        // Dependency/SDK errors can contain paths, command arguments, or task
        // input. Only our fixed policy messages are returned by safe wrappers.
        return { ok: false, message: error instanceof SetupPolicyError ? error.message : "Setup could not complete. Refresh diagnostics or preview again; check the host and keepgoing version." + (input.withGoal ? " Goal creation needs init --with-goal support (after v0.1.4); update it or turn off that choice and preview again." : "") + " No automatic retry or transfer was performed." };
      }
    }),
    dispose: () => previews.clear(),
  };
}

class SetupPolicyError extends Error {}
