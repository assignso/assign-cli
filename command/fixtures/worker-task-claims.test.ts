import { expect, test } from "bun:test";
import { createWorkerTaskOperations } from "../src/worker-task-claims/operations.ts";
import type { WorkerTaskClaimReceiptV1 } from "../src/worker-task-claims/index.ts";
import type { FetchAPI } from "../src/transport.ts";

const id = (tail: number) => `00000000-0000-4000-8000-${String(tail).padStart(12, "0")}`;
const subject = { workspace_id: id(1), owner_id: id(2), device_id: id(3), worker_id: id(4) };
const input = {
  ...subject,
  body: {
    task_id: id(5), delegation_id: id(6), run_id: id(7), operation_id: id(8),
    expected_revision: "9007199254740993", delegation_revision: "9", saved_revision: "10",
    applied_revision: "10", connection_epoch: "11", connection_intent: id(9),
  },
};
const receipt: WorkerTaskClaimReceiptV1 = {
  task_id: id(5), delegation_id: id(6), run_id: id(7), operation_id: id(8), token: id(10),
  generation: "2", revision: "9007199254740994", expires_at: "2026-11-13T16:00:00Z", state: "active", current: true,
};
const origin = "https://worker-api.invalid";

function operations(fetchApi: FetchAPI, token = "wdev_at_fixture") {
  return createWorkerTaskOperations({ origin, workerDeviceToken: async () => token, fetchApi });
}

test("a lost claim response preserves the exact request and reconciles through the read-only route", async () => {
  const seen: { path: string; body: unknown }[] = [];
  const client = operations(async (url, init) => {
    expect(init?.method).toBe("POST");
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer wdev_at_fixture");
    expect(init?.redirect).toBe("error");
    expect(init?.credentials).toBe("omit");
    seen.push({ path: new URL(String(url)).pathname, body: JSON.parse(String(init?.body)) });
    if (seen.length === 1) throw new Error("prepared lost response");
    return Response.json({ ...receipt, current: false });
  });
  await expect(client.claim.apply(input)).rejects.toMatchObject({
    operationID: id(8), uncertain: true, exitCode: 1,
  });
  expect(await client.claim.reconcile(input)).toEqual({ ...receipt, current: false });
  const base = `/api/v1/cli/worker-devices/${Object.values(subject).join("/")}/task-claims`;
  expect(seen).toEqual([
    { path: `${base}/claim`, body: input.body },
    { path: `${base}/claim-receipt`, body: input.body },
  ]);
});

test("maps refusal and interruption, and treats missing or malformed receipts as uncertain", async () => {
  for (const [status, exitCode] of [[400, 2], [401, 3], [403, 3], [409, 5], [503, 1]]) {
    const client = operations(async () => new Response(null, { status }));
    await expect(client.claim.apply(input)).rejects.toMatchObject({
      operationID: id(8), uncertain: status === 503 || status === 409, status, exitCode,
    });
  }
  const missing = operations(async () => new Response(null, { status: 404 }));
  await expect(missing.claim.reconcile(input)).rejects.toMatchObject({ operationID: id(8), uncertain: true, status: 404 });
  for (const invalid of [{ ...receipt, operation_id: id(11) }, { ...receipt, revision: 9007199254740992 }]) {
    const client = operations(async () => Response.json(invalid));
    await expect(client.claim.apply(input)).rejects.toMatchObject({ operationID: id(8), uncertain: true, exitCode: 1 });
  }
  const controller = new AbortController();
  controller.abort();
  const interrupted = operations(async () => { throw new Error("must not dispatch"); });
  await expect(interrupted.claim.apply(input, controller.signal)).rejects.toMatchObject({ operationID: id(8), exitCode: 130 });
});

test("uses the correct bearer for finalization and its historical receipt", async () => {
  const tokens: (string | null)[] = [];
  const client = createWorkerTaskOperations({
    origin, workerDeviceToken: async () => "wdev_at_fixture", workerRunToken: async () => "wrun_at_fixture",
    fetchApi: async (_url, init) => {
      tokens.push(new Headers(init?.headers).get("Authorization"));
      const { token: _token, ...released } = receipt;
      return Response.json({ ...released, state: "released", current: false });
    },
  });
  const finalize = {
    ...subject, body: {
      task_id: id(5), delegation_id: id(6), run_id: id(7), operation_id: id(8),
      expected_revision: receipt.revision, generation: receipt.generation, token: id(10), verification_passed: true as const,
    },
  };
  expect((await client.finalize.apply(finalize)).state).toBe("released");
  expect((await client.finalize.reconcile(finalize)).current).toBe(false);
  expect(tokens).toEqual(["Bearer wrun_at_fixture", "Bearer wdev_at_fixture"]);
  let dispatched = false;
  const wrongAudience = operations(async () => {
    dispatched = true;
    return Response.json(receipt);
  }, "wrun_at_fixture");
  await expect(wrongAudience.claim.apply(input)).rejects.toMatchObject({ operationID: id(8), exitCode: 1 });
  expect(dispatched).toBe(false);
});

test("posts Task evidence with a run credential and looks up the same intent with the device credential", async () => {
  const seen: string[] = [];
  const evidence = {
    ...subject,
    body: {
      task_id: input.body.task_id, delegation_id: input.body.delegation_id,
      run_id: input.body.run_id, operation_id: input.body.operation_id,
      expected_revision: receipt.revision, generation: receipt.generation,
      token: receipt.token!, event: "verification-passed" as const,
    },
  };
  const client = createWorkerTaskOperations({
    origin,
    workerDeviceToken: async () => "wdev_at_fixture",
    workerRunToken: async () => "wrun_at_fixture",
    fetchApi: async (url, init) => {
      seen.push(new URL(String(url)).pathname.split("/").at(-1)!);
      const reconciling = seen.at(-1) === "evidence-receipt";
      expect(new Headers(init?.headers).get("Authorization")).toBe(
        reconciling ? "Bearer wdev_at_fixture" : "Bearer wrun_at_fixture",
      );
      expect(JSON.parse(String(init?.body))).toEqual(evidence.body);
      return Response.json({ ...receipt, current: false });
    },
  });
  expect(await client.evidence.apply(evidence)).toEqual({ ...receipt, current: false });
  expect(await client.evidence.reconcile(evidence)).toEqual({ ...receipt, current: false });
  expect(seen).toEqual(["evidence", "evidence-receipt"]);
});
