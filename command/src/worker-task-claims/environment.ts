import { execFile, spawn } from "node:child_process";
import { constants } from "node:fs";
import { access, lstat, mkdir, realpath, rename, writeFile } from "node:fs/promises";
import { isAbsolute, join, relative } from "node:path";
import { promisify } from "node:util";
import type { createExternalWorkerSelection } from "./selection.ts";
import { sameID, workerUUID } from "./operations.ts";
import { scopeWorkerInterrupts } from "../worker-interrupts.ts";
import { publishWorkerPullRequest, type WorkerPullRequestOptions, type WorkerPullRequestResult } from "./pull-request.ts";

type Selection = Awaited<ReturnType<ReturnType<typeof createExternalWorkerSelection>["select"]>>;
export type ConfirmedClaim = Extract<Selection, { state: "claimed" }>;

export type WorkerEnvironmentProfile = {
  // Resolved from the approved local profile, before accepting a claim.
  repository: string;
  scratchRoot: string;
  gitExecutable: string;
  baseRef: string;
  setup?: { path: string; args: readonly string[] };
  path: string;
};

export type Environment = {
  taskID: string;
  runID: string;
  branch: string;
  baseRef: string;
  baseSHA: string;
  worktree: string;
  state: string;
  env: Readonly<Record<string, string> & {
    HOME: string;
    XDG_CACHE_HOME: string;
    TMPDIR: string;
    ASSIGN_WORKER_PORT: "0";
  }>;
  signal: AbortSignal;
  // The existing supervisor acknowledges only after its native processes and
  // services have stopped. Without this, leave the worktree for reconciliation.
  confirmStopped(): void;
  publishPullRequest(options: Omit<WorkerPullRequestOptions, "gitExecutable">): Promise<WorkerPullRequestResult>;
};

type Stage = "reserved" | "checkout" | "setup" | "execution";
type Cleanup = "removed" | "retained" | "partial";

const execute = promisify(execFile);
const oid = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
async function git(executable: string, env: Record<string, string>, cwd: string, ...args: string[]): Promise<string> {
  try {
    const result = await execute(executable, ["-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
      "-c", "maintenance.auto=false", "-c", "gc.auto=0", ...args], {
      cwd, env, timeout: 30_000, maxBuffer: 64 * 1024,
    });
    return result.stdout;
  } catch {
    throw new Error("Worker Git operation failed");
  }
}

function groupStopped(pid: number): boolean {
  try {
    process.kill(-pid, 0);
    return false;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "ESRCH";
  }
}

async function runSetup(executable: string, args: readonly string[], cwd: string,
  env: Record<string, string>, signal: AbortSignal, register: (pid: number) => Promise<void>): Promise<void> {
  signal.throwIfAborted();
  const child = spawn(executable, args, { cwd, env, detached: true, stdio: "ignore" });
  const pid = child.pid;
  const stop = () => {
    if (pid === undefined) return;
    try {
      process.kill(-pid, "SIGTERM");
    } catch {
      // The cleanup check also covers a failed signal or a surviving group.
    }
  };
  const timeout = AbortSignal.timeout(120_000);
  const bounded = AbortSignal.any([signal, timeout]);
  bounded.addEventListener("abort", stop, { once: true });
  try {
    const completion = new Promise<void>((resolve, reject) => {
      // Allow cooperative termination, then hand back a retained environment.
      let grace: ReturnType<typeof setTimeout> | undefined;
      const finish = (error?: Error) => {
        clearTimeout(grace);
        bounded.removeEventListener("abort", abort);
        if (error) reject(error);
        else resolve();
      };
      const abort = () => {
        grace = setTimeout(() => {
          child.unref();
          finish(new Error("Worker setup did not stop after cancellation"));
        }, 5_000);
      };
      child.once("error", () => finish(new Error("Worker setup could not start")));
      child.once("exit", (code) => finish(code === 0 ? undefined : new Error("Worker setup failed")));
      bounded.addEventListener("abort", abort, { once: true });
      if (bounded.aborted) {
        stop();
        abort();
      }
    });
    const registration = pid === undefined ? undefined : register(pid).catch(error => {
      stop();
      throw error;
    });
    const [exit, journal] = await Promise.allSettled([completion, registration]);
    if (journal.status === "rejected") throw journal.reason;
    if (exit.status === "rejected") throw exit.reason;
    bounded.throwIfAborted();
    if (pid !== undefined && !groupStopped(pid)) throw new Error("Worker setup left active processes");
  } finally {
    bounded.removeEventListener("abort", stop);
  }
}

