import { experimental_defineHostEntry } from "@get-bb/plugin-sdk/host";
import { hostContract } from "./host-contract.ts";
import { setupOnHost, commandOnHost, standardRoutesOnHost } from "./setup-host.ts";
import { transferOnHost } from "./transfer-host.ts";
import { attachmentOnHost } from "./attach-host.ts";
export default experimental_defineHostEntry({
  contract: hostContract,
  handlers: {
    attachment: (input, context) => attachmentOnHost(input, context.signal),
    transfer: (input, context) => transferOnHost(input, context.signal),
    init: (input, context) => setupOnHost(input, context.signal),
    command: (input, context) => commandOnHost(input, context.signal),
    standardRoutes: (input, context) => standardRoutesOnHost(input, context.signal),
  },
});
