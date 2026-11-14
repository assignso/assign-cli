import { describe, expect, test } from "bun:test";
import {
  createWorkerRegistrationClient,
  type CLIWorkerCatalogItemV1,
  type CLIWorkerPolicyConflictV1,
  type CLIWorkerPolicyUpdateV1,
  type CLIWorkerRegistrationV1,
  type WorkerRegistrationSuccess,
} from "../src/worker-registration/index.ts";
import type { FetchAPI } from "../src/transport.ts";
import contract from "../src/worker-registration/contract.json";

const origin = "https://worker-api.invalid";
const worker_id = "00000000-0000-4000-8000-000000000001";
const registration = {
  project_id: "00000000-0000-4000-8000-000000000002",
  enrollment_intent: "00000000-0000-4000-8000-000000000003",
  profile_intent: "00000000-0000-4000-8000-000000000004",
  installation_key: "ab".repeat(32),
  repository_digest: "cd".repeat(32),
  name: "Fixture worker",
  runner_kind: "codex",
  runner_version: "0.1.0",
} satisfies CLIWorkerRegistrationV1;
const item: CLIWorkerCatalogItemV1 = {
  id: worker_id, device_id: "00000000-0000-4000-8000-000000000005", project_id: registration.project_id,
  name: registration.name, runner_kind: "codex", runner_version: registration.runner_version,
  revision: 2, applied_revision: 0, readiness: "pending-apply", connection_state: "offline",
  blockers: ["profile-revision-not-applied"],
};
const savedPolicy = {
  schema_version: 1,
  grant: {
    workspace_ids: [], project_ids: [], repository_digests: [], task_ids: [], operations: [],
    allow_assign_to_self: false,
  },
  native: {
    local_scope_ids: [], branch: "isolated", push: false, pull_request: false, publish: false,
    concurrency: 1, elapsed_seconds: 60, output_bytes: 1024,
  },
} satisfies WorkerRegistrationSuccess["readCLIWorkerPolicy"]["saved"];
const current: WorkerRegistrationSuccess["readCLIWorkerPolicy"] = {
  schema_version: 1, worker_id, name: item.name, saved_revision: 2, applied_revision: 0,
  authorization_state: "pending-apply", saved: savedPolicy, applied: null, paused: false, revoked: false,
};
const basePath = "/api/v1/cli/workers";
const signal = new AbortController().signal;

function setup(fetchApi: FetchAPI, token = "cli_at_fixture") {
  return createWorkerRegistrationClient({ origin, accessToken: async () => token, fetchApi });
}

