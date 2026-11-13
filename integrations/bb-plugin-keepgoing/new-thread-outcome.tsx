// What a thread shows after it was created with Keep going on: that setup is
// under way, why it could not go ahead, or the one button that starts it.
import { useCallback, useEffect, useState } from "react";
import { boundsPhrase, type NewThreadResult, type Outcome } from "./new-thread-contract.ts";
import type { NewThreadCall } from "./new-thread-control.tsx";
import type { SetupResult } from "./setup-contract.ts";

export type StartCall = (input: { action: "start"; threadId: string }) => Promise<SetupResult>;
const NONE: Outcome = { state: "none", message: "" };
const button = { background: "none", color: "inherit", font: "inherit", fontSize: 12, lineHeight: "22px", padding: "0 10px", border: "1px solid currentColor", borderRadius: 6, cursor: "pointer", fontWeight: 600 } as const;

/** Reads the outcome for a thread, and keeps asking while setup is waiting for the thread's environment. */
export function useOutcome(threadId: string, call: NewThreadCall, refreshKey: unknown = 0, pollMs = 5_000) {
  const [outcome, setOutcome] = useState<Outcome>(NONE);
  const read = useCallback(() => call({ action: "outcome", threadId })
    .then((r: NewThreadResult) => { if (r.ok && r.outcome) setOutcome(r.outcome); })
    .catch(() => { /* the ordinary status row reports a lost connection */ }), [call, threadId]);
  useEffect(() => { setOutcome(NONE); }, [threadId]);
  useEffect(() => { void read(); }, [read, refreshKey]);
  useEffect(() => {
    if (outcome.state !== "waiting") return;
    const timer = setInterval(() => void read(), pollMs);
    return () => clearInterval(timer);
  }, [outcome.state, read, pollMs]);
  return { outcome, read };
}

/** A short label for the collapsed banner; null when this thread was not created with Keep going on. */
export function outcomeLabel(outcome: Outcome, controlState: string | undefined, supervising: boolean | undefined): string | null {
  if (outcome.state === "waiting") return "keepgoing: setting up";
  if (outcome.state === "failed") return controlState ? null : "keepgoing: not set up";
  if (outcome.state !== "ready") return null;
  if (controlState === "disabled") return "keepgoing: ready, not started";
  if (controlState === "enabled" && supervising) return "keepgoing: keeping this going";
  return null;
}

export type ControlCall = (action: "pause" | "stop") => Promise<{ ok: boolean; message: string }>;

export function NewThreadOutcome({ threadId, outcome, controlState, supervising, start, control, onChanged }: {
  threadId: string; outcome: Outcome; controlState: string | undefined; supervising: boolean | undefined; start: StartCall; control?: ControlCall; onChanged(): void;
}) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; message: string }>();
  if (outcome.state === "none") return null;
  const box = { display: "grid", gap: 6, padding: "6px 12px", fontSize: 12 } as const;
  if (outcome.state === "waiting") return <section aria-label="Keep going for this thread" aria-live="polite" style={box}><strong>keepgoing: setting up</strong><span role="status">{outcome.message}</span></section>;
  // Once a loop exists for the thread, the ordinary status row speaks for it.
  if (outcome.state === "failed") return controlState ? null : <section aria-label="Keep going for this thread" style={box}>
    <strong>keepgoing could not set this thread up</strong><span role="alert">{outcome.message}</span>
    {outcome.configPath && <span>Configuration: {outcome.configPath}</span>}
  </section>;
  const label = outcomeLabel(outcome, controlState, supervising);
  if (!label) return null;
  const act = () => {
    setBusy(true); setResult(undefined);
    start({ action: "start", threadId })
      .then(r => setResult({ ok: r.ok, message: r.message }))
      .catch(() => setResult({ ok: false, message: "Start could not be confirmed. Refresh status before trying again." }))
      .finally(() => { setBusy(false); onChanged(); });
  };
  const hold = (action: "pause" | "stop") => {
    if (!control) return;
    setBusy(true); setResult(undefined);
    control(action)
      .then(r => setResult(r))
      .catch(() => setResult({ ok: false, message: "The control could not be confirmed. Refresh status before trying again." }))
      .finally(() => { setBusy(false); onChanged(); });
  };
  const started = controlState === "enabled";
  return <section aria-label="Keep going for this thread" style={box}>
    <strong>{label}</strong>
    <span>Loop {outcome.loop}. {started ? (outcome.startedAutomatically ? "Started when this thread was created." : "Started from this thread.") : outcome.message}</span>
    {outcome.bounds && <span>{boundsPhrase(outcome.bounds)}</span>}
    {!started && <span><button type="button" disabled={busy} onClick={act} style={button}>Start keeping this going</button></span>}
    {started && control && <span style={{ display: "flex", gap: 8 }}>
      <button type="button" disabled={busy} onClick={() => hold("pause")} style={{ ...button, fontWeight: 400 }}>Pause</button>
      <button type="button" disabled={busy} onClick={() => hold("stop")} style={{ ...button, fontWeight: 400 }}>Stop</button>
    </span>}
    {outcome.cadence && <details><summary>Details</summary><div style={{ paddingTop: 6 }}>{outcome.cadence} Starting launches a background supervisor on this thread's host; it may prompt the agent when a check finds the thread idle. Other timers are not detected or changed.</div></details>}
    {result && <span role={result.ok ? "status" : "alert"}>{result.message}</span>}
  </section>;
}
