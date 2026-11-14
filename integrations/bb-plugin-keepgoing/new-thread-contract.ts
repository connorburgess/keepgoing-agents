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
  // `shownMode` is the start mode the banner displayed when the button was pressed.
  z.object({ action: z.literal("arm"), draftKey: id, projectId: id, bounds: boundsSchema, shownMode: startModeSchema }),
  z.object({ action: z.literal("cancel"), intent: id }),
  z.object({ action: z.literal("outcome"), threadId: id }),
]);
export type NewThreadInput = z.infer<typeof newThreadInput>;

/**
 * What the person is told about the loop created for a thread. It never holds
 * task text. `configPath` is set only when creation was attempted and could
 * not be confirmed, so that what may exist on the host can be found.
 */
export const outcomeSchema = z.object({
  state: z.enum(["none", "waiting", "ready", "failed"]),
  message: z.string().max(1000),
  loop: z.string().max(100).optional(),
  bounds: boundsSchema.optional(),
  cadence: z.string().max(400).optional(),
  configPath: z.string().max(4096).optional(),
  startedAutomatically: z.boolean().optional(),
});
export type Outcome = z.infer<typeof outcomeSchema>;
export const newThreadResult = z.object({
  ok: z.boolean(), message: z.string().max(1000),
  mode: startModeSchema.optional(),
  intent: z.string().max(200).optional(), expiresAt: z.number().optional(),
  outcome: outcomeSchema.optional(),
  /** Setups this plugin holds, of the most it can hold. */
  setups: z.object({ used: z.number().int().min(0), max: z.number().int().positive() }).optional(),
});
export type NewThreadResult = z.infer<typeof newThreadResult>;

/** The marker `composer.submit` carries to the dispatch hook. */
export const markerSchema = z.object({ v: z.literal(1), intent: id }).strict();
export const marker = (intent: string) => ({ v: 1 as const, intent });

const HOURS: Record<(typeof TIMEOUT_CHOICES)[number], number> = { "2h": 2, "6h": 6, "12h": 12, "24h": 24, "72h": 72 };
/** One short phrase for the loop-wide bound, used wherever the bound is stated. */
export const boundsPhrase = (b: Bounds) => `Stops for you after ${b.maxSends} automatic messages, or ${HOURS[b.noProgressTimeout]} hours without progress (a Git change).`;

/**
 * The loop-wide bounds a setup plan reports, when they are exactly one of the
 * choices this control offers; null when either is missing or anything else.
 */
export function planBounds(limits: { maxSends?: number; noProgressTimeout?: string }): Bounds | null {
  const ms = durationMs(limits.noProgressTimeout ?? "");
  const choice = TIMEOUT_CHOICES.find(c => durationMs(c) === ms);
  if (ms === null || !choice || !Number.isInteger(limits.maxSends)) return null;
  const parsed = boundsSchema.safeParse({ maxSends: limits.maxSends, noProgressTimeout: choice });
  return parsed.success ? parsed.data : null;
}
export const sameBounds = (a: Bounds, b: Bounds) => a.maxSends === b.maxSends && a.noProgressTimeout === b.noProgressTimeout;

/** Go prints durations as 24h0m0s; compare them as milliseconds. Returns null when unreadable. */
export function durationMs(text: string): number | null {
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(text.trim());
  if (!m || m[0] === "") return null;
  return ((Number(m[1] ?? 0) * 60 + Number(m[2] ?? 0)) * 60 + Number(m[3] ?? 0)) * 1000;
}

/** Lowercase letters, digits and hyphens only. Empty when the text has none. */
export function slug(text: string | null | undefined): string {
  return (text ?? "").normalize("NFKD").replace(/[\u0300-\u036f]/g, "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 48).replace(/-+$/, "");
}
/**
 * A loop name that says where the work is and nothing about what it is: the
 * project directory's name and a short random suffix, such as example-app-k3f9.
 * The first message, and a thread title made from it, are task text; a loop
 * name reaches command arguments, plugin storage and a directory name, so it
 * is never derived from either.
 */
export function loopName(projectPath: string, random: string): string {
  const project = slug(projectPath.split(/[\\/]/).filter(Boolean).pop()) || "loop";
  const suffix = random.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 4) || "0000";
  return `${project}-${suffix}`;
}
/** The stricter of two start modes: review-first wins. */
export const stricter = (a: StartMode, b: StartMode): StartMode => (a === "automatic" && b === "automatic" ? "automatic" : "manual");
