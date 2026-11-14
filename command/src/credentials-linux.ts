import { MacOSCredentialStore, type StoredCredential } from './credentials-macos.ts';

export type LinuxFailure = 'missing-session-service' | 'locked' | 'denied' | 'unavailable';
export class LinuxCredentialError extends Error {
  constructor(readonly code: LinuxFailure) {
    super(`Linux credential store: ${code}`);
    this.name = 'LinuxCredentialError';
  }
}
/** Internal port. Values must never be passed in process arguments or diagnostics. */
export interface LinuxSecretService {
  read(service: string, username: string): Promise<string | null>;
  write(service: string, username: string, value: string): Promise<void>;
  remove(service: string, username: string): Promise<void>;
}

export class LinuxCredentialStore {
  constructor(private readonly port: LinuxSecretService) {}
  private async run<T>(action: (store: MacOSCredentialStore) => Promise<T>): Promise<T> {
    let failure: LinuxFailure | undefined;
    const call = async <R>(action: () => Promise<R>): Promise<R> => {
      try { return await action(); }
      catch (error) {
        failure = error instanceof LinuxCredentialError ? error.code : 'unavailable';
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
    catch (error) { if (failure) throw new LinuxCredentialError(failure); throw error; }
  }
  load(host: string): Promise<StoredCredential> { return this.run(store => store.load(host)); }
  save(host: string, value: StoredCredential): Promise<void> { return this.run(store => store.save(host, value)); }
  delete(host: string): Promise<void> { return this.run(store => store.delete(host)); }
}

/** Absolute helper path supplied by the package, never discovered through PATH. */
export function nativeLinuxCredentialStore(helper: string): LinuxCredentialStore {
  if (process.platform !== 'linux' || !helper.startsWith('/')) throw new LinuxCredentialError('unavailable');
  const invoke = async (operation: string, service: string, username: string, value = '') => {
    if ([service, username, value].some(part => part.includes('\0'))) throw new LinuxCredentialError('unavailable');
    try {
      const child = Bun.spawn([helper, operation], {
        stdin: new TextEncoder().encode(`${service}\0${username}\0${value}\0`),
        stdout: 'pipe', stderr: 'ignore', timeout: 15000,
      });
      const [output, status] = await Promise.all([new Response(child.stdout).text(), child.exited]);
      if (status === 0) return output;
      if (status === 4) return null;
      const codes: Record<number, LinuxFailure> = { 10: 'missing-session-service', 11: 'locked', 12: 'denied' };
      throw new LinuxCredentialError(codes[status] ?? 'unavailable');
    } catch (error) {
      throw new LinuxCredentialError(error instanceof LinuxCredentialError ? error.code : 'unavailable');
    }
  };
  return new LinuxCredentialStore({
    read: (service, username) => invoke('read', service, username),
    write: async (service, username, value) => { await invoke('write', service, username, value); },
    remove: async (service, username) => { await invoke('delete', service, username); },
  });
}
