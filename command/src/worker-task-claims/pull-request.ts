import { execFile } from "node:child_process";
import { lstat, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { isAbsolute, join } from "node:path";
import { promisify } from "node:util";
import { decodeResponse, type FetchAPI } from "../transport.ts";
import type { CLIWorkerPolicyStateV1 } from "../worker-registration/index.ts";
import type { ConfirmedClaim, Environment } from "./environment.ts";
import type { WorkerTaskClaimTransport, WorkerTaskEvidenceInputV1 } from "./index.ts";
import { createWorkerTaskOperations, exactPositiveDecimal, sameID, WorkerTaskOperationFailure, workerUUID } from "./operations.ts";

export type WorkerPullRequestConfig = Readonly<{
  provider: string;
  repository: string; // GitHub OWNER/REPO, explicitly consented on this device
  base: string;
  localScopeID: string;
}>;

type Target = Pick<WorkerPullRequestConfig, "repository" | "base" | "localScopeID"> & {
  branch: string;
  sourceSHA: string;
};
type PR = NonNullable<WorkerTaskEvidenceInputV1["pull_request"]>;
type Evidence = Pick<WorkerTaskEvidenceInputV1, "operation_id" | "expected_revision" | "generation">;
type Journal = { target: Target; pushed?: true; pr?: PR; evidence?: Evidence; linked?: true };
type Phase = "authority" | "source" | "push" | "pull-request" | "ticket";
export type WorkerPullRequestResult =
  | { state: "skipped"; reason: "disabled" | "unsupported-provider" }
  | { state: "linked"; pullRequest: PR }
  | { state: "recoverable"; phase: Phase; pushed: boolean; pullRequest?: PR; receiptPath: string };

type PublicationRun = Pick<Environment, "taskID" | "runID" | "branch" | "worktree" | "state" | "env" | "signal">;
type Git = (cwd: string, env: Record<string, string>, args: string[], signal: AbortSignal) => Promise<string>;

export type WorkerPullRequestOptions = {
  config?: WorkerPullRequestConfig;
  // The exact SHA authorized for publication by the existing supervisor.
  sourceSHA: string;
  gitExecutable: string;
  transport: WorkerTaskClaimTransport;
  // The supervisor resolves this device's locally consented config and reads
  // the bound worker's policy through the existing human-authorized client.
  readPolicy(signal: AbortSignal): Promise<CLIWorkerPolicyStateV1>;
  githubToken(signal: AbortSignal): Promise<string>;
  fetchGitHub?: FetchAPI;
};

const execute = promisify(execFile);
const githubRepository = /^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}\/[a-zA-Z0-9_.-]{1,100}$/;
const sha = /^(?!0{40}$)[a-f0-9]{40}$/;
const gitTimeoutMs = 30_000;
const publicationTimeoutMs = 90_000;

const runGit = (executable: string): Git => async (cwd, env, args, signal) => {
  signal.throwIfAborted();
  return (await execute(executable, args, { cwd, env, signal, timeout: gitTimeoutMs, maxBuffer: 64 * 1024 })).stdout.trim();
};

function matchesTarget(left: Target, right: Target): boolean {
  return left.repository === right.repository && left.base === right.base &&
    left.localScopeID === right.localScopeID && left.branch === right.branch && left.sourceSHA === right.sourceSHA;
}

function pullRequestURL(url: unknown, target: Target): url is string {
  const prefix = `https://github.com/${target.repository}/pull/`;
  return typeof url === "string" && url.startsWith("https://github.com/") && url.toLowerCase().startsWith(prefix) &&
    /^[1-9][0-9]{0,15}$/.test(url.slice(prefix.length));
}

