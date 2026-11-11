import { z } from "zod";

// Only diagnostic metadata crosses this boundary. No task text, paths,
// provider output, goal specifications or pending-transfer records are exposed.
export const checkSchema = z.object({
  at: z.string(), target: z.string(), action: z.string(), outcome: z.string(), dryRun: z.boolean().optional(),
});
export const runtimeSchema = z.object({
  state: z.string(), source: z.string(), freshness: z.string(),
});
export const daemonSchema = z.object({
  state: z.string(), phase: z.string().optional(), observedAt: z.string().optional(),
  nextCheckAt: z.string().optional(), dryRun: z.boolean().optional(), binaryComparison: z.string().optional(),
});
export const detailsSchema = z.object({
  readAt: z.string(), completed: z.boolean(),
  targetThread: z.string().nullable(), targetAdapter: z.string(), targetConfigured: z.boolean().nullable(),
  runtime: runtimeSchema.nullable(), daemon: daemonSchema.nullable(), lastCheck: checkSchema.nullable(),
});
export type StatusDetails = z.infer<typeof detailsSchema>;

export function dateLabel(value: string | null | undefined): string {
  if (!value || value.startsWith("0001-")) return "unavailable";
  const time = new Date(value);
  return Number.isNaN(time.getTime()) ? "unavailable" : time.toLocaleString();
}

export function diagnosticLines(d: StatusDetails | undefined, target: string, supervising: boolean): string[] {
  if (!d) return ["Detailed status unavailable from this build"];
  const lines = [
    `Status read: ${dateLabel(d.readAt)} (snapshot)`,
    `Task completion: ${d.completed ? "declared; not independently verified" : "not declared"}`,
    `Current target: ${d.targetThread ?? d.targetAdapter} (${target})${supervising ? "; this thread" : "; not this thread"}${d.targetConfigured === false ? "; missing or disabled in config" : ""}`,
    d.runtime ? `Check evidence: ${d.runtime.state} · ${d.runtime.freshness} · source ${d.runtime.source}` : "Check evidence: unavailable from this build",
  ];
  const daemon = d.daemon;
  if (daemon?.state === "responsive") {
    lines.push(`Daemon answered: ${dateLabel(daemon.observedAt)} · ${daemon.phase ?? "phase unavailable"}${daemon.dryRun ? " · dry run" : ""} · build comparison ${daemon.binaryComparison ?? "unknown"}`);
    lines.push(`Next daemon check: ${daemon.nextCheckAt ? dateLabel(daemon.nextCheckAt) + " (expected, not guaranteed)" : "not scheduled yet"}`);
  } else {
    lines.push(`Daemon: ${daemon?.state ?? "unavailable from this build"}; running supervisor not confirmed`);
  }
  lines.push("External scheduler health: not inspected; a daemon reply does not establish agent health");
  const check = d.lastCheck;
  lines.push(check
    ? `Last recorded action: ${check.action} · ${check.outcome}${check.dryRun ? " · dry run" : ""} · target ${check.target} · ${dateLabel(check.at)}`
    : "Last recorded action: unavailable");
  return lines;
}
