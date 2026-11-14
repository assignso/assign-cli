import { test, expect } from 'bun:test';
import { inspect } from 'node:util';
import { LinuxCredentialStore, LinuxCredentialError, type LinuxFailure } from '../src/credentials-linux.ts';
const host = 'https://assign.example';
const value = {access_token: 'dummy-access', refresh_token: 'dummy-refresh', expires_at: '2026-11-13T12:00:00Z'};
test('Linux Go attributes, schema, exact host partitions, replacement and idempotent deletion', async () => {
  const items = new Map<string, string>();
  const key = (service: string, username: string) => { expect(service).toBe('Assign CLI'); return username; };
  const store = new LinuxCredentialStore({
    async read(s, u) { return items.get(key(s, u)) ?? null; },
    async write(s, u, v) { items.set(key(s, u), v); },
    async remove(s, u) { items.delete(key(s, u)); },
  });
  await store.save(host, value);
  expect(JSON.parse(items.get(host)!)).toEqual({schema: 1, ...value});
  await store.save(host + '/', value);
  await store.save(host, {...value, access_token: 'replacement'});
  expect((await store.load(host)).access_token).toBe('replacement');
  await store.delete(host); await store.delete(host);
  await expect(store.load(host)).rejects.toMatchObject({code:'not-found'});
  expect(await store.load(host + '/')).toEqual(value);
  items.set(host, 'dummy-access');
  await expect(store.load(host)).rejects.toMatchObject({code:'invalid'});
  await expect(store.save(host, {...value, access_token:''})).rejects.toMatchObject({code:'invalid'});
  expect(items.get(host)).toBe('dummy-access');
});
for (const condition of ['missing-session-service', 'locked', 'denied', 'unavailable'] as LinuxFailure[]) {
  for (const operation of ['load', 'save', 'delete'] as const) {
    test(`${condition}: ${operation} fails closed with content-free diagnostics`, async () => {
      const fail = async (): Promise<never> => { throw new LinuxCredentialError(condition); };
      const store = new LinuxCredentialStore({read:fail, write:fail, remove:fail});
      try {
        if (operation === 'save') await store.save(host, value); else await store[operation](host);
        throw new Error('expected failure');
      } catch (error) {
        expect(error).toBeInstanceOf(LinuxCredentialError);
        expect(error).toMatchObject({code:condition});
        expect(inspect(error)).not.toContain(value.access_token);
        expect((error as Error).cause).toBeUndefined();
      }
    });
  }
}
test('unknown native errors discard secrets and underlying messages', async () => {
  const fail = async (): Promise<never> => { throw new Error(value.access_token); };
  const store = new LinuxCredentialStore({read:fail, write:fail, remove:fail});
  await expect(store.load(host)).rejects.toMatchObject({code:'unavailable', message:'Linux credential store: unavailable'});
});
