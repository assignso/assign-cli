import { expect, test } from "bun:test";
import { PassThrough, Readable, Writable } from "node:stream";
import { runCommand, writeStdout } from "../src/main.ts";
import type { RegisterCommands } from "../src/main.ts";

test.each([[["help"]], [["--help"]], [["help", "config"]]])("%j displays help and exits successfully", async (args) => {
  const stdout = new PassThrough();
  const stderr = new PassThrough();
  const exit = await runCommand(args, { stdin: Readable.from([]), stdout, stderr });
  expect(exit).toBe(0);
  expect(stdout.read()?.toString()).toContain("Usage: assign");
  expect(stderr.read()).toBeNull();
});

test("registered commands keep the built-in config command", async () => {
  const stdout = new PassThrough();
  const io = { stdin: Readable.from([]), stdout, stderr: new PassThrough() };
  const register: RegisterCommands = program => { program.command("probe"); };

  expect(await runCommand(["--host", "https://fixture.invalid", "config", "show"], io, undefined, register))
    .toBe(0);
  expect(stdout.read()?.toString()).toBe("API host: https://fixture.invalid\nHost source: flag\n");
});

test("the root host guard runs before an added command", async () => {
  const stderr = new PassThrough();
  const io = { stdin: Readable.from([]), stdout: new PassThrough(), stderr };
  let called = false;
  const register: RegisterCommands = program => {
    program.command("probe").action(() => { called = true; });
  };

  expect(await runCommand(["--host", "invalid", "probe"], io, undefined, register)).toBe(2);
  expect(called).toBe(false);
  expect(stderr.read()?.toString()).toBe("assign: --host must be an absolute HTTPS URL\n");
});

test("a command EPIPE remains an operation failure", async () => {
  const stderr = new PassThrough();
  const io = { stdin: Readable.from([]), stdout: new PassThrough(), stderr };
  const register: RegisterCommands = program => {
    program.command("probe").action(() => {
      throw Object.assign(new Error("socket write failed"), { code: "EPIPE" });
    });
  };

  expect(await runCommand(["probe"], io, undefined, register)).toBe(1);
  expect(stderr.read()?.toString()).toBe("assign: command failed\n");
});

function closedPipe() {
  const stream = new Writable({
    write(_chunk, _encoding, done) {
      done(Object.assign(new Error("pipe closed"), { code: "EPIPE" }));
    },
  });
  stream.on("error", () => {});
  return stream;
}

test("a closed stdout stops an added command successfully", async () => {
  const stderr = new PassThrough();
  const io = { stdin: Readable.from([]), stdout: closedPipe(), stderr };
  let continued = false;
  const register: RegisterCommands = program => {
    program.command("probe").action(async () => {
      await writeStdout(io, "output\n");
      continued = true;
    });
  };

  expect(await runCommand(["probe"], io, undefined, register)).toBe(0);
  expect(continued).toBe(false);
  expect(stderr.read()).toBeNull();
});

test("a closed stderr preserves the original failure exit code", async () => {
  const io = { stdin: Readable.from([]), stdout: new PassThrough(), stderr: closedPipe() };

  expect(await runCommand(["--host", "invalid", "config", "show"], io)).toBe(2);
});
