import { z } from "zod";
import { goalSummarySchema, goalStateSchema } from "./status-details.ts";

const text = z.string().min(1).max(4096);
export const targetSchema = z.object({
  threadId: text, environmentId: text, projectId: text, hostId: text, project: text, provider: text,
});
export type SetupTarget = z.infer<typeof targetSchema>;
// `startJson` and `startJsonBin` record that the preview this setup was created
// from, run by that binary, advertised `start --json`. Absent on older records.
export const recordSchema = targetSchema.extend({ id: text, configPath: text, loop: text, startJson: z.literal(true).optional(), startJsonBin: text.optional() });
export type SetupRecord = z.infer<typeof recordSchema>;
export const planSchema = z.object({
  withGoal: z.literal(true).optional(),
  loop: text, project: text, configPath: text, stateDir: text,
  threadId: text, provider: text,
  // Advertised by a CLI whose `start` accepts --json and reports a refusal in it.
  startJson: z.literal(true).optional(),
  limits: z.object({ wakeEvery: text, stallAfter: text, interval: text, backoff: text, maxNudges: z.number().int().positive(), maxReprompts: z.number().int().positive(), maxSends: z.number().int().positive().optional(), noProgressTimeout: text.optional() }),
});
export type SetupPlan = z.infer<typeof planSchema>;
export const setupInput = z.object({
  action: z.enum(["preview", "create", "inspect", "start"]), threadId: text,
  withGoal: z.boolean().optional(),
  name: z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/).optional(),
  task: z.string().max(262144).optional(), ticket: text.optional(),
});
/**
 * A refusal keepgoing reports in a structured form. The plugin shows a fixed
 * sentence for each known code, with the other loop's name and nothing else:
 * no path, no account, no text from the CLI. The name must be a loop name.
 * A refusal is never overridden from the plugin.
 */
export const refusalSchema = z.object({
  code: z.literal("second_loop_on_session"),
  otherLoop: z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/),
});
export type Refusal = z.infer<typeof refusalSchema>;
/** What `keepgoing init --json` and `start` print on stdout when they refuse. */
export const refusedReportSchema = z.object({ schemaVersion: z.literal(1), outcome: z.literal("refused"), refusal: refusalSchema });
export function refusalMessage(r: Refusal, command: "init" | "start"): string {
  return `looptender refused to ${command === "init" ? "set up" : "start"} this loop: loop ${r.otherLoop} already supervises this thread's session, and two loops on one session would both prompt it. ${command === "init" ? "Nothing was created." : "Nothing was started."} To have this thread supervised here instead, pause or disable ${r.otherLoop} first.`;
}
export const checkSchema = z.object({ id: text, status: z.enum(["ok", "warning", "error", "unavailable"]), message: text, fix: z.string().max(4096).optional() });
export const setupResult = z.object({
  ok: z.boolean(), message: z.string(), ticket: z.string().optional(), plan: planSchema.optional(),
  record: recordSchema.optional(), goal: goalSummarySchema.nullable().optional(), goalState: goalStateSchema.optional(), checks: z.array(checkSchema).optional(), canStart: z.boolean().optional(),
  // True only when this call ran the start command and it reported success.
  started: z.boolean().optional(),
  // Set when keepgoing itself refused, with a structured reason.
  refused: refusalSchema.optional(),
});
export type SetupResult = z.infer<typeof setupResult>;
