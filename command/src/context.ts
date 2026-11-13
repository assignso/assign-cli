import { constants } from "node:fs";
import { lstat, open, rename } from "node:fs/promises";
import { atomicWrite, configPath, validateHost } from "./config.ts";
import { CommandFailure } from "./main.ts";
import { posix, win32 } from "node:path";

const limit = 1024 * 1024;
const invalid = () => new CommandFailure("stored CLI context is invalid", 2);
type Context = { schema: 1; projects: Record<string, string> };

export function contextPath(env: NodeJS.ProcessEnv, platform: NodeJS.Platform, home: string): string {
  if (env.ASSIGN_CONTEXT_FILE) return env.ASSIGN_CONTEXT_FILE;
  const path = platform === "win32" ? win32 : posix;
  // The Go context store falls back to ~/.config when UserConfigDir is unavailable.
  const settings = configPath({ ...env, ASSIGN_CONFIG_FILE: undefined,
    ...(platform === "win32" && !env.APPDATA && home ? { APPDATA: path.join(home, ".config") } : {}) }, platform, home);
  return path.join(path.dirname(settings), "context.json");
}

async function readContext(path: string): Promise<Context> {
  let file;
  try {
    const info = await lstat(path);
    if (!info.isFile()) throw invalid();
    file = await open(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) | (constants.O_NONBLOCK ?? 0));
  }
  catch (error) {
    if (error instanceof Error && "code" in error && error.code === "ENOENT") return { schema: 1, projects: {} };
    throw invalid();
  }
  try {
    const info = await file.stat();
    if (!info.isFile() || info.size > limit || (process.platform !== "win32" && (info.mode & 0o077))) throw invalid();
    const bytes = Buffer.alloc(limit + 1);
    let total = 0;
    while (total < bytes.length) {
      const { bytesRead } = await file.read(bytes, total, bytes.length - total, null);
      if (!bytesRead) break;
      total += bytesRead;
    }
    if (total > limit) throw invalid();
    const value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(0, total)));
    if (!value || value.schema !== 1 || !value.projects || typeof value.projects !== "object" ||
        Array.isArray(value.projects) || Object.values(value.projects).some(v => typeof v !== "string")) throw invalid();
    return { schema: 1, projects: value.projects };
  } catch { throw invalid(); }
  finally { await file.close(); }
}

function key(host: string, workspace: string): string {
  if (!workspace || /[\r\n]/.test(workspace)) throw new CommandFailure("Workspace selection is invalid", 2);
  return validateHost(host) + "\n" + workspace;
}

export async function loadProject(path: string, host: string, workspace: string): Promise<string | undefined> {
  const partition = key(host, workspace);
  return (await readContext(path)).projects[partition] || undefined;
}

export async function saveProject(path: string, host: string, workspace: string, project: string, replace = rename): Promise<void> {
  const partition = key(host, workspace);
  if (!project || /[\r\n]/.test(project)) throw new CommandFailure("Project selection is invalid", 2);
  const value = await readContext(path);
  Object.defineProperty(value.projects, partition, { value: project, enumerable: true, configurable: true, writable: true });
  const contents = JSON.stringify(value) + "\n";
  if (Buffer.byteLength(contents) > limit) throw invalid();
  await atomicWrite(path, contents, replace, limit);
}
