import { useState } from "react";
import type { z } from "zod";
import { attachInput, type AttachResult } from "./attach-contract.ts";

type Call = (input: z.infer<typeof attachInput>) => Promise<AttachResult>;
export function AttachPanel({ threadId, call, onChanged }: { threadId: string; call: Call; onChanged(): void }) {
  const [result, setResult] = useState<AttachResult>();
  const [candidates, setCandidates] = useState<{ registryId: string; loop: string }[]>([]);
  const [id, setId] = useState("");
  const [busy, setBusy] = useState(false);
  async function act(action: z.infer<typeof attachInput>["action"]) {
    setBusy(true);
    try {
      const r = await call({ action, threadId, ...(action !== "list" ? { registryId: id } : {}), ...(action === "attach" ? { ticket: result?.ticket } : {}) });
      setResult(r);
      if (r.candidates) { setCandidates(r.candidates); setId(""); }
      if (r.attached) onChanged();
    } catch {
      setResult({ ok: false, message: "The response could not be confirmed. Check attachment status before trying again; no automatic retry or start was requested." });
    } finally { setBusy(false); }
  }
  return <section aria-label="Attach an existing loop" style={{ display: "grid", gap: 8, padding: 12, minWidth: 240, maxWidth: 560, fontSize: 12 }}>
    <strong>Already have a loop for this thread?</strong>
    <span>Attach a loop from this device’s registry to show it in BB. Attachment changes only the plugin’s reference; it never starts or resumes supervision.</span>
    <button type="button" disabled={busy} onClick={() => void act("list")}>Find registered loops</button>
    {candidates.length > 0 && <>
      <label>Registered loop<select aria-label="Registered loop" value={id} disabled={busy} onChange={e => { setId(e.target.value); setResult(undefined); }} style={{ display: "block", width: "100%" }}>
        <option value="">Choose a loop</option>
        {candidates.map(c => <option key={c.registryId} value={c.registryId}>{c.loop} · {c.registryId}</option>)}
      </select></label>
      <button type="button" disabled={busy || !id} onClick={() => void act("preview")}>Preview attachment</button>
      <button type="button" disabled={busy || !id} onClick={() => void act("inspect")}>Check attachment status</button>
    </>}
    {result?.candidates?.length === 0 && <span>No loops are registered on this device. Register the existing loop with the CLI first; this action does not search for files.</span>}
    {result?.record && <span style={{ overflowWrap: "anywhere" }}>Configuration: {result.record.configPath}</span>}
    {result?.ticket && <button type="button" disabled={busy} onClick={() => void act("attach")}>Attach existing loop</button>}
    {busy && <span role="status">Checking attachment…</span>}
    {result && <span role={result.ok ? "status" : "alert"}>{result.message}</span>}
  </section>;
}
