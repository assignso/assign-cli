import { describe, expect, test } from "bun:test";
import { decodeResponse, sdkResult, transportConfiguration } from "../src/transport.ts";
import { runCommand, writeStdout } from "../src/main.ts";
import { PassThrough, Readable } from "node:stream";

// Offline source extracted from ADR 0170's pinned Git revision, never ambient SDK.
const source = process.env.ASSIGN_TEST_SDK_SOURCE;
if (!source) throw new Error("Set ASSIGN_TEST_SDK_SOURCE to the pinned SDK export");
const sdk = await import(source + "/src/recovery.ts");
const { Configuration } = await import(source + "/src/runtime.ts");
const { TasksApi } = await import(source + "/src/apis/TasksApi.ts");
const { iteratePages } = await import(source + "/src/pagination.ts");
const origin = "https://api.example.invalid";
function config(fetchApi: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>, maxAttempts = 1) {
  return transportConfiguration(sdk, { origin, fetchApi, accessToken: async () => "prepared-token", maxAttempts, timeoutMs: 1000 });
}

describe("pinned SDK transport", () => {
  test("generated authenticated read retains raw decimal revision", async () => {
    const api = new TasksApi(new Configuration(config(async (input, init) => {
      expect(String(input)).toStartWith(origin + "/api/v1/workspaces/w/tasks?");
      expect(new Headers(init?.headers).get("authorization")).toBe("Bearer prepared-token");
      expect(init?.redirect).toBe("error");
      expect(init?.credentials).toBe("omit");
      return Response.json({ data: [{ revision: "9223372036854775807" }], next_cursor: null });
    })));
    const page = await sdkResult(() => api.listWorkspaceTasksRaw({ workspaceId: "w", limit: 100 }));
    expect(await decodeResponse((page as { raw: Response }).raw, 1000)).toEqual({ data: [{ revision: "9223372036854775807" }], next_cursor: null });
  });
  test.each([[401,3], [403,3], [409,5], [412,5], [429,1], [406,2], [415,2], [400,2], [500,1]])("HTTP %i maps exit %i without leaking body", async (status, exitCode) => {
    const api = new TasksApi(new Configuration(config(async () => new Response("secret", { status }))));
    try { await sdkResult(() => api.listWorkspaceTasksRaw({ workspaceId: "w" })); throw new Error("expected failure"); }
    catch (error) { expect(error).toMatchObject({ exitCode }); expect(String(error)).not.toContain("secret"); }
  });
  test("rejects cross-origin before credential dispatch and redirects", async () => {
    let calls = 0;
    const c = config(async () => { calls++; return new Response(null, { status: 302 }); });
    await expect(c.fetchApi("https://other.invalid/")).rejects.toThrow();
    expect(calls).toBe(0);
    await expect(c.fetchApi(origin)).rejects.toThrow();
    expect(calls).toBe(1);
    expect(() => transportConfiguration(sdk, { origin: "https://user:pass@api.invalid", accessToken: async () => "x" })).toThrow();
  });
  test("read attempts bounded; writes never replay without registry authority", async () => {
    let calls = 0;
    const c = config(async () => { calls++; return new Response(null, { status: 503 }); }, 2);
    const configuration = new Configuration(c);
    await (await configuration.fetchApi(origin)).body?.cancel();
    expect(calls).toBe(2);
    calls = 0;
    await (await configuration.fetchApi(origin, { method: "POST", headers: { "Idempotency-Key": "prepared-key-123456789" }, body: "{}" })).body?.cancel();
    expect(calls).toBe(1);
  });
  test("cancellation maps interruption", async () => {
    const controller = new AbortController(); controller.abort();
    await expect(sdkResult(async () => { throw new Error("unreachable"); }, controller.signal)).rejects.toMatchObject({ exitCode: 130 });
  });
  test("explicit key-required replay retains body and key", async () => {
    const seen: unknown[] = [];
    const c = transportConfiguration(sdk, { origin, accessToken: async () => "prepared",
      timeoutMs: 1000, maxAttempts: 2, retryPolicy: () => "key-required",
      fetchApi: async (_url, init) => {
        seen.push([new Headers(init?.headers).get("Idempotency-Key"), init?.body]);
        return new Response(null, { status: 503 });
      },
    });
    await c.fetchApi(origin, { method: "POST", headers: { "Idempotency-Key": "prepared-key-123456789" }, body: "{}" });
    expect(seen).toEqual([["prepared-key-123456789", "{}"], ["prepared-key-123456789", "{}"]]);
  });
  test("SDK cursors remain opaque and traversal is bounded", async () => {
    const cursors: unknown[] = [];
    await expect((async () => {
      for await (const _page of iteratePages(async (cursor: unknown) => {
        cursors.push(cursor); return { data: [], next_cursor: "opaque+/=" };
      }, { maxPages: 2 })) { /* consume */ }
    })()).rejects.toThrow();
    expect(cursors).toEqual([undefined, "opaque+/="]);
  });
});

test.each([[200,0], [403,3], [409,5], [429,1], [406,2], [415,2], [299,1]])("stream boundary HTTP %i exits %i", async (status, expected) => {
  const stdout = new PassThrough(), stderr = new PassThrough();
  let out = "", err = "";
  stdout.on("data", chunk => { out += chunk; }); stderr.on("data", chunk => { err += chunk; });
  const code = await runCommand(["read"], { stdin: Readable.from([]), stdout, stderr }, undefined, program => {
    program.command("read").action(async () => {
      const refusal = status === 406 ? 'not_acceptable' : status === 415 ? 'unsupported_media_type' : undefined;
      const body = refusal ? JSON.stringify({ status, code: refusal }) : status === 299 ? '{' : '{"revision":"9223372036854775807"}';
      const result = await decodeResponse(new Response(body, { status }), 1000);
      await writeStdout({ stdin: Readable.from([]), stdout, stderr }, JSON.stringify(result) + "\n");
    });
  });
  expect(code).toBe(expected);
  expect(out).toBe(expected === 0 ? '{"revision":"9223372036854775807"}\n' : "");
  expect(err.length > 0).toBe(expected !== 0);
});

test.each(['{"revision":9007199254740993}', '{"revision":1e30}', '{', '{"x":1e999}'])("rejects malformed or unsafe wire %s", async body => {
  await expect(decodeResponse(new Response(body), 1000)).rejects.toMatchObject({ exitCode: 1 });
});
test("safe numeric maximum, exact strings, bytes and fatal UTF-8", async () => {
  expect(await decodeResponse(new Response('{"id":"0198ac5e-f9b7-7000-8000-000000000001","revision":9007199254740991}'), 200)).toMatchObject({ revision: Number.MAX_SAFE_INTEGER });
  expect(await decodeResponse(new Response('"9007199254740993"'), 100)).toBe("9007199254740993");
  await expect(decodeResponse(new Response('"long"'), 2)).rejects.toThrow();
  await expect(decodeResponse(new Response(new Uint8Array([0xff])), 100)).rejects.toThrow();
});
