import { z } from "zod";
import { goalSummarySchema, goalStateSchema } from "./status-details.ts";

const text = z.string().min(1).max(4096);
export const targetSchema = z.object({
  threadId: text, environmentId: text, projectId: text, hostId: text, project: text, provider: text,
});
export type SetupTarget = z.infer<typeof targetSchema>;
export const recordSchema = targetSchema.extend({ id: text, configPath: text, loop: text });
export type SetupRecord = z.infer<typeof recordSchema>;
export const planSchema = z.object({
  withGoal: z.literal(true).optional(),
  loop: text, project: text, configPath: text, stateDir: text,
  threadId: text, provider: text,
  limits: z.object({ wakeEvery: text, stallAfter: text, interval: text, backoff: text, maxNudges: z.number().int().positive(), maxReprompts: z.number().int().positive(), maxSends: z.number().int().positive().optional(), noProgressTimeout: text.optional() }),
});
export type SetupPlan = z.infer<typeof planSchema>;
export const setupInput = z.object({
  action: z.enum(["preview", "create", "inspect", "start"]), threadId: text,
  withGoal: z.boolean().optional(),
  name: z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/).optional(),
  task: z.string().max(262144).optional(), ticket: text.optional(),
});
export const checkSchema = z.object({ id: text, status: z.enum(["ok", "warning", "error", "unavailable"]), message: text, fix: z.string().max(4096).optional() });
export const setupResult = z.object({
  ok: z.boolean(), message: z.string(), ticket: z.string().optional(), plan: planSchema.optional(),
  record: recordSchema.optional(), goal: goalSummarySchema.nullable().optional(), goalState: goalStateSchema.optional(), checks: z.array(checkSchema).optional(), canStart: z.boolean().optional(),
  // True only when this call ran the start command and it reported success.
  started: z.boolean().optional(),
});
export type SetupResult = z.infer<typeof setupResult>;
