/** Internal vault port. Only the credential-owning process may instantiate this adapter. */
export interface MacOSSecrets {
  get(options: { service: string; name: string }): Promise<string | null>;
  set(options: { service: string; name: string; value: string }): Promise<void>;
  delete(options: { service: string; name: string }): Promise<boolean>;
}

export interface StoredCredential {
  access_token: string;
  refresh_token: string;
  expires_at: string;
}

export class CredentialStoreError extends Error {
  constructor(readonly code: "unavailable" | "not-found" | "invalid") {
    super(code === "invalid" ? "Stored CLI credential is invalid" :
      code === "not-found" ? "CLI credential not found" : "OS credential vault unavailable");
    this.name = "CredentialStoreError";
  }
}

function credential(value: unknown): StoredCredential {
  if (!value || typeof value !== "object") throw new CredentialStoreError("invalid");
  const item = value as Record<string, unknown>;
  if (typeof item.access_token !== "string" || !item.access_token ||
      typeof item.refresh_token !== "string" || !item.refresh_token ||
      typeof item.expires_at !== "string" ||
      !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(item.expires_at) ||
      !Number.isFinite(Date.parse(item.expires_at)) || item.expires_at.startsWith("0001-01-01T00:00:00")) {
    throw new CredentialStoreError("invalid");
  }
  return { access_token: item.access_token, refresh_token: item.refresh_token, expires_at: item.expires_at };
}

export class MacOSCredentialStore {
  constructor(private readonly secrets: MacOSSecrets) {}

  private key(host: string) {
    // Host is already selected/validated by the caller. Do not normalize existing account keys.
    return { service: "Assign CLI", name: host };
  }

  async load(host: string): Promise<StoredCredential> {
    let encoded: string | null;
    try { encoded = await this.secrets.get(this.key(host)); }
    catch { throw new CredentialStoreError("unavailable"); }
    if (encoded === null) throw new CredentialStoreError("not-found");
    try {
      const item = JSON.parse(encoded);
      if (item?.schema !== 1) throw new CredentialStoreError("invalid");
      return credential(item);
    } catch { throw new CredentialStoreError("invalid"); }
  }

  async save(host: string, value: StoredCredential): Promise<void> {
    const encoded = JSON.stringify({ schema: 1, ...credential(value) });
    try { await this.secrets.set({ ...this.key(host), value: encoded }); }
    catch { throw new CredentialStoreError("unavailable"); }
  }

  async delete(host: string): Promise<void> {
    try { await this.secrets.delete(this.key(host)); }
    catch { throw new CredentialStoreError("unavailable"); }
  }
}

/** Lazy native binding: importing fixtures never loads or reads the user's vault. */
export async function nativeMacOSCredentialStore(): Promise<MacOSCredentialStore> {
  if (process.platform !== "darwin") throw new CredentialStoreError("unavailable");
  try {
    const { secrets } = await import("bun");
    return new MacOSCredentialStore(secrets);
  } catch { throw new CredentialStoreError("unavailable"); }
}
