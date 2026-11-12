import { defineRpcContract } from "@get-bb/plugin-sdk";
import { z } from "zod";
import { targetSchema, recordSchema, planSchema } from "./setup-contract.ts";
import { transferHostInput } from "./transfer-contract.ts";
const text = z.string().min(1).max(4096);
// Only the server calls the host entry. Browser input never selects a binary,
// host, project path, config path, provider, or arbitrary command arguments.
export const hostContract = defineRpcContract({
  transfer: { input: transferHostInput, output: z.object({ code: z.number().int(), data: z.unknown() }) },
  init: {
    input: z.object({ bin: text, target: targetSchema, name: text, task: z.string().max(262144), dryRun: z.boolean(), withGoal: z.boolean().optional() }),
    output: z.object({ plan: planSchema }),
  },
  command: {
    input: z.object({ bin: text, record: recordSchema, operation: z.enum(["status", "doctor", "start", "pause", "resume", "stop"]) }),
    output: z.object({ code: z.number().int(), data: z.unknown() }),
  },
});
