// bb-plugin-keepgoing: shows keepgoing's state beside the threads it
// supervises, and offers pause, resume, and stop. It only calls the keepgoing
// CLI; every decision stays in keepgoing.
import { execFile } from "node:child_process";
import { defineRpcContract, type BbPluginApi } from "@get-bb/plugin-sdk";
import { z } from "zod";

/** The subset of `keepgoing status --json` this plugin reads. */
const agentRef = z.object({ adapter: z.string(), bbThread: z.string().optional(), model: z.string().optional() });
const statusSchema = z.object({
  loop: z.string(),
  target: z.string(),
  controlState: z.string(),
  control: z.object({ note: z.string().optional() }).passthrough(),
  window: z.string(),
  state: z
    .object({
      lastState: z.string().optional(),
      lastWakeAt: z.string().optional(),
      checkedAt: z.string().optional(),
      handoffs: z.number().optional(),
    })
    .passthrough(),
  redundancy: z.object({ healthy: z.array(z.string()), distinct: z.number(), min: z.number(), meets: z.boolean() }).optional(),
  primaryAgent: agentRef,
  targetAgent: agentRef,
});

const loopSchema = z.object({
  loop: z.string(),
  configPath: z.string(),
  controlState: z.string(),
  note: z.string().nullable(),
  target: z.string(),
  supervising: z.boolean(), // this thread is the loop's current target
  lastState: z.string().nullable(),
  lastWakeAt: z.string().nullable(),
  checkedAt: z.string().nullable(),
  handoffs: z.number(),
  fallbacks: z.string().nullable(),
});
export type LoopView = z.infer<typeof loopSchema>;

const actionSchema = z.enum(["pause", "resume", "stop"]);

export const rpcContract = defineRpcContract({
  forThread: {
    input: z.object({ threadId: z.string().min(1).max(200) }),
    output: z.object({ loops: z.array(loopSchema), errors: z.array(z.string()) }),
  },
  control: {
    input: z.object({ configPath: z.string().min(1).max(1000), action: actionSchema }),
    output: z.object({ ok: z.boolean(), message: z.string() }),
  },
});

const CHANGED = "changed";

function run(bin: string, args: string[]): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve) => {
    execFile(bin, args, { timeout: 15_000, maxBuffer: 1 << 20 }, (error, stdout, stderr) => {
      const code = error ? (typeof (error as { code?: unknown }).code === "number" ? (error as { code: number }).code : 1) : 0;
      resolve({ code, stdout: String(stdout), stderr: String(stderr) });
    });
  });
}

