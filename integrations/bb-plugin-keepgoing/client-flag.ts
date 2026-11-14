// The plugin tells the CLI which version of itself is calling, with
// --client bb-plugin@VERSION on status, so the panel's Versions view can say
// which build the plugin runs. A CLI too old to know the flag refuses it as a
// usage error: exit 2 with nothing on stdout. Only then is the same call made
// again without the flag, and only if that second call succeeds is the CLI
// remembered as one that refuses it. A timeout, any other exit code, or a
// second call that also fails is not taken for a refusal. A CLI is known by
// its path, size and modification time, so one upgraded in place is given the
// flag again. The name carries no product name, so the rename does not
// change it.
import { stat } from "node:fs/promises";

// The plugin's version. A test keeps it equal to package.json's.
export const PLUGIN_VERSION = "0.3.1";

export const clientArgs = (): string[] => ["--client", `bb-plugin@${PLUGIN_VERSION}`];

/** What a call reports, enough to tell a refusal of the flag from anything else. */
export type CallOutcome = { code: number; emptyStdout: boolean; timedOut: boolean };

/** A usage error with nothing printed, from a call that finished in time. */
export const refusesFlag = (o: CallOutcome) => !o.timedOut && o.code === 2 && o.emptyStdout;

const refused = new Set<string>();

/** The CLI's identity: its path, size and modification time when it can be
 *  read, else its name as given. */
export async function cliIdentity(bin: string): Promise<string> {
  try {
    const s = await stat(bin);
    return `${bin}|${s.size}|${s.mtimeMs}`;
  } catch {
    return bin;
  }
}

/** Runs a status call with the flag, unless this CLI refused it before. */
export async function withClientFlag<T>(bin: string, call: (extra: string[]) => Promise<T>, outcome: (result: T) => CallOutcome): Promise<T> {
  const id = await cliIdentity(bin);
  if (refused.has(id)) return call([]);
  const first = await call(clientArgs());
  if (!refusesFlag(outcome(first))) return first;
  const second = await call([]);
  const o = outcome(second);
  if (!o.timedOut && o.code === 0) refused.add(id);
  return second;
}

/** For tests. */
export const forgetRefusals = () => refused.clear();