function validatePullRequest(raw: unknown, target: Target): PR {
  const row = raw as {
    html_url?: string;
    head?: { ref?: string; sha?: string; repo?: { full_name?: string } };
    base?: { ref?: string; repo?: { full_name?: string } };
  } | null;
  const url = row?.html_url;
  if (!pullRequestURL(url, target) ||
      row?.head?.ref !== target.branch || row?.head.sha !== target.sourceSHA ||
      row?.head.repo?.full_name?.toLowerCase() !== target.repository ||
      row?.base?.ref !== target.base || row?.base.repo?.full_name?.toLowerCase() !== target.repository) {
    throw new Error("GitHub pull request does not match the approved repository, refs and source SHA");
  }
  return { url, source_sha: target.sourceSHA };
}

async function githubRequest(fetchApi: FetchAPI, token: string, url: URL, signal: AbortSignal, body?: object) {
  const response = await fetchApi(url, {
    method: body ? "POST" : "GET",
    redirect: "error",
    headers: {
      Accept: "application/vnd.github+json",
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      "X-GitHub-Api-Version": "2026-03-10",
    },
    body: body && JSON.stringify(body),
    signal,
  });
  if (response.status !== (body ? 201 : 200)) {
    void response.body?.cancel().catch(() => {});
    throw new Error("GitHub rejected the pull request operation");
  }
  return decodeResponse(response, 512 * 1024);
}

async function reconcilePullRequest(
  target: Target, token: string, fetchApi: FetchAPI, signal: AbortSignal,
  authorizeCreate: () => Promise<unknown>,
) {
  const endpoint = new URL(`https://api.github.com/repos/${target.repository}/pulls`);
  const search = new URL(endpoint);
  search.search = new URLSearchParams({
    state: "all", head: `${target.repository.split("/")[0]}:${target.branch}`, per_page: "2",
  }).toString();
  const lookup = async () => {
    const rows = await githubRequest(fetchApi, token, search, signal);
    if (!Array.isArray(rows) || rows.length > 1) throw new Error("Ambiguous GitHub pull request");
    return rows.length ? validatePullRequest(rows[0], target) : undefined;
  };
  const existing = await lookup();
  if (existing) return existing;

  await authorizeCreate();
  try {
    const created = await githubRequest(fetchApi, token, endpoint, signal, {
      title: "Assign worker changes",
      head: target.branch,
      base: target.base,
    });
    return validatePullRequest(created, target);
  } catch {
    // A duplicate response or lost create response can hide a successful write.
    // Reconcile once under this attempt's deadline, then leave the journal for retry.
    const recovered = await lookup();
    if (recovered) return recovered;
    throw new Error("GitHub pull request creation requires reconciliation");
  }
}

