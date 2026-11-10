import { defineRpcContract } from "@get-bb/plugin-sdk";
import { z } from "zod";

const text = z.string().min(1).max(4096);
export const targetSchema = z.object({
  threadId: text, environmentId: text, projectId: text, hostId: text, project: text, provider: text,
});
export type SetupTarget = z.infer<typeof targetSchema>;
export const recordSchema = targetSchema.extend({ id: text, configPath: text, loop: text });
export type SetupRecord = z.infer<typeof recordSchema>;
export const planSchema = z.object({
  loop: text, project: text, configPath: text, stateDir: text,
  threadId: text, provider: text,
  limits: z.object({ wakeEvery: text, stallAfter: text, interval: text, backoff: text, maxNudges: z.number().int().positive(), maxReprompts: z.number().int().positive() }),
});
export type SetupPlan = z.infer<typeof planSchema>;
export const setupInput = z.object({
  action: z.enum(["preview", "create", "inspect", "start"]), threadId: text,
  name: z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/).optional(),
  task: z.string().max(262144).optional(), ticket: text.optional(),
});
export const checkSchema = z.object({ id: text, status: z.enum(["ok", "warning", "error", "unavailable"]), message: text, fix: z.string().max(4096).optional() });
export const setupResult = z.object({
  ok: z.boolean(), message: z.string(), ticket: z.string().optional(), plan: planSchema.optional(),
  record: recordSchema.optional(), checks: z.array(checkSchema).optional(), canStart: z.boolean().optional(),
});
export type SetupResult = z.infer<typeof setupResult>;

// Only the server calls the host entry. Browser input never selects a binary,
// host, project path, config path, provider, or arbitrary command arguments.
export const hostContract = defineRpcContract({
  init: {
    input: z.object({ bin: text, target: targetSchema, name: text, task: z.string().max(262144), dryRun: z.boolean() }),
    output: z.object({ plan: planSchema }),
  },
  command: {
    input: z.object({ bin: text, record: recordSchema, operation: z.enum(["status", "doctor", "start", "pause", "resume", "stop"]) }),
    output: z.object({ code: z.number().int(), data: z.unknown() }),
  },
});
