import { useEffect, useState } from "react";
import type { z } from "zod";
import type { transferInput, TransferResult } from "./transfer-contract.ts";

type Call = (input: z.infer<typeof transferInput>) => Promise<TransferResult>;
export function TransferPanel({ threadId, call, onChanged }: { threadId: string; call: Call; onChanged(): void }) {
  const [choices, setChoices] = useState<NonNullable<TransferResult["choices"]>>([]);
  const [selected, setSelected] = useState("");
  const [result, setResult] = useState<TransferResult>();
  const [busy, setBusy] = useState(true);
  useEffect(() => {
    let mounted = true;
    call({ action: "list", threadId }).then(r => { if (mounted) { setChoices(r.choices ?? []); setResult(r); } })
      .catch(() => { if (mounted) setResult({ ok: false, message: "Move list unavailable. Check host access and refresh." }); })
      .finally(() => { if (mounted) setBusy(false); });
    return () => { mounted = false; };
  }, [threadId, call]);
  async function act(action: z.infer<typeof transferInput>["action"]) {
    setBusy(true);
    try {
      const r = await call({ action, threadId, ...(selected ? { recordId: selected } : {}), ...(action === "apply" && result?.ticket ? { ticket: result.ticket } : {}) });
      setResult(r);
      if (r.choices) setChoices(r.choices);
      if (r.moved || action === "apply" || action === "recover") onChanged();
    } catch {
      setResult({ ok: false, message: "Move response lost. Check move status before retrying; no automatic retry was made." });
    } finally { setBusy(false); }
  }
  return <section aria-label="Move supervision here" style={{ display: "grid", gap: 8, padding: 12, maxWidth: 560, fontSize: 12 }}>
    <strong>Move supervision here</strong>
    <span>Destination: {threadId}</span>
    <label>Existing loop<select aria-label="Existing loop" disabled={busy} value={selected} onChange={e => { setSelected(e.target.value); setResult(undefined); }}>
      <option value="">Choose a loop</option>
      {choices.map(c => <option key={c.id} value={c.id}>{c.loop} — {c.from}{c.pending ? " (move pending)" : ""}</option>)}
    </select></label>
    <button type="button" disabled={busy} onClick={() => void act("list")}>Refresh move list</button>
    {selected && !result?.moved && <>
      <button type="button" disabled={busy || choices.find(c => c.id === selected)?.pending} onClick={() => void act("preview")}>Preview move</button>
      <button type="button" disabled={busy} onClick={() => void act("inspect")}>Check move status</button>
    </>}
    {result?.summary && <div aria-label="Move preview" style={{ display: "grid", gap: 4 }}>
      <span>{result.summary.loop}: {result.summary.from} → {result.summary.to}</span>
      <span>Provider: {result.summary.provider}; model and account identity unverified.</span>
      <span>Current controls: {Object.entries(result.summary.holds).map(([k, v]) => `${k}=${v}`).join(", ")}</span>
      {result.summary.owner && <span>Current owner: {result.summary.owner}</span>}
      {result.summary.goal && <span>Goal: {result.summary.goal.id}, revision {result.summary.goal.revision}</span>}
      <span>The move transfers ownership, preserves existing holds, and adds an awaiting-user hold. It does not cancel the old thread’s active turn or change a timer. Resume is a separate decision.</span>
      {result.ticket && <button type="button" disabled={busy} onClick={() => void act("apply")}>Move and keep held</button>}
    </div>}
    {result?.canRecover && <button type="button" disabled={busy} onClick={() => void act("recover")}>Recover move</button>}
    {busy && <span role="status">Checking move…</span>}
    {result && <span role={result.ok ? "status" : "alert"}>{result.message}</span>}
  </section>;
}
