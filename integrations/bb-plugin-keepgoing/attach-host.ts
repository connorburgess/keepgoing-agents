import { createHash } from "node:crypto";
import { lstat, open, realpath } from "node:fs/promises";
import { dirname, isAbsolute, join, relative } from "node:path";
import { z } from "zod";
import { attachHostInput, attachHostResult, registryEntry } from "./attach-contract.ts";
import { execute, type Execute } from "./setup-host.ts";
import { checkSchema } from "./status-details.ts";

const listing = z.object({ schemaVersion: z.literal(1), operation: z.literal("list"), entries: z.array(registryEntry).max(128) });
const agent = z.object({ adapter: z.literal("bb"), bbThread: z.string(), provider: z.string() });
const status = z.object({
  loop: z.string(), stateDir: z.string(), target: z.literal("primary"), primaryAgent: agent, targetAgent: agent,
  targetConfigured: z.boolean().optional(), redundancy: z.unknown().optional(), routes: z.unknown().optional(),
  controlState: z.string(),
  control: z.object({ ownership: z.object({ threadId: z.string(), generation: z.number().int().positive().safe() }), bbTarget: z.string().optional(), pendingTransfer: z.unknown().optional() }),
  state: z.object({ failedChecks: z.number().int().nonnegative().optional(), lastCheck: checkSchema.optional() }),
  daemon: z.object({ state: z.string() }).optional(),
});
const clientStatus = z.object({ schemaVersion: z.literal(1), registrationId: registryEntry.shape.id, outcome: z.literal("available"), issues: z.array(z.string()).length(0), snapshot: z.object({ loop: z.string(), target: z.object({ alias: z.literal("primary"), adapter: z.literal("bb"), bbThread: z.string() }) }) });
const bad = () => new Error("Registered loop identity or state could not be verified. Inspect the selected registration on its host; nothing was started or resumed.");
function within(root: string, path: string) { const r = relative(root, path); return r === "" || (!r.startsWith("..") && !isAbsolute(r)); }
async function privatePath(path: string, project: string, file: boolean) {
  if (!isAbsolute(path)) throw bad();
  const info = await lstat(path);
  if (info.isSymbolicLink() || (file ? !info.isFile() || info.size > 1 << 20 : !info.isDirectory())) throw bad();
  const resolved = await realpath(path);
  if (within(project, resolved)) throw bad();
  for (let p = file ? dirname(resolved) : resolved; ; p = dirname(p)) {
    try { await lstat(join(p, ".git")); throw bad(); } catch (e) { if ((e as NodeJS.ErrnoException).code !== "ENOENT") throw e; }
    if (dirname(p) === p) break;
  }
  return resolved;
}
async function digest(path: string) {
  const info = await lstat(path);
  if (!info.isFile() || info.size > 1 << 20) throw bad();
  const file = await open(path, "r");
  try {
    const data = Buffer.alloc((1 << 20) + 1);
    let size = 0;
    while (size < data.length) {
      const { bytesRead } = await file.read(data, size, data.length - size, null);
      if (!bytesRead) break;
      size += bytesRead;
    }
    if (size > 1 << 20) throw bad();
    return createHash("sha256").update(data.subarray(0, size)).digest("hex");
  } finally { await file.close(); }
}
/** Reads only explicitly registered references. No provider, probe, lane or lifecycle command. */
export async function attachmentOnHost(raw: z.infer<typeof attachHostInput>, signal?: AbortSignal, run: Execute = execute): Promise<z.infer<typeof attachHostResult>> {
  const { bin, target, registryId } = attachHostInput.parse(raw);
  const list = async () => {
    const r = await run(bin, ["registry", "list", "--private", "--json"], "", target.threadId, signal);
    if (r.code !== 0) throw bad();
    const parsed = listing.parse(r.data);
    if (new Set(parsed.entries.map(e => e.id)).size !== parsed.entries.length || new Set(parsed.entries.map(e => e.configPath)).size !== parsed.entries.length) throw bad();
    return parsed.entries;
  };
  const entries = await list();
  if (!registryId) return { candidates: entries.map(e => ({ registryId: e.id, loop: e.loop })) };
  const entry = entries.find(e => e.id === registryId);
  if (!entry) throw bad();
  const project = await realpath(target.project);
  await privatePath(entry.configPath, project, true);
  const stateDir = await privatePath(entry.stateDir, project, false);
  const before = await digest(entry.configPath);
  // Client status verifies the registry/name/runtime binding and pending/stale
  // target fences. Ordinary status adds provider, owner and check evidence.
  const client = await run(bin, ["client", "status", "--id", registryId, "--offline", "--json"], "", target.threadId, signal);
  const c = clientStatus.parse(client.data);
  if (client.code !== 0 || c.registrationId !== registryId || c.snapshot.loop !== entry.loop || c.snapshot.target.bbThread !== target.threadId) throw bad();
  const result = await run(bin, ["status", "--json", "-c", entry.configPath], "", target.threadId, signal);
  const s = status.parse(result.data);
  const matches = (a: z.infer<typeof agent>) => a.bbThread === target.threadId && a.provider === target.provider;
  if (result.code !== 0 || s.loop !== entry.loop || await realpath(s.stateDir) !== stateDir || !matches(s.primaryAgent) || !matches(s.targetAgent) || s.targetConfigured === false || s.redundancy !== undefined || s.routes !== undefined || s.control.pendingTransfer !== undefined || (s.control.bbTarget !== undefined && s.control.bbTarget !== target.threadId) || s.control.ownership.threadId !== target.threadId) throw bad();
  const fresh = (await list()).find(e => e.id === registryId);
  if (JSON.stringify(fresh) !== JSON.stringify(entry) || await digest(entry.configPath) !== before || signal?.aborted) throw bad();
  return { snapshot: { entry, digest: before, generation: s.control.ownership.generation, state: { controlState: s.controlState, daemonState: s.daemon?.state ?? "unavailable", failedChecks: s.state.failedChecks ?? 0, lastCheck: s.state.lastCheck ?? null } } };
}
