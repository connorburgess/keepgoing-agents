// Carries "keep going" from the New thread screen to the thread it creates.
//
// The link between the request and the thread is exact or absent. The browser
// asks for a single-use request when the person presses the button, then
// sends the message itself with that request as `experimental_data`; BB hands
// the marker back to the dispatch hook together with the thread it created.
// Nothing is ever matched by project, time or text. A thread that arrives
// without a valid marker gets no supervision, and shows the ordinary offer.
//
// A send that BB did not deliver never leads to supervision: setup waits for
// the thread's first turn to have started, the browser withdraws the request
// when the send is rejected, a different first message on the thread ends
// the request, and a request whose thread never starts expires.
//
// The plugin never creates, sends to or changes a thread. The task text is
// held in memory only until setup has been asked to create the loop, and
// reaches keepgoing through the existing setup flow, on stdin.
import type { SetupResult } from "./setup-contract.ts";
import { boundsPhrase, loopName, markerSchema, newThreadInput, planBounds, sameBounds, stricter, type Bounds, type NewThreadInput, type NewThreadResult, type Outcome, type StartMode } from "./new-thread-contract.ts";

export const INTENT_TTL_MS = 120_000;
export const JOB_WAIT_MS = 600_000;
export const MAX_INTENTS = 16;
const MAX_JOBS = 32;

/** The facts of one dispatch the server copies out of BB's hook context. */
export interface Dispatch {
  data: unknown;
  threadId: string; projectId: string; threadCreatedAt: number;
  attempt: string; initiator: string; senderThreadId: string | null; parentThreadId: string | null; sourceThreadId: string | null;
  queued: number; text: string;
  /** The thread's status as BB reports it in the hook: "pending" until its first turn starts. */
  threadStatus: string;
}
type SetupCall = { action: "preview" | "create" | "start"; threadId: string; name?: string; task?: string; ticket?: string; withGoal?: boolean };
export interface NewThreadDependencies {
  now(): number;
  /** A fresh random identifier. */
  id(): string;
  mode(): Promise<StartMode>;
  /** Null when keepgoing can be used; otherwise why not, in words safe to show. */
  unavailable(): Promise<string | null>;
  /** How many setups the plugin holds, of the most it can hold. */
  capacity(): Promise<{ used: number; max: number }>;
  /**
   * The thread's project directory once its first turn has started and its
   * environment and host are ready for setup; null while they are not; a
   * reason when they never will be.
   */
  ready(threadId: string): Promise<{ project: string } | { reason: string } | null>;
  /** "none" when no loop supervises the thread; "unverified" when that could not be established. */
  supervision(threadId: string): Promise<"none" | "supervised" | "unverified">;
  setup(input: SetupCall, bounds: Bounds): Promise<SetupResult>;
  changed(): void;
}
interface Intent { id: string; draftKey: string; projectId: string; bounds: Bounds; mode: StartMode; createdAt: number; expires: number; threadId?: string }
interface Job {
  threadId: string; task: string; bounds: Bounds; mode: StartMode; expires: number; outcome: Outcome;
  /** Set when the request was ended from outside a setup pass: nothing more may be done for it. */
  withdrawn?: string;
}

const NOT_READY = "This thread's first turn did not start in time, so looptender set nothing up. Use Supervise this thread to set it up here.";
const NOT_MATCHED = "looptender could not confirm that this thread is the one Send and keep going was pressed for, so nothing was created or started. Use Supervise this thread to set it up here.";
const NOT_SENT = "The message may not have been sent, so looptender set nothing up and started nothing.";
const LIMITS = "Setup did not report the loop-wide limits that were asked for, so looptender did not create it. Nothing was created or started.";
const FULL = (c: { used: number; max: number }) => `This plugin already holds ${c.used} of ${c.max} setups, which is the most it can hold, and it has no way to remove one yet. Keep going cannot create another.`;

