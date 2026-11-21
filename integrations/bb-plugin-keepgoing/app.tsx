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
import { AttachPanel } from "./attach-panel";
import { TransferPanel } from "./transfer-panel";
import { KeepGoingBanner, KeepGoingSwitch, turnOn } from "./new-thread-control";
import { NewThreadOutcome, outcomeLabel, useOutcome } from "./new-thread-outcome";

type Rpc = ReturnType<typeof useRpc<typeof rpcContract>>;

// One cached lookup per thread, shared by the card row, the banner segment,
// and its visibility check, so a thread is fetched once per refresh.
const STALE_MS = 25_000;
const cache = new Map<string, { loops: LoopView[]; at: number; inflight: boolean; failed: boolean; references: Map<string, number> }>();
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
  cache.set(threadId, { loops: entry?.loops ?? [], at: entry?.at ?? 0, inflight: true, failed: entry?.failed ?? false, references: entry?.references ?? new Map() });
  rpc
    .call("forThread", { threadId })
    .then((result) => {
      const references = new Map(entry?.references);
      for (const loop of result.loops) if (!references.has(loopKey(loop))) references.set(loopKey(loop), references.size + 1);
      cache.set(threadId, { loops: result.loops, at: Date.now(), inflight: false, failed: result.errors.length > 0, references });
    })
    .catch(() => cache.set(threadId, { loops: entry?.loops ?? [], at: Date.now(), inflight: false, failed: true, references: entry?.references ?? new Map() }))
    .finally(notify);
}

function useLoops(threadId: string) {
  const rpc = useRpc<typeof rpcContract>();
  const entry = useSyncExternalStore(subscribe, () => cache.get(threadId));
  const refresh = useCallback(() => fetchLoops(rpc, threadId, true), [rpc, threadId]);
  useEffect(() => {
    fetchLoops(rpc, threadId);
    const timer = setInterval(() => fetchLoops(rpc, threadId), 30_000);
    return () => clearInterval(timer);
  }, [rpc, threadId]);
  useRealtime("changed", refresh);
  return { loops: entry?.loops ?? [], references: entry?.references ?? new Map<string, number>(), refresh, failed: entry?.failed ?? false, loaded: !!entry?.at };
}

function label(loop: LoopView): string {
  if (loop.imported && loop.controlState === "enabled" && ((loop.details?.failedChecks ?? 0) > 0 || ["failed", "error"].includes(loop.details?.lastCheck?.outcome ?? ""))) return "looptender: checks failing";
  if (loop.imported && loop.controlState === "enabled" && !loop.details?.lastCheck) return "looptender: no check recorded";
  if (loop.controlState === "enabled" && loop.details?.schedule?.overdue) return "looptender: checks have stopped";
  if (loop.controlState === "enabled") return loop.supervising ? "looptender: enabled" : `looptender: watching ${loop.details?.targetThread ?? loop.target}`;
  return `looptender: ${loop.controlState}`;
}

function describe(loop: LoopView): string[] {
  return [
    `Loop ${loop.loop}`,
    loop.referenceSource === "plugin_setting" ? "Reference: plugin setting; CLI menu registration checked separately" : loop.referenceSource === "registry_attachment" ? "Reference: explicitly attached from CLI registry" : loop.referenceSource === "plugin_setup" ? "Reference: saved BB setup" : "Reference source: unavailable",
    loop.publication?.state === "registered" ? "Last explicit CLI menu publication: registered; current health checked separately" : loop.referenceSource === "plugin_setup" ? "CLI menu publication: unverified; use explicit registry reconcile on the host" : null,
    ...diagnosticLines(loop.details, loop.target, loop.supervising),
    loop.note ? `Note: ${loop.note}` : null,
    loop.details?.schedule?.overdue ? `Needs you: ${loop.details.schedule.message ?? "no check has run for longer than usual"}` : null,
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
      .call("control", { configPath: loop.configPath, action: "pause", managedId: loop.managedId, expectedThreadId: threadId })
      .then(result => { if (!result.ok) setError(result.message); })
      .catch(() => setError("Control could not be confirmed. Refresh status before retrying."))
      .finally(() => {
        setBusy(false);
        onDone();
      });
  };
  return <>
    {/* Resume is a person's, from the panel or a terminal: the plugin
        cannot tell a click from the supervised session. */}
    {paused
      ? <span>A person resumes this from an ordinary terminal: <code>keepgoing resume -c {JSON.stringify(loop.configPath)}</code>, or the looptender panel started there.</span>
      : <button type="button" disabled={busy} onClick={act} style={buttonStyle}>Pause</button>}
    {error && <span role="alert">{error}</span>}
  </>;
}

