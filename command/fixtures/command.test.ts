import { expect, test } from "bun:test";
import { PassThrough, Readable } from "node:stream";
import { runCommand } from "../src/main.ts";

test.each([[["help"]], [["--help"]], [["help", "config"]]])("%j displays help and exits successfully", async (args) => {
  const stdout = new PassThrough();
  const stderr = new PassThrough();
  const exit = await runCommand(args, { stdin: Readable.from([]), stdout, stderr });
  expect(exit).toBe(0);
  expect(stdout.read()?.toString()).toContain("Usage: assign");
  expect(stderr.read()).toBeNull();
});