export function createNewThreadController(d: NewThreadDependencies) {
  const intents = new Map<string, Intent>();
  const jobs = new Map<string, Job>();
  let queue: Promise<unknown> = Promise.resolve();
  const serialize = <T>(fn: () => Promise<T>): Promise<T> => { const next = queue.then(fn, fn); queue = next.catch(() => {}); return next; };
  const fail = (job: Job, message: string, extra: Partial<Outcome> = {}) => { job.task = ""; job.outcome = { state: "failed", message, ...extra }; };
  // Ends a request from outside a setup pass. A pass that is running sees the
  // mark at its next step and does nothing more.
  const withdraw = (job: Job, message: string) => { job.withdrawn = message; fail(job, message); d.changed(); };
  // Setup passes run one at a time per thread; threads do not wait for each other.
  const chains = new Map<string, Promise<void>>();
  const queue_ = (threadId: string): Promise<void> => {
    const next = (chains.get(threadId) ?? Promise.resolve()).then(() => advance(threadId)).catch(() => {});
    chains.set(threadId, next);
    void next.then(() => { if (chains.get(threadId) === next) chains.delete(threadId); });
    return next;
  };
  const prune = () => {
    const now = d.now();
    for (const [key, i] of intents) if (i.expires < now) intents.delete(key);
    // A request nobody advanced in time must not keep its task text.
    for (const job of jobs.values()) if (job.outcome.state === "waiting" && job.expires < now) fail(job, NOT_READY);
    // Finished records are kept for display only; drop the oldest beyond the cap.
    for (const key of jobs.keys()) { if (jobs.size <= MAX_JOBS) break; if (jobs.get(key)?.outcome.state !== "waiting") jobs.delete(key); }
  };
  const explain = (threadId: string, message: string) => {
    if (jobs.size < MAX_JOBS * 2) jobs.set(threadId, { threadId, task: "", bounds: { maxSends: 1, noProgressTimeout: "2h" }, mode: "manual", expires: 0, outcome: { state: "failed", message } });
  };

  // One pass over a waiting job. Nothing happens until the thread's first
  // turn has started and its environment and host are ready, and nothing is
  // retried once setup has been asked to create anything.
  const advance = async (threadId: string) => {
    const job = jobs.get(threadId);
    if (!job || job.outcome.state !== "waiting") return;
    if (d.now() > job.expires) { fail(job, NOT_READY); d.changed(); return; }
    let ready: Awaited<ReturnType<NewThreadDependencies["ready"]>>;
    try { ready = await d.ready(threadId); } catch { ready = null; }
    if (!ready) return;
    if ("reason" in ready) { fail(job, ready.reason); d.changed(); return; }
    const task = job.task;
    let attempted: { loop: string; configPath: string } | undefined;
    try {
      const supervision = await d.supervision(threadId);
      if (supervision !== "none") { fail(job, supervision === "supervised" ? "Another loop already supervises this thread. Nothing was created or started." : "looptender could not check whether this thread is already supervised, so it set nothing up. Refresh, then use Supervise this thread."); return; }
      const capacity = await d.capacity();
      if (capacity.used >= capacity.max) { fail(job, FULL(capacity)); return; }
      const preview = await d.setup({ action: "preview", threadId, name: loopName(ready.project, d.id()), task, withGoal: true }, job.bounds);
      if (!preview.ok || !preview.ticket || !preview.plan) { fail(job, preview.message || "Setup could not be previewed. Nothing was created."); return; }
      // The request may have been withdrawn while the preview ran.
      if (jobs.get(threadId) !== job || job.withdrawn) return;
      // The limits the setup will actually have, from keepgoing's own plan.
      // They must be exactly the ones asked for; what the thread shows later
      // comes from here, not from the request.
      const planned = planBounds(preview.plan.limits);
      if (!planned || !sameBounds(planned, job.bounds)) { fail(job, LIMITS); return; }
      attempted = { loop: preview.plan.loop, configPath: preview.plan.configPath };
      const created = await d.setup({ action: "create", threadId, ticket: preview.ticket, task, withGoal: true }, job.bounds);
      job.task = "";
      // keepgoing refused before writing anything: its words, nothing more.
      if (created.refused) { fail(job, created.message); return; }
      if (!created.ok || !created.record) {
        // Creation was asked for and not confirmed: something may exist on the
        // host. Say what and where, so it is not left behind unseen.
        fail(job, `${created.message || "Setup could not be confirmed."} A switched-off setup named ${attempted.loop} may exist on this thread's host and was not started. Check it there with keepgoing status -c (looptender's command in releases up to v0.2.0) on the path below; to discard it, delete its directory.`, attempted);
        return;
      }
      const l = preview.plan.limits;
      job.outcome = {
        state: "ready", loop: created.record.loop, bounds: planned,
        cadence: `Checks every ${l.interval}. Nudges a stalled turn after ${l.stallAfter}, at most ${l.maxNudges} times. Re-prompts an error at most ${l.maxReprompts} times, ${l.backoff} apart. No fallbacks.`,
        message: created.canStart ? "Created from this thread's first message. It is switched off until you start it." : `Created from this thread's first message and left switched off: ${created.message}`,
        startedAutomatically: false,
      };
      // Automatic start only when the banner showed that mode when the button
      // was pressed, it is still the setting now, and setup's own diagnostics
      // allow a start. One attempt: anything uncertain leaves the setup for
      // the person to review and start, with the reason.
      if (job.withdrawn) {
        // Withdrawn while the setup was being created: it exists, and it stays
        // switched off.
        job.outcome = { ...job.outcome, message: `Created from this thread's first message and left switched off, because the request was withdrawn while it was being set up: ${job.withdrawn}` };
        return;
      }
      if (job.mode === "automatic" && created.canStart && await d.mode() === "automatic" && jobs.get(threadId) === job && !job.withdrawn) {
        const started = await d.setup({ action: "start", threadId }, job.bounds);
        // `ok` alone is not a start: setup answers ok without starting when
        // its re-check declines. Only a confirmed start is reported as one.
        job.outcome = started.started === true
          ? { ...job.outcome, startedAutomatically: true, message: "Started when this thread was created." }
          : { ...job.outcome, message: started.refused ? `Created from this thread's first message and left switched off. ${started.message}` : `Created from this thread's first message. It was not started automatically${started.ok ? `: ${started.message}` : ", because the start could not be confirmed, and it was not tried again. Check the status below, then start it here if it is not running."}` };
      }
    } catch {
      fail(job, attempted
        ? `Setup could not complete. A switched-off setup named ${attempted.loop} may exist on this thread's host and was not started. Check it there with keepgoing status -c (looptender's command in releases up to v0.2.0) on the path below; to discard it, delete its directory.`
        : "Setup could not complete. Nothing was created or started.", attempted);
    } finally { job.task = ""; d.changed(); }
  };

  const handle = async (input: NewThreadInput): Promise<NewThreadResult> => {
    prune();
    if (input.action === "outcome") {
      // Reading never waits for a setup, this thread's or another's: it asks
      // for a pass and answers with what is known now.
      void queue_(input.threadId);
      return { ok: true, message: "", outcome: jobs.get(input.threadId)?.outcome ?? { state: "none", message: "" } };
    }
    if (input.action === "cancel") {
      // The browser withdraws a request whose send did not go through. Before
      // BB reported a thread for it, the request simply ends. After, the job
      // ends too, whatever it was waiting for: a send that was not delivered
      // must never lead to supervision. Nothing that already ran is undone.
      const intent = intents.get(input.intent);
      if (intent && !intent.threadId) intents.delete(input.intent);
      const job = intent?.threadId ? jobs.get(intent.threadId) : undefined;
      if (job && !job.withdrawn && !job.outcome.startedAutomatically) {
        if (job.outcome.state === "waiting") withdraw(job, NOT_SENT);
        // A pass between creating the setup and starting it sees the mark and
        // does not start.
        else job.withdrawn = NOT_SENT;
      }
      return { ok: true, message: "Keep going is off." };
    }
    const reason = await d.unavailable();
    if (reason) return { ok: false, message: reason };
    const current = await d.mode();
    const setups = await d.capacity();
    if (setups.used >= setups.max) return { ok: false, message: FULL(setups), setups };
    if (input.action === "check") return { ok: true, mode: current, setups, message: "" };
    // The mode the banner showed when the button was pressed applies, unless
    // the setting has since become stricter.
    const mode = stricter(input.shownMode, current);
    // One live request per draft: pressing again replaces it.
    for (const [key, i] of intents) if (i.draftKey === input.draftKey && !i.threadId) intents.delete(key);
    if (intents.size >= MAX_INTENTS) return { ok: false, message: "Too many Keep going requests are waiting. Wait two minutes and try again." };
    const now = d.now();
    const intent: Intent = { id: d.id(), draftKey: input.draftKey, projectId: input.projectId, bounds: input.bounds, mode, createdAt: now, expires: now + INTENT_TTL_MS };
    intents.set(intent.id, intent);
    return { ok: true, mode, setups, intent: intent.id, expiresAt: intent.expires, message: boundsPhrase(input.bounds) };
  };

  return {
    call: (raw: NewThreadInput): Promise<NewThreadResult> => serialize(async () => {
      try { return await handle(newThreadInput.parse(raw)); }
      catch { return { ok: false, message: "Keep going could not be checked. Nothing was created or started." }; }
    }),
    /**
     * Called from the dispatch hook for every message BB dispatches. It is
     * synchronous and never throws: the hook must not delay or fail a send.
     * It records a marked first message and returns; setup happens later.
     * `data` is null for a message that does not carry this plugin's marker.
     */
    dispatched(x: Dispatch): void {
      try {
        prune();
        const job = jobs.get(x.threadId);
        if (x.data === null || x.data === undefined) {
          // A different message is starting this thread's first turn while a
          // request is still waiting for it: the marked message was not what
          // BB delivered. A drained re-attempt of the marked message arrives
          // with its queued rows, and a message joining a running turn means
          // the first turn started; neither ends the request.
          // Only while the thread is still pending: once its first turn has
          // started, the marked message was delivered, and a follow-up is
          // just a follow-up.
          if (job && !job.withdrawn && job.outcome.state === "waiting" && x.threadStatus === "pending" && x.attempt === "start-turn" && x.queued === 0) withdraw(job, NOT_MATCHED);
          return;
        }
        const parsed = markerSchema.safeParse(x.data);
        if (!parsed.success) return;
        const intent = intents.get(parsed.data.intent);
        // BB runs the hook again for the same message after a restart or a
        // drain. The first pass already decided; later passes change nothing.
        if (intent?.threadId === x.threadId || job) return;
        if (!intent || intent.threadId || intent.expires < d.now()) return explain(x.threadId, NOT_MATCHED);
        intent.threadId = x.threadId; // single use, whatever happens next
        const exact = x.projectId === intent.projectId && x.attempt === "start-turn" && x.initiator === "user" && x.senderThreadId === null && x.parentThreadId === null && x.sourceThreadId === null && x.queued === 0 && x.threadCreatedAt >= intent.createdAt;
        if (!exact) return explain(x.threadId, NOT_MATCHED);
        if (!x.text.trim() || Buffer.byteLength(x.text) > 262144) return explain(x.threadId, "The first message has no text looptender can use as a task, or is larger than 256 KiB. Nothing was created. Use Supervise this thread to enter a task.");
        jobs.set(x.threadId, { threadId: x.threadId, task: x.text, bounds: intent.bounds, mode: intent.mode, expires: d.now() + JOB_WAIT_MS, outcome: { state: "waiting", message: "keepgoing is waiting for this thread's first turn to start, then sets it up from the first message." } });
      } catch { /* never fail a send */ }
    },
    /** A thread reached a state in which its first turn may have started. */
    advance: (threadId: string): Promise<void> => queue_(threadId),
    /** Resolves when no setup pass is running or waiting. For tests and shutdown. */
    whenIdle: async (): Promise<void> => { while (chains.size) await Promise.all([...chains.values()]); },
    /** The thread is gone: forget the request and its task text. */
    forget(threadId: string): void { jobs.delete(threadId); },
    /** Counts only, for diagnostics and tests: how much is held in memory. */
    held(): { requests: number; threads: number; taskBytes: number } {
      let taskBytes = 0;
      for (const job of jobs.values()) taskBytes += Buffer.byteLength(job.task);
      return { requests: intents.size, threads: jobs.size, taskBytes };
    },
    dispose(): void { intents.clear(); jobs.clear(); },
  };
}
