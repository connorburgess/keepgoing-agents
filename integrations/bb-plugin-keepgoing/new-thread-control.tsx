// The Keep going control on BB's New thread screen: a switch in the
// composer's action row, and a banner that explains it while it is on and
// holds the one send that carries the request.
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { DEFAULT_BOUNDS, TIMEOUT_CHOICES, boundsPhrase, boundsSchema, marker, type Bounds, type NewThreadInput, type NewThreadResult, type StartMode } from "./new-thread-contract.ts";

export type NewThreadCall = (input: NewThreadInput) => Promise<NewThreadResult>;
/** The part of BB's composer handle this control uses. */
export interface ComposerHandle {
  readonly scope: { kind: string; projectId?: string | null };
  readonly key: string;
  readonly isSubmittingBlocked: boolean;
  readonly submittingBlockedReason: string | null;
  submit(options?: { experimental_data?: { v: 1; intent: string } }): Promise<void>;
  /** Successful sends from this composer, however they were made. */
  onSubmitted?(listener: () => void): () => void;
}

// The switch and the banner are separate components mounted by BB, so the
// state they share is kept per draft, outside React. It holds no task text.
interface DraftState { on: boolean; bounds: Bounds; notice: string }
const OFF: DraftState = { on: false, bounds: DEFAULT_BOUNDS, notice: "" };
const drafts = new Map<string, DraftState>();
const listeners = new Set<() => void>();
const subscribe = (l: () => void) => { listeners.add(l); return () => { listeners.delete(l); }; };
export function setDraft(key: string, patch: Partial<DraftState>) {
  drafts.set(key, { ...(drafts.get(key) ?? OFF), ...patch });
  for (const l of listeners) l();
}
const useDraft = (key: string) => useSyncExternalStore(subscribe, () => drafts.get(key) ?? OFF);
/** For the row in the menu beside Send, which BB draws itself. */
export const turnOn = (key: string) => setDraft(key, { on: true, notice: "" });
export const resetDrafts = () => drafts.clear();

const button = { background: "none", color: "inherit", font: "inherit", fontSize: 12, lineHeight: "22px", padding: "0 10px", border: "1px solid currentColor", borderRadius: 6, cursor: "pointer" } as const;
const card = { display: "grid", gap: 6, padding: "10px 12px", border: "1px solid currentColor", borderRadius: 10, fontSize: 12, marginBottom: 6 } as const;

export function KeepGoingSwitch({ composer }: { composer: ComposerHandle }) {
  const draft = useDraft(composer.key);
  if (composer.scope.kind !== "new-thread") return null;
  return <button type="button" role="switch" aria-checked={draft.on} title="Supervise the thread this message creates"
    onClick={() => setDraft(composer.key, { on: !draft.on, notice: "" })}
    style={{ ...button, opacity: draft.on ? 1 : 0.7, fontWeight: draft.on ? 600 : 400 }}>
    Keep going
  </button>;
}