async function cleanupCheckout(
  call: (cwd: string, ...args: string[]) => Promise<string>,
  repository: string, commonDir: string, worktree: string, ref: string, baseSHA: string,
): Promise<Cleanup> {
  try {
    if ((await call(worktree, "rev-parse", "--show-toplevel")).trim() !== worktree ||
        await realpath((await call(worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")).trim()) !== commonDir ||
        (await call(worktree, "symbolic-ref", "HEAD")).trim() !== ref ||
        (await call(worktree, "rev-parse", "HEAD")).trim() !== baseSHA ||
        await call(worktree, "status", "--porcelain=v1", "--untracked-files=all", "--ignored", "--ignore-submodules=none")) {
      return "retained";
    }
    for (const marker of ["MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_START"]) {
      const path = (await call(worktree, "rev-parse", "--path-format=absolute", "--git-path", marker)).trim();
      try {
        await lstat(path);
        return "retained";
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") return "retained";
      }
    }
    // Reflogs catch commits that were subsequently reset to the base.
    for (const log of ["HEAD", ref]) {
      const entries = (await call(worktree, "reflog", "show", "--format=%H", log)).trim().split("\n");
      if (entries.some(entry => entry !== baseSHA)) return "retained";
    }
    await call(repository, "worktree", "remove", worktree);
    await call(repository, "update-ref", "--no-deref", "-d", ref, baseSHA);
    return "removed";
  } catch {
    return "partial";
  }
}

/** Scoped host adapter for the existing worker supervisor. */
export async function withExternalWorkerEnvironment<T>(
  claim: ConfirmedClaim, profile: WorkerEnvironmentProfile,
  run: (environment: Environment) => Promise<T>, signal?: AbortSignal,
): Promise<T> {
  if (claim.state !== "claimed") throw new Error("Worker environment requires a confirmed claim");
  const receipt = claim.receipt;
  const intent = claim.intent.body;
  const taskID = intent.task_id.toLowerCase();
  const runID = intent.run_id.toLowerCase();
  const expires = Date.parse(receipt.expires_at);
  if (!receipt.current || receipt.state !== "active" || !receipt.token ||
      !workerUUID.test(taskID) || !workerUUID.test(runID) || !(expires > Date.now()) ||
      !sameID(taskID, receipt.task_id) || !sameID(runID, receipt.run_id) ||
      !sameID(intent.operation_id, receipt.operation_id) || !sameID(intent.delegation_id, receipt.delegation_id)) {
    throw new Error("Worker environment requires a current confirmed claim");
  }
  if (process.platform === "win32") throw new Error("Worker environment requires POSIX process groups");
  const { baseRef, gitExecutable } = profile;
  const { repository: sourceRoot, scratchRoot, path } = profile;
  const setup = profile.setup && { path: profile.setup.path, args: [...profile.setup.args] };
  if (!isAbsolute(gitExecutable) || !isAbsolute(sourceRoot) || !isAbsolute(scratchRoot) ||
      !path || !(/^(?:refs\/(?:heads|tags|remotes)\/[^\s]+)$/.test(baseRef) || oid.test(baseRef)) ||
      (setup && !setup.path.split("/").every(part => /^[a-zA-Z0-9_.-]+$/.test(part) && part !== "." && part !== ".."))) {
    throw new Error("Invalid trusted worker environment profile");
  }

  const controller = new AbortController();
  const bounded = signal ? AbortSignal.any([signal, controller.signal]) : controller.signal;
  const releaseInterrupts = scopeWorkerInterrupts(controller);
  try {
    bounded.throwIfAborted();
    const repository = await realpath(sourceRoot);
    const scratch = await realpath(scratchRoot);
    const inside = relative(repository, scratch);
    if (!inside || (!inside.startsWith("../") && inside !== "..")) {
      throw new Error("Worker scratch must be outside the source checkout");
    }
    const name = `assign-${taskID}-${runID}`;
    const root = join(scratch, name);
    const branch = `assign-worker/${taskID}/${runID}`;
    const ref = `refs/heads/${branch}`;
    const worktree = join(root, "worktree");
    const state = join(root, "state");
    const env = {
      PATH: path,
      HOME: join(state, "home"),
      XDG_CONFIG_HOME: join(state, "config"),
      XDG_CACHE_HOME: join(state, "cache"),
      XDG_DATA_HOME: join(state, "data"),
      XDG_STATE_HOME: state,
      XDG_RUNTIME_DIR: join(state, "runtime"),
      TMPDIR: join(state, "tmp"),
      ASSIGN_WORKER_STATE: state,
      ASSIGN_WORKER_RUN_ID: runID,
      ASSIGN_WORKER_PORT: "0" as const,
      GIT_CONFIG_NOSYSTEM: "1",
      GIT_CONFIG_GLOBAL: "/dev/null",
      GIT_TERMINAL_PROMPT: "0",
      GIT_NO_LAZY_FETCH: "1",
    };
    const call = (cwd: string, ...args: string[]) => git(gitExecutable, env, cwd, ...args);
    if ((await call(repository, "rev-parse", "--show-toplevel")).trim() !== repository) {
      throw new Error("Worker profile must name the approved repository root");
    }
    const commonDir = await realpath((await call(repository, "rev-parse", "--path-format=absolute", "--git-common-dir")).trim());
    const insideGit = relative(commonDir, scratch);
    if (!insideGit || (!insideGit.startsWith("../") && insideGit !== "..")) {
      throw new Error("Worker scratch must be outside the repository metadata");
    }
    if (!oid.test(baseRef)) {
      await call(repository, "check-ref-format", baseRef);
      await call(repository, "show-ref", "--verify", "--", baseRef);
    }
    const baseSHA = (await call(repository, "rev-parse", "--verify", "--end-of-options", `${baseRef}^{commit}`)).trim();
    if (!oid.test(baseSHA)) throw new Error("Invalid worker base commit");
    bounded.throwIfAborted();
    try {
      await mkdir(root, { mode: 0o700 });
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "EEXIST") {
        throw new Error(`Worker run already reserved; reconcile ${join(root, "environment.json")}`);
      }
      throw error;
    }
    let stage: Stage = "reserved";
    let cleanup: Cleanup = "retained";
    let checkoutAttempted = false;
    let checkoutReady = false;
    let setupPID: number | undefined;
    let stopped = true;
    let executionStarted = false;
    const record = async (result: string) => {
      const data = { taskID, runID, branch, baseRef, baseSHA, worktree, state, stage, result, cleanup,
        ...(setupPID !== undefined && { setupPID }),
      };
      const temp = join(root, `environment-${crypto.randomUUID()}.tmp`);
      await writeFile(temp, JSON.stringify(data) + "\n", { mode: 0o600, flag: "wx" });
      await rename(temp, join(root, "environment.json"));
    };
    let result = "failed";
    try {
      await record("running");
      await mkdir(state, { mode: 0o700 });
      for (const path of [env.HOME, env.XDG_CONFIG_HOME, env.XDG_CACHE_HOME, env.XDG_DATA_HOME, env.XDG_RUNTIME_DIR, env.TMPDIR]) {
        await mkdir(path, { mode: 0o700 });
      }
      bounded.throwIfAborted();
      stage = "checkout";
      await record("running");
      bounded.throwIfAborted();
      checkoutAttempted = true;
      await call(repository, "-c", "core.logAllRefUpdates=true", "worktree", "add", "--no-track", "-b", branch, worktree, baseSHA);
      checkoutReady = true;
      bounded.throwIfAborted();
      if (setup) {
        stage = "setup";
        await record("running");
        const entry = (await call(worktree, "ls-tree", baseSHA, "--", setup.path)).trim();
        const executable = join(worktree, setup.path);
        const blob = (await call(worktree, "hash-object", "--no-filters", "--", executable)).trim();
        if (!oid.test(blob) || entry !== `100755 blob ${blob}\t${setup.path}` ||
            await realpath(executable) !== executable) {
          throw new Error("Worker setup must be a tracked executable from the approved base");
        }
        await access(executable, constants.X_OK);
        await runSetup(executable, setup.args, worktree, env, bounded, async pid => {
          setupPID = pid;
          await record("running");
        });
        setupPID = undefined;
      }
      bounded.throwIfAborted();
      stage = "execution";
      await record("running");
      bounded.throwIfAborted();
      if (!(expires > Date.now())) throw new Error("Worker claim expired during setup");
      stopped = false;
      executionStarted = true;
      const publicationRun = { taskID, runID, branch, worktree, state, env: Object.freeze(env), signal: bounded };
      let publication: Promise<WorkerPullRequestResult> | undefined;
      let runOpen = true;
      let value: T;
      try {
        value = await run({ ...publicationRun, baseRef, baseSHA,
          confirmStopped: () => { stopped = true; },
          publishPullRequest: options => {
            if (!runOpen || !stopped) throw new Error("Worker process must stop before pull request publication");
            if (publication) throw new Error("Worker pull request publication is already running");
            publication = publishWorkerPullRequest(claim, publicationRun, { ...options, gitExecutable });
            void publication.finally(() => { publication = undefined; }).catch(() => {});
            return publication;
          },
        });
      } finally {
        runOpen = false;
        await publication;
      }
      bounded.throwIfAborted();
      result = "finished";
      return value;
    } catch (error) {
      result = bounded.aborted ? "interrupted" : "failed";
      if (executionStarted && !bounded.aborted) throw error;
      // Git/setup errors can carry arbitrary process output. Expose safe evidence.
      throw new Error(`Worker environment ${result} during ${stage}; inspect ${join(root, "environment.json")}`);
    } finally {
      const setupStopped = setupPID === undefined || groupStopped(setupPID);
      if (checkoutReady && stopped && setupStopped) {
        cleanup = await cleanupCheckout(call, repository, commonDir, worktree, ref, baseSHA);
      } else if (checkoutAttempted && !checkoutReady) {
        cleanup = "partial";
      }
      await record(result);
    }
  } finally {
    releaseInterrupts();
  }
}
