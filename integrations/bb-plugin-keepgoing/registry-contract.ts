import { z } from "zod";

// Last explicit publication outcome, not current health or complete coverage.
export const publicationSchema = z.discriminatedUnion("state", [
  z.object({ state: z.literal("registered"), source: z.literal("cli_registry"), id: z.string().regex(/^loop_[0-9a-f]{32}$/) }),
  z.object({ state: z.literal("repair_needed"), source: z.literal("cli_registry") }),
]);
export type Publication = z.infer<typeof publicationSchema>;
export const publicationMessage = (p?: Publication) => p?.state === "registered"
  ? " CLI menu reference published; registration is not a health check."
  : " CLI menu publication is unverified. Use explicit registry reconcile on the host; do not repeat setup, start or move to repair visibility.";
