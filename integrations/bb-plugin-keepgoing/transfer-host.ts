import { publishReference } from "./registry-host.ts";
import { z } from "zod";
import { transferHostInput } from "./transfer-contract.ts";
import { execute, type Execute } from "./setup-host.ts";
export function transferOnHost(input: z.infer<typeof transferHostInput>, signal?: AbortSignal, run: Execute = execute) {
  const { bin, record, destination, operation, recoveryId } = transferHostInput.parse(input);
  if (record.hostId !== destination.hostId || record.environmentId !== destination.environmentId || record.projectId !== destination.projectId || record.provider !== destination.provider || record.project !== destination.project || record.threadId === destination.threadId) throw new Error("Unsupported transfer scope");
  const args = ["transfer", operation, "-c", record.configPath, "--json"];
  if (operation === "recover") {
    if (!recoveryId) throw new Error("Recovery identity required");
    args.push("--id", recoveryId, "--take-over");
  } else {
    args.push("--thread", destination.threadId);
    if (operation === "apply") args.push("--from", record.threadId, "--take-over");
  }
  return run(bin, args, "", destination.threadId, signal).then(async result => {
    const committed = z.object({ outcome: z.literal("applied"), id: z.string().regex(/^move_[0-9a-f]{32}$/), from: z.literal(record.threadId), to: z.literal(destination.threadId), awaitingUser: z.literal(true), ownerGeneration: z.number().int().positive().safe() }).safeParse(result.data);
    if (operation !== "preview" && result.code === 0 && committed.success) return { ...result, registration: await publishReference(bin, record.configPath, destination.threadId, destination.provider, signal, run) };
    return result;
  });
}
