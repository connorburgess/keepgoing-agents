import { randomUUID } from "node:crypto";
import { z } from "zod";
import { attachInput, attachmentEvidence, type AttachSnapshot, type AttachResult } from "./attach-contract.ts";
import { targetOf, type SetupRecord, type SetupTarget } from "./setup-contract.ts";
import { MAX_SETUPS } from "./setup-controller.ts";

export interface AttachDependencies {
  target(threadId: string): Promise<SetupTarget>;
  binary(): Promise<string>;
  list(bin: string, target: SetupTarget): Promise<{ registryId: string; loop: string }[]>;
  inspect(bin: string, target: SetupTarget, id: string): Promise<AttachSnapshot>;
  records(): Promise<SetupRecord[]>;
  save(records: SetupRecord[]): Promise<void>;
  guard(target: SetupTarget, snapshot: AttachSnapshot, record?: SetupRecord): Promise<void | string>;
  changed(): void;
}
const equal = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
class PolicyError extends Error {}
function samePlace(a: SetupTarget, b: SetupTarget) { return a.hostId === b.hostId && a.projectId === b.projectId && a.environmentId === b.environmentId && a.project === b.project; }
const identity = (s: AttachSnapshot) => ({ entry: s.entry, digest: s.digest, generation: s.generation });

/** Called inside the plugin's shared registration mutation queue. Inspection
 * after an uncertain response only reads; it never repeats a registration save. */
export function createAttachController(d: AttachDependencies) {
  const tickets = new Map<string, { target: SetupTarget; bin: string; snapshot: AttachSnapshot; record?: SetupRecord; expires: number }>();
  const recordFor = (records: SetupRecord[], t: SetupTarget, s: AttachSnapshot) => {
    if (new Set(records.map(r => r.id)).size !== records.length) throw new PolicyError("Plugin registration identities are duplicated. Inspect saved registrations before attaching.");
    const matches = records.filter(r => r.hostId === t.hostId && (r.configPath === s.entry.configPath || r.registryStateDir === s.entry.stateDir));
    if (matches.length > 1 || records.some(r => r.threadId === t.threadId && !matches.includes(r))) throw new PolicyError("Another plugin registration already refers to this thread or loop. Inspect existing registrations before attaching.");
    const old = matches[0];
    if (old && (!samePlace(old, t) || old.loop !== s.entry.loop || (old.registryId !== undefined && old.registryId !== s.entry.id))) throw new PolicyError("The existing registration belongs to a different place or loop. No attachment was saved.");
    return old;
  };
  const current = async (bin: string, target: SetupTarget, id: string) => {
    const snapshot = await d.inspect(bin, target, id);
    if (!equal(await d.target(target.threadId), target) || await d.binary() !== bin) throw new PolicyError("Thread metadata or the configured binary changed. Preview again.");
    return snapshot;
  };
  const guard = async (target: SetupTarget, snapshot: AttachSnapshot, old?: SetupRecord) => {
    const message = await d.guard(target, snapshot, old);
    if (message) throw new PolicyError(message);
  };
  const call = async (raw: z.infer<typeof attachInput>): Promise<AttachResult> => {
    try {
      const input = attachInput.parse(raw);
      const target = await d.target(input.threadId), bin = await d.binary();
      if (input.action === "list") return { ok: true, candidates: await d.list(bin, target), message: "Choose an existing loop registered on this thread's host, then preview its attachment. Nothing has been created or started." };
      if (!input.registryId) throw new PolicyError("Select a registry entry first.");
      const snapshot = await current(bin, target, input.registryId);
      const old = recordFor(await d.records(), target, snapshot);
      await guard(target, snapshot, old);
      const registered = old && equal(targetOf(old), target) && old.configPath === snapshot.entry.configPath && old.registryId === input.registryId && old.registryStateDir === snapshot.entry.stateDir;
      if (input.action === "inspect") return { ok: true, attached: !!registered, ...(registered ? { record: old } : {}), state: snapshot.state, message: registered ? "Plugin attachment verified. " + attachmentEvidence(snapshot.state) : "No matching plugin attachment is recorded. Preview before attaching; no registration was changed. " + attachmentEvidence(snapshot.state) };
      if (input.action === "preview") {
        for (const [key,p] of tickets) if (p.expires <= Date.now()) tickets.delete(key);
        if (tickets.size >= 64) throw new PolicyError("Too many attachment previews. Wait five minutes and retry.");
        const ticket = randomUUID();
        tickets.set(ticket, { target, bin, snapshot, record: old, expires: Date.now() + 300_000 });
        return { ok: true, ticket, record: { ...target, id: old?.id ?? "attachment-preview", configPath: snapshot.entry.configPath, loop: snapshot.entry.loop }, state: snapshot.state, message: "Preview only: save or reconcile the plugin's reference to this existing loop. Controls, budgets and services stay unchanged. " + attachmentEvidence(snapshot.state) };
      }
      if (registered) return { ok: true, attached: true, record: old, state: snapshot.state, message: "Plugin attachment already verified; no registration changed. " + attachmentEvidence(snapshot.state) };
      const p = tickets.get(input.ticket ?? ""); tickets.delete(input.ticket ?? "");
      if (!p || p.expires <= Date.now() || p.bin !== bin || !equal(p.target, target) || !equal(identity(p.snapshot), identity(snapshot)) || !equal(p.record, old)) throw new PolicyError("Attachment preview expired or identity/configuration/ownership changed. Check attachment status, then preview again if needed.");
      // Re-read within the serialized registration write boundary, including
      // after guard awaits. Preserve all unrelated registrations/settings.
      await guard(target, snapshot, old);
      const fresh = await current(bin, target, input.registryId);
      if (!equal(identity(fresh), identity(p.snapshot))) throw new PolicyError("Loop identity, configuration or ownership changed before saving. Preview again.");
      const records = await d.records();
      const existing = recordFor(records, target, fresh);
      if (!existing && records.length >= MAX_SETUPS) throw new PolicyError("Plugin registration capacity reached; nothing was attached.");
      if (!equal(existing, old)) throw new PolicyError("Plugin registrations changed before saving. Check attachment status before retrying.");
      const record: SetupRecord = { ...target, id: existing?.id ?? randomUUID(), configPath: fresh.entry.configPath, loop: fresh.entry.loop, registryId: input.registryId, registryStateDir: fresh.entry.stateDir };
      if (!equal(existing, record)) {
        try { await d.save(existing ? records.map(r => r.id === existing.id ? record : r) : [...records, record]); }
        catch { return { ok: false, state: fresh.state, message: "Attachment save could not be confirmed. Check attachment status; it may already be saved. Do not create a replacement loop or start supervision to recover this response." }; }
        d.changed();
      }
      return { ok: true, attached: true, record, state: fresh.state, message: "Existing loop attached to this plugin. Nothing was started or resumed. " + attachmentEvidence(fresh.state) };
    } catch (e) {
      return { ok: false, message: e instanceof PolicyError ? e.message : "Attachment identity or status is unavailable. Check the host, registry and ownership; no automatic retry, start or resume was performed." };
    }
  };
  return { call, dispose: () => tickets.clear() };
}
