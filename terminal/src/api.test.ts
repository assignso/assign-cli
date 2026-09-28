import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { DiscussAPI, type DiscussEnvironment } from "./api.js";

const environment: DiscussEnvironment = {
  broker: "http://127.0.0.1:1234",
  session: "prepared-session",
  workspaceId: "workspace",
  workspaceName: "Assign",
  workspaceSlug: "assign",
};

describe("terminal Discuss API", () => {
  it("queues canonical turns and cancels the exact pending response", async () => {
    const requests: Request[] = [];
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (input, init) => {
      const request = new Request(input, init);
      requests.push(request);
      if (request.url.endsWith("/turns")) {
        return Response.json({
          stream_id: "stream",
          user: { id: "user" },
          response: { id: "response" },
          replayed: false,
        });
      }
      return Response.json({ id: "response", state: "cancelled" });
    };
    try {
      const api = new DiscussAPI(environment);
      const turn = await api.send("Inspect this");
      await api.cancel(turn.response.id);
    } finally {
      globalThis.fetch = originalFetch;
    }

    assert.equal(requests[0]?.url, "http://127.0.0.1:1234/api/v1/workspaces/workspace/discuss/turns");
    assert.equal(requests[1]?.url, "http://127.0.0.1:1234/api/v1/workspaces/workspace/discuss/messages/response/cancel");
    for (const request of requests) {
      assert.equal(request.headers.get("X-Assign-Discuss-Session"), "prepared-session");
      assert.equal(request.headers.has("Authorization"), false);
    }
  });
});
