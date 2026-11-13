import { strict as assert } from 'node:assert';
import { LinuxCredentialStore, LinuxCredentialError } from '../src/credentials-linux.ts';
const items = new Map<string, string>();
const store = new LinuxCredentialStore({
  async read(s, u) { assert.equal(s, 'Assign CLI'); return items.get(u) ?? null; },
  async write(s, u, v) { assert.equal(s, 'Assign CLI'); items.set(u, v); },
  async remove(s, u) { assert.equal(s, 'Assign CLI'); items.delete(u); },
});
const host = 'https://dummy.invalid';
const credential = {access_token: 'dummy', refresh_token: 'dummy', expires_at: '2026-11-13T12:00:00Z'};
await store.save(host, credential);
assert.deepEqual(await store.load(host), credential);
await store.delete(host);
await assert.rejects(store.load(host), {code: 'not-found'});
const fail = async (): Promise<never> => { throw new LinuxCredentialError('locked'); };
await assert.rejects(new LinuxCredentialStore({read: fail, write: fail, remove: fail}).load(host), {code: 'locked'});
console.log('Linux credential compiled fake-store fixture passed');
