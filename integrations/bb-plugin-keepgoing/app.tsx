// Shows keepgoing's state beside each supervised thread: a row in the thread
// context card when bb shows the card, and a segment in the context banner
// above the composer when it shows the banner. On the New thread screen it
// adds the Keep going switch and its banner to the composer.
import {
  definePluginApp,
  experimental_THREAD_INFO_ROW_GROUPS as ROW_GROUPS,
  experimental_ThreadContextCardRow as ContextRow,
  experimental_useThreadContextKind as useContextKind,
  useComposer,
  useRealtime,
  useRpc,
} from "@get-bb/plugin-sdk/app";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import type { LoopView, rpcContract } from "./server";
import { diagnosticLines, dateLabel } from "./status-details";
import { SetupPanel } from "./setup-panel";
import { TransferPanel } from "./transfer-panel";
import { KeepGoingBanner, KeepGoingSwitch, turnOn } from "./new-thread-control";
import { NewThreadOutcome, outcomeLabel, useOutcome } from "./new-thread-outcome";

type Rpc = ReturnType<typeof useRpc<typeof rpcContract>>;

// One cached lookup per thread, shared by the card row, the banner segment,
// and its visibility check, so a thread is fetched once per refresh.
const STALE_MS = 25_000;
const cache = new Map<string, { loops: LoopView[]; at: number; inflight: boolean; failed: boolean }>();
const listeners = new Set<() => void>();
const notify = () => {
  for (const l of listeners) l();
};
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

function fetchLoops(rpc: Rpc, threadId: string, force = false) {
  const entry = cache.get(threadId);
  if (entry?.inflight || (!force && entry && Date.now() - entry.at < STALE_MS)) return;
  cache.set(threadId, { loops: entry?.loops ?? [], at: entry?.at ?? 0, inflight: true, failed: entry?.failed ?? false });
  rpc
    .call("forThread", { threadId })
    .then((result) => cache.set(threadId, { loops: result.loops, at: Date.now(), inflight: false, failed: result.errors.length > 0 }))
    .catch(() => cache.set(threadId, { loops: entry?.loops ?? [], at: Date.now(), inflight: false, failed: true }))
    .finally(notify);
}

function useLoop(threadId: string) {
  const rpc = useRpc<typeof rpcContract>();
  const entry = useSyncExternalStore(subscribe, () => cache.get(threadId));
  const refresh = useCallback(() => fetchLoops(rpc, threadId, true), [rpc, threadId]);
  useEffect(() => {
    fetchLoops(rpc, threadId);
    const timer = setInterval(() => fetchLoops(rpc, threadId), 30_000);
    return () => clearInterval(timer);
  }, [rpc, threadId]);
  useRealtime("changed", refresh);
  return { loop: entry?.loops[0], refresh, failed: entry?.failed ?? false, loaded: !!entry?.at };
}

function label(loop: LoopView): string {
  if (loop.controlState === "enabled") return loop.supervising ? "keepgoing: enabled" : `keepgoing: watching ${loop.details?.targetThread ?? loop.target}`;
  return `keepgoing: ${loop.controlState}`;
}

function describe(loop: LoopView): string[] {
  return [
    `Loop ${loop.loop}`,
    ...diagnosticLines(loop.details, loop.target, loop.supervising),
    loop.note ? `Note: ${loop.note}` : null,
    dateLabel(loop.lastWakeAt) !== "unavailable" ? `Last sent ${dateLabel(loop.lastWakeAt)}` : "Last sent: unavailable",
    loop.handoffs > 0 ? `${loop.handoffs} handoffs` : null,
    loop.fallbacks ? `Fallbacks: ${loop.fallbacks}` : null,
  ].filter((line): line is string => line !== null);
}

const buttonStyle = {
  background: "none",
  color: "inherit",
  font: "inherit",
  fontSize: 12,
  lineHeight: "18px",
  padding: "0 8px",
  border: "1px solid currentColor",
  borderRadius: 6,
  opacity: 0.85,
  cursor: "pointer",
} as const;

