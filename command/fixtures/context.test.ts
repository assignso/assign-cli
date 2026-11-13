import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { contextPath, loadProject, saveProject } from "../src/context.ts";

test("context paths keep environment overrides separate from settings", () => {
  assert.equal(contextPath({ ASSIGN_CONFIG_FILE: "ignored", APPDATA: "D:\\Users\\名 Space" }, "win32", ""), "D:\\Users\\名 Space\\assign\\context.json");
  assert.equal(contextPath({ ASSIGN_CONTEXT_FILE: "D:\\名 Space\\context.json" }, "win32", ""), "D:\\名 Space\\context.json");
  assert.equal(contextPath({}, "linux", "/home/名 Space"), "/home/名 Space/.config/assign/context.json");
});

test("Go context partitions survive reads, updates and failed atomic replacement", async () => {
  const directory = await mkdtemp(join(tmpdir(), "assign-context-"));
  const file = join(directory, "名 Space.json");
  const a = "https://one.invalid", b = "https://two.invalid";
  try {
    assert.equal(await loadProject(file, a, "workspace-a"), undefined);
    const original = JSON.stringify({ schema: 1, projects: { [a + "\nworkspace-a"]: "FIRST", [b + "\nworkspace-a"]: "OTHER" } });
    await writeFile(file, original, { mode: 0o600 });
    assert.equal(await loadProject(file, a, "workspace-a"), "FIRST");
    assert.equal(await loadProject(file, a, "workspace-b"), undefined);
    assert.equal(await readFile(file, "utf8"), original);
    await assert.rejects(saveProject(file, a, "workspace-a", "NEXT", async () => { throw Error("replace failed"); }));
    assert.equal(await readFile(file, "utf8"), original);
    assert.deepEqual(await readdir(directory), ["名 Space.json"]);
    await saveProject(file, a, "workspace-b", "SECOND");
    assert.equal(await loadProject(file, a, "workspace-a"), "FIRST");
    assert.equal(await loadProject(file, b, "workspace-a"), "OTHER");
    assert.equal(await loadProject(file, a, "workspace-b"), "SECOND");
    const stored = JSON.parse(await readFile(file, "utf8"));
    assert.deepEqual(Object.keys(stored).sort(), ["projects", "schema"]);
    await assert.rejects(saveProject(file, "https://token@one.invalid", "workspace-a", "BAD"));
    await assert.rejects(saveProject(file, a, "workspace\ninjection", "BAD"));
    for (const data of ['{', '{"schema":2,"projects":{}}', '{"schema":1,"projects":null}', '{"schema":1,"projects":{"x":42}}']) {
      await writeFile(file, data);
      await assert.rejects(loadProject(file, a, "workspace-a"));
      await assert.rejects(saveProject(file, a, "workspace-a", "NEXT"));
      assert.equal(await readFile(file, "utf8"), data);
    }
  } finally { await rm(directory, { recursive: true, force: true }); }
});


test("context rejects symlinks, public files and oversized data without replacement", async () => {
  const directory = await mkdtemp(join(tmpdir(), "assign-context-security-"));
  const target = join(directory, "target.json"), link = join(directory, "link.json");
  const body = '{"schema":1,"projects":{}}';
  try {
    await writeFile(target, body, { mode: 0o600 });
    if (process.platform !== "win32") {
      await symlink(target, link);
      await assert.rejects(loadProject(link, "https://one.invalid", "workspace"));
      await assert.rejects(saveProject(link, "https://one.invalid", "workspace", "PROJECT"));
      assert.equal(await readFile(target, "utf8"), body);
      await chmod(target, 0o644);
      await assert.rejects(loadProject(target, "https://one.invalid", "workspace"));
      await chmod(target, 0o600);
    }
    await writeFile(target, "x".repeat(1024 * 1024 + 1));
    await assert.rejects(loadProject(target, "https://one.invalid", "workspace"));
    await assert.rejects(saveProject(target, "https://one.invalid", "workspace", "PROJECT"));
  } finally { await rm(directory, { recursive: true, force: true }); }
});
