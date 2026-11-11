import { randomUUID } from "node:crypto";
import { z } from "zod";
import { type SetupRecord, type SetupTarget } from "./setup-contract.ts";
import { holdsSchema, moveSummarySchema, transferInput, type MoveIntent, type TransferResult } from "./transfer-contract.ts";

export interface TransferDependencies {
  target(id: string): Promise<SetupTarget>;
  binary(): Promise<string>;
  records(): Promise<SetupRecord[]>;
  save(records: SetupRecord[]): Promise<void>;
  intents(): Promise<MoveIntent[]>;
  saveIntents(intents: MoveIntent[]): Promise<void>;
  guard(destination: string, record: SetupRecord): Promise<void>;
  command(bin: string, record: SetupRecord, operation: "status"): Promise<{ code: number; data: unknown }>;
  transfer(intent: MoveIntent, operation: "preview" | "apply" | "recover", recoveryId?: string): Promise<{ code: number; data: unknown }>;
  changed(): void;
}
const identity = z.object({ thread: z.string(), project: z.string(), environment: z.string(), host: z.string(), provider: z.string() });
const previewSchema = z.object({
  schemaVersion: z.literal(1), outcome: z.literal("preview"), applySupported: z.literal(true), compatibility: z.literal("compatible_metadata"),
  from: identity, to: identity, controls: holdsSchema, owner: z.string().optional(), goal: moveSummarySchema.shape.goal,
  configuredProvider: z.string(), configuredModel: z.string().optional(), configuredReasoning: z.string().optional(), configuredQuotaGroup: z.string(),
});
const agent = z.object({ adapter: z.literal("bb"), bbThread: z.string(), provider: z.string() });
const statusSchema = z.object({
  loop: z.string(), target: z.literal("primary"), primaryAgent: agent, targetAgent: agent,
  redundancy: z.unknown().optional(),
  control: holdsSchema.extend({
    bbTarget: z.string().optional(), ownership: z.object({ threadId: z.string(), generation: z.number().int().positive().safe() }).optional(),
    pendingTransfer: z.object({ version: z.literal(1), id: z.string().regex(/^move_[0-9a-f]{32}$/), from: z.string(), to: z.string() }).optional(),
  }),
});
const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
function savedTarget(r: SetupRecord): SetupTarget {
  const { id, configPath, loop, ...target } = r;
  return target;
}
function compatible(a: SetupTarget, b: SetupTarget) {
  return a.threadId !== b.threadId && a.projectId === b.projectId && a.environmentId === b.environmentId && a.hostId === b.hostId && a.project === b.project && a.provider === b.provider;
}
class PolicyError extends Error {}

