// The plugin tells the CLI which version of itself is calling, with
// --client bb-plugin@VERSION on status, so the panel's Versions view can say
// which build the plugin runs. A CLI too old to know the flag refuses it; the
// plugin then asks again without it and does not pass it to that executable
// again. The name carries no product name, so the rename does not change it.

// The plugin's version: package.json's, which a test keeps them equal to.
export const PLUGIN_VERSION = "0.3.1";

export const clientArgs = (): string[] => ["--client", `bb-plugin@${PLUGIN_VERSION}`];

const refused = new Set<string>();

/** Runs a status call with the flag, unless this executable refused it before.
 *  `refusedFlag` says whether a result with the flag looks like an older CLI
 *  refusing it; then the call is made again without it and remembered. */
export async function withClientFlag<T>(bin: string, call: (extra: string[]) => Promise<T>, refusedFlag: (result: T) => boolean, retried: (result: T) => boolean = () => true): Promise<T> {
  if (refused.has(bin)) return call([]);
  const first = await call(clientArgs());
  if (!refusedFlag(first)) return first;
  const second = await call([]);
  if (retried(second)) refused.add(bin);
  return second;
}

/** For tests. */
export const forgetRefusals = () => refused.clear();
