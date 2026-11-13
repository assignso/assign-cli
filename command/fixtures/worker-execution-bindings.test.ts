import { expect, test } from "bun:test";
import type { FetchAPI } from "../src/transport.ts";
import {
  createWorkerTaskClaimsClient,
  decodeAcknowledgeCLIWorkerPolicy,
  decodeCreateCLIWorkerDelegation,
  decodeStartCLIWorkerConnection,
  type CLIWorkerDelegationCreateV1,
  type CLIWorkerPolicyAcknowledgmentV1,
  type WorkerTaskClaimsProblems,
  type WorkerTaskClaimsSuccess,
} from "../src/worker-task-claims/index.ts";
import contract from "../src/worker-task-claims/contract.json";

const id = (tail: number) => `00000000-0000-4000-8000-${String(tail).padStart(12, "0")}`;
const subject = { workspace_id: id(1), owner_id: id(2), device_id: id(3), worker_id: id(4) };
const base = `/api/v1/cli/worker-devices/${Object.values(subject).join("/")}`;
const origin = "https://worker-api.invalid";
const human = async () => "cli_at_fixture";
const device = async () => "wdev_at_fixture";
const run = async () => "wrun_at_fixture";
const revision = 9007199254740993n;
const fence = { epoch: revision, connection_intent: id(5) };
const task_id = id(6);
const fixture = (fetchApi: FetchAPI) => createWorkerTaskClaimsClient({
  origin, fetchApi, developerAccessToken: human, workerDeviceToken: device, workerRunToken: run,
});

test("projects the 29 Core operations and keeps the current-human Next route out", () => {
  const client = fixture(async () => { throw new Error("unexpected fetch"); });
  const paths = Object.entries(contract.paths);
  const operations = paths.flatMap(([, methods]) => Object.values(methods).map(op => op.operationId));
  expect(operations).toHaveLength(29);
  expect(Object.keys(client).sort()).toEqual(operations.sort());
  expect(paths.some(([path]) => path.includes("task-recommendations"))).toBe(false);
  expect(Object.hasOwn(client, "listTaskRecommendations")).toBe(false);
  expect(Object.keys(contract.components.securitySchemes).sort()).toEqual([
    "developerAccessToken", "workerDeviceBearerV1", "workerDeviceToken", "workerRunToken",
  ]);
});

test("device connection and policy acknowledgment preserve exact int64 JSON and return Core conflicts", async () => {
  const calls: string[] = [];
  const conflict = {
    type: "https://docs.assign.so/errors/conflict", title: "Resource conflict", status: 409,
    reason: "worker_connection_stale", detail: "The connection is offline or its epoch has changed.",
    instance: `urn:assign:problem:${id(18)}`, code: "conflict", request_id: id(19), retryable: false,
  } satisfies WorkerTaskClaimsProblems["heartbeatCLIWorkerConnection"][409];
  const client = fixture(async (url, init) => {
    const path = new URL(String(url)).pathname;
    calls.push(path);
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer wdev_at_fixture");
    expect(init?.credentials).toBe("omit");
    if (init?.method === "POST") {
      expect(String(init.body)).toContain(`"epoch":${revision}`);
      expect(JSON.parse(String(init.body)).connection_intent).toBe(fence.connection_intent);
    }
    if (path.endsWith("/heartbeat")) return Response.json(conflict, { status: 409 });
    if (path.endsWith("/close")) return Response.json({}, { status: 429, headers: { "Retry-After": "1" } });
    if (path.endsWith("/policy-acknowledgment")) {
      expect(String(init?.body)).toContain(`"revision":${revision}`);
      return new Response(`{"applied_revision":${revision},"connection_epoch":${revision}}`);
    }
    return new Response(`{"epoch":${revision},"expires_at":"2026-11-13T18:00:00Z","heartbeat_seconds":30}`);
  });
  const acknowledgment = {
    ...fence, revision, schema_version: 1, repository_digest: "ab".repeat(32),
    runner_version: "1.0.0", capability_verified: true, local_consent: true,
  } satisfies CLIWorkerPolicyAcknowledgmentV1;
  const lease = await decodeStartCLIWorkerConnection(await client.startCLIWorkerConnection({ ...subject, body: fence }), 1024);
  expect(lease.epoch).toBe(revision);
  expect(lease.heartbeat_seconds).toBe(30);
  const stale = await client.heartbeatCLIWorkerConnection({ ...subject, body: fence });
  expect(stale.status).toBe(409);
  expect(await stale.json()).toEqual(conflict);
  const throttled = await client.closeCLIWorkerConnection({ ...subject, body: fence });
  expect(throttled.status).toBe(429);
  expect(throttled.headers.get("Retry-After")).toBe("1");
  const ack = await decodeAcknowledgeCLIWorkerPolicy(
    await client.acknowledgeCLIWorkerPolicy({ ...subject, body: acknowledgment }), 1024,
  );
  expect(ack).toEqual({ applied_revision: revision, connection_epoch: revision });
  expect(calls).toEqual(["start", "heartbeat", "close", "policy-acknowledgment"].map(action => `${base}/connection/${action}`));
  await expect(decodeStartCLIWorkerConnection(new Response('{"epoch":9223372036854775808}'), 1024)).rejects.toThrow();
  await expect(decodeStartCLIWorkerConnection(new Response('{"epoch":"9007199254740993"}'), 1024)).rejects.toThrow();
});