function ToggleButton({ loop, threadId, onDone }: { loop: LoopView; threadId: string; onDone: () => void }) {
  const rpc = useRpc<typeof rpcContract>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const paused = loop.controlState !== "enabled";
  if (loop.managedId && ["disabled", "move requires inspection"].includes(loop.controlState)) return null;
  const act = () => {
    setBusy(true);
    setError("");
    rpc
      .call("control", { configPath: loop.configPath, action: paused ? "resume" : "pause", managedId: loop.managedId, expectedThreadId: threadId })
      .then(result => { if (!result.ok) setError(result.message); })
      .catch(() => setError("Control could not be confirmed. Refresh status before retrying."))
      .finally(() => {
        setBusy(false);
        onDone();
      });
  };
  return <>
    <button type="button" disabled={busy} onClick={act} style={buttonStyle}>
      {paused ? "Resume" : "Pause"}
    </button>
    {error && <span role="alert">{error}</span>}
  </>;
}

function ThreadSetup({ threadId, onChanged, hideStart = false }: { threadId: string; onChanged(): void; hideStart?: boolean }) {
  const rpc = useRpc<typeof rpcContract>();
  const call = useCallback((input: Parameters<typeof rpc.call<"setup">>[1]) => rpc.call("setup", input), [rpc]);
  const [generation, setGeneration] = useState(0);
  const transfer = useCallback(async (input: Parameters<typeof rpc.call<"transfer">>[1]) => {
    const result = await rpc.call("transfer", input);
    if (result.moved) setGeneration(n => n + 1);
    return result;
  }, [rpc]);
  return <><SetupPanel key={`${threadId}-${generation}`} threadId={threadId} call={call} onChanged={onChanged} hideStart={hideStart} />
    <TransferPanel key={`move-${threadId}`} threadId={threadId} call={transfer} onChanged={onChanged} /></>;
}

function StatusWarning({ refresh }: { refresh(): void }) {
  return <div role="alert">Status could not be verified for every registered loop. Controls and setup are hidden until refreshed. <button type="button" style={buttonStyle} onClick={refresh}>Refresh status</button></div>;
}

// What a thread created with Keep going on shows about that request.
function useThreadOutcome(threadId: string, loop: LoopView | undefined) {
  const rpc = useRpc<typeof rpcContract>();
  const call = useCallback((input: Parameters<typeof rpc.call<"newThread">>[1]) => rpc.call("newThread", input), [rpc]);
  const start = useCallback((input: { action: "start"; threadId: string }) => rpc.call("setup", input), [rpc]);
  const { outcome } = useOutcome(threadId, call, `${loop?.controlState ?? ""}:${loop?.checkedAt ?? ""}`);
  // Pause and stop go to the loop's own recorded configuration, as the ordinary controls do.
  const control = useCallback((action: "pause" | "stop") => loop
    ? rpc.call("control", { configPath: loop.configPath, action, managedId: loop.managedId, expectedThreadId: threadId })
    : Promise.resolve({ ok: false, message: "Status is not loaded yet." }), [rpc, loop, threadId]);
  return { outcome, start, control };
}

const READY_NOT_STARTED = "keepgoing: ready, not started";
// A thread started from the New thread screen carries its own Pause and Stop.
const KEEPING_GOING = "keepgoing: keeping this going";

function CardRow({ threadId }: { threadId: string }) {
  const { loop, refresh, failed, loaded } = useLoop(threadId);
  const { outcome, start, control } = useThreadOutcome(threadId, loop);
  const created = outcomeLabel(outcome, loop?.controlState, loop?.supervising);
  const [open, setOpen] = useState(false);
  return <div>
    {failed && <StatusWarning refresh={refresh} />}
    {loaded && !failed && <NewThreadOutcome threadId={threadId} outcome={outcome} controlState={loop?.controlState} supervising={loop?.supervising} start={start} control={control} onChanged={refresh} />}
    {!loaded ? <ContextRow icon="Repeat" label="keepgoing: checking status" /> : !loop ? <ContextRow icon="Repeat" label="keepgoing" action={!failed && <button type="button" style={buttonStyle} onClick={() => setOpen(!open)} aria-expanded={open}>Supervise this thread</button>} /> : <ContextRow
      icon="Repeat"
      label={failed ? "keepgoing: status unavailable (previous snapshot)" : created ?? label(loop)}
      title={describe(loop).join("\n")}
      trailing={loop.handoffs > 0 ? `${loop.handoffs} handoffs` : undefined}
      action={<>{!failed && created !== KEEPING_GOING && <ToggleButton loop={loop} threadId={threadId} onDone={refresh} />}{!failed && loop.managedId && <button type="button" style={buttonStyle} onClick={() => setOpen(!open)} aria-expanded={open}>Setup</button>}</>}
    />}
    {open && <div hidden={failed}><ThreadSetup key={threadId} threadId={threadId} onChanged={refresh} hideStart={created === READY_NOT_STARTED} /></div>}
  </div>;
}

