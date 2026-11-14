import { processIO, runCommand } from "./main.ts";

process.exitCode = await runCommand(process.argv.slice(2), processIO());
