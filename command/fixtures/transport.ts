import { readFileSync, writeFileSync } from "node:fs";

interface RequestFixture {
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: string;
  status: number;
  response: string;
}

export interface FixtureCase {
  argv: string[];
  stdin?: string;
  brokenPipe?: boolean;
  requests: RequestFixture[];
}

const fixtureOrigin = "https://cli-fixture.invalid";
export const fixtureToken = "apt_cli_fixture_only";

export function loadFixture() {
  const path = process.env.ASSIGN_FIXTURE_CASE;
  const tracePath = process.env.ASSIGN_FIXTURE_TRACE;
  if (!path || !tracePath) throw new Error("Missing fixture paths");

  const fixture = JSON.parse(readFileSync(path, "utf8")) as FixtureCase;
  if (!Array.isArray(fixture.requests) || fixture.requests.length > 8) {
    throw new Error("Invalid request fixture");
  }

  const records: Array<{ method: string; path: string }> = [];
  let unexpected = false;
  const fetchApi = async (input: string | URL | Request, init?: RequestInit) => {
    const request = new Request(input, init);
    const url = new URL(request.url);
    url.searchParams.sort();
    const path = url.pathname + url.search;
    const next = fixture.requests[records.length];
    records.push({ method: request.method, path });
    const headersMatch = Object.entries(next?.headers ?? {})
      .every(([key, value]) => request.headers.get(key) === value);
    if (url.origin !== fixtureOrigin || !next || next.method !== request.method ||
        next.path !== path || !headersMatch ||
        request.headers.get("Authorization") !== "Bearer " + fixtureToken ||
        (await request.text()) !== (next.body ?? "")) {
      unexpected = true;
      throw new Error("Unexpected fixture request");
    }
    return new Response(next.response, {
      status: next.status,
      headers: { "Content-Type": "application/json" },
    });
  };

  return {
    fixture,
    fetchApi,
    origin: fixtureOrigin,
    finish() {
      writeFileSync(tracePath, JSON.stringify(records));
      return !unexpected && records.length === fixture.requests.length;
    },
  };
}