test("owner delegation and inspections retain distinct Task-safe and owner receipts", async () => {
  const workerPath = `/api/v1/cli/workers/${subject.worker_id}`;
  const delegation = {
    task_id, operation_id: id(7), expected_task_revision: revision,
    expected_saved_revision: revision, expected_applied_revision: revision, assign_to_self: true,
  } satisfies CLIWorkerDelegationCreateV1;
  const sharedRun = {
    run_id: id(8), worker_id: subject.worker_id, human_actor_id: id(9), runner_kind: "codex",
    status: "claimed", outcome: "progress", result_ready: true,
  } satisfies WorkerTaskClaimsSuccess["listCLITaskWorkerRuns"]["data"][number];
  const ownerRun = {
    ...sharedRun, task_id, owner_id: subject.owner_id, claim_generation: String(revision),
    policy_revision: String(revision), result_receipt: id(10), verification_receipt: id(11),
  } satisfies WorkerTaskClaimsSuccess["listCLIWorkerRuns"]["data"][number];
  const requests: string[] = [];
  const client = fixture(async (url, init) => {
    const path = new URL(String(url));
    requests.push(`${init?.method} ${path.pathname}`);
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer cli_at_fixture");
    if (path.pathname === `${workerPath}/runs`) {
      expect(path.searchParams.get("cursor")).toBe("opaque+/=?");
      expect(path.searchParams.get("limit")).toBe("100");
      return Response.json({ data: [ownerRun], next_cursor: null });
    }
    if (path.pathname.endsWith("/worker-runs")) return Response.json({ data: [sharedRun], next_cursor: null });
    if (path.pathname.endsWith("/delegations")) expect(String(init?.body)).toContain(`"expected_task_revision":${revision}`);
    if (path.pathname.endsWith("/cancel")) expect(String(init?.body)).toContain(`"expected_revision":${revision}`);
    return new Response(`{"id":"${id(12)}","worker_id":"${subject.worker_id}","task_id":"${task_id}","project_id":"${id(13)}",` +
      `"task_revision":${revision},"saved_revision":${revision},"applied_revision":${revision},"assign_to_self":true,` +
      `"revision":${revision},"state":"queued"}`);
  });
  const worker_id = subject.worker_id;
  const queued = await decodeCreateCLIWorkerDelegation(await client.createCLIWorkerDelegation({ worker_id, body: delegation }), 2048);
  expect(queued.revision).toBe(revision);
  await client.readCLIWorkerDelegation({ worker_id, delegation_id: queued.id });
  await client.cancelCLIWorkerDelegation({
    worker_id, delegation_id: queued.id, body: { operation_id: id(14), expected_revision: queued.revision },
  });
  const taskPage = await (await client.listCLITaskWorkerRuns({ task_id })).json();
  const ownerPage = await (await client.listCLIWorkerRuns({ worker_id, limit: 100, cursor: "opaque+/=?" })).json();
  expect(taskPage.data).toEqual([sharedRun]);
  expect(ownerPage.data).toEqual([ownerRun]);
  expect(requests).toEqual([
    `POST ${workerPath}/delegations`, `GET ${workerPath}/delegations/${queued.id}`,
    `POST ${workerPath}/delegations/${queued.id}/cancel`, `GET /api/v1/cli/tasks/${task_id}/worker-runs`, `GET ${workerPath}/runs`,
  ]);
});

