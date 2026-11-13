// Compiled adapter integration probe. Never reads or modifies the real vault.
import { MacOSCredentialStore, nativeMacOSCredentialStore } from "../src/credentials-macos.ts";
import { strict as assert } from "node:assert";
if (process.platform === "darwin") {
  assert.ok(await nativeMacOSCredentialStore() instanceof MacOSCredentialStore);
  const { secrets } = await import("bun");
  for (const method of ["get", "set", "delete"] as const) assert.equal(typeof secrets[method], "function");
} else {
  await assert.rejects(nativeMacOSCredentialStore(), { code: "unavailable" });
}
let encoded: string | null = null;
const store = new MacOSCredentialStore({
  async get() { return encoded; },
  async set(key) { assert.equal(key.service, "Assign CLI"); encoded = key.value; },
  async delete() { encoded = null; return true; },
});
const dummy = { access_token: "prepared-access", refresh_token: "prepared-refresh", expires_at: "2026-11-13T12:00:00Z" };
await store.save("https://assign.example", dummy);
assert.deepEqual(await store.load("https://assign.example"), dummy);
await store.delete("https://assign.example");
await assert.rejects(store.load("https://assign.example"), { code: "not-found" });
console.log("credential adapter compiled fixture passed");
