import { z } from "zod";
import { recordSchema, targetSchema } from "./setup-contract.ts";
import { checkSchema } from "./status-details.ts";
const registryId = z.string().regex(/^loop_[0-9a-f]{32}$/);
export const attachInput = z.object({
  action: z.enum(["list", "preview", "attach", "inspect"]), threadId: z.string().min(1).max(200),
  registryId: registryId.optional(), ticket: z.string().max(200).optional(),
}).strict();
export const attachmentState = z.object({
  controlState: z.string(), daemonState: z.string(), failedChecks: z.number().int().nonnegative(),
  lastCheck: checkSchema.nullable(),
});
export type AttachmentState = z.infer<typeof attachmentState>;
export function attachmentEvidence(s: AttachmentState): string {
  const prefix = `Controls: ${s.controlState}. Daemon: ${s.daemonState} (response only). `;
  if (s.failedChecks > 0 || s.lastCheck?.outcome === "failed" || s.lastCheck?.outcome === "error") return prefix + "Checks are failing; operational supervision is not established. Inspect host diagnostics.";
  if (!s.lastCheck) return prefix + "No check is recorded; operational supervision is not established.";
  return prefix + `Last recorded check: ${s.lastCheck.action}, ${s.lastCheck.outcome}${s.lastCheck.dryRun ? " (dry run)" : ""}. This snapshot does not certify current supervision or provider health.`;
}
export const attachResult = z.object({
  ok: z.boolean(), message: z.string(), attached: z.boolean().optional(), ticket: z.string().optional(),
  candidates: z.array(z.object({ registryId, loop: z.string() })).max(128).optional(),
  record: recordSchema.optional(), state: attachmentState.optional(),
});
export type AttachResult = z.infer<typeof attachResult>;
export const registryEntry = z.object({ id: registryId, loop: z.string().regex(/^[a-z0-9][a-z0-9-]{0,63}$/), configPath: z.string().min(1).max(4096), stateDir: z.string().min(1).max(4096) });
export const attachSnapshot = z.object({
  entry: registryEntry, digest: z.string().regex(/^[0-9a-f]{64}$/),
  generation: z.number().int().positive().safe(), state: attachmentState,
});
export type AttachSnapshot = z.infer<typeof attachSnapshot>;
export const attachHostInput = z.object({ bin: z.string().min(1).max(4096), target: targetSchema, registryId: registryId.optional() }).strict();
export const attachHostResult = z.object({ candidates: attachResult.shape.candidates, snapshot: attachSnapshot.optional() });
