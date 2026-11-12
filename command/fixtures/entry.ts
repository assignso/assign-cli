import type { Command } from "commander";
import { Writable } from "node:stream";
import { CommandFailure, processIO, runCommand, writeOutput } from "../src/main.ts";
import type { CommandIO } from "../src/main.ts";
import { fixtureToken, loadFixture } from "./transport.ts";

export type TaskPageReader = (
  fetchApi: typeof fetch, origin: string, workspace: string, code: string,
) => Promise<unknown>;

function normalizeCode(code: string) {
  return code.trim().toUpperCase();
}

export async function runFixture(readTaskPage?: TaskPageReader) {
  const { fixture, fetchApi, origin, finish } = loadFixture();
  const io = processIO();
  if (fixture.brokenPipe) {
    io.stdout = new Writable({
      write(_chunk, _encoding, done) {
        done(Object.assign(new Error("fixture pipe closed"), { code: "EPIPE" }));
      },
    });
    io.stdout.on("error", () => {});
  }
  const headers = { Authorization: "Bearer " + fixtureToken };

  async function json(response: Response, message: string): Promise<unknown> {
    if (!response.ok) {
      if (response.status === 401 || response.status === 403) {
        throw new CommandFailure("Task resource access denied", 3);
      }
      throw new CommandFailure(message, 1);
    }
    try {
      return await response.json();
    } catch {
      throw new CommandFailure(message, 1);
    }
  }

  function register(program: Command, streams: CommandIO) {
    const task = program.command("task").alias("t").description("Fixture Task operations");
    task.command("show").alias("view").argument("<code>").requiredOption("--field <field>")
      .action(async (rawCode: string, options: { field: string }) => {
        if (options.field !== "revision") throw new CommandFailure("unsupported fixture field", 2);
        const code = normalizeCode(rawCode);
        const workspace = await json(await fetchApi(origin + "/api/v1/dev/workspace", { headers }),
          "credential Workspace identity is incomplete");
        if (typeof workspace !== "object" || workspace === null || !("id" in workspace) ||
            typeof workspace.id !== "string") {
          throw new CommandFailure("credential Workspace identity is incomplete", 1);
        }
        const message = "Task resource representation is incomplete or exceeds its limit";
        const query = new URLSearchParams({ code, include_archived: "true", limit: "100", state: "all" });
        const path = "/api/v1/workspaces/" + encodeURIComponent(workspace.id) + "/tasks?" + query;
        const page = readTaskPage
          ? await readTaskPage(fetchApi as typeof fetch, origin, workspace.id, code)
          : await json(await fetchApi(origin + path, { headers }), message);
        if (typeof page !== "object" || page === null || !("data" in page) || !Array.isArray(page.data)) {
          throw new CommandFailure("Task discovery page is incomplete", 1);
        }
        const row = page.data.find(value => value?.code === code);
        if (!row || typeof row.revision !== "string" || !/^[1-9][0-9]{0,18}$/.test(row.revision) ||
            BigInt(row.revision) > 9223372036854775807n) {
          throw new CommandFailure("Task response is incomplete", 1);
        }
        await writeOutput(streams.stdout, row.revision + "\n");
      });
    program.command("start").argument("<code>").requiredOption("--task-id <id>")
      .requiredOption("--if-match <tag>").requiredOption("--idempotency-key <key>")
      .action(async (rawCode: string, options: { taskId: string; ifMatch: string; idempotencyKey: string }) => {
        const quote = (value: string) => "'" + value.replaceAll("'", "'\"'\"'") + "'";
        const retry = [
          "assign", "--host", quote(origin), "task", "start", quote(normalizeCode(rawCode)),
          "--task-id", quote(options.taskId), "--if-match", quote(options.ifMatch),
          "--idempotency-key", quote(options.idempotencyKey),
        ].join(" ");
        await writeOutput(streams.stderr, "assign: retain the same credential; identical retry: " + retry + "\n");
        const response = await fetchApi(origin + "/api/v1/tasks/" + encodeURIComponent(options.taskId) + "/start", {
          method: "POST", headers: { ...headers, "If-Match": options.ifMatch, "Idempotency-Key": options.idempotencyKey },
        });
        if (response.status === 409) {
          throw new CommandFailure("Task action conflict; reconcile current state before a new operation; identical retry with the same credential: " + retry, 5);
        }
        throw new CommandFailure("unexpected fixture mutation response", 1);
      });
    const child = program.command("__fixture", { hidden: true });
    child.command("wait").action(async () => {
      await writeOutput(streams.stdout, "ready\n");
      for await (const _chunk of streams.stdin) {
        break;
      }
    });
    child.command("echo").action(async () => {
      let length = 0;
      const chunks: Buffer[] = [];
      for await (const chunk of streams.stdin) {
        const bytes = Buffer.from(chunk);
        length += bytes.length;
        if (length > 4096) throw new CommandFailure("fixture input exceeds limit", 2);
        chunks.push(bytes);
      }
      await writeOutput(streams.stdout, Buffer.concat(chunks).toString("utf8"));
    });
  }

  const exit = await runCommand(process.argv.slice(2), io, undefined, register);
  return finish() ? exit : 70;
}

if (import.meta.main) process.exitCode = await runFixture();
