import { test, expect } from 'bun:test';
import { resolve } from 'node:path';
import { nativeWindowsCredentialStore } from '../src/credentials-windows.ts';
// Explicit selector only; synthetic credentials, never real accounts or environment secrets.
const native = process.platform === 'win32' && ['x64', 'arm64'].includes(process.arch);
(native ? test : test.skip)('native Windows vault Unicode round-trip, replacement and logout deletion', async () => {
  expect(Bun.version).toBe('1.4.3');
  expect(Bun.revision).toBe('c6da4a4d3010e5553438c60f6bd76d981976867c');
  const host = `https://fixture-${crypto.randomUUID()}.invalid/東京/árvíz`;
  const store = nativeWindowsCredentialStore(resolve('dist/assign-credential-manager.exe'));
  const value = {access_token:'dummy-東京', refresh_token:'dummy-refresh', expires_at:'2026-11-13T12:00:00Z'};
  try {
    await expect(store.load(host)).rejects.toMatchObject({code:'not-found'});
    await store.save(host, value);
    expect(await store.load(host)).toEqual(value);
    await store.save(host, {...value, access_token:'replacement'});
    expect((await store.load(host)).access_token).toBe('replacement');
    await store.delete(host);
    await store.delete(host);
    await expect(store.load(host)).rejects.toMatchObject({code:'not-found'});
  } finally { await store.delete(host); }
});
