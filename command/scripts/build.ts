import { mkdirSync } from "node:fs";

if (Bun.version !== "1.4.3" || Bun.revision !== "c6da4a4d3010e5553438c60f6bd76d981976867c") {
  throw new Error("Use Bun 1.4.3+c6da4a4d3 to build the CLI candidate");
}
const option = process.argv[2];
if (process.argv.length > 3 || (option !== undefined && option !== "--fixture" && option !== "--sdk-fixture")) {
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
const entry = option === "--sdk-fixture" ? "fixtures/sdk-entry.ts" :
  option === "--fixture" ? "fixtures/entry.ts" : "src/entry.ts";
const output = option === "--sdk-fixture" ? "assign-sdk-fixture" : option === "--fixture" ? "assign-fixture" : "assign";
mkdirSync("dist", { recursive: true });
const build = Bun.spawnSync([
  process.execPath, "build", entry,
  "--compile", "--minify", "--no-compile-autoload-dotenv", "--no-compile-autoload-bunfig",
  "--define", "ASSIGN_BUILD_IDENTITY=" + JSON.stringify(identity),
  "--outfile", `dist/${output}${suffix}`,
], { stdout: "inherit", stderr: "inherit" });
process.exitCode = build.exitCode;
