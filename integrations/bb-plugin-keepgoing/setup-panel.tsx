import { useEffect, useState } from "react";
import { goalLabel } from "./status-details.ts";
import type { SetupResult } from "./setup-contract.ts";
import type { z } from "zod";
import type { setupInput } from "./setup-contract.ts";

type Call = (input: z.infer<typeof setupInput>) => Promise<SetupResult>;
export function SetupPanel({ threadId, call, onChanged }: { threadId: string; call: Call; onChanged(): void }) {
  const [name, setName] = useState(`bb-${threadId.replace(/[^a-z0-9-]/g, "-").slice(0, 50)}`);
  const [withGoal, setWithGoal] = useState(true);
  const [task, setTask] = useState("");
  const [result, setResult] = useState<SetupResult>();
  const [busy, setBusy] = useState(true);
  useEffect(() => {
    let mounted = true;
    call({ action: "inspect", threadId }).then(r => { if (mounted) setResult(r); })
      .catch(() => { if (mounted) setResult({ ok: false, message: "Setup status is unavailable. Check the host connection and retry." }); })
      .finally(() => { if (mounted) setBusy(false); });
    return () => { mounted = false; };
  }, [threadId, call]);
  async function act(action: "preview" | "create" | "inspect" | "start") {
    setBusy(true);
    try {
      const r = await call({ action, threadId, ...(action === "preview" || action === "create" ? { name, task, withGoal, ticket: result?.ticket } : {}) });
      setResult({
        ...r,
        ...(!r.ok && action === "create" && !r.record ? { plan: result?.plan } : {}),
        ...(!r.record && (action === "inspect" || action === "start") ? { record: result?.record } : {}),
      });
      if (r.record) { setTask(""); onChanged(); }
    } catch { setResult({ ok: false, record: result?.record, plan: result?.plan, message: "The request could not be confirmed. Refresh status before retrying; a disabled setup may already exist." }); }
    finally { setBusy(false); }
  }
  const edit = () => setResult(undefined);
  return <section aria-label="Set up thread supervision" style={{ display: "grid", gap: 8, padding: 12, minWidth: 240, maxWidth: 560, fontSize: 12 }}>
    <strong>Supervise this thread</strong>
    <span>Target: {threadId}</span>
    {!result?.record && <>
      <label>Loop name<input aria-label="Loop name" value={name} disabled={busy} onChange={e => { setName(e.target.value); edit(); }} style={{ display: "block", width: "100%" }} /></label>
      <label>Task and completion criteria<textarea aria-label="Task and completion criteria" value={task} disabled={busy} maxLength={262144} rows={5} onChange={e => { setTask(e.target.value); edit(); }} style={{ display: "block", width: "100%" }} /></label>
      <label><input type="checkbox" aria-label="Record a goal for this task" checked={withGoal} disabled={busy} onChange={e => { setWithGoal(e.target.checked); edit(); }} /> Record a goal for this task</label>
      <span>{withGoal ? "Use the loop name as the initial goal title. Save the task once in a private file and share current goal references in future prompts. Requires an updated looptender CLI; v0.1.4 does not support this setup option." : "Copy the task into wake instructions without creating a goal record."}</span>
      <span>One existing thread, with its current model and login. No fallbacks. Creating the setup leaves supervision disabled.</span>
      <button type="button" disabled={busy || !task.trim() || !name} onClick={() => void act("preview")}>Preview setup</button>
    </>}
    {result?.plan && <div aria-label="Setup preview" style={{ display: "grid", gap: 4 }}>
      <span>Provider: {result.plan.provider}</span><span>Project: {result.plan.project}</span>
      <span>Configuration: {result.plan.configPath}</span>
      <span>{result.plan.withGoal ? "Goal: record revision 1 referencing a private task snapshot. Creation stays disabled." : "Task: copied into wake instructions; no goal record."}</span>
      <span>Check every {result.plan.limits.interval}. Idle threads can wake on a check; stall nudges are spaced at least {result.plan.limits.wakeEvery} apart.</span>
      <span>Stall after {result.plan.limits.stallAfter}; {result.plan.limits.maxNudges} nudges; {result.plan.limits.maxReprompts} error re-prompts with {result.plan.limits.backoff} backoff.</span>
      {result.ticket && <button type="button" disabled={busy} onClick={() => void act("create")}>Create disabled setup</button>}
    </div>}
    {result?.record && <>
      <span>{goalLabel(result.goal, result.goalState)}</span>
      <span>Configuration: {result.record.configPath}</span>
      <button type="button" disabled={busy} onClick={() => void act("inspect")}>Refresh diagnostics</button>
      {result.canStart && <>
        <span>Starting enables the loop and launches a background supervisor on this thread’s host. It may prompt the agent when eligible. Other timers are not detected or changed.</span>
        <button type="button" disabled={busy} onClick={() => void act("start")}>Start background supervision</button>
      </>}
    </>}
    {result?.checks && <details><summary>Diagnostics ({result.checks.length})</summary>
      <ul>{result.checks.map((c, i) => <li key={`${c.id}-${i}`}>{c.status}: {c.message}{c.fix && ` ${c.fix}`}</li>)}</ul>
    </details>}
    {busy && <span role="status">Checking setup…</span>}
    {result && <span role={result.ok ? "status" : "alert"}>{result.message}</span>}
  </section>;
}
