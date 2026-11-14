import type { FetchAPI } from "../transport.ts";

export interface WorkerTransport {
  origin: string;
  /** Access token for the request's configured credential family. */
  accessToken: () => Promise<string>;
  fetchApi?: FetchAPI;
}

type RequestData = { query: URLSearchParams } | { body: unknown; maxBytes: number };

/** Return the raw response so the caller retains Problem Details and Retry-After. */
export function createWorkerRequest(transport: WorkerTransport, bearerPrefix: string, credentialLabel = "an interactive Assign CLI access token", encodeBody = JSON.stringify) {
  const origin = new URL(transport.origin);
  if (origin.protocol !== "https:" || origin.username || origin.password ||
      origin.pathname !== "/" || origin.search || origin.hash) {
    throw new TypeError("Worker API requires an HTTPS origin");
  }
  const { accessToken, fetchApi = fetch } = transport;
  return async (method: string, path: string, signal?: AbortSignal, data?: RequestData): Promise<Response> => {
    signal?.throwIfAborted();
    const url = new URL(path, origin);
    const headers = new Headers({ Accept: "application/json, application/problem+json" });
    let body: string | undefined;
    if (data && "body" in data) {
      body = encodeBody(data.body);
      if (body === undefined) throw new TypeError("Worker request body is required");
      if (new TextEncoder().encode(body).byteLength > data.maxBytes) {
        throw new RangeError("Worker request exceeds the UTF-8 body limit");
      }
      headers.set("Content-Type", "application/json");
    } else if (data) {
      url.search = data.query.toString();
    }
    const token = await accessToken();
    signal?.throwIfAborted();
    if (!token.startsWith(bearerPrefix) || token.length === bearerPrefix.length || !/^[A-Za-z0-9_-]+$/.test(token)) {
      throw new TypeError(`Worker API requires ${credentialLabel}`);
    }
    headers.set("Authorization", `Bearer ${token}`);
    const response = await fetchApi(url, {
      method, headers, body, signal, redirect: "error", credentials: "omit", cache: "no-store",
    });
    if (response.redirected || (response.url && new URL(response.url).origin !== origin.origin) ||
        (response.status >= 300 && response.status < 400)) {
      void response.body?.cancel().catch(() => {});
      throw new TypeError("Worker API redirect rejected");
    }
    return response;
  };
}
