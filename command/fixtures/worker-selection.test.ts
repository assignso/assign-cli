import { expect, test } from "bun:test";
import { createExternalWorkerSelection } from "../src/worker-task-claims/selection.ts";
import type { WorkerTaskClaimInputV1 } from "../src/worker-task-claims/index.ts";

const id = (i: number) => `10000000-0000-4000-8000-${String(i).padStart(12, "0")}`;
const connection = {
  epoch: "9007199254740993", connection_intent: id(1), workspace_id: id(2),
  owner_id: id(3), device_id: id(4), worker_id: id(5),
};
const candidate = (i: number, worker = connection.worker_id) => ({
  id: id(i + 10), task_id: id(i), worker_id: worker, project_id: id(6), revision: "2",
  task_revision: "9007199254740993", saved_revision: "4", applied_revision: "4",
  assign_to_self: false, state: "queued",
});
const ready = (data: unknown[], next_cursor: string | null = null, active_task_ids: string[] = []) =>
  Response.json({ data, next_cursor, active_task_ids });
const receipt = (intent: WorkerTaskClaimInputV1, current = true) => Response.json({
  task_id: intent.task_id, delegation_id: intent.delegation_id, run_id: intent.run_id,
  operation_id: intent.operation_id, state: "active", generation: "1", token: id(9),
  revision: "9007199254740994", expires_at: "2099-01-01T00:00:00Z", current,
});

function setup(handler: (url: URL, input?: WorkerTaskClaimInputV1) => Response | Promise<Response>) {
  const events: unknown[] = [];
  const requests: string[] = [];
  let pending: WorkerTaskClaimInputV1 | undefined;
  const transport = {
    origin: "https://worker.example",
    workerDeviceToken: async () => "wdev_at_fixture",
    fetchApi: async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer wdev_at_fixture");
      requests.push(url.pathname.split("/").at(-1)!);
      if (init?.body) {
        const intent = JSON.parse(String(init.body)) as WorkerTaskClaimInputV1;
        if (!pending) throw new Error("Claim dispatched before journaling");
        expect(intent).toEqual(pending);
        return handler(url, intent);
      }
      return handler(url);
    },
  };
  const worker = createExternalWorkerSelection(transport, {
    async save({ body }) {
      expect(pending).toBeUndefined();
      pending = body;
    },
    async refused({ body }) {
      expect(pending).toEqual(body);
      pending = undefined;
    },
  });
  return { worker, requests, events, report: (event: unknown) => { events.push(event); } };
}

test("acknowledges active Tasks, walks short pages and tries a distinct candidate after a Core refusal", async () => {
  const h = setup((url, intent) => {
    if (!intent) {
      expect(url.searchParams.get("epoch")).toBe(connection.epoch);
      expect(url.searchParams.get("limit")).toBe("25");
      return url.searchParams.has("cursor") ? ready([candidate(22), candidate(23)]) : ready([], "next", [id(21)]);
    }
    expect(intent.expected_revision).toBe("9007199254740993");
    return intent.task_id === id(22)
      ? Response.json({ reason: "worker_claim_conflict" }, { status: 409 })
      : receipt(intent);
  });
  const result = await h.worker.select(connection, h.report, 0);
  expect(result).toMatchObject({ state: "claimed", intent: { body: { task_id: id(23) } } });
  expect(h.events).toEqual([{ taskID: id(21), state: "active" }, { taskID: id(22), state: "conflict" }]);
  expect(h.requests).toEqual(["ready-delegations", "ready-delegations", "claim", "claim"]);
});

test("only the original receipt can authorize continuation after an uncertain write", async () => {
  for (const outcome of ["confirmed", "historical", "missing", "generic-conflict"] as const) {
    const h = setup((url, intent) => {
      if (!intent) return ready([candidate(22), candidate(23)]);
      if (url.pathname.endsWith("/claim")) {
        if (outcome === "generic-conflict") return new Response(null, { status: 409 });
        throw new Error("response lost");
      }
      return outcome === "missing" || outcome === "generic-conflict" ? new Response(null, { status: 404 })
        : receipt(intent, outcome === "confirmed");
    });
    const result = await h.worker.select(connection, h.report, 0);
    expect(result.state).toBe(outcome === "confirmed" ? "claimed" : "reconciliation-required");
    expect(h.requests).toEqual(["ready-delegations", "claim", "claim-receipt"]);
  }
});

test("bounds pages and backoff and fails closed on a mismatched worker or unsafe revision", async () => {
  let reads = 0;
  const h = setup(() => ready([], String(++reads)));
  expect(await h.worker.select(connection, h.report, 20)).toEqual({ state: "idle", retryAfterMs: 30_000 });
  expect(reads).toBe(4);
  const stalled = setup(() => ready([], "unchanged"));
  await expect(stalled.worker.select(connection, stalled.report, 0)).rejects.toThrow("Malformed worker ready feed");
  expect(stalled.requests).toEqual(["ready-delegations", "ready-delegations"]);
  for (const row of [candidate(22, id(90)), { ...candidate(22), task_revision: 9007199254740992 }]) {
    const invalid = setup(() => ready([row]));
    await expect(invalid.worker.select(connection, invalid.report, 0)).rejects.toThrow();
    expect(invalid.requests).toEqual(["ready-delegations"]);
  }
});

test("same-owner workers can progress independently after a contention race", async () => {
  let owner: string | undefined;
  const finish = (intent: WorkerTaskClaimInputV1) => {
    if (intent.task_id === id(22)) {
      if (owner !== undefined) return Response.json({ reason: "worker_claim_conflict" }, { status: 409 });
      owner = intent.run_id;
    }
    return receipt(intent);
  };
  const first = setup((_url, intent) => intent ? finish(intent) : ready([candidate(22), candidate(23)]));
  const secondConnection = { ...connection, device_id: id(50), worker_id: id(51) };
  const second = setup((_url, intent) => intent ? finish(intent) : ready([candidate(22, id(51)), candidate(24, id(51))]));
  const [a, b] = await Promise.all([
    first.worker.select(connection, first.report, 0),
    second.worker.select(secondConnection, second.report, 0),
  ]);
  if (a.state !== "claimed" || b.state !== "claimed") throw new Error("Both workers should claim");
  expect(a.intent.owner_id).toBe(b.intent.owner_id);
  expect(a.intent.worker_id).not.toBe(b.intent.worker_id);
  expect(a.intent.body.run_id).not.toBe(b.intent.body.run_id);
  expect(a.intent.body.task_id).not.toBe(b.intent.body.task_id);
  expect([first.events, second.events].flat()).toEqual([{ taskID: id(22), state: "conflict" }]);
});