function BannerSummary({ threadId, isExpanded, onToggle }: { threadId: string; isExpanded: boolean; onToggle(): void }) {
  const { loop, failed, loaded } = useLoop(threadId);
  const { outcome } = useThreadOutcome(threadId, loop);
  const created = outcomeLabel(outcome, loop?.controlState, loop?.supervising);
  return (
    <button type="button" onClick={onToggle} aria-expanded={isExpanded} title={failed ? "Status could not be refreshed. Open details to retry." : loop ? describe(loop).join("\n") : "Set up a loop for this thread"}
      style={{ background: "none", border: 0, padding: 0, font: "inherit", color: "inherit", cursor: "pointer" }}>
      {failed ? "keepgoing: status unavailable" : !loaded ? "keepgoing: checking status" : created ?? (loop ? label(loop) : "Supervise this thread")}
    </button>
  );
}

function BannerDetails({ threadId, isExpanded }: { threadId: string; isExpanded: boolean }) {
  const { loop, refresh, failed, loaded } = useLoop(threadId);
  const { outcome, start, control } = useThreadOutcome(threadId, loop);
  const created = outcomeLabel(outcome, loop?.controlState, loop?.supervising);
  if (!isExpanded) return null;
  return <>
    {failed && <StatusWarning refresh={refresh} />}
    {loaded && !failed && <NewThreadOutcome threadId={threadId} outcome={outcome} controlState={loop?.controlState} supervising={loop?.supervising} start={start} control={control} onChanged={refresh} />}
    {loop && <div style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap", padding: "4px 12px 6px", fontSize: 12, opacity: 0.8 }}>
      <div>{failed && <div>Previous snapshot</div>}{describe(loop).map((line, i) => <div key={i}>{line}</div>)}</div>
      {!failed && created !== KEEPING_GOING && <ToggleButton loop={loop} threadId={threadId} onDone={refresh} />}
    </div>}
    {loaded && (!loop || loop.managedId) && <div hidden={failed}><ThreadSetup key={threadId} threadId={threadId} onChanged={refresh} hideStart={created === READY_NOT_STARTED} /></div>}
  </>;
}

function ComposerSwitch() {
  return <KeepGoingSwitch composer={useComposer()} />;
}

function ComposerBanner() {
  const rpc = useRpc<typeof rpcContract>();
  const call = useCallback((input: Parameters<typeof rpc.call<"newThread">>[1]) => rpc.call("newThread", input), [rpc]);
  return <KeepGoingBanner composer={useComposer()} call={call} />;
}

export default definePluginApp((app) => {
  // New thread screen only. The banner draws its own frame, so nothing shows
  // while the switch is off.
  app.composer.customize({
    id: "keepgoing-new-thread",
    scopes: ["new-thread"],
    actions: [{ id: "keep-going", component: ComposerSwitch }],
    banners: [{ id: "keep-going", chrome: "bare", component: ComposerBanner }],
    // The action row is not drawn in the compact layout; this row reaches the
    // same switch from the menu beside Send.
    sendMenu: [{
      id: "keep-going",
      label: "Keep going…",
      icon: "Repeat",
      description: "Supervise the thread this message creates",
      run: ({ composer }) => { if (composer.scope.kind === "new-thread") turnOn(composer.key); },
    }],
  });
  app.slots.experimental_threadContextCardRow({
    id: "keepgoing",
    group: ROW_GROUPS.lifecycle,
    order: 50,
    component: CardRow,
  });
  app.slots.experimental_threadContextSegment({
    id: "keepgoing",
    title: "keepgoing",
    group: "6_keepgoing",
    summary: BannerSummary,
    details: BannerDetails,
    // Offer setup on unsupervised threads using whichever context UI is active.
    useVisible: () => {
      const kind = useContextKind();
      return kind === "banner";
    },
  });
});