function ThreadSetup({ threadId, onChanged, hideStart = false }: { threadId: string; onChanged(): void; hideStart?: boolean }) {
  const rpc = useRpc<typeof rpcContract>();
  const call = useCallback((input: Parameters<typeof rpc.call<"setup">>[1]) => rpc.call("setup", input), [rpc]);
  const [generation, setGeneration] = useState(0);
  const attach = useCallback(async (input: Parameters<typeof rpc.call<"attachment">>[1]) => {
    const result = await rpc.call("attachment", input);
    if (result.attached) setGeneration(n => n + 1);
    return result;
  }, [rpc]);
  const transfer = useCallback(async (input: Parameters<typeof rpc.call<"transfer">>[1]) => {
    const result = await rpc.call("transfer", input);
    if (result.moved) setGeneration(n => n + 1);
    return result;
  }, [rpc]);
  return <><AttachPanel key={`attach-${threadId}`} threadId={threadId} call={attach} onChanged={onChanged} />
    <SetupPanel key={`${threadId}-${generation}`} threadId={threadId} call={call} onChanged={onChanged} hideStart={hideStart} />
    <TransferPanel key={`move-${threadId}`} threadId={threadId} call={transfer} onChanged={onChanged} /></>;
}

function StatusWarning({ refresh }: { refresh(): void }) {
  return <div role="alert">Status could not be verified for every registered loop. Controls and setup are hidden until refreshed. <button type="button" style={buttonStyle} onClick={refresh}>Refresh status</button></div>;
}

// What a thread created with Keep going on shows about that request.
function useThreadOutcome(threadId: string, loops: LoopView[]) {
  const rpc = useRpc<typeof rpcContract>();
  const call = useCallback((input: Parameters<typeof rpc.call<"newThread">>[1]) => rpc.call("newThread", input), [rpc]);
  const start = useCallback((input: { action: "start"; threadId: string }) => rpc.call("setup", input), [rpc]);
  const { outcome } = useOutcome(threadId, call, JSON.stringify(loops.map(l => [l.configPath, l.managedId, l.controlState, l.checkedAt])));
  // New-thread controls belong only to one unambiguous saved setup for this
  // thread, never the first reference or a loop inferred from its display name.
  const setups = loops.filter(l => l.managedThreadId === threadId && l.managedId && !l.imported && (!l.referenceSource || l.referenceSource === "plugin_setup"));
  const loop = setups.length === 1 ? setups[0] : undefined;
  // Pause and stop go to the loop's own recorded configuration, as the ordinary controls do.
  const control = useCallback((action: "pause" | "stop") => loop
    ? rpc.call("control", { configPath: loop.configPath, action, managedId: loop.managedId, expectedThreadId: threadId })
    : Promise.resolve({ ok: false, message: "Status is not loaded yet." }), [rpc, loop, threadId]);
  return { outcome, start, control, loop };
}

const READY_NOT_STARTED = "looptender: ready, not started";
// A thread started from the New thread screen carries its own Pause and Stop.
const KEEPING_GOING = "looptender: keeping this going";

// Stable record/config identity keeps pending controls attached to the same loop
// when a status refresh changes order or removes another reference.
function loopKey(loop: LoopView) { return JSON.stringify([loop.configPath, loop.managedId ?? null]); }

function referenceName(loop: LoopView, loops: LoopView[], references: Map<string, number>) {
  return loops.filter(l => l.loop === loop.loop).length > 1 ? `${loop.loop} · reference ${references.get(loopKey(loop))}` : loop.loop;
}

function ReferenceDetails({ loop, name }: { loop: LoopView; name: string }) {
  return name === loop.loop ? null : <details><summary>Configuration reference</summary><code>{loop.configPath}</code><div>Reference numbers identify rows in this plugin session; they are not loop IDs.</div></details>;
}

function CardLoop({ loop, name, threadId, refresh, failed, created, showName }: { loop: LoopView; name: string; threadId: string; refresh(): void; failed: boolean; created: string | null; showName: boolean }) {
  return <section aria-label={`Loop ${name}`} style={{ overflowWrap: "anywhere", minWidth: 0 }}>
    {showName && <strong style={{ fontSize: 12 }}>{name}</strong>}
    <ReferenceDetails loop={loop} name={name} />
    <ContextRow icon="Repeat" label={failed ? "looptender: status unavailable (previous snapshot)" : created ?? label(loop)}
      title={describe(loop).join("\n")} trailing={loop.handoffs > 0 ? `${loop.handoffs} handoffs` : undefined}
      action={<>{!failed && created !== KEEPING_GOING && <ToggleButton loop={loop} threadId={threadId} onDone={refresh} />}</>} />
  </section>;
}