export function createTransferController(d: TransferDependencies) {
  const tickets = new Map<string, { intent: MoveIntent; preview: z.infer<typeof previewSchema>; expires: number }>();
  const validateTargets = async (i: MoveIntent) => {
    if (!equal(await d.target(i.record.threadId), savedTarget(i.record)) || !equal(await d.target(i.destination.threadId), i.destination) || !compatible(i.record, i.destination)) throw new PolicyError("Thread, provider or environment changed. No move was authorized; inspect the saved setup.");
    await d.guard(i.destination.threadId, i.record);
  };
  const preview = async (i: MoveIntent) => {
    await validateTargets(i);
    const r = await d.transfer(i, "preview");
    const p = previewSchema.safeParse(r.data);
    if (r.code !== 0 || !p.success) throw new PolicyError("Transfer preview is unavailable or blocked. Check diagnostics and the host's keepgoing version; apply/recover support is required.");
    const matches = (a: z.infer<typeof identity>, t: SetupTarget) => a.thread === t.threadId && a.project === t.projectId && a.environment === t.environmentId && a.host === t.hostId && a.provider === t.provider;
    if (!matches(p.data.from, i.record) || !matches(p.data.to, i.destination) || p.data.configuredProvider !== i.record.provider) throw new PolicyError("The CLI target differs from the registered setup. No move was performed.");
    return p.data;
  };
  const summary = (i: MoveIntent, p: z.infer<typeof previewSchema>) => ({ loop: i.record.loop, from: i.record.threadId, to: i.destination.threadId, provider: i.destination.provider, holds: p.controls, ...(p.owner ? { owner: p.owner } : {}), ...(p.goal ? { goal: p.goal } : {}) });
  const inspect = async (i: MoveIntent): Promise<TransferResult> => {
    // A lost apply response may already have committed. Only verified held
    // destination state can repair the plugin reference; never infer success
    // from an exit code or repeat apply automatically.
    if (!equal(await d.target(i.destination.threadId), i.destination)) throw new PolicyError("Destination metadata changed. Inspect this move on its host before proceeding.");
    const r = await d.command(i.bin, { ...i.record, ...i.destination }, "status");
    const p = statusSchema.safeParse(r.data);
    if (r.code !== 0 || !p.success || p.data.loop !== i.record.loop || p.data.redundancy !== undefined) throw new PolicyError("Move status is unavailable. The saved recovery reference was retained; inspect the configuration on its host.");
    const s = p.data;
    if (s.control.pendingTransfer) {
      const p = s.control.pendingTransfer;
      if (p.from !== i.record.threadId || p.to !== i.destination.threadId || !s.control.awaitingUser) throw new PolicyError("The pending CLI move does not match this request. Inspect it on the host.");
      return { ok: false, canRecover: true, message: "The CLI move is pending. Recover move continues this saved request and leaves all holds intact." };
    }
    const matches = (a: z.infer<typeof agent>, thread: string) => a.bbThread === thread && a.provider === i.destination.provider;
    if (matches(s.primaryAgent, i.destination.threadId) && matches(s.targetAgent, i.destination.threadId) && s.control.bbTarget === i.destination.threadId && s.control.ownership?.threadId === i.destination.threadId && s.control.awaitingUser) {
      await d.guard(i.destination.threadId, i.record);
      const records = await d.records();
      const current = records.find(r => r.id === i.record.id);
      const moved = { ...i.record, ...i.destination };
      if (!current || (!equal(current, i.record) && !equal(current, moved))) throw new PolicyError("The saved setup reference changed. The loop remains held; reconcile its reference on the host.");
      if (records.some(r => r.id !== moved.id && r.threadId === moved.threadId)) throw new PolicyError("Another saved setup now targets this thread. The move remains held for inspection.");
      await d.save(records.map(r => r.id === moved.id ? moved : r));
      await d.saveIntents((await d.intents()).filter(x => x.record.id !== i.record.id));
      d.changed();
      return { ok: true, moved: true, message: "Supervision moved here and is awaiting explicit resume. Existing holds and work allowances survived. Review diagnostics before resuming; a background daemon may need to be started separately. Timers were not changed." };
    }
    if (matches(s.primaryAgent, i.record.threadId) && matches(s.targetAgent, i.record.threadId)) return { ok: false, canRecover: true, message: "No completed move is verified. Recover move explicitly retries this saved source/destination request if the CLI has not begun it; it never clears holds." };
    throw new PolicyError("The loop no longer matches either transfer target or its required hold. Inspect it on the host; no automatic retry was made.");
  };
  const handle = async (raw: z.infer<typeof transferInput>): Promise<TransferResult> => {
    const input = transferInput.parse(raw);
    const records = await d.records(), intents = await d.intents();
    if (input.action === "list") {
      const destination = await d.target(input.threadId);
      return { ok: true, message: "Choose a plugin-created loop in this environment. Manually configured loops use the CLI transfer commands.", choices: records.filter(r => compatible(r, destination) || intents.some(i => i.record.id === r.id && i.destination.threadId === input.threadId)).map(r => ({ id: r.id, loop: r.loop, from: intents.find(i => i.record.id === r.id)?.record.threadId ?? r.threadId, pending: intents.some(i => i.record.id === r.id) })) };
    }
    const pending = intents.find(i => i.record.id === input.recordId);
    if (pending) {
      if (pending.destination.threadId !== input.threadId || !["inspect", "recover"].includes(input.action)) throw new PolicyError("This setup has a saved move request. Open its destination and check move status before continuing.");
      if (pending.bin !== await d.binary()) throw new PolicyError("The keepgoing binary setting changed. Restore it before recovering this saved request.");
      const result = await inspect(pending);
      if (input.action !== "recover" || result.moved || !result.canRecover) return result;
      await validateTargets(pending);
      const status = await d.command(pending.bin, { ...pending.record, ...pending.destination }, "status");
      const s = statusSchema.safeParse(status.data);
      if (status.code !== 0 || !s.success) throw new PolicyError("Recovery status unavailable; no command was sent.");
      const p = s.data.control.pendingTransfer;
      if (p) {
        if (p.from !== pending.record.threadId || p.to !== pending.destination.threadId) throw new PolicyError("Pending move identity changed; inspect the host.");
        await d.transfer(pending, "recover", p.id);
      } else {
        await preview(pending);
        await d.transfer(pending, "apply");
      }
      return inspect(pending);
    }
    if (input.action === "inspect" || input.action === "recover") return { ok: false, message: "No saved move request. Refresh the list before previewing another move." };
    const record = records.find(r => r.id === input.recordId);
    if (!record) throw new PolicyError("That setup is not registered with this plugin.");
    const intent: MoveIntent = { record, destination: await d.target(input.threadId), bin: await d.binary() };
    if (input.action === "preview") {
      for (const [k, t] of tickets) if (t.expires < Date.now()) tickets.delete(k);
      if (tickets.size >= 64) throw new PolicyError("Too many previews. Wait five minutes before retrying.");
      const p = await preview(intent), ticket = randomUUID();
      tickets.set(ticket, { intent, preview: p, expires: Date.now() + 300_000 });
      return { ok: true, ticket, summary: summary(intent, p), message: "Moving transfers loop ownership and adds an awaiting-user hold. Model and account identity are unverified. No thread or timer is created or stopped." };
    }
    const ticket = tickets.get(input.ticket ?? "");
    tickets.delete(input.ticket ?? "");
    if (!ticket || ticket.expires < Date.now() || !equal(ticket.intent, intent)) throw new PolicyError("The preview expired or the setup changed. Preview again.");
    const p = await preview(intent);
    if (!equal(p, ticket.preview)) throw new PolicyError("The target, controls, goal or configured labels changed. Review a fresh preview.");
    // Persist first. Reload/lost responses must leave a visible recovery path.
    await d.saveIntents([...intents, intent]);
    d.changed();
    await d.transfer(intent, "apply");
    return inspect(intent);
  };
  return {
    call: async (input: z.infer<typeof transferInput>): Promise<TransferResult> => {
      try { return await handle(input); }
      catch (e) { return { ok: false, message: e instanceof PolicyError ? e.message : "Move could not be confirmed. Check move status before retrying; a saved request or held CLI move may already exist. Private host output was withheld." }; }
    },
    dispose: () => tickets.clear(),
  };
}