export default async function plugin(bb: BbPluginApi) {
  const settings = bb.settings.define({
    keepgoingPath: {
      type: "string",
      label: "keepgoing binary",
      description: "Absolute path to the keepgoing executable. A bare name is looked up on the BB server's PATH.",
      default: "keepgoing",
    },
    configs: {
      type: "string",
      label: "Loop configs",
      description: "One keepgoing.yaml path per line. Only these loops are shown and controlled.",
      experimental_multiline: true,
      default: "",
    },
  });

  const configured = async () => {
    const { keepgoingPath, configs } = await settings.get();
    const paths = configs
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line !== "" && !line.startsWith("#"));
    return { bin: keepgoingPath.trim() || "keepgoing", paths };
  };

  const status = async (bin: string, configPath: string) => {
    const result = await run(bin, ["status", "--json", "-c", configPath]);
    if (result.code !== 0) throw new Error(`${configPath}: keepgoing status exited ${result.code}`);
    let raw: unknown;
    try {
      raw = JSON.parse(result.stdout);
    } catch {
      throw new Error(`${configPath}: keepgoing status did not print JSON`);
    }
    const parsed = statusSchema.safeParse(raw);
    if (!parsed.success) {
      const old = typeof raw === "object" && raw !== null && !("primaryAgent" in raw);
      throw new Error(old ? `${configPath}: this keepgoing is too old for the plugin; it needs a build with primaryAgent in status --json (after 0.1.0)` : `${configPath}: unexpected keepgoing status JSON`);
    }
    return parsed.data;
  };

  const view = (s: z.infer<typeof statusSchema>, configPath: string, threadId: string | null): LoopView => ({
    loop: s.loop,
    configPath,
    controlState: s.controlState,
    note: s.control.note ?? null,
    target: s.target,
    supervising: threadId !== null && s.targetAgent.bbThread === threadId,
    lastState: s.state.lastState ?? null,
    lastWakeAt: s.state.lastWakeAt ?? null,
    checkedAt: s.state.checkedAt ?? null,
    handoffs: s.state.handoffs ?? 0,
    fallbacks: s.redundancy ? `${s.redundancy.healthy.length} healthy, need ${s.redundancy.min}` : null,
  });

  const allLoops = async (threadId: string | null) => {
    const { bin, paths } = await configured();
    const loops: LoopView[] = [];
    const errors: string[] = [];
    for (const configPath of paths) {
      try {
        const s = await status(bin, configPath);
        const matches = threadId === null || s.primaryAgent.bbThread === threadId || s.targetAgent.bbThread === threadId;
        if (matches) loops.push(view(s, configPath, threadId));
      } catch (error) {
        errors.push(error instanceof Error ? error.message : String(error));
      }
    }
    return { loops, errors };
  };

  // Control only loops listed in settings: the path comes from the browser,
  // so it must never choose an arbitrary config file.
  const control = async (configPath: string, action: z.infer<typeof actionSchema>) => {
    const { bin, paths } = await configured();
    if (!paths.includes(configPath)) return { ok: false, message: "That loop is not in the plugin's configured list." };
    const result = await run(bin, [action, "-c", configPath]);
    bb.realtime.publish(CHANGED, { at: Date.now() });
    return result.code === 0
      ? { ok: true, message: result.stdout.trim() || `${action} done` }
      : { ok: false, message: `keepgoing ${action} exited ${result.code}` };
  };

  bb.rpc.register(rpcContract, {
    forThread: ({ threadId }) => allLoops(threadId),
    control: ({ configPath, action }) => control(configPath, action),
  });

  bb.cli.register({
    name: "keepgoing",
    summary: "Show and control the keepgoing loops this plugin is configured for",
    commands: [
      { name: "status", summary: "List configured loops with their control state and target", usage: "bb keepgoing status" },
      { name: "pause", summary: "Pause a loop by name", usage: "bb keepgoing pause <loop>" },
      { name: "resume", summary: "Resume a loop by name", usage: "bb keepgoing resume <loop>" },
      { name: "stop", summary: "Stop a loop's running session by name", usage: "bb keepgoing stop <loop>" },
    ],
    async run(argv) {
      const [command, name, ...rest] = argv;
      if (command === "status" && name === undefined) {
        const { loops, errors } = await allLoops(null);
        const lines = loops.map((l) => `${l.loop}\t${l.controlState}\ttarget=${l.target}\tlast=${l.lastState ?? "never"}${l.note ? `\t(${l.note})` : ""}`);
        return { exitCode: errors.length > 0 ? 1 : 0, stdout: `${lines.join("\n")}\n`, stderr: errors.join("\n") };
      }
      const parsed = actionSchema.safeParse(command);
      if (!parsed.success || !name || rest.length > 0) return { exitCode: 2, stderr: "Usage: bb keepgoing status | pause <loop> | resume <loop> | stop <loop>" };
      const { loops } = await allLoops(null);
      const loop = loops.find((l) => l.loop === name);
      if (!loop) return { exitCode: 1, stderr: `No configured loop named ${name}` };
      const result = await control(loop.configPath, parsed.data);
      return { exitCode: result.ok ? 0 : 1, stdout: `${result.message}\n` };
    },
  });
}
