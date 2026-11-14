// This entry is compiled only by the pinned SDK fixture staging command.
import { Configuration } from "@assignso/sdk/dist/esm/runtime.js";
import { createRecoveryFetch } from "@assignso/sdk/dist/esm/recovery.js";
import { TasksApi, ListWorkspaceTasksStateEnum } from "@assignso/sdk/dist/esm/lazy/TasksApi/listWorkspaceTasks.js";
import { CommandFailure } from "../src/main.ts";
import { runFixture } from "./entry.ts";
import { fixtureToken } from "./transport.ts";
import { decodeResponse, sdkResult, transportConfiguration } from "../src/transport.ts";

process.exitCode = await runFixture(async (fetchApi, origin, workspaceId, code) => {
  const api = new TasksApi(new Configuration(transportConfiguration({ createRecoveryFetch }, {
    origin, accessToken: async () => fixtureToken, fetchApi, timeoutMs: 1000, maxAttempts: 1,
  })));
  const malformed = "Task resource representation is incomplete or exceeds its limit";
  try {
    const page = await sdkResult(() => api.listWorkspaceTasksRaw({
      workspaceId, code, includeArchived: true, limit: 100, state: ListWorkspaceTasksStateEnum.All,
    }, { redirect: "error" }));
    // Keep the canonical wire representation for the fixture's exact decimal
    // revision assertion. The raw operation still uses the generated SDK route.
    return await decodeResponse(page.raw, 1024 * 1024);
  } catch (error) {
    if (error instanceof CommandFailure && error.exitCode === 3) {
      throw new CommandFailure("Task resource access denied", 3);
    }
    throw new CommandFailure(malformed, 1);
  }
});
