import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { publishWorkerPullRequest, type WorkerPullRequestOptions } from "../src/worker-task-claims/pull-request.ts";
import type { ConfirmedClaim } from "../src/worker-task-claims/environment.ts";

const roots: string[] = [];
const id = (tail: number) => `00000000-0000-4000-8000-${String(tail).padStart(12, "0")}`;
const sourceSHA = "a".repeat(40);
const repository = "example/repository";
const url = `https://github.com/${repository}/pull/42`;

afterEach(async () => {
  await Promise.all(roots.splice(0).map(root => rm(root, { recursive: true, force: true })));
});

async function fixture() {
  const state = await mkdtemp(join(import.meta.dir, ".worker-pr-"));
  roots.push(state);
  const ids = { task_id: id(1), delegation_id: id(2), run_id: id(3), operation_id: id(4) };
  const claim: ConfirmedClaim = {
    state: "claimed",
    intent: {
      workspace_id: id(5), owner_id: id(6), device_id: id(7), worker_id: id(8),
      body: { ...ids, expected_revision: "1", saved_revision: "2", applied_revision: "2",
        delegation_revision: "1", connection_epoch: "1", connection_intent: id(9) },
    },
    receipt: { ...ids, current: true, state: "active", revision: "2", generation: "1",
      expires_at: new Date(Date.now() + 60_000).toISOString(), token: id(10) },
  };
  const branch = `assign-worker/${ids.task_id}/${ids.run_id}`;
  const run = { taskID: ids.task_id, runID: ids.run_id, branch, worktree: "/fixture/worktree", state,
    env: { HOME: state, XDG_CACHE_HOME: state, TMPDIR: state, ASSIGN_WORKER_PORT: "0" as const },
    signal: new AbortController().signal };
  const pull = { html_url: url, head: { sha: sourceSHA, ref: branch, repo: { full_name: repository } },
    base: { ref: "main", repo: { full_name: repository } } };
  const effects = { pushed: 0, created: 0, linked: 0, reads: 0 };
  const faults = {
    push: false, create: false, lookup: false, evidence: false, deny: false, head: "", stale: false, wrongSHA: false,
  };
  let remote = "";
  let found: typeof pull | undefined;
  let linked: Record<string, unknown> | undefined;
  const options: WorkerPullRequestOptions = {
    config: { provider: "github", repository, base: "main", localScopeID: id(11) },
    sourceSHA, gitExecutable: "/usr/bin/git",
    async readPolicy() {
      const policy = { schema_version: 1, grant: {
        workspace_ids: [id(5)], project_ids: [id(12)], task_ids: [id(1)], repository_digests: [],
        operations: ["comment" as const], allow_assign_to_self: false,
      }, native: {
        local_scope_ids: [id(11)], branch: "isolated" as const, push: !faults.deny, pull_request: true,
        publish: true, elapsed_seconds: 60, output_bytes: 4096, concurrency: 1,
      } };
      return { schema_version: 1, worker_id: id(8), name: "fixture", saved_revision: 2, applied_revision: 2,
        authorization_state: "applied", saved: policy, applied: policy, revoked: false, paused: false };
    },
    githubToken: async () => "prepared_github_token",
    fetchGitHub: async (input, init) => {
      const endpoint = new URL(String(input));
      expect(endpoint.origin + endpoint.pathname).toBe(`https://api.github.com/repos/${repository}/pulls`);
      expect(init?.redirect).toBe("error");
      if (init?.method === "POST") {
        expect(JSON.parse(String(init.body))).toEqual({ title: "Assign worker changes", head: branch, base: "main" });
        effects.created++;
        found = pull;
        if (faults.create) throw new Error("lost PR response");
        return Response.json(found, { status: 201 });
      }
      effects.reads++;
      expect(endpoint.searchParams.get("head")).toBe(`example:${branch}`);
      expect(endpoint.searchParams.get("state")).toBe("all");
      if (faults.lookup && found) throw new Error("lookup unavailable");
      return Response.json(found ? [{
        ...found, head: { ...found.head, sha: faults.wrongSHA ? "b".repeat(40) : found.head.sha },
      }] : []);
    },
    transport: {
      origin: "https://worker.invalid",
      workerDeviceToken: async () => "wdev_at_fixture",
      workerRunToken: async () => "wrun_at_fixture",
      fetchApi: async (input, init) => {
        const route = new URL(String(input)).pathname.split("/").at(-1);
        if (route === "claim-receipt") return Response.json({ ...claim.receipt, current: !faults.stale });
        const body = JSON.parse(String(init?.body));
        expect(body.event).toBe("pull-request");
        expect(body.pull_request).toEqual({ url, source_sha: sourceSHA });
        if (route === "evidence-receipt") {
          if (!linked) return Response.json({}, { status: 404 });
          expect(body).toEqual(linked);
        } else {
          expect(route).toBe("evidence");
          linked = body;
          effects.linked++;
          if (faults.evidence) throw new Error("lost comment response");
        }
        return Response.json({ ...claim.receipt, current: false, operation_id: body.operation_id });
      },
    },
  };
  const git = async (cwd: string, env: Record<string, string>, args: string[]) => {
    switch (args[0]) {
      case "symbolic-ref": return `refs/heads/${branch}`;
      case "rev-parse": return args.includes("--git-path") ? "/fixture/objects" : sourceSHA;
      case "check-ref-format":
      case "init": return "";
      case "ls-remote": return faults.head || remote;
      case "push":
        expect(cwd).toStartWith(state);
        expect(env.GIT_OBJECT_DIRECTORY).toBe("/fixture/objects");
        expect(args).toEqual(["push", `--force-with-lease=refs/heads/${branch}:`, "--",
          `https://github.com/${repository}.git`, `${sourceSHA}:refs/heads/${branch}`]);
        effects.pushed++;
        remote = `${sourceSHA}\trefs/heads/${branch}`;
        if (faults.push) throw new Error("lost push response");
        return "";
      default: throw new Error("Unexpected Git command");
    }
  };
  return { options, effects, faults, publish: () => publishWorkerPullRequest(claim, run, options, git) };
}

