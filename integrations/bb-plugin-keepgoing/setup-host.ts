import { execFile } from "node:child_process";
import { basename, dirname, isAbsolute, join } from "node:path";
import { realpath } from "node:fs/promises";
import { z } from "zod";
import { withClientFlag } from "./client-flag.ts";
import { goalSummarySchema } from "./status-details.ts";
import { planSchema, refusedReportSchema, type Refusal, type SetupTarget, type SetupRecord } from "./setup-contract.ts";
import { durationMs, type Bounds } from "./new-thread-contract.ts";

// code is the real exit code (1 when there is none); emptyStdout and
// timedOut are set by execute, so a refusal of --client can be told apart.
export type Execute = (bin: string, args: string[], input: string, threadId: string, signal?: AbortSignal) => Promise<{ code: number; data: unknown; emptyStdout?: boolean; timedOut?: boolean }>;
// Bounded output and duration; task text travels only through stdin. Neither
// command output nor child-process errors are logged or included in errors.
export const execute: Execute = (bin, args, input, threadId, signal) => new Promise((resolve) => {
  const child = execFile(bin, args, { timeout: 20_000, maxBuffer: 1 << 20, signal, env: { ...process.env, BB_THREAD_ID: threadId } }, (error, stdout) => {
    let data: unknown = null;
    try { data = JSON.parse(String(stdout)); } catch { /* text commands need no output */ }
    const e = error as { code?: unknown; killed?: boolean } | null;
    const code = e ? (typeof e.code === "number" && e.code !== 0 ? e.code : 1) : 0;
    resolve({ code, data, emptyStdout: String(stdout) === "", timedOut: !!e && e.killed === true });
  });
  child.stdin?.on("error", () => { /* early exit/EPIPE is reported by callback */ });
  child.stdin?.end(input);
});
const cliPlan = z.object({
  schemaVersion: z.literal(1), outcome: z.enum(["preview", "created"]),
  goal: z.unknown().optional(), taskPath: z.string().optional(),
  // Only the literal true is believed; anything else means today's start.
  startJson: z.unknown().optional(),
  loop: z.string(), project: z.string(), configPath: z.string(), stateDir: z.string(),
  primary: z.object({ agent: z.object({ adapter: z.literal("bb"), bbThread: z.string(), provider: z.string() }) }),
  fallbacks: z.array(z.unknown()).length(0),
  limits: planSchema.shape.limits.extend({ failoverOn: z.array(z.unknown()).length(0) }),
});
// A setup that follows the standard route list: the routes that apply now,
// and handoff on a rate limit. Only id, model and group are read from each.
const followingPlan = cliPlan.extend({
  routes: z.literal("standard"),
  fallbacks: z.array(z.object({ id: z.string().min(1).max(64), target: z.object({ agent: z.object({ model: z.string().max(256).optional(), group: z.string().max(256).optional() }) }) })).min(1).max(64),
  limits: planSchema.shape.limits.extend({ failoverOn: z.tuple([z.literal("rate_limit")]) }),
});
// What `routes list --json` says about the standard list: its state and,
// for each route, only what decides whether it applies to a new loop on
// this thread. `can` is required because v0.2.0 lists routes without it,
// and its init cannot follow the list. A build from between the commands
// that change the list and init --routes standard reports `can` too; its
// init refuses the flag, and the checked preview then fails closed.
const routeLists = z.object({
  schemaVersion: z.literal(1), kind: z.literal("route_lists"),
  standard: z.object({
    state: z.enum(["ok", "missing", "unreadable"]), can: z.array(z.string()),
    routes: z.array(z.object({ provider: z.string(), enter: z.string(), group: z.string(), disabled: z.boolean() }).passthrough()),
  }),
});
/**
 * Whether a standard route could apply to a loop set up here: this thread
 * on another model, which is a switched-on same-conversation route on the
 * thread's provider that draws on another usage limit than the loop's own
 * (setup labels it with the provider). The preview, which resolves the
 * loop for real, still decides; this only keeps the choice from being
 * offered when nothing could apply.
 */
function couldApply(r: { provider: string; enter: string; group: string; disabled: boolean }, target: SetupTarget) {
  return !r.disabled && r.enter === "same_conversation" && r.provider === target.provider && r.group !== target.provider;
}
/**
 * Whether the host's looptender has a usable standard route list with
 * routes. An older CLI has no `routes list`, and v0.2.0 cannot follow the
 * list: any refusal or other shape means no, and setup is as it was.
 */
