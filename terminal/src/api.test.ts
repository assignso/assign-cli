import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { DiscussAPI, type DiscussEnvironment } from "./api.js";
import type { DiscussMessage } from "./types.js";

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

  it("recovers a lost decision response with the original request and key", async () => {
    const requests: Request[] = [];
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async (input, init) => {
      requests.push(new Request(input, init));
      if (requests.length === 1) throw new TypeError("Lost response");
      if (requests.length === 2) return Response.json({ message: "Try again" }, { status: 429 });
      return Response.json({ interaction: { state: "resolved" } }, { status: 202 });
    };
    const api = new DiscussAPI(environment);
    try {
      await assert.rejects(api.decide(openDecision, "Approve"), /outcome is unconfirmed/);
      await assert.rejects(api.decide(openDecision, "Decline"), /outcome is still unconfirmed/);
      await assert.rejects(api.checkDecision(), /outcome is unconfirmed/);
      await api.checkDecision();
      assert.equal(api.decisionPending, false);
      await api.checkDecision();
    } finally {
      globalThis.fetch = originalFetch;
    }

    assert.equal(requests.length, 3);
    const originalKey = requests[0]?.headers.get("Idempotency-Key");
    assert.ok(originalKey);
    for (const request of requests) {
      assert.equal(request.method, "POST");
      assert.equal(request.url, "http://127.0.0.1:1234/api/v1/workspaces/workspace/discuss/interactions/interaction/decision");
      assert.equal(request.headers.get("Idempotency-Key"), originalKey);
      assert.equal(request.headers.get("X-Assign-Discuss-Session"), "prepared-session");
      assert.deepEqual(await request.json(), { expected_revision: 7, decision: "Approve" });
    }
  });

  it("releases the pending decision after a definite stale denial", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async () => Response.json({ code: "interaction_stale", detail: "The interaction expired." }, {
      status: 409,
      headers: { "Content-Type": "application/problem+json" },
    });
    const api = new DiscussAPI(environment);
    try {
      await assert.rejects(api.decide(openDecision, "Approve"), /interaction expired/);
      assert.equal(api.decisionPending, false);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});

const openDecision: DiscussMessage = {
  id: "message", sequence: 1, role: "assistant", kind: "message", state: "final",
  content: "", correlation_id: "request", created_at: "2026-11-15T00:00:00Z", finalized_at: null,
  parts: [{ type: "interaction", state: "open", id: "interaction", revision: 7 }],
};
