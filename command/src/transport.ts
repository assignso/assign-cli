import { CommandFailure } from "./main.ts";

/** Bind the reviewed public SDK at composition time; no sibling/domain imports. */
export type FetchAPI = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
type RetryPolicy = (context: { url: string; method: string; headers: Headers }) =>
  "safe" | "idempotent" | "key-required" | "never";

export interface SDKRecovery {
  createRecoveryFetch(options: {
    fetchApi: FetchAPI; timeoutMs: number; maxAttempts: number; retryPolicy?: RetryPolicy;
  }): FetchAPI;
}

export function transportConfiguration(sdk: SDKRecovery, options: {
  origin: string; accessToken: () => Promise<string>; fetchApi?: FetchAPI;
  timeoutMs?: number; maxAttempts?: number;
  /** Only pass classifications supplied by the canonical operation owner. */
  retryPolicy?: RetryPolicy;
}) {
  const base = new URL(options.origin);
  if (base.protocol !== "https:" || base.username || base.password ||
      base.pathname !== "/" || base.search || base.hash) {
    throw new CommandFailure("API origin must be an HTTPS origin", 2);
  }
  const fetchApi: FetchAPI = async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    if (url.origin !== base.origin || url.username || url.password) {
      throw new CommandFailure("API request origin rejected", 1);
    }
    const response = await (options.fetchApi ?? fetch)(input, {
      ...init, redirect: "error", credentials: "omit",
    });
    if (response.redirected || (response.url && new URL(response.url).origin !== base.origin) ||
        (response.status >= 300 && response.status < 400)) {
      void response.body?.cancel().catch(() => {});
      throw new CommandFailure("API redirect rejected", 1);
    }
    return response;
  };
  return {
    basePath: base.origin, accessToken: options.accessToken,
    fetchApi: sdk.createRecoveryFetch({ fetchApi, timeoutMs: options.timeoutMs ?? 30000,
      maxAttempts: options.maxAttempts ?? 3, retryPolicy: options.retryPolicy }),
  };
}

export function responseFailure(status: number): CommandFailure {
  const code = status === 401 || status === 403 ? 3 :
    [400, 406, 413, 415, 422].includes(status) ? 2 : status === 409 || status === 412 ? 5 : 1;
  return new CommandFailure(`API request failed with HTTP ${status}`, code);
}

/** Keep raw wire shape; operation owners validate before writing any stdout. */
export async function decodeResponse(response: Response, maxBytes: number): Promise<unknown> {
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 1) throw new RangeError("Invalid response limit");
  if (!response.ok) {
    void response.body?.cancel().catch(() => {});
    throw responseFailure(response.status);
  }
  const reader = response.body?.getReader();
  if (!reader) throw new CommandFailure("Malformed API response", 1);
  try {
    const decoder = new TextDecoder("utf-8", { fatal: true });
    let text = "", bytes = 0;
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      bytes += part.value.byteLength;
      if (bytes > maxBytes) throw new Error("limit");
      text += decoder.decode(part.value, { stream: true });
    }
    text += decoder.decode();
    // Inspect number lexemes before JSON.parse can round them. Rejecting all
    // unsafe magnitudes is conservative and does not invent an identity schema.
    const tokens = text.match(/"(?:[^"\\]|\\.)*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g) ?? [];
    for (const token of tokens) {
      if (token.startsWith('"')) continue;
      const number = Number(token);
      if (!Number.isFinite(number) || Math.abs(number) > Number.MAX_SAFE_INTEGER ||
          (Number.isInteger(number) && /[.eE]/.test(token))) throw new Error("unsafe number");
    }
    return JSON.parse(text);
  } catch {
    throw new CommandFailure("Malformed or unsafe API response", 1);
  } finally {
    void reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export async function sdkResult<T>(operation: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  try {
    signal?.throwIfAborted();
    return await operation();
  } catch (error) {
    if (signal?.aborted) throw new CommandFailure("Request interrupted", 130);
    if (error instanceof CommandFailure) throw error;
    // SDK ResponseError is intentionally recognized structurally across bundles.
    if (error instanceof Error && "response" in error && error.response instanceof Response) {
      void error.response.body?.cancel().catch(() => {});
      throw responseFailure(error.response.status);
    }
    throw new CommandFailure("API request failed", 1);
  }
}