test("progress, result and receipt operations preserve run fence and credential custody", async () => {
  const body = {
    task_id, run_id: id(8), delegation_id: id(12), operation_id: id(15),
    expected_revision: String(revision), generation: String(revision), token: id(16),
  };
  const receipt = {
    task_id, run_id: body.run_id, delegation_id: body.delegation_id, operation_id: body.operation_id,
    generation: body.generation, revision: body.expected_revision, current: true,
    state: "active", expires_at: "2026-11-13T18:00:00Z",
  } satisfies WorkerTaskClaimsSuccess["recordWorkerTaskResult"];
  const calls: [string, string | null][] = [];
  const client = fixture(async (url, init) => {
    const path = new URL(String(url)).pathname;
    calls.push([path.slice(base.length), new Headers(init?.headers).get("Authorization")]);
    expect(JSON.parse(String(init?.body))).toEqual(path.endsWith("/evidence") ? { ...body, event: "progress" } : body);
    return Response.json(receipt);
  });
  await client.recordWorkerTaskEvidence({ ...subject, body: { ...body, event: "progress" } });
  expect(await (await client.recordWorkerTaskResult({ ...subject, body })).json()).toEqual(receipt);
  expect(await (await client.readWorkerTaskResultReceipt({ ...subject, body })).json()).toEqual(receipt);
  expect(calls).toEqual([
    ["/task-claims/evidence", "Bearer wrun_at_fixture"], ["/task-claims/result", "Bearer wrun_at_fixture"],
    ["/task-claims/result-receipt", "Bearer wdev_at_fixture"],
  ]);
});

test("rejects wrong credential families and preflights Core page and body limits", async () => {
  const fetchApi: FetchAPI = async () => { throw new Error("unexpected fetch"); };
  const client = fixture(fetchApi);
  const deviceOnly = createWorkerTaskClaimsClient({ origin, workerDeviceToken: device, fetchApi });
  for (const wrong of [device, run]) {
    const workerAsHuman = createWorkerTaskClaimsClient({ origin, developerAccessToken: wrong, fetchApi });
    await expect(workerAsHuman.listCLITaskWorkerRuns({ task_id })).rejects.toThrow("bound credential");
  }
  await expect(deviceOnly.listCLITaskWorkerRuns({ task_id })).rejects.toThrow("Missing worker credential");
  const humanAsDevice = createWorkerTaskClaimsClient({ origin, workerDeviceToken: human, fetchApi });
  await expect(humanAsDevice.readCLIWorkerConnection(subject)).rejects.toThrow("bound credential");
  expect(() => client.listCLIWorkerRuns({ worker_id: subject.worker_id, limit: 101 })).toThrow(RangeError);
  expect(() => client.listCLIWorkerReadyDelegations({
    ...subject, epoch: String(revision), connection_intent: id(5), limit: 0,
  })).toThrow(RangeError);
  await expect(client.startCLIWorkerConnection({
    ...subject, body: { ...fence, connection_intent: "é".repeat(8192) },
  })).rejects.toThrow("UTF-8 body limit");
  await expect(client.startCLIWorkerConnection({
    ...subject, body: { ...fence, epoch: 2n ** 63n },
  })).rejects.toThrow("64-bit range");
});
