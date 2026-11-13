import { execFile } from "node:child_process";
import { basename, dirname, isAbsolute, join } from "node:path";
import { realpath } from "node:fs/promises";
import { z } from "zod";
import { goalSummarySchema } from "./status-details.ts";
import { planSchema, type SetupTarget, type SetupRecord } from "./setup-contract.ts";
import { durationMs, type Bounds } from "./new-thread-contract.ts";

export type Execute = (bin: string, args: string[], input: string, threadId: string, signal?: AbortSignal) => Promise<{ code: number; data: unknown }>;
// Bounded output and duration; task text travels only through stdin. Neither
// command output nor child-process errors are logged or included in errors.
export const execute: Execute = (bin, args, input, threadId, signal) => new Promise((resolve) => {
  const child = execFile(bin, args, { timeout: 20_000, maxBuffer: 1 << 20, signal, env: { ...process.env, BB_THREAD_ID: threadId } }, (error, stdout) => {
    let data: unknown = null;
    try { data = JSON.parse(String(stdout)); } catch { /* text commands need no output */ }
    resolve({ code: error ? 1 : 0, data });
  });
  child.stdin?.on("error", () => { /* early exit/EPIPE is reported by callback */ });
  child.stdin?.end(input);
});
const cliPlan = z.object({
  schemaVersion: z.literal(1), outcome: z.enum(["preview", "created"]),
  goal: z.unknown().optional(), taskPath: z.string().optional(),
  loop: z.string(), project: z.string(), configPath: z.string(), stateDir: z.string(),
  primary: z.object({ agent: z.object({ adapter: z.literal("bb"), bbThread: z.string(), provider: z.string() }) }),
  fallbacks: z.array(z.unknown()).length(0),
  limits: planSchema.shape.limits.extend({ failoverOn: z.array(z.unknown()).length(0) }),
});
export async function setupOnHost(input: { bin: string; target: SetupTarget; name: string; task: string; dryRun: boolean; withGoal?: boolean; bounds?: Bounds }, signal?: AbortSignal, run: Execute = execute) {
  const { bin, target, name, task, dryRun, withGoal = false, bounds } = input;
  const project = await realpath(target.project);
  const args = ["init", "--non-interactive", "--json", "--name", name, "--agent", "bb", "--thread", target.threadId, "--provider", target.provider, "--project", project, "--task-file", "-"];
  if (withGoal) args.push("--with-goal");
  // Loop-wide bounds are fixed values from a closed list, never free text.
  if (bounds) args.push("--max-sends", String(bounds.maxSends), "--no-progress-timeout", bounds.noProgressTimeout);
  if (dryRun) args.push("--dry-run");
  const result = await run(bin, args, task, target.threadId, signal);
  const parsed = cliPlan.safeParse(result.data);
  if (result.code !== 0 || !parsed.success) throw new Error("Setup failed or returned an unsupported result. " + (withGoal ? "Goal setup needs a CLI with init --with-goal (after v0.1.4); update the CLI or explicitly turn off goal creation and preview again. " : "") + "Check the setup destination; existing files are never overwritten.");
  const p = parsed.data;
  if (!isAbsolute(p.configPath) || basename(p.configPath) !== "keepgoing.yaml" || p.stateDir !== join(dirname(p.configPath), "state")) throw new Error("Setup paths could not be verified.");
  if (p.outcome !== (dryRun ? "preview" : "created") || p.loop !== name || p.project !== project || p.primary.agent.bbThread !== target.threadId || p.primary.agent.provider !== target.provider) throw new Error("Setup returned a different target. No supervisor was started.");
  if (bounds && (p.limits.maxSends !== bounds.maxSends || durationMs(p.limits.noProgressTimeout ?? "") !== durationMs(bounds.noProgressTimeout))) throw new Error("Setup did not report the requested loop-wide limits. No supervisor was started.");
  if (withGoal) {
    const goal = z.object({ promptContext: z.literal(true), titleSource: z.literal("loop_name"), specificationSource: z.literal("task_snapshot"), record: goalSummarySchema.optional() }).safeParse(p.goal);
    if (!goal.success || p.taskPath !== join(dirname(p.configPath), "task.md") || (dryRun ? goal.data.record !== undefined : goal.data.record?.revision !== 1)) throw new Error("Goal setup could not be verified. A disabled setup may exist; inspect the destination before retrying.");
  } else if (p.goal !== undefined) throw new Error("Setup unexpectedly included a goal. No supervisor was started.");
  return { plan: planSchema.parse({ ...p, ...(withGoal ? { withGoal: true } : {}), threadId: target.threadId, provider: target.provider }) };
}
export function commandOnHost(input: { bin: string; record: SetupRecord; operation: "status" | "doctor" | "start" | "pause" | "resume" | "stop" }, signal?: AbortSignal, run: Execute = execute) {
  const { bin, record, operation } = input;
  const args: string[] = [operation];
  if (operation === "status" || operation === "doctor") args.push("--json");
  if (operation === "start") args.push("--background");
  args.push("-c", record.configPath);
  return run(bin, args, "", record.threadId, signal);
}
