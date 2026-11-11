import { z } from "zod";
import { recordSchema, targetSchema } from "./setup-contract.ts";

const id = z.string().min(1).max(200);
export const transferInput = z.object({
  action: z.enum(["list", "preview", "apply", "inspect", "recover"]),
  threadId: id, recordId: id.optional(), ticket: id.optional(),
});
export const holdsSchema = z.object({
  enabled: z.boolean(), paused: z.boolean(), stop: z.boolean(), awaitingUser: z.boolean(), completed: z.boolean().default(false),
});
export const moveSummarySchema = z.object({
  loop: z.string().max(4096), from: id, to: id, provider: id,
  holds: holdsSchema, owner: id.optional(),
  goal: z.object({ id, revision: z.number().int().positive(), state: z.literal("recorded") }).optional(),
});
export const transferResult = z.object({
  ok: z.boolean(), message: z.string(), ticket: id.optional(),
  choices: z.array(z.object({ id, loop: z.string(), from: id, pending: z.boolean() })).max(32).optional(),
  summary: moveSummarySchema.optional(), canRecover: z.boolean().optional(), moved: z.boolean().optional(),
});
export type TransferResult = z.infer<typeof transferResult>;
// Private plugin recovery references; no prompts, control notes or CLI output.
export const moveIntentSchema = z.object({
  record: recordSchema, destination: targetSchema, bin: z.string().min(1).max(4096),
});
export type MoveIntent = z.infer<typeof moveIntentSchema>;
export const transferHostInput = z.object({
  bin: z.string().min(1).max(4096), record: recordSchema, destination: targetSchema,
  operation: z.enum(["preview", "apply", "recover"]),
  recoveryId: z.string().regex(/^move_[0-9a-f]{32}$/).optional(),
});