test("disabled and unsupported publication skip every provider mutation", async () => {
  const f = await fixture();
  f.options.config = undefined;
  expect(await f.publish()).toEqual({ state: "skipped", reason: "disabled" });
  f.options.config = { provider: "other", repository, base: "main", localScopeID: id(11) };
  expect(await f.publish()).toEqual({ state: "skipped", reason: "unsupported-provider" });
  expect(f.effects).toEqual({ pushed: 0, created: 0, linked: 0, reads: 0 });
});

test("missing local/applied grant, stale claims and conflicting remote heads fail before mutation", async () => {
  const f = await fixture();
  f.faults.deny = true;
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "authority", pushed: false });
  f.faults.deny = false;
  f.faults.stale = true;
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "authority", pushed: false });
  f.faults.stale = false;
  f.faults.head = "other commit";
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "push", pushed: false });
  expect(f.effects).toEqual({ pushed: 0, created: 0, linked: 0, reads: 0 });
});

test("reconciles lost push, create and comment responses across repeated calls", async () => {
  const f = await fixture();
  f.faults.push = true;
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "push" });
  f.faults.create = true;
  f.faults.lookup = true;
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "pull-request", pushed: true });
  f.faults.lookup = false;
  f.faults.wrongSHA = true;
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "pull-request", pushed: true });
  expect(f.effects).toMatchObject({ pushed: 1, created: 1, linked: 0 });
  f.faults.wrongSHA = false;
  f.faults.evidence = true;
  expect(await f.publish()).toMatchObject({ state: "recoverable", phase: "ticket", pushed: true });
  f.faults.stale = true;
  const linked = { state: "linked" as const, pullRequest: { url, source_sha: sourceSHA } };
  expect(await f.publish()).toEqual(linked);
  expect(await f.publish()).toEqual(linked);
  expect(f.effects).toMatchObject({ pushed: 1, created: 1, linked: 1 });
});
