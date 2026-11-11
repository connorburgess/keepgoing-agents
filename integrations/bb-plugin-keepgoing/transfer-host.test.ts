import { test } from "node:test";
import assert from "node:assert/strict";
import { transferOnHost } from "./transfer-host.ts";
import { experimental_createHostEntryHarness } from "@get-bb/plugin-sdk/testing/host";
import hostEntry from "./host.ts";
const record = { id: "record", threadId: "thr_source", environmentId: "env_example", projectId: "proj_example", hostId: "host_example", project: "/projects/example", provider: "codex", configPath: "/private/path with spaces/keepgoing.yaml", loop: "example-loop" };
const destination = { ...record, threadId: "thr_destination" };
test("host move uses fixed argv, destination caller and explicit takeover only for mutations", async () => {
  for (const operation of ["preview", "apply", "recover"] as const) {
    const id = "move_" + "a".repeat(32);
    await transferOnHost({ bin: "keepgoing", record, destination, operation, recoveryId: id }, undefined, async (bin, args, stdin, owner) => {
      assert.equal(bin, "keepgoing"); assert.equal(stdin, ""); assert.equal(owner, destination.threadId);
      assert.deepEqual(args.slice(0, 5), ["transfer", operation, "-c", record.configPath, "--json"]);
      assert.equal(args.includes("--take-over"), operation !== "preview");
      assert.equal(args.includes("--from"), operation === "apply");
      assert.equal(args.includes("--id"), operation === "recover");
      if (operation === "recover") assert.equal(args[args.indexOf("--id") + 1], id);
      return { code: 0, data: null };
    });
  }
});
test("host boundary rejects other environments, unknown operations and invalid recovery IDs", async () => {
  assert.throws(() => transferOnHost({ bin: "keepgoing", record, destination: { ...destination, hostId: "host_other" }, operation: "apply" }));
  assert.throws(() => transferOnHost({ bin: "keepgoing", record, destination, operation: "recover" }));
  const h = experimental_createHostEntryHarness(hostEntry);
  try {
    await assert.rejects(h.experimental_call("transfer", { bin: "keepgoing", record, destination, operation: "shell" as never }));
    await assert.rejects(h.experimental_call("transfer", { bin: "keepgoing", record, destination, operation: "recover", recoveryId: "untrusted" }));
  } finally { await h.experimental_dispose(); }
});
