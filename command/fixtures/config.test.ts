import assert from "node:assert/strict";
import { mkdtemp, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
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