export function KeepGoingBanner({ composer, call }: { composer: ComposerHandle; call: NewThreadCall }) {
  const key = composer.key;
  const draft = useDraft(key);
  const [mode, setMode] = useState<StartMode>();
  const [setups, setSetups] = useState<{ used: number; max: number }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const check = useCallback(() => {
    let live = true;
    setMode(undefined); setError("");
    call({ action: "check" })
      .then(r => { if (!live) return; if (r.ok && r.mode) { setMode(r.mode); setSetups(r.setups); } else setDraft(key, { on: false, notice: r.message || "Keep going is unavailable." }); })
      .catch(() => { if (live) setDraft(key, { on: false, notice: "Keep going could not be checked. Check the connection and try again." }); });
    return () => { live = false; };
  }, [call, key]);
  // Turning the switch on is what asks whether keepgoing can be used here.
  useEffect(() => { if (draft.on) return check(); }, [draft.on, check]);
  // Any send from this composer ends the switch and its edited limits, so
  // neither carries silently into the next draft. A plain Enter is such a send.
  useEffect(() => composer.onSubmitted?.(() => setDraft(key, { on: false, notice: "", bounds: DEFAULT_BOUNDS })), [composer, key]);
  if (composer.scope.kind !== "new-thread") return null;
  if (!draft.on) {
    if (!draft.notice) return null;
    return <section aria-label="Keep going" aria-live="polite" style={card}>
      <strong>Keep going is unavailable here</strong>
      <span role="alert">{draft.notice}</span>
      <span><button type="button" style={button} onClick={() => turnOn(key)}>Check again</button></span>
    </section>;
  }
  if (!mode) return <section aria-label="Keep going" aria-live="polite" style={card}><span role="status">Checking keepgoing…</span></section>;

  const send = async () => {
    setBusy(true); setError("");
    try {
      const projectId = composer.scope.projectId;
      if (!projectId) { setError("Choose a project first."); return; }
      const bounds = boundsSchema.safeParse(draft.bounds);
      if (!bounds.success) { setError("Set a limit of 1 to 1000 automatic messages."); return; }
      // The mode shown at this moment is the most that may happen.
      const armed = await call({ action: "arm", draftKey: key, projectId, bounds: bounds.data, shownMode: mode });
      if (!armed.ok || !armed.intent) { setError(armed.message || "Keep going could not be turned on. The message was not sent."); return; }
      try {
        await composer.submit({ experimental_data: marker(armed.intent) });
        setDraft(key, { on: false, notice: "", bounds: DEFAULT_BOUNDS });
      } catch (failure) {
        // The message did not go; the request must not outlive it.
        await call({ action: "cancel", intent: armed.intent }).catch(() => {});
        setError(failure instanceof Error && failure.message ? failure.message : "The message was not sent.");
      }
    } catch { setError("Keep going could not be confirmed. The message was not sent."); }
    finally { setBusy(false); }
  };
  const blocked = composer.isSubmittingBlocked;
  return <section aria-label="Keep going" aria-live="polite" style={card}>
    <span>{mode === "automatic"
      ? "keepgoing will supervise the thread this message creates, starting once the thread exists."
      : "keepgoing will set up supervision for the thread this message creates. It is created switched off; you start it from the thread."}</span>
    <span>{boundsPhrase(draft.bounds)}</span>
    <span style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
      <button type="button" disabled={busy || blocked} title={blocked ? composer.submittingBlockedReason ?? "" : undefined} onClick={() => void send()} style={{ ...button, fontWeight: 600 }}>Send and keep going</button>
      <button type="button" disabled={busy} onClick={() => setDraft(key, { on: false, notice: "", bounds: DEFAULT_BOUNDS })} style={{ ...button, border: 0 }}>Turn off</button>
      <span>A plain Enter sends without supervision.</span>
    </span>
    <details>
      <summary>Details</summary>
      <div style={{ display: "grid", gap: 6, paddingTop: 6 }}>
        <span>Your first message is the task, recorded as the loop's goal. One thread, with the model and login it already has; no fallbacks.</span>
        <label>Stop for me after this many automatic messages{" "}
          <input type="number" aria-label="Automatic messages before stopping" min={1} max={1000} value={draft.bounds.maxSends} disabled={busy}
            onChange={e => setDraft(key, { bounds: { ...draft.bounds, maxSends: Number(e.target.value) } })} style={{ width: 72 }} /></label>
        <label>Stop for me after this long without a Git change{" "}
          <select aria-label="Time without a Git change before stopping" value={draft.bounds.noProgressTimeout} disabled={busy}
            onChange={e => setDraft(key, { bounds: { ...draft.bounds, noProgressTimeout: e.target.value as Bounds["noProgressTimeout"] } })}>
            {TIMEOUT_CHOICES.map(choice => <option key={choice} value={choice}>{choice}</option>)}
          </select></label>
        <span>The loop is named after the project, with a short suffix; it does not use the message or the thread's title. Later wakes point the agent at the saved task; the first message is not sent again.</span>
        {setups && <span>This plugin holds {setups.used} of {setups.max} setups and cannot remove one yet.</span>}
        <span>How often it checks, and how many times it nudges a stalled turn or re-prompts after an error, are keepgoing's defaults. The thread shows the exact numbers once the loop is created.</span>
      </div>
    </details>
    {error && <span role="alert">{error}</span>}
  </section>;
}
