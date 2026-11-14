import { mkdirSync } from "node:fs";

if (Bun.version !== "1.4.3" || Bun.revision !== "c6da4a4d3010e5553438c60f6bd76d981976867c") {
  throw new Error("Use Bun 1.4.3+c6da4a4d3 to build the CLI candidate");
}
const option = process.argv[2];
if (process.argv.length > 3 || (option !== undefined && option !== "--fixture" && option !== "--sdk-fixture" && option !== "--credential-fixture")) {
  throw new Error("Unsupported build option");
}
const identity = {
  version: process.env.ASSIGN_BUILD_VERSION ?? "dev",
  commit: process.env.ASSIGN_BUILD_COMMIT ?? "none",
  date: process.env.ASSIGN_BUILD_DATE ?? "unknown",
};
if (Object.values(identity).some(value => !/^[!-~]{1,128}$/.test(value))) {
  throw new Error("Invalid CLI build identity");
}
const suffix = process.platform === "win32" ? ".exe" : "";
const entry = option === "--credential-fixture" ? "fixtures/credentials-entry.ts" : option === "--sdk-fixture" ? "fixtures/sdk-entry.ts" :
  option === "--fixture" ? "fixtures/entry.ts" : "src/entry.ts";
const output = option === "--credential-fixture" ? "assign-credential-fixture" : option === "--sdk-fixture" ? "assign-sdk-fixture" : option === "--fixture" ? "assign-fixture" : "assign";
mkdirSync("dist", { recursive: true });
const build = Bun.spawnSync([
  process.execPath, "build", entry,
  "--compile", "--minify", "--no-compile-autoload-dotenv", "--no-compile-autoload-bunfig",
  "--define", "ASSIGN_BUILD_IDENTITY=" + JSON.stringify(identity),
  "--outfile", `dist/${output}${suffix}`,
], { stdout: "inherit", stderr: "inherit" });
process.exitCode = build.exitCode;

// Linux packages must ship this private executable beside the compiled command.
if (build.exitCode === 0 && process.platform === "linux") {
  const flags = Bun.spawnSync(["pkg-config", "--cflags", "--libs", "libsecret-1"], {stderr: "inherit"});
  if (flags.exitCode !== 0) throw new Error("Linux builds require libsecret-1 development files and pkg-config");
  const native = Bun.spawnSync(["cc", "-std=c11", "-Wall", "-Wextra", "-Werror", "-O2",
    "native/secret-service.c", "-o", "dist/assign-secret-service",
    ...flags.stdout.toString().trim().split(/\s+/)], {stdout: "inherit", stderr: "inherit"});
  if (native.exitCode !== 0) throw new Error("Linux credential helper compilation failed");
}

if (build.exitCode === 0 && process.platform === 'win32') {
  await import('./build-windows-helper.ts');
}
