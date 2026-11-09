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
  if (loop.controlState === "enabled") return loop.supervising ? "keepgoing: running" : `keepgoing: watching (${loop.target})`;
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

function ToggleButton({ loop, onDone }: { loop: LoopView; onDone: () => void }) {
  const rpc = useRpc<typeof rpcContract>();
  const [busy, setBusy] = useState(false);
  const paused = loop.controlState !== "enabled";
  const act = () => {
    setBusy(true);
    rpc
      .call("control", { configPath: loop.configPath, action: paused ? "resume" : "pause" })
      .finally(() => {
        setBusy(false);
        onDone();
      });
  };
  return (
    <button type="button" disabled={busy} onClick={act} style={buttonStyle}>
      {paused ? "Resume" : "Pause"}
    </button>
  );
}

function CardRow({ threadId }: { threadId: string }) {
  const { loop, refresh } = useLoop(threadId);
  if (!loop) return null;
  return (
    <ContextRow
      icon="Repeat"
      label={label(loop)}
      title={describe(loop).join("\n")}
      trailing={loop.handoffs > 0 ? `${loop.handoffs} handoffs` : undefined}
      action={<ToggleButton loop={loop} onDone={refresh} />}
    />
  );
}

function BannerSummary({ threadId, isExpanded, onToggle }: { threadId: string; isExpanded: boolean; onToggle(): void }) {
  const { loop } = useLoop(threadId);
  if (!loop) return null;
  return (
    <button type="button" onClick={onToggle} aria-expanded={isExpanded} title={describe(loop).join("\n")}
      style={{ background: "none", border: 0, padding: 0, font: "inherit", color: "inherit", cursor: "pointer" }}>
      {label(loop)}
    </button>
  );
}

function BannerDetails({ threadId, isExpanded }: { threadId: string; isExpanded: boolean }) {
  const { loop, refresh } = useLoop(threadId);
  if (!loop || !isExpanded) return null;
  return (
    <div style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap", padding: "4px 12px 6px", fontSize: 12, opacity: 0.8 }}>
      <span>{describe(loop).join(" · ")}</span>
      <ToggleButton loop={loop} onDone={refresh} />
    </div>
  );
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
    // Show in the banner only when bb is not showing the card, and only for
    // threads a configured loop supervises.
    useVisible: ({ threadId }) => {
      const kind = useContextKind();
      const { loop } = useLoop(threadId);
      return kind === "banner" && loop !== undefined;
    },
  });
});
