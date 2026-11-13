import { afterEach, expect, test } from "bun:test";
import { execFile } from "node:child_process";
import { access, chmod, mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { processIO } from "../src/main.ts";
import { withExternalWorkerEnvironment, type WorkerEnvironmentProfile } from "../src/worker-task-claims/environment.ts";

const execute = promisify(execFile);
const fixtureTest = process.platform === "win32" ? test.skip : test;
const roots: string[] = [];
afterEach(async () => {
  for (const root of roots.splice(0)) await rm(root, { force: true, recursive: true });
});

function claim() {
  const ids = { task_id: crypto.randomUUID(), run_id: crypto.randomUUID(),
    delegation_id: crypto.randomUUID(), operation_id: crypto.randomUUID() };
  return {
    state: "claimed" as const,
    intent: {
      workspace_id: crypto.randomUUID(), owner_id: crypto.randomUUID(),
      device_id: crypto.randomUUID(), worker_id: crypto.randomUUID(),
      body: {
        ...ids, expected_revision: "1", delegation_revision: "1", saved_revision: "1", applied_revision: "1",
        connection_epoch: "1", connection_intent: crypto.randomUUID(),
      },
    },
    receipt: {
      ...ids, token: "prepared-token", current: true, state: "active" as const,
      generation: "1", revision: "2", expires_at: new Date(Date.now() + 60_000).toISOString(),
    },
  };
}

async function fixture() {
  const gitExecutable = Bun.which("git");
  if (!gitExecutable) throw new Error("The CW-07 environment fixture requires Git");
  const root = await realpath(await mkdtemp(join(tmpdir(), "assign-worker-fixture-")));
  roots.push(root);
  const repository = join(root, "repository");
  const scratchRoot = join(root, "scratch");
  await mkdir(repository);
  await mkdir(scratchRoot);
  const env = { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: root, GIT_CONFIG_NOSYSTEM: "1" };
  const git = async (cwd: string, ...args: string[]) =>
    (await execute(gitExecutable, ["-c", "user.name=Prepared Fixture", "-c", "user.email=fixture@example.invalid", ...args], { cwd, env })).stdout.trim();
  await git(repository, "init", "-b", "base");
  await writeFile(join(repository, ".gitignore"), "ignored-work\n");
  const setupPath = join(repository, "setup.sh");
  await writeFile(setupPath, [
    "#!/bin/sh", "set -eu",
    'printf "%s" "$ASSIGN_WORKER_RUN_ID" > "$ASSIGN_WORKER_STATE/setup"',
    'case "$*" in', '  fail) exit 3 ;;', '  dirty) printf work > ignored-work; exit 4 ;;', '  wait) exec /bin/sleep 60 ;;', "esac", "",
  ].join("\n"));
  await chmod(setupPath, 0o755);
  await git(repository, "add", ".");
  await git(repository, "commit", "-m", "Create fixture");
  const baseSHA = await git(repository, "rev-parse", "HEAD");
  await git(repository, "switch", "-c", "source");
  await git(repository, "commit", "--allow-empty", "-m", "Advance source checkout");
  const profile: WorkerEnvironmentProfile = { gitExecutable, repository, scratchRoot,
    path: env.PATH, baseRef: "refs/heads/base", setup: { path: "setup.sh", args: [] } };
  const record = async (selected: ReturnType<typeof claim>) => {
    const { task_id, run_id } = selected.receipt;
    const raw = await readFile(join(scratchRoot, `assign-${task_id}-${run_id}`, "environment.json"), "utf8");
    expect(raw).not.toContain("prepared-token");
    return JSON.parse(raw);
  };
  return { profile, baseSHA, git, record };
}

fixtureTest("isolates overlapping claims and service sockets and removes clean checkouts", async () => {
  const { profile, baseSHA, git, record } = await fixture();
  const selected = [claim(), claim()];
  const both = Promise.withResolvers<void>();
  const addresses = new Set<number>();
  let entered = 0;
  await Promise.all(selected.map(item => withExternalWorkerEnvironment(item, profile, async environment => {
    const { worktree, state, env } = environment;
    expect(environment.baseSHA).toBe(baseSHA);
    expect(await readFile(join(state, "setup"), "utf8")).toBe(item.receipt.run_id);
    expect(env.XDG_CACHE_HOME.startsWith(state)).toBe(true);
    expect(env.TMPDIR.startsWith(state)).toBe(true);
    expect(env.HOME.startsWith(state)).toBe(true);
    const service = createServer();
    try {
      await new Promise<void>((resolve, reject) => {
        service.once("error", reject);
        service.listen(Number(env.ASSIGN_WORKER_PORT), "127.0.0.1", resolve);
      });
      const address = service.address();
      if (!address || typeof address === "string") throw new Error("Expected an isolated TCP listener");
      addresses.add(address.port);
      if (++entered === 2) both.resolve();
      await Promise.race([both.promise, Bun.sleep(5_000).then(() => { throw new Error("Worker did not overlap"); })]);
      expect(await git(worktree, "branch", "--show-current")).toBe(environment.branch);
    } finally {
      await new Promise<void>(resolve => service.close(() => resolve()));
      environment.confirmStopped();
    }
  })));
  expect(addresses.size).toBe(2);
  for (const item of selected) {
    const evidence = await record(item);
    expect(evidence.cleanup).toBe("removed");
    await expect(access(evidence.worktree)).rejects.toThrow();
    await expect(git(profile.repository, "show-ref", "--verify", `refs/heads/${evidence.branch}`)).rejects.toThrow();
  }
  expect(await git(profile.repository, "branch", "--show-current")).toBe("source");
}, 15_000);

fixtureTest("retains dirty setup failures and rejects a duplicate run", async () => {
  const { profile, record } = await fixture();
  const selected = claim();
  let launched = false;
  const launch = async () => { launched = true; };
  await expect(withExternalWorkerEnvironment(selected, { ...profile, setup: { path: "setup.sh", args: ["dirty"] } }, launch))
    .rejects.toThrow("during setup");
  const retained = await record(selected);
  expect(retained.cleanup).toBe("retained");
  expect(await readFile(join(retained.worktree, "ignored-work"), "utf8")).toBe("work");
  await expect(withExternalWorkerEnvironment(selected, profile, launch)).rejects.toThrow();
  expect(await record(selected)).toEqual(retained);

  const cleanFailure = claim();
  await expect(withExternalWorkerEnvironment(cleanFailure, { ...profile, setup: { path: "setup.sh", args: ["fail"] } }, launch))
    .rejects.toThrow("during setup");
  expect((await record(cleanFailure)).cleanup).toBe("removed");
  expect(launched).toBe(false);
}, 15_000);

fixtureTest("preserves a run commit that was reset to the base", async () => {
  const { profile, git, record } = await fixture();
  const selected = claim();
  let commit = "";
  await withExternalWorkerEnvironment(selected, profile, async environment => {
    await git(environment.worktree, "commit", "--allow-empty", "-m", "Retain unique work");
    commit = await git(environment.worktree, "rev-parse", "HEAD");
    await git(environment.worktree, "reset", "--hard", environment.baseSHA);
    environment.confirmStopped();
  });
  const evidence = await record(selected);
  expect(evidence.cleanup).toBe("retained");
  expect(await git(evidence.worktree, "cat-file", "-t", commit)).toBe("commit");
}, 15_000);

fixtureTest("routes a foreground interrupt through cleanup and retains unconfirmed processes", async () => {
  const { profile, record } = await fixture();
  processIO();
  for (const stopped of [true, false]) {
    const selected = claim();
    await expect(withExternalWorkerEnvironment(selected, profile, async environment => {
      process.emit("SIGINT");
      expect(environment.signal.aborted).toBe(true);
      if (stopped) environment.confirmStopped();
    })).rejects.toThrow("interrupted during execution");
    const evidence = await record(selected);
    expect(evidence.result).toBe("interrupted");
    expect(evidence.cleanup).toBe(stopped ? "removed" : "retained");
  }
}, 15_000);

fixtureTest("leaves a journaled partial checkout for reconciliation", async () => {
  const { profile, git, record } = await fixture();
  const selected = claim();
  const wrapper = join(profile.scratchRoot, "git-fail-after-add");
  const escapedGit = "'" + profile.gitExecutable.replaceAll("'", "'\\''") + "'";
  await writeFile(wrapper, [
    "#!/bin/sh", "set -eu",
    'case " $* " in',
    `  *" worktree add "*) ${escapedGit} "$@"; exit 19 ;;`,
    "esac",
    `exec ${escapedGit} "$@"`, "",
  ].join("\n"), { mode: 0o700 });
  await expect(withExternalWorkerEnvironment(selected, { ...profile, gitExecutable: wrapper }, async () => {
    throw new Error("Partial checkout reached execution");
  })).rejects.toThrow("during checkout");
  const evidence = await record(selected);
  expect(evidence.cleanup).toBe("partial");
  expect(await git(evidence.worktree, "branch", "--show-current")).toBe(evidence.branch);
  await expect(withExternalWorkerEnvironment(selected, profile, async () => {})).rejects.toThrow("already reserved");
  expect(await record(selected)).toEqual(evidence);
}, 15_000);

fixtureTest("aborts the tracked setup process before execution", async () => {
  const { profile, record } = await fixture();
  const selected = claim();
  const controller = new AbortController();
  let launched = false;
  const outcome = withExternalWorkerEnvironment(selected, { ...profile, setup: { path: "setup.sh", args: ["wait"] } },
    async () => { launched = true; }, controller.signal);
  const rejection = expect(outcome).rejects.toThrow("interrupted during setup");
  try {
    for (let attempt = 0; attempt < 200; attempt++) {
      const receipt = await record(selected).catch(() => undefined);
      if (receipt?.setupPID) break;
      await Bun.sleep(20);
    }
    expect((await record(selected)).setupPID).toBeNumber();
  } finally {
    controller.abort();
    await rejection;
  }
  const evidence = await record(selected);
  expect(launched).toBe(false);
  expect(evidence.cleanup).toBe("removed");
  expect(() => process.kill(-evidence.setupPID, 0)).toThrow();
}, 15_000);