export async function standardRoutesOnHost(input: { bin: string; target: SetupTarget }, signal?: AbortSignal, run: Execute = execute): Promise<{ available: boolean }> {
  try {
    const result = await run(input.bin, ["routes", "list", "--json"], "", input.target.threadId, signal);
    const parsed = routeLists.safeParse(result.data);
    return { available: result.code === 0 && parsed.success && parsed.data.standard.state === "ok" && parsed.data.standard.routes.some(r => couldApply(r, input.target)) };
  } catch {
    return { available: false };
  }
}
export async function setupOnHost(input: { bin: string; target: SetupTarget; name: string; task: string; dryRun: boolean; withGoal?: boolean; bounds?: Bounds; followStandard?: true }, signal?: AbortSignal, run: Execute = execute): Promise<{ plan: z.infer<typeof planSchema> } | { refused: Refusal } | { noRoutes: true }> {
  const { bin, target, name, task, dryRun, withGoal = false, bounds, followStandard } = input;
  const project = await realpath(target.project);
  const args = ["init", "--non-interactive", "--json", "--name", name, "--agent", "bb", "--thread", target.threadId, "--provider", target.provider, "--project", project, "--task-file", "-"];
  if (withGoal) args.push("--with-goal");
  // Loop-wide bounds are fixed values from a closed list, never free text.
  if (bounds) args.push("--max-sends", String(bounds.maxSends), "--no-progress-timeout", bounds.noProgressTimeout);
  if (followStandard) args.push("--routes", "standard");
  if (dryRun) args.push("--dry-run");
  const result = await run(bin, args, task, target.threadId, signal);
  // keepgoing's own refusal, when it reports one in a structured form. Only
  // the code and the other loop's name cross back; never its stderr.
  const refused = result.code !== 0 ? refusedReportSchema.safeParse(result.data) : undefined;
  if (refused?.success) return { refused: refused.data.refusal };
  // Following the list, but none of its routes applies to this thread: said
  // plainly, not as a setup failure.
  if (followStandard && result.code === 0 && followingPlan.extend({ fallbacks: z.array(z.unknown()).length(0) }).safeParse(result.data).success) return { noRoutes: true };
  const following = followStandard ? followingPlan.safeParse(result.data) : undefined;
  const parsed = following ?? cliPlan.safeParse(result.data);
  if (result.code !== 0 || !parsed.success) throw new Error("Setup failed or returned an unsupported result. " + (withGoal ? "Goal setup needs a CLI with init --with-goal (after v0.1.4); update the CLI or explicitly turn off goal creation and preview again. " : "") + "Check the setup destination; existing files are never overwritten.");
  const p = parsed.data;
  if (!isAbsolute(p.configPath) || basename(p.configPath) !== "keepgoing.yaml" || p.stateDir !== join(dirname(p.configPath), "state")) throw new Error("Setup paths could not be verified.");
  if (p.outcome !== (dryRun ? "preview" : "created") || p.loop !== name || p.project !== project || p.primary.agent.bbThread !== target.threadId || p.primary.agent.provider !== target.provider) throw new Error("Setup returned a different target. No supervisor was started.");
  if (bounds && (p.limits.maxSends !== bounds.maxSends || durationMs(p.limits.noProgressTimeout ?? "") !== durationMs(bounds.noProgressTimeout))) throw new Error("Setup did not report the requested loop-wide limits. No supervisor was started.");
  if (withGoal) {
    const goal = z.object({ promptContext: z.literal(true), titleSource: z.literal("loop_name"), specificationSource: z.literal("task_snapshot"), record: goalSummarySchema.optional() }).safeParse(p.goal);
    if (!goal.success || p.taskPath !== join(dirname(p.configPath), "task.md") || (dryRun ? goal.data.record !== undefined : goal.data.record?.revision !== 1)) throw new Error("Goal setup could not be verified. A disabled setup may exist; inspect the destination before retrying.");
  } else if (p.goal !== undefined) throw new Error("Setup unexpectedly included a goal. No supervisor was started.");
  const { startJson, fallbacks, routes: _routes, ...plan } = p as typeof p & { routes?: string };
  const routes = following?.success ? { list: "standard" as const, handoffOn: "rate_limit" as const, apply: following.data.fallbacks.map(f => ({ id: f.id, model: f.target.agent.model ?? "", group: f.target.agent.group ?? "" })) } : undefined;
  void fallbacks;
  return { plan: planSchema.parse({ ...plan, ...(startJson === true ? { startJson: true } : {}), ...(withGoal ? { withGoal: true } : {}), ...(routes ? { routes } : {}), threadId: target.threadId, provider: target.provider }) };
}
export function commandOnHost(input: { bin: string; record: SetupRecord; operation: "status" | "doctor" | "start" | "pause" | "resume" | "stop"; json?: true }, signal?: AbortSignal, run: Execute = execute) {
  const { bin, record, operation } = input;
  const args: string[] = [operation];
  if (operation === "status" || operation === "doctor") args.push("--json");
  if (operation === "start") args.push("--background");
  // Passed only when the CLI advertised it in the setup preview; never guessed.
  if (operation === "start" && input.json === true) args.push("--json");
  if (operation === "status") {
    return withClientFlag(bin, (extra) => run(bin, [...args, ...extra, "-c", record.configPath], "", record.threadId, signal), (r) => ({ code: r.code, emptyStdout: r.emptyStdout ?? false, timedOut: r.timedOut ?? false }));
  }
  args.push("-c", record.configPath);
  return run(bin, args, "", record.threadId, signal);
}
