import { experimental_defineHostEntry } from "@get-bb/plugin-sdk/host";
import { hostContract } from "./setup-contract.ts";
import { setupOnHost, commandOnHost } from "./setup-host.ts";
export default experimental_defineHostEntry({
  contract: hostContract,
  handlers: {
    init: (input, context) => setupOnHost(input, context.signal),
    command: (input, context) => commandOnHost(input, context.signal),
  },
});