function CardRow({ threadId }: { threadId: string }) {
  const { loops, references, refresh, failed, loaded } = useLoops(threadId);
  const { outcome, start, control, loop } = useThreadOutcome(threadId, loops);
  const created = outcomeLabel(outcome, loop?.controlState, loop?.supervising);
  const [open, setOpen] = useState(false);
  return <div>
    {failed && <StatusWarning refresh={refresh} />}
    {loaded && !failed && <NewThreadOutcome threadId={threadId} outcome={outcome} controlState={loop?.controlState} supervising={loop?.supervising} start={start} control={control} onChanged={refresh} />}
    {!loaded ? <ContextRow icon="Repeat" label="looptender: checking status" /> : !loops.length ? <ContextRow icon="Repeat" label="looptender" action={!failed && <button type="button" style={buttonStyle} onClick={() => setOpen(!open)} aria-expanded={open}>Add to Looptender</button>} /> : loops.map(l => <CardLoop key={loopKey(l)} loop={l} name={referenceName(l, loops, references)} threadId={threadId} refresh={refresh} failed={failed} created={l === loop ? created : null} showName={loops.length > 1} />)}
    {!failed && loops.some(l => l.managedId) && <button type="button" style={buttonStyle} onClick={() => setOpen(!open)} aria-expanded={open}>{loops.length > 1 ? "Thread setup" : "Setup"}</button>}
    {open && <div hidden={failed}><ThreadSetup key={threadId} threadId={threadId} onChanged={refresh} hideStart={created === READY_NOT_STARTED} /></div>}
  </div>;
}

function BannerSummary({ threadId, isExpanded, onToggle }: { threadId: string; isExpanded: boolean; onToggle(): void }) {
  const { loops, failed, loaded } = useLoops(threadId);
  const { outcome, loop } = useThreadOutcome(threadId, loops);
  const created = outcomeLabel(outcome, loop?.controlState, loop?.supervising);
  const only = loops.length === 1 ? loops[0] : undefined;
  return <button type="button" onClick={onToggle} aria-expanded={isExpanded} title={failed ? "Status could not be refreshed. Open details to retry." : loops.length ? loops.map(l => describe(l).join("\n")).join("\n\n") : "Set up a loop for this thread"}
    style={{ background: "none", border: 0, padding: 0, font: "inherit", color: "inherit", cursor: "pointer" }}>
    {failed ? "looptender: status unavailable" : !loaded ? "looptender: checking status" : loops.length > 1 ? `looptender: ${loops.length} matching loops` : created ?? (only ? label(only) : "Add to Looptender")}
  </button>;
}

function BannerDetails({ threadId, isExpanded }: { threadId: string; isExpanded: boolean }) {
  const { loops, references, refresh, failed, loaded } = useLoops(threadId);
  const { outcome, start, control, loop } = useThreadOutcome(threadId, loops);
  const created = outcomeLabel(outcome, loop?.controlState, loop?.supervising);
  if (!isExpanded) return null;
  return <>
    {failed && <StatusWarning refresh={refresh} />}
    {loaded && !failed && <NewThreadOutcome threadId={threadId} outcome={outcome} controlState={loop?.controlState} supervising={loop?.supervising} start={start} control={control} onChanged={refresh} />}
    {loops.map(l => <section key={loopKey(l)} aria-label={`Loop ${referenceName(l, loops, references)}`} style={{ display: "flex", gap: 10, alignItems: "center", flexWrap: "wrap", padding: "4px 12px 6px", fontSize: 12, opacity: 0.8, overflowWrap: "anywhere", minWidth: 0 }}>
      <div style={{ minWidth: 0, flex: "1 1 220px" }}>{failed && <div>Previous snapshot</div>}{describe(l).map((line, i) => <div key={i}>{i === 0 ? `Loop ${referenceName(l, loops, references)}` : line}</div>)}<ReferenceDetails loop={l} name={referenceName(l, loops, references)} /><div>{l === loop ? created ?? label(l) : label(l)}</div></div>
      {!failed && !(l === loop && created === KEEPING_GOING) && <ToggleButton loop={l} threadId={threadId} onDone={refresh} />}
    </section>)}
    {loaded && (!loops.length || loops.some(l => l.managedId)) && <div hidden={failed}><ThreadSetup key={threadId} threadId={threadId} onChanged={refresh} hideStart={created === READY_NOT_STARTED} /></div>}
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
    title: "looptender",
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
