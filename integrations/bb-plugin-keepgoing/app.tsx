// Shows keepgoing's state beside each supervised thread: a row in the thread
// context card when bb shows the card, and a segment in the context banner
// above the composer when it shows the banner.
import {
  definePluginApp,
  experimental_THREAD_INFO_ROW_GROUPS as ROW_GROUPS,
  experimental_ThreadContextCardRow as ContextRow,
  experimental_useThreadContextKind as useContextKind,
  useRealtime,
  useRpc,
} from "@get-bb/plugin-sdk/app";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import type { LoopView, rpcContract } from "./server";
import { SetupPanel } from "./setup-panel";
import { TransferPanel } from "./transfer-panel";

type Rpc = ReturnType<typeof useRpc<typeof rpcContract>>;

// One cached lookup per thread, shared by the card row, the banner segment,
// and its visibility check, so a thread is fetched once per refresh.
const STALE_MS = 25_000;
const cache = new Map<string, { loops: LoopView[]; at: number; inflight: boolean }>();
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
  cache.set(threadId, { loops: entry?.loops ?? [], at: entry?.at ?? 0, inflight: true });
  rpc
    .call("forThread", { threadId })
    .then((result) => cache.set(threadId, { loops: result.loops, at: Date.now(), inflight: false }))
    .catch(() => cache.set(threadId, { loops: [], at: Date.now(), inflight: false }))
    .finally(notify);
}

function useLoop(threadId: string): { loop: LoopView | undefined; refresh: () => void } {
  const rpc = useRpc<typeof rpcContract>();
  const loops = useSyncExternalStore(subscribe, () => cache.get(threadId)?.loops);
  const refresh = useCallback(() => fetchLoops(rpc, threadId, true), [rpc, threadId]);
  useEffect(() => {
    fetchLoops(rpc, threadId);
    const timer = setInterval(() => fetchLoops(rpc, threadId), 30_000);
    return () => clearInterval(timer);
  }, [rpc, threadId]);
  useRealtime("changed", refresh);
  return { loop: loops?.[0], refresh };
}

function label(loop: LoopView): string {
  if (loop.controlState === "enabled") return loop.supervising ? "keepgoing: enabled" : `keepgoing: watching (${loop.target})`;
  return `keepgoing: ${loop.controlState}`;
}

function describe(loop: LoopView): string[] {
  return [
    `Loop ${loop.loop}`,
    loop.note ? `Note: ${loop.note}` : null,
    loop.lastWakeAt ? `Last sent ${new Date(loop.lastWakeAt).toLocaleString()}` : "Nothing sent yet",
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

function ThreadSetup({ threadId, onChanged }: { threadId: string; onChanged(): void }) {
  const rpc = useRpc<typeof rpcContract>();
  const call = useCallback((input: Parameters<typeof rpc.call<"setup">>[1]) => rpc.call("setup", input), [rpc]);
  const [generation, setGeneration] = useState(0);
  const transfer = useCallback(async (input: Parameters<typeof rpc.call<"transfer">>[1]) => {
    const result = await rpc.call("transfer", input);
    if (result.moved) setGeneration(n => n + 1);
    return result;
  }, [rpc]);
  return <><SetupPanel key={`${threadId}-${generation}`} threadId={threadId} call={call} onChanged={onChanged} />
    <TransferPanel key={`move-${threadId}`} threadId={threadId} call={transfer} onChanged={onChanged} /></>;
}

function CardRow({ threadId }: { threadId: string }) {
  const { loop, refresh } = useLoop(threadId);
  const [open, setOpen] = useState(false);
  return <div>
    {!loop ? <ContextRow icon="Repeat" label="keepgoing" action={<button type="button" style={buttonStyle} onClick={() => setOpen(!open)} aria-expanded={open}>Supervise this thread</button>} /> : <ContextRow
      icon="Repeat"
      label={label(loop)}
      title={describe(loop).join("\n")}
      trailing={loop.handoffs > 0 ? `${loop.handoffs} handoffs` : undefined}
      action={<><ToggleButton loop={loop} threadId={threadId} onDone={refresh} />{loop.managedId && <button type="button" style={buttonStyle} onClick={() => setOpen(!open)} aria-expanded={open}>Setup</button>}</>}
    />}
    {open && <ThreadSetup key={threadId} threadId={threadId} onChanged={refresh} />}
  </div>;
}

function BannerSummary({ threadId, isExpanded, onToggle }: { threadId: string; isExpanded: boolean; onToggle(): void }) {
  const { loop } = useLoop(threadId);
  return (
    <button type="button" onClick={onToggle} aria-expanded={isExpanded} title={loop ? describe(loop).join("\n") : "Set up a loop for this thread"}
      style={{ background: "none", border: 0, padding: 0, font: "inherit", color: "inherit", cursor: "pointer" }}>
      {loop ? label(loop) : "Supervise this thread"}
    </button>
  );
}

function BannerDetails({ threadId, isExpanded }: { threadId: string; isExpanded: boolean }) {
  const { loop, refresh } = useLoop(threadId);
  if (!isExpanded) return null;
  return <>
    {loop && <div style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap", padding: "4px 12px 6px", fontSize: 12, opacity: 0.8 }}>
      <span>{describe(loop).join(" · ")}</span>
      <ToggleButton loop={loop} threadId={threadId} onDone={refresh} />
    </div>}
    {(!loop || loop.managedId) && <ThreadSetup key={threadId} threadId={threadId} onChanged={refresh} />}
  </>;
}

export default definePluginApp((app) => {
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
