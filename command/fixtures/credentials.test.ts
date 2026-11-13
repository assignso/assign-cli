import { test, expect } from "bun:test";
import { inspect } from "node:util";
import { MacOSCredentialStore, CredentialStoreError, type MacOSSecrets } from "../src/credentials-macos.ts";

const host = "https://assign.example";
const value = { access_token: "dummy-access", refresh_token: "dummy-refresh", expires_at: "2026-11-13T12:00:00.123456789Z" };
function fixture() {
  const items = new Map<string, string>();
  const port: MacOSSecrets = {
    async get(key) { expect(key.service).toBe("Assign CLI"); return items.get(key.name) ?? null; },
    async set(key) { expect(key.service).toBe("Assign CLI"); items.set(key.name, key.value); },
    async delete(key) { expect(key.service).toBe("Assign CLI"); return items.delete(key.name); },
  };
  return { items, port, store: new MacOSCredentialStore(port) };
}

test("Go schema, exact account, host partition, update and idempotent deletion", async () => {
  const { items, store } = fixture();
  items.set(host, JSON.stringify({ schema: 1, ...value, ignored: true }));
  expect(await store.load(host)).toEqual(value);
  await store.save(host + "/", { ...value, access_token: "other" });
  await store.save(host, { ...value, access_token: "updated" });
  expect(JSON.parse(items.get(host)!)).toEqual({ schema: 1, ...value, access_token: "updated" });
  await store.delete(host);
  await store.delete(host);
  expect(await store.load(host + "/")).toEqual({ ...value, access_token: "other" });
  await expect(store.load(host)).rejects.toMatchObject({ code: "not-found" });
});

for (const state of ["denied", "locked", "unavailable"]) {
  for (const operation of ["load", "save", "delete"] as const) {
    test(`${state} ${operation} fails closed and discards native exception details`, async () => {
      const fail = async (): Promise<never> => { throw new Error(`native ${state} ${value.access_token} ${value.refresh_token}`); };
      const store = new MacOSCredentialStore({ get: fail, set: fail, delete: fail });
      try {
        if (operation === "save") await store.save(host, value);
        else await store[operation](host);
        throw new Error("expected failure");
      } catch (error) {
        expect(error).toBeInstanceOf(CredentialStoreError);
        expect(error).toMatchObject({ code: "unavailable" });
        for (const rendered of [String(error), inspect(error), JSON.stringify(error)]) {
          expect(rendered).not.toContain(value.access_token);
          expect(rendered).not.toContain(value.refresh_token);
          expect(rendered).not.toContain(`native ${state}`);
        }
        expect((error as Error).cause).toBeUndefined();
      }
    });
  }
}

test("corrupt vault payloads produce fixed errors without leaking payload", async () => {
  const { store, items } = fixture();
  for (const payload of ["dummy-access", "null", "[]", JSON.stringify({schema: 2, ...value}),
    JSON.stringify({schema: 1, ...value, refresh_token: ""}),
    JSON.stringify({schema: 1, ...value, expires_at: "bad"}),
    JSON.stringify({schema: 1, ...value, expires_at: "0001-01-01T00:00:00Z"})]) {
    items.set(host, payload);
    await expect(store.load(host)).rejects.toMatchObject({code: "invalid", message: "Stored CLI credential is invalid"});
  }
});

test("invalid save never calls the native port or overwrites existing data", async () => {
  const { store, items } = fixture();
  await store.save(host, value);
  const original = items.get(host);
  await expect(store.save(host, { ...value, access_token: "" })).rejects.toMatchObject({code: "invalid"});
  expect(items.get(host)).toBe(original);
});
