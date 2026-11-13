import { processIO, runCommand, writeStdout } from '../src/main.ts';
process.exitCode = await runCommand(['wait'], processIO(), undefined, program => {
 program.command('wait').action(async () => {
  await writeStdout(processIO(), 'ready\n');
  process.stdin.resume();
  await new Promise(() => {});
 });
});
