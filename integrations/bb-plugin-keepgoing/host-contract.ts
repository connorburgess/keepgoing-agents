import { defineRpcContract } from "@get-bb/plugin-sdk";
import { z } from "zod";
import { targetSchema, recordSchema, planSchema, refusalSchema } from "./setup-contract.ts";
import { transferHostInput } from "./transfer-contract.ts";
import { boundsSchema } from "./new-thread-contract.ts";
const text = z.string().min(1).max(4096);
// Only the server calls the host entry. Browser input never selects a binary,
// host, project path, config path, provider, or arbitrary command arguments.
export const hostContract = defineRpcContract({
  transfer: { input: transferHostInput, output: z.object({ code: z.number().int(), data: z.unknown() }) },
  init: {
    input: z.object({ bin: text, target: targetSchema, name: text, task: z.string().max(262144), dryRun: z.boolean(), withGoal: z.boolean().optional(), bounds: boundsSchema.optional(), followStandard: z.literal(true).optional() }),
    // A plan, or keepgoing's own structured refusal.
    output: z.union([z.object({ plan: planSchema }), z.object({ refused: refusalSchema }), z.object({ noRoutes: z.literal(true) })]),
  },
  // Whether the host's looptender has a standard route list with routes.
  standardRoutes: {
    input: z.object({ bin: text, target: targetSchema }),
    output: z.object({ available: z.boolean() }),
  },
  command: {
    // `json` is honoured for start only, and only set when the CLI advertised it.
    input: z.object({ bin: text, record: recordSchema, operation: z.enum(["status", "doctor", "start", "pause", "resume", "stop"]), json: z.literal(true).optional() }),
    output: z.object({ code: z.number().int(), data: z.unknown() }),
  },
});