describe("private CLI worker registration binding", () => {
  test("contains only the approved operations and their reachable components", () => {
    const reachable = new Set<string>();
    const visit = (node: unknown) => {
      if (!node || typeof node !== "object") return;
      for (const [key, value] of Object.entries(node)) {
        if (key === "$ref") {
          if (typeof value !== "string" || !value.startsWith("#/components/")) throw new Error("Unexpected contract reference");
          if (reachable.has(value)) continue;
          const [section, name] = value.slice("#/components/".length).split("/");
          const groups: Record<string, Record<string, unknown>> = contract.components;
          const target = groups[section!]?.[name!];
          expect(target).toBeDefined();
          reachable.add(value);
          visit(target);
        } else {
          visit(value);
        }
      }
    };
    visit(contract.paths);
    reachable.add("#/components/securitySchemes/developerAccessToken");
    expect([...reachable].sort()).toEqual(Object.entries(contract.components).flatMap(([section, values]) =>
      Object.keys(values).map(key => `#/components/${section}/${key}`)).sort());
    expect(Object.keys(contract.components.securitySchemes)).toEqual(["developerAccessToken"]);
    expect(JSON.stringify(contract)).not.toMatch(/browserSession|__Host-assign_session|workerDeviceBearer|\/api\/v1\/cli\/worker-devices/);
    const client = setup(async () => { throw new Error("Unexpected fetch"); });
    expect(Object.keys(client).sort()).toEqual([
      "getCLIWorkerRegistrationReceipt", "listCLIWorkers", "readCLIWorkerPolicy",
      "registerCLIWorker", "showCLIWorker", "updateCLIWorkerPolicy",
    ]);
  });

  test("serializes registration and recovers the original receipt without a bearer in the result", async () => {
    const calls: Request[] = [];
    const client = setup(async (url, init) => {
      const request = new Request(url, init);
      expect(init?.signal).toBe(signal);
      expect(request.headers.get("Authorization")).toBe("Bearer cli_at_fixture");
      expect(request.headers.get("Cookie")).toBeNull();
      expect(init?.credentials).toBe("omit");
      expect(init?.redirect).toBe("error");
      expect(init?.cache).toBe("no-store");
      calls.push(request);
      return Response.json(item);
    });
    const created = await client.registerCLIWorker({ body: registration }, signal);
    expect(await created.json()).toEqual(item);
    const receipt = await client.getCLIWorkerRegistrationReceipt({
      enrollment_intent: registration.enrollment_intent, profile_intent: registration.profile_intent,
    }, signal);
    const recovered = await receipt.json();
    expect(recovered).toEqual(item);
    expect(recovered.installation_key).toBeUndefined();
    expect(recovered.access_token).toBeUndefined();
    expect(calls.map(({ method, url }) => [method, url])).toEqual([
      ["POST", origin + basePath],
      ["GET", `${origin}${basePath}/registration-receipts/${registration.enrollment_intent}/${registration.profile_intent}`],
    ]);
    expect(calls[0]!.headers.get("Content-Type")).toBe("application/json");
    expect(await calls[0]!.json()).toEqual(registration);
    expect(calls[1]!.body).toBeNull();
  });

  test("uses the server page default and preserves opaque continuation from an empty page", async () => {
    const opaque = "+/?&% =";
    const seen: URL[] = [];
    const client = setup(async (input) => {
      const url = new URL(String(input));
      seen.push(url);
      return Response.json({ data: [], next_cursor: seen.length === 1 ? opaque : null });
    });
    const first = await (await client.listCLIWorkers()).json() as WorkerRegistrationSuccess["listCLIWorkers"];
    expect(first).toEqual({ data: [], next_cursor: opaque });
    await client.listCLIWorkers({ limit: 100, cursor: first.next_cursor! });
    expect(seen[0]!.href).toBe(origin + basePath);
    expect(seen[1]!.searchParams.get("cursor")).toBe(opaque);
    expect(seen[1]!.searchParams.get("limit")).toBe("100");
  });

  test("serializes policy edits and revocation and preserves typed conflict reconciliation", async () => {
    const conflict: CLIWorkerPolicyConflictV1 = {
      type: "https://docs.assign.so/errors/conflict", title: "Conflict", status: 409,
      detail: "Reconcile the current revision", instance: "urn:assign:problem:fixture", code: "conflict",
      request_id: "fixture", retryable: false, reason: "worker_policy_conflict", current,
    };
    const bodies: unknown[] = [];
    const client = setup(async (input, init) => {
      const request = new Request(input, init);
      if (request.method === "PATCH") {
        expect(request.url).toBe(`${origin}${basePath}/${worker_id}/policy`);
        const body = await request.json() as CLIWorkerPolicyUpdateV1;
        bodies.push(body);
        if (body.expected_revision === 1) return Response.json(conflict, {
          status: 409, headers: { "Content-Type": "application/problem+json" },
        });
        return Response.json({ ...current, revoked: true });
      }
      const path = new URL(request.url).pathname;
      expect(request.method).toBe("GET");
      expect(path).toBe(`${basePath}/${worker_id}${path.endsWith("/policy") ? "/policy" : ""}`);
      return Response.json(path.endsWith("/policy") ? current : item);
    });
    expect(await (await client.showCLIWorker({ worker_id })).json()).toEqual(item);
    const edit = { expected_revision: 1, policy: savedPolicy } satisfies CLIWorkerPolicyUpdateV1;
    const stale = await client.updateCLIWorkerPolicy({ worker_id, body: edit });
    expect(stale.status).toBe(409);
    expect(stale.headers.get("Content-Type")).toBe("application/problem+json");
    expect(await stale.json()).toEqual(conflict);
    expect(bodies).toEqual([edit]);
    const state = await (await client.readCLIWorkerPolicy({ worker_id })).json() as WorkerRegistrationSuccess["readCLIWorkerPolicy"];
    expect(state.applied).toBeNull();
    const revoke = { expected_revision: state.saved_revision, control: "revoke" } satisfies CLIWorkerPolicyUpdateV1;
    expect(await (await client.updateCLIWorkerPolicy({ worker_id, body: revoke })).json()).toEqual({ ...current, revoked: true });
    expect(bodies).toEqual([edit, revoke]);
  });

  test("rejects credential families, unsafe URLs and malformed selectors before fetch", async () => {
    const forbiddenFetch: FetchAPI = async () => { throw new Error("Unexpected fetch"); };
    for (const token of ["", "cli_at_", "wdev_at_dummy", "wrun_at_dummy", "mcp_at_dummy", "cli_at_dummy\r\nCookie: dummy"]) {
      await expect(setup(forbiddenFetch, token).listCLIWorkers()).rejects.toThrow("interactive Assign CLI");
    }
    for (const unsafe of ["http://worker-api.invalid", "https://user@worker-api.invalid", origin + "/other"]) {
      expect(() => createWorkerRegistrationClient({
        origin: unsafe, accessToken: async () => "cli_at_fixture", fetchApi: forbiddenFetch,
      })).toThrow("HTTPS origin");
    }
    expect(() => setup(forbiddenFetch).showCLIWorker({ worker_id: "../registration-receipts" })).toThrow("identifier");
    const aborted = AbortSignal.abort();
    await expect(setup(forbiddenFetch).listCLIWorkers({}, aborted)).rejects.toThrow();
  });

  test("honors UTF-8 and page bounds and preserves a rate limit without retries", async () => {
    const requestBodies: number[] = [];
    const client = setup(async (input, init) => {
      const request = new Request(input, init);
      requestBodies.push((await request.arrayBuffer()).byteLength);
      return Response.json({ status: 429, code: "rate_limited" }, {
        status: 429, headers: { "Content-Type": "application/problem+json", "Retry-After": "1" },
      });
    });
    for (const limit of [0, 101, 1.5, NaN]) {
      expect(() => client.listCLIWorkers({ limit })).toThrow("Invalid worker limit");
    }
    const byteLength = (name: string) => new TextEncoder().encode(JSON.stringify({ ...registration, name })).byteLength;
    const boundary = "é".repeat(Math.floor((16384 - byteLength("")) / 2));
    const name = byteLength(boundary) === 16384 ? boundary : boundary + "a";
    const limited = await client.registerCLIWorker({ body: { ...registration, name } });
    expect(limited.status).toBe(429);
    expect(limited.headers.get("Retry-After")).toBe("1");
    expect(await limited.json()).toEqual({ status: 429, code: "rate_limited" });
    await expect(client.registerCLIWorker({ body: { ...registration, name: name + "é" } })).rejects.toThrow("UTF-8 body limit");
    await expect(client.updateCLIWorkerPolicy({
      worker_id, body: { expected_revision: 2, name: "é".repeat(8192) },
    })).rejects.toThrow("UTF-8 body limit");
    expect(requestBodies).toEqual([16384]);
  });

  test("preserves server auth denials and rejects redirected transport responses", async () => {
    for (const status of [401, 403]) {
      const client = setup(async () => Response.json({ status }, { status }));
      const response = await client.registerCLIWorker({ body: registration });
      expect(response.status).toBe(status);
      expect(await response.json()).toEqual({ status });
    }
    const redirect = setup(async () => Response.redirect("https://untrusted.invalid"));
    await expect(redirect.listCLIWorkers()).rejects.toThrow("redirect rejected");
  });
});