async function pushSource(
  run: PublicationRun, target: Target, git: Git, token: string,
  allowCreate: boolean, signal: AbortSignal, authorizePush: () => Promise<unknown>,
) {
  const ref = `refs/heads/${target.branch}`;
  const objects = await git(run.worktree, run.env, ["rev-parse", "--path-format=absolute", "--git-path", "objects"], signal);
  // Use the worktree's objects with a fresh Git configuration. Remote URLs,
  // credential helpers, rewrite rules and hooks from the worktree cannot steer the push.
  const scratch = await mkdtemp(join(run.state, "pr-git-"));
  try {
    const url = `https://github.com/${target.repository}.git`;
    const env = {
      ...run.env,
      GIT_CONFIG_COUNT: "4",
      GIT_CONFIG_KEY_0: "http.followRedirects",
      GIT_CONFIG_VALUE_0: "false",
      GIT_CONFIG_KEY_1: `http.${url}.extraHeader`,
      GIT_CONFIG_VALUE_1: `Authorization: Basic ${Buffer.from(`x-access-token:${token}`).toString("base64")}`,
      GIT_CONFIG_KEY_2: "core.hooksPath",
      GIT_CONFIG_VALUE_2: "/dev/null",
      GIT_CONFIG_KEY_3: "credential.helper",
      GIT_CONFIG_VALUE_3: "",
    };
    await git(scratch, env, ["init", "--bare", "--template=", "--object-format=sha1", "."], signal);
    const remote = (...args: string[]) => git(scratch, { ...env, GIT_OBJECT_DIRECTORY: objects }, args, signal);
    const observed = await remote("ls-remote", "--refs", "--", url, ref);
    if (observed === `${target.sourceSHA}\t${ref}`) return;
    if (observed || !allowCreate) throw new Error("Remote worker branch no longer matches the publication intent");
    // The empty expected ref is a create-only CAS. A concurrent branch writer
    // cannot be overwritten even if its commit is an ancestor of sourceSHA.
    await authorizePush();
    await remote("push", `--force-with-lease=${ref}:`, "--", url, `${target.sourceSHA}:${ref}`);
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}

/** Use from the existing supervisor's exclusive run slot, including recovery. */
export async function publishWorkerPullRequest(
  claim: ConfirmedClaim, run: PublicationRun, options: WorkerPullRequestOptions,
  git = runGit(options.gitExecutable),
): Promise<WorkerPullRequestResult> {
  const config = options.config;
  if (!config) return { state: "skipped", reason: "disabled" };
  if (config.provider !== "github") return { state: "skipped", reason: "unsupported-provider" };

  const { body, ...subject } = claim.intent;
  const { token: claimToken, generation } = claim.receipt;
  const target: Target = {
    repository: config.repository.toLowerCase(), base: config.base, localScopeID: config.localScopeID,
    branch: run.branch, sourceSHA: options.sourceSHA,
  };
  const receiptPath = join(run.state, "pull-request.json");
  const signal = AbortSignal.any([run.signal, AbortSignal.timeout(publicationTimeoutMs)]);
  let journal: Journal = { target };
  let savedIntent = false;
  let phase: Phase = "authority";
  const save = async (next: Journal) => {
    const temp = join(run.state, `pull-request-${crypto.randomUUID()}.tmp`);
    await writeFile(temp, JSON.stringify(next) + "\n", { mode: 0o600, flag: "wx", flush: true });
    await rename(temp, receiptPath);
    journal = next;
  };
  const intent = (pr: PR, fence: Evidence) => ({
    ...subject,
    body: { task_id: body.task_id, delegation_id: body.delegation_id, run_id: body.run_id,
      ...fence, token: claimToken!, event: "pull-request" as const, pull_request: pr },
  });

  try {
    if (!githubRepository.test(target.repository) || target.repository.endsWith(".git") ||
        /\/\.{1,2}$/.test(target.repository) ||
        !workerUUID.test(target.localScopeID) || !sha.test(target.sourceSHA) || !isAbsolute(options.gitExecutable) ||
        claim.state !== "claimed" || !claimToken || !workerUUID.test(claimToken) ||
        !sameID(claim.receipt.operation_id, body.operation_id) ||
        !sameID(claim.receipt.task_id, body.task_id) || !sameID(claim.receipt.run_id, body.run_id) ||
        !sameID(claim.receipt.delegation_id, body.delegation_id) ||
        !sameID(run.taskID, body.task_id) || !sameID(run.runID, body.run_id) ||
        target.branch !== `assign-worker/${body.task_id.toLowerCase()}/${body.run_id.toLowerCase()}` ||
        !target.base || target.base === target.branch) {
      throw new Error("Invalid worker pull request configuration");
    }
    const { evidence, claim: claims } = createWorkerTaskOperations(options.transport);

    try {
      const info = await lstat(receiptPath);
      if (!info.isFile() || info.size > 8 * 1024) throw new Error("Invalid publication receipt");
      const saved = JSON.parse(await readFile(receiptPath, "utf8")) as Journal;
      if (!matchesTarget(saved.target, target)) throw new Error("Publication intent changed");
      if (saved.pr && (!saved.pushed || saved.pr.source_sha !== target.sourceSHA ||
          !pullRequestURL(saved.pr.url, target)) ||
          saved.evidence && (!saved.pr || saved.evidence.generation !== generation ||
            !exactPositiveDecimal(saved.evidence.expected_revision) ||
            !workerUUID.test(saved.evidence.operation_id)) ||
          saved.linked && !saved.evidence) {
        throw new Error("Invalid publication receipt");
      }
      journal = saved;
      savedIntent = true;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }

    if (journal.pr && journal.evidence) {
      phase = "ticket";
      if (journal.linked) return { state: "linked", pullRequest: journal.pr };
      try {
        await evidence.reconcile(intent(journal.pr, journal.evidence), signal);
        await save({ ...journal, linked: true });
        return { state: "linked", pullRequest: journal.pr };
      } catch (error) {
        if (!(error instanceof WorkerTaskOperationFailure) || error.status !== 404) throw error;
      }
    }

    phase = "authority";
    const checkAuthority = async () => {
      const policy = await options.readPolicy(signal);
      const native = policy.applied?.native;
      if (!sameID(policy.worker_id, subject.worker_id) || policy.revoked || policy.authorization_state !== "applied" ||
          String(policy.applied_revision) !== body.applied_revision || policy.saved_revision !== policy.applied_revision ||
          native?.branch !== "isolated" || !native.push || !native.pull_request || !native.publish ||
          !native.local_scope_ids.some(id => sameID(id, target.localScopeID)) ||
          !policy.applied?.grant.operations.includes("comment") ||
          !policy.applied.grant.workspace_ids.some(id => sameID(id, subject.workspace_id)) ||
          !policy.applied.grant.task_ids.some(id => sameID(id, body.task_id))) {
        throw new Error("Worker policy does not grant pull request publication");
      }
      const current = await claims.reconcile(claim.intent, signal);
      if (!current.current || current.state !== "active" || !current.token || !sameID(current.token, claimToken) ||
          current.generation !== generation ||
          !(Date.parse(current.expires_at) > Date.now())) {
        throw new Error("Pull request publication requires a current claim");
      }
      signal.throwIfAborted();
      return current;
    };

    if (!journal.pr) {
      phase = "source";
      if (!savedIntent) {
        const local = (...args: string[]) => git(run.worktree, run.env, args, signal);
        const ref = `refs/heads/${target.branch}`;
        if (await local("symbolic-ref", "HEAD") !== ref || await local("rev-parse", "--verify", "HEAD^{commit}") !== target.sourceSHA) {
          throw new Error("Worker branch changed before publication");
        }
        await local("check-ref-format", `refs/heads/${target.base}`);
        await save(journal);
      }

      phase = "authority";
      await checkAuthority();
      const token = await options.githubToken(signal);
      if (!/^[a-zA-Z0-9_]{1,512}$/.test(token)) throw new Error("Invalid GitHub credential");
      phase = "push";
      await pushSource(run, target, git, token, !journal.pushed, signal, checkAuthority);
      if (!journal.pushed) await save({ ...journal, pushed: true });
      phase = "pull-request";
      const pr = await reconcilePullRequest(target, token, options.fetchGitHub ?? fetch, signal, checkAuthority);
      await save({ ...journal, pr });
    }

    phase = "ticket";
    const pr = journal.pr!;
    const current = await checkAuthority();
    if (!journal.evidence) {
      await save({ ...journal, evidence: {
        operation_id: crypto.randomUUID(), expected_revision: current.revision,
        generation: current.generation,
      } });
    }
    if (current.revision !== journal.evidence!.expected_revision || current.generation !== journal.evidence!.generation) {
      throw new Error("Saved Task evidence fence changed");
    }
    await evidence.apply(intent(pr, journal.evidence!), signal);
    await save({ ...journal, linked: true });
    return { state: "linked", pullRequest: pr };
  } catch {
    return { state: "recoverable", phase, pushed: journal.pushed === true, pullRequest: journal.pr, receiptPath };
  }
}
