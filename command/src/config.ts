import type { Command } from "commander";
import { constants } from "node:fs";
import { lstat, mkdir, mkdtemp, open, rename, rm, unlink } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, posix, win32 } from "node:path";
import { CommandFailure, writeStdout } from "./main.ts";
import type { CommandIO } from "./main.ts";

const defaultHost = "https://api.assign.so";
const maxSettingsSize = 4096;
const privateSettingsError = "CLI settings must be a regular, private file of at most 4 KiB";
const invalidSettingsError = "CLI settings are invalid; run assign config set host <https-origin> to replace them";

function hasCode(error: unknown, code: string): boolean {
  return error instanceof Error && "code" in error && error.code === code;
}

export function configPath(env: NodeJS.ProcessEnv, platform: NodeJS.Platform, home: string): string {
  if (env.ASSIGN_CONFIG_FILE) return env.ASSIGN_CONFIG_FILE;
  const directory = platform === "win32" ? env.APPDATA
    : platform === "darwin" ? home && posix.join(home, "Library", "Application Support")
    : env.XDG_CONFIG_HOME || (home && posix.join(home, ".config"));
  if (!directory) throw new CommandFailure("user configuration directory is unavailable; set ASSIGN_CONFIG_FILE", 2);
  return (platform === "win32" ? win32 : posix).join(directory, "assign", "settings.json");
}

export function validateHost(raw: string): string {
  let url: URL;
  try {
    if (!raw.startsWith("https://") || /[\s\\]/.test(raw)) throw new Error();
    url = new URL(raw);
    if (!url.hostname) throw new Error();
  } catch {
    throw new CommandFailure("--host must be an absolute HTTPS URL", 2);
  }
  if (url.username || url.password || raw.includes("@") || raw.includes("?") || raw.includes("#") ||
      url.pathname !== "/" || raw.slice("https://".length).replace(/\/$/, "").includes("/")) {
    throw new CommandFailure("--host must not include credentials, a path, query, or fragment", 2);
  }
  return url.origin;
}

function checkInfo(info: { isFile(): boolean; mode: number; size: number }) {
  if (!info.isFile() || (process.platform !== "win32" && (info.mode & 0o077) !== 0) ||
      info.size > maxSettingsSize) {
    throw new CommandFailure(privateSettingsError, 2);
  }
}

async function checkFile(path: string): Promise<boolean> {
  try {
    checkInfo(await lstat(path));
    return true;
  } catch (error) {
    if (hasCode(error, "ENOENT")) return false;
    throw error;
  }
}

export async function loadHost(path: string): Promise<string | undefined> {
  if (!await checkFile(path)) return undefined;
  const file = await open(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) |
    (constants.O_NONBLOCK ?? 0));
  let settings: unknown;
  try {
    checkInfo(await file.stat());
    const bytes = Buffer.alloc(maxSettingsSize + 1);
    let total = 0;
    while (total < bytes.length) {
      const { bytesRead } = await file.read(bytes, total, bytes.length - total, null);
      if (!bytesRead) break;
      total += bytesRead;
    }
    if (total > maxSettingsSize) throw new CommandFailure(privateSettingsError, 2);
    try {
      settings = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(0, total)));
    } catch {
      throw new CommandFailure(invalidSettingsError, 2);
    }
  } finally {
    await file.close();
  }
  if (typeof settings !== "object" || settings === null || Object.keys(settings).length !== 2 ||
      !("schema" in settings) || settings.schema !== 1 || !("host" in settings) ||
      typeof settings.host !== "string") {
    throw new CommandFailure(invalidSettingsError, 2);
  }
  try {
    return validateHost(settings.host);
  } catch {
    throw new CommandFailure("stored API host is invalid; run assign config set host <https-origin>", 2);
  }
}

export async function saveHost(path: string, input: string, replace = rename): Promise<void> {
  const host = validateHost(input);
  await atomicWrite(path, JSON.stringify({ schema: 1, host }) + "\n", replace);
}

export async function atomicWrite(path: string, contents: string, replace = rename, sizeLimit = maxSettingsSize): Promise<void> {
  try {
    const info = await lstat(path);
    if (!info.isFile() || (process.platform !== "win32" && (info.mode & 0o077)) || info.size > sizeLimit) {
      throw new CommandFailure(privateSettingsError, 2);
    }
  } catch (error) { if (!hasCode(error, "ENOENT")) throw error; }
  const directory = dirname(path);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const temporary = await mkdtemp(join(directory, ".settings-"));
  const source = join(temporary, "settings.json");
  try {
    const file = await open(source, "wx", 0o600);
    try {
      await file.writeFile(contents);
      await file.sync();
    } finally {
      await file.close();
    }
    await replace(source, path);
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}

export async function resetHost(path: string): Promise<void> {
  if (await checkFile(path)) {
    try {
      await unlink(path);
    } catch (error) {
      if (!hasCode(error, "ENOENT")) throw error;
    }
  }
}

export function registerConfig(program: Command, io: CommandIO) {
  const current = () => configPath(process.env, process.platform, homedir());
  const show = async () => {
    const flag = program.opts<{ host?: string }>().host;
    const stored = flag === undefined ? await loadHost(current()) : undefined;
    const host = flag === undefined ? stored ?? defaultHost : validateHost(flag);
    const source = flag !== undefined ? "flag" : stored === undefined ? "default" : "stored";
    await writeStdout(io, "API host: " + host + "\nHost source: " + source + "\n");
  };
  const config = program.command("config").description("Inspect and change local CLI settings").action(show);
  config.command("show").description("Show the effective API host without credentials").action(show);
  config.command("set").argument("<setting>").argument("<https-origin>").action(async (setting: string, host: string) => {
    if (setting !== "host") throw new CommandFailure("only the host setting is supported", 2);
    validateHost(host);
    try { await saveHost(current(), host); }
    catch { throw new CommandFailure("could not save CLI settings", 1); }
    await writeStdout(io, "API host saved\n");
  });
  config.command("unset").argument("<setting>").action(async (setting: string) => {
    if (setting !== "host") throw new CommandFailure("only the host setting is supported", 2);
    try { await resetHost(current()); }
    catch { throw new CommandFailure("could not reset CLI settings", 1); }
    await writeStdout(io, "API host reset\n");
  });
}
