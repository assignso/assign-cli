import { MacOSCredentialStore, type StoredCredential } from './credentials-macos.ts';

export type WindowsFailure = 'denied' | 'unavailable';
export class WindowsCredentialError extends Error {
  constructor(readonly code: WindowsFailure) {
    super(`Windows credential store: ${code}`);
    this.name = 'WindowsCredentialError';
  }
}
/** Internal port. Values must never be passed in process arguments or diagnostics. */
export interface WindowsCredentialManager {
  read(service: string, username: string): Promise<string | null>;
  write(service: string, username: string, value: string): Promise<void>;
  remove(service: string, username: string): Promise<void>;
}

export class WindowsCredentialStore {
  constructor(private readonly port: WindowsCredentialManager) {}
  private async run<T>(action: (store: MacOSCredentialStore) => Promise<T>): Promise<T> {
    let failure: WindowsFailure | undefined;
    const call = async <R>(action: () => Promise<R>): Promise<R> => {
      try { return await action(); }
      catch (error) {
        failure = error instanceof WindowsCredentialError ? error.code : 'unavailable';
        throw new Error('vault operation failed');
      }
    };
    // Reuse the schema-1 codec and exact host partition from the macOS adapter.
    const store = new MacOSCredentialStore({
      get: ({ service, name }) => call(() => this.port.read(service, name)),
      set: ({ service, name, value }) => call(() => this.port.write(service, name, value)),
      delete: ({ service, name }) => call(async () => { await this.port.remove(service, name); return true; }),
    });
    try { return await action(store); }
    catch (error) { if (failure) throw new WindowsCredentialError(failure); throw error; }
  }
  load(host: string): Promise<StoredCredential> { return this.run(store => store.load(host)); }
  save(host: string, value: StoredCredential): Promise<void> { return this.run(store => store.save(host, value)); }
  delete(host: string): Promise<void> { return this.run(store => store.delete(host)); }
}

/** Package-owned absolute native helper; never resolved from PATH. */
export function nativeWindowsCredentialStore(helper: string): WindowsCredentialStore {
  if (process.platform !== 'win32' || !/^[A-Za-z]:[\\/]/.test(helper)) throw new WindowsCredentialError('unavailable');
  const invoke = async (operation: string, service: string, username: string, value = '') => {
    if ([service, username, value].some(part => part.includes('\0'))) throw new WindowsCredentialError('unavailable');
    try {
      const child = Bun.spawn([helper, operation], {
        stdin: new TextEncoder().encode(`${service}\0${username}\0${value}\0`),
        stdout: 'pipe', stderr: 'ignore', timeout: 15000,
      });
      const [output, status] = await Promise.all([new Response(child.stdout).text(), child.exited]);
      if (status === 0) return output;
      if (status === 4 && operation === 'read') return null;
      throw new WindowsCredentialError(status === 12 ? 'denied' : 'unavailable');
    } catch (error) {
      throw new WindowsCredentialError(error instanceof WindowsCredentialError ? error.code : 'unavailable');
    }
  };
  return new WindowsCredentialStore({
    read: (service, username) => invoke('read', service, username),
    write: async (service, username, value) => { await invoke('write', service, username, value); },
    remove: async (service, username) => { await invoke('delete', service, username); },
  });
}
