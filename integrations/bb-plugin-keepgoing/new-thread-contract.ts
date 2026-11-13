// Contract for turning keepgoing on from BB's New thread screen. The task is
// the thread's first message, which reaches the server through BB's dispatch
// hook: it never travels through this contract and is never stored.
import { z } from "zod";

/** Loop-wide bounds for a loop created from the New thread screen, in both start modes. Change them here only. */
export const DEFAULT_BOUNDS = { maxSends: 40, noProgressTimeout: "24h" } as const;
export const TIMEOUT_CHOICES = ["2h", "6h", "12h", "24h", "72h"] as const;
export const boundsSchema = z.object({
  maxSends: z.number().int().min(1).max(1000),
  noProgressTimeout: z.enum(TIMEOUT_CHOICES),
});
export type Bounds = z.infer<typeof boundsSchema>;
export const startModeSchema = z.enum(["manual", "automatic"]);
export type StartMode = z.infer<typeof startModeSchema>;

const id = z.string().min(1).max(200);
export const newThreadInput = z.discriminatedUnion("action", [
  z.object({ action: z.literal("check") }),
  z.object({ action: z.literal("arm"), draftKey: id, projectId: id, bounds: boundsSchema }),
  z.object({ action: z.literal("cancel"), intent: id }),
  z.object({ action: z.literal("outcome"), threadId: id }),
]);
export type NewThreadInput = z.infer<typeof newThreadInput>;

/** What the person is told about the loop created for a thread. Holds no task text and no path. */
export const outcomeSchema = z.object({
  state: z.enum(["none", "waiting", "ready", "failed"]),
  message: z.string().max(1000),
  loop: z.string().max(100).optional(),
  bounds: boundsSchema.optional(),
  cadence: z.string().max(400).optional(),
  startedAutomatically: z.boolean().optional(),
});
export type Outcome = z.infer<typeof outcomeSchema>;
export const newThreadResult = z.object({
  ok: z.boolean(), message: z.string().max(1000),
  mode: startModeSchema.optional(),
  intent: z.string().max(200).optional(), expiresAt: z.number().optional(),
  outcome: outcomeSchema.optional(),
});
export type NewThreadResult = z.infer<typeof newThreadResult>;

/** The marker `composer.submit` carries to the dispatch hook. */
export const markerSchema = z.object({ v: z.literal(1), intent: id }).strict();
export const marker = (intent: string) => ({ v: 1 as const, intent });

const HOURS: Record<(typeof TIMEOUT_CHOICES)[number], number> = { "2h": 2, "6h": 6, "12h": 12, "24h": 24, "72h": 72 };
/** One short phrase for the loop-wide bound, used wherever the bound is stated. */
export const boundsPhrase = (b: Bounds) => `Stops for you after ${b.maxSends} automatic messages, or ${HOURS[b.noProgressTimeout]} hours without progress (a Git change).`;

/** Go prints durations as 24h0m0s; compare them as milliseconds. Returns null when unreadable. */
export function durationMs(text: string): number | null {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(text.trim());
  if (!m || m[0] === "") return null;
  return ((Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0)) * 60 + Number(m[3] ?? 0)) * 1000;
}

/** A loop name from a thread title: lowercase letters, digits and hyphens. Empty when the title has none. */
export function loopName(title: string | null | undefined): string {
  return (title ?? "").normalize("NFKD").replace(/[\u0300-\u036f]/g, "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 48).replace(/-+$/, "");
}
export const fallbackName = (threadId: string) => `bb-${threadId.toLowerCase().replace(/[^a-z0-9-]/g, "-").slice(0, 50)}`.replace(/-+$/, "");
