import type { Command } from "commander";
import type { Readable, Writable } from "node:stream";
import { developmentBuild, formatVersion } from "./version.ts";
import type { BuildIdentity } from "./version.ts";

export interface CommandIO {
  stdin: Readable;
  stdout: Writable;
  stderr: Writable;
}

export type RegisterCommands = (program: Command, io: CommandIO) => void;

export class CommandFailure extends Error {
  constructor(message: string, readonly exitCode: number) {
    super(message);
  }
}

export function writeOutput(stream: Writable, text: string): Promise<void> {
  return new Promise((resolve, reject) => {
    stream.write(text, error => error ? reject(error) : resolve());
  });
}

function isBrokenPipe(error: unknown): boolean {
  return error instanceof Error && "code" in error && error.code === "EPIPE";
}

export async function runCommand(
  args: string[],
  io: CommandIO,
  build: BuildIdentity = developmentBuild,
  register?: RegisterCommands,
): Promise<number> {
  const versionText = formatVersion(build);
  // Keep the version path independent of the parser and command modules.
  try {
    if (args.length === 1 && (args[0] === "version" || args[0] === "v")) {
      await writeOutput(io.stdout, versionText);
      return 0;
    }

    const { Command } = await import("commander");
    const program = new Command();
    program.name("assign").description("Work with Assign from the terminal");
    program.exitOverride().configureOutput({
      writeOut: text => io.stdout.write(text),
      writeErr: text => io.stderr.write(text),
      outputError: (message, write) => write(`assign: ${message.replace(/^error: /, "")}`),
    });
    program.command("version").alias("v").description("Print version and build information")
      .action(() => writeOutput(io.stdout, versionText));
    if (register) {
      register(program, io);
    } else {
      const { registerConfig, validateHost } = await import("./config.ts");
      program.option("--host <https-origin>", "Assign API host for this command");
      program.hook("preAction", () => {
        const host = program.opts<{ host?: string }>().host;
        if (host !== undefined) validateHost(host);
      });
      registerConfig(program, io);
    }
    await program.parseAsync(args, { from: "user" });
    return 0;
  } catch (error) {
    if (isBrokenPipe(error)) return 0;
    if (error instanceof Error && "code" in error && typeof error.code === "string" &&
        error.code.startsWith("commander.")) {
      return "exitCode" in error && error.exitCode === 0 ? 0 : 2;
    }
    const failure = error instanceof CommandFailure ? error : new CommandFailure("command failed", 1);
    await writeOutput(io.stderr, `assign: ${failure.message}\n`);
    return failure.exitCode;
  }
}

export function processIO(): CommandIO {
  // A write failure is also emitted as an event. The write callback owns the exit decision.
  process.stdout.on("error", () => {});
  process.stderr.on("error", () => {});
  return { stdin: process.stdin, stdout: process.stdout, stderr: process.stderr };
}
