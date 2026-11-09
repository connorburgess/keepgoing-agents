// Shows keepgoing's state as a row in each supervised thread's context card.
import {
  definePluginApp,
  experimental_THREAD_INFO_ROW_GROUPS as ROW_GROUPS,
  experimental_ThreadContextCardRow as ContextRow,
  useRealtime,
  useRpc,
} from "@get-bb/plugin-sdk/app";
import { useCallback, useEffect, useState } from "react";
import type { LoopView, rpcContract } from "./server";

function label(loop: LoopView): string {
  if (loop.controlState === "enabled") return loop.supervising ? "keepgoing: running" : `keepgoing: watching (${loop.target})`;
  return `keepgoing: ${loop.controlState}`;
}

function KeepgoingRow({ threadId }: { threadId: string }) {
  const rpc = useRpc<typeof rpcContract>();
  const [loops, setLoops] = useState<LoopView[]>([]);
  const [busy, setBusy] = useState(false);
  const refresh = useCallback(() => {
    rpc
      .call("forThread", { threadId })
      .then((result) => setLoops(result.loops))
      .catch(() => setLoops([]));
  }, [rpc, threadId]);
  useEffect(() => {
    refresh();
    const timer = setInterval(refresh, 30_000);
    return () => clearInterval(timer);
  }, [refresh]);
  useRealtime("changed", refresh);

  const loop = loops[0];
  if (!loop) return null;
  const paused = loop.controlState !== "enabled";
  const act = (action: "pause" | "resume") => {
    setBusy(true);
    rpc
      .call("control", { configPath: loop.configPath, action })
      .finally(() => {
        setBusy(false);
        refresh();
      });
  };
  const title = [
    `Loop ${loop.loop}`,
    loop.note ? `Note: ${loop.note}` : null,
    loop.lastWakeAt ? `Last sent ${loop.lastWakeAt}` : "Nothing sent yet",
    loop.fallbacks ? `Fallbacks: ${loop.fallbacks}` : null,
  ]
    .filter(Boolean)
    .join("\n");
  return (
    <ContextRow
      icon="Repeat"
      label={label(loop)}
      title={title}
      trailing={loop.handoffs > 0 ? `${loop.handoffs} handoffs` : undefined}
      action={
        <button type="button" disabled={busy} onClick={() => act(paused ? "resume" : "pause")}>
          {paused ? "Resume" : "Pause"}
        </button>
      }
    />
  );
}

export default definePluginApp((app) => {
  app.slots.experimental_threadContextCardRow({
    id: "keepgoing",
    group: ROW_GROUPS.lifecycle,
    order: 50,
    component: KeepgoingRow,
  });
});
