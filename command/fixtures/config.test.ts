import assert from "node:assert/strict";
import { chmod, mkdtemp, readdir, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { configPath, loadHost, resetHost, saveHost, validateHost } from "../src/config.ts";

test("config location preserves Go paths and the explicit file override", () => {
  assert.equal(configPath({ APPDATA: "C:\\Users\\Fixture 名\\App Data" }, "win32", ""),
    "C:\\Users\\Fixture 名\\App Data\\assign\\settings.json");
  assert.equal(configPath({}, "darwin", "/tmp/fixture home"),
    "/tmp/fixture home/Library/Application Support/assign/settings.json");
  assert.equal(configPath({ XDG_CONFIG_HOME: "/tmp/fixture" }, "linux", ""),
    "/tmp/fixture/assign/settings.json");
  assert.equal(configPath({ ASSIGN_CONFIG_FILE: "/tmp/fixture settings" }, "linux", ""),
    "/tmp/fixture settings");
});

test("atomic replacement preserves the previous Go settings after a failed rename", async () => {
  const directory = await mkdtemp(join(tmpdir(), "assign-settings-"));
  const file = join(directory, "settings 名.json");
  try {
    assert.equal(await loadHost(file), undefined);
    const original = '{"schema":1,"host":"https://fixture.invalid"}\n';
    await writeFile(file, original, { mode: 0o600 });
    assert.equal(await loadHost(file), "https://fixture.invalid");
    await assert.rejects(saveHost(file, "https://next.invalid", async () => { throw new Error("rename failed"); }));
    assert.equal(await readFile(file, "utf8"), original);
    assert.deepEqual(await readdir(directory), ["settings 名.json"]);
    await saveHost(file, "https://next.invalid/");
    assert.equal(await loadHost(file), "https://next.invalid");
    if (process.platform !== "win32") assert.equal((await stat(file)).mode & 0o777, 0o600);
    await resetHost(file);
    assert.equal(await loadHost(file), undefined);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("host selection rejects credentials, unsafe origins and unsupported configuration", async () => {
  for (const host of ["http://fixture.invalid", "https://secret@fixture.invalid", "https://fixture.invalid/path",
    "https://fixture.invalid?token=fixture", "https://fixture.invalid#fragment", "https://fixture.invalid\\path"]) {
    assert.throws(() => validateHost(host));
  }
  const directory = await mkdtemp(join(tmpdir(), "assign-settings-"));
  const file = join(directory, "settings.json");
  try {
    for (const input of ['{"schema":2,"host":"https://fixture.invalid"}',
      '{"schema":1,"host":"https://fixture.invalid","token":"never-display"}',
      '{"schema":1,"host":"https://fixture.invalid"} {}', "x".repeat(4097)]) {
      await writeFile(file, input, { mode: 0o600 });
      await assert.rejects(loadHost(file), error => error instanceof Error && !error.message.includes("never-display"));
    }
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("CLI precedence, repair and diagnostics preserve the settings and context", async () => {
  const { runCommand } = await import("../src/main.ts");
  const { PassThrough, Readable } = await import("node:stream");
  const directory = await mkdtemp(join(tmpdir(), "assign precedence 名 "));
  const file = join(directory, "settings.json");
  const previous = process.env.ASSIGN_CONFIG_FILE;
  process.env.ASSIGN_CONFIG_FILE = file;
  const run = async (args: string[]) => {
    const stdout = new PassThrough(), stderr = new PassThrough();
    const exit = await runCommand(args, { stdin: Readable.from([]), stdout, stderr });
    return { exit, out: stdout.read()?.toString() ?? "", err: stderr.read()?.toString() ?? "" };
  };
  try {
    assert.deepEqual(await run(["config"]), { exit: 0, out: "API host: https://api.assign.so\nHost source: default\n", err: "" });
    assert.equal((await run(["config", "set", "host", "https://stored.invalid"])).exit, 0);
    assert.match((await run(["config"])).out, /stored.invalid\nHost source: stored/);
    const original = await readFile(file, "utf8");
    assert.match((await run(["--host", "https://flag.invalid", "config"])).out, /flag.invalid\nHost source: flag/);
    assert.equal(await readFile(file, "utf8"), original);
    for (const host of ["http://bad.invalid", "https://secret@bad.invalid", "https://bad.invalid/path"]) {
      const result = await run(["--host", host, "config"]);
      assert.equal(result.exit, 2);
      assert.equal(result.out, "");
      assert.match(result.err, /^assign: --host must/);
      assert.ok(!result.err.includes("secret"));
      assert.equal(await readFile(file, "utf8"), original);
    }
    await writeFile(file, '{"schema":2}');
    assert.equal((await run(["config"])).exit, 2);
    assert.equal((await run(["--host", "https://flag.invalid", "config"])).exit, 0);
    assert.equal((await run(["version"])).exit, 0);
    assert.equal((await run(["config", "set", "host", "https://repaired.invalid"])).exit, 0);
    assert.equal((await run(["config", "unset", "host"])).exit, 0);
    assert.equal((await run(["config", "unset", "host"])).exit, 0);
    assert.deepEqual(await readdir(directory), []);
  } finally {
    if (previous === undefined) delete process.env.ASSIGN_CONFIG_FILE; else process.env.ASSIGN_CONFIG_FILE = previous;
    await rm(directory, { recursive: true, force: true });
  }
});


test("settings reject public and symlinked files for read, save and reset", async () => {
  if (process.platform === "win32") return; // Native ACL qualification is a separate gate.
  const directory = await mkdtemp(join(tmpdir(), "assign-settings-security-"));
  const file = join(directory, "settings.json"), link = join(directory, "link.json");
  const body = '{"schema":1,"host":"https://one.invalid"}';
  try {
    await writeFile(file, body, { mode: 0o600 });
    await symlink(file, link);
    for (const path of [link, file]) {
      if (path === file) await chmod(file, 0o644);
      await assert.rejects(loadHost(path));
      await assert.rejects(saveHost(path, "https://next.invalid"));
      await assert.rejects(resetHost(path));
      assert.equal(await readFile(file, "utf8"), body);
    }
  } finally { await rm(directory, { recursive: true, force: true }); }
});
