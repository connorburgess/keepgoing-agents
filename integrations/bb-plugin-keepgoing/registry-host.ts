import { z } from "zod";
import type { Execute } from "./setup-host.ts";
import type { Publication } from "./registry-contract.ts";

const report = z.object({ schemaVersion: z.literal(1), registration: z.object({
  source: z.literal("cli_registry"), state: z.enum(["preview", "registered"]),
  binding: z.string().regex(/^[0-9a-f]{64}$/), id: z.string().regex(/^loop_[0-9a-f]{32}$/).optional(),
}) });
/** Explicit lifecycle publication only. Never invoked by status or page reads.
 * The lifecycle result is already committed: every publication failure remains
 * separate and must not cause a lifecycle replay or control rollback.
 */
export async function publishReference(bin: string, configPath: string, threadId: string, provider: string, signal: AbortSignal | undefined, run: Execute): Promise<Publication> {
  const unavailable: Publication = { state: "repair_needed", source: "cli_registry" };
  const args = ["registry", "reconcile", "-c", configPath, "--thread", threadId, "--provider", provider, "--json"];
  try {
    if (signal?.aborted) return unavailable;
    const preview = await run(bin, args, "", threadId, signal);
    const p = report.safeParse(preview.data);
    if (preview.code !== 0 || !p.success || p.data.registration.state !== "preview" || p.data.registration.id !== undefined || signal?.aborted) return unavailable;
    const apply = await run(bin, [...args, "--binding", p.data.registration.binding], "", threadId, signal);
    const a = report.safeParse(apply.data);
    if (apply.code !== 0 || !a.success || a.data.registration.state !== "registered" || !a.data.registration.id || a.data.registration.binding !== p.data.registration.binding || signal?.aborted) return unavailable;
    return { state: "registered", source: "cli_registry", id: a.data.registration.id };
  } catch { return unavailable; }
}
