import { expect, test } from 'bun:test';
import { PassThrough, Readable, Writable } from 'node:stream';
import contract from '../src/shell-contract.json';
import { runCommand } from '../src/main.ts';

async function run(args: string[]) {
 const stdout = new PassThrough(); const stderr = new PassThrough();
 const code = await runCommand(args, {stdin:Readable.from([]),stdout,stderr});
 return {code,stdout:stdout.read()?.toString() ?? '',stderr:stderr.read()?.toString() ?? ''};
}
for (const node of contract.nodes) {
 test(`${node.path} frozen help and aliases`, async () => {
  const args = node.path.split(' ').slice(1);
  expect(await run([...args,'--help'])).toEqual({code:0,stdout:node.help,stderr:''});
  for (const alias of node.aliases ?? []) {
   expect(await run([...args.slice(0,-1),alias,'--help'])).toEqual({code:0,stdout:node.help,stderr:''});
  }
 });
 if (node.args[0]) test(`${node.path} missing arguments`, async () => {
  expect(await run(node.path.split(' ').slice(1))).toEqual({code:2,stdout:'',stderr:`assign: ${node.args[0]}\n`});
 });
}
test.each(['bash','zsh','fish'] as const)('%s completion is frozen and offline', async shell => {
 expect(await run(['completions',shell])).toEqual({code:0,stdout:contract.scripts[shell],stderr:''});
});
test('PowerShell remains unsupported', async () => {
 expect(await run(['completion','powershell'])).toEqual({code:2,stdout:'',stderr:'assign: unsupported shell "powershell"; expected bash, zsh, or fish\n'});
});
test('aliases retain registration order', async () => {
 expect(await run(['aliases'])).toEqual({code:0,stdout:contract.aliases,stderr:''});
});
test('offline completion protocol resolves aliases and flags', async () => {
 expect((await run(['__complete','si'])).stdout).toContain('signin\nsignout\n:4');
 expect((await run(['__complete','t','show','--f'])).stdout).toContain('--field\n:4');
});
test('grammar errors are diagnostics without stack traces', async () => {
 for (const args of [['task','show'], ['version','extra'], ['--unknown']]) {
  const result = await run(args);
  expect(result.code).toBe(2); expect(result.stdout).toBe(''); expect(result.stderr).toStartWith('assign: '); expect(result.stderr).not.toContain(' at ');
 }
});
test('help and completion tolerate a closed output pipe', async () => {
 for (const args of [['--help'],['completion','bash']]) {
  const stdout=new Writable({write(_chunk,_encoding,done){done(Object.assign(new Error('closed'),{code:'EPIPE'}));}});
  stdout.on('error',()=>{});
  expect(await runCommand(args,{stdin:Readable.from([]),stdout,stderr:new PassThrough()})).toBe(0);
 }
});
test('SIGINT returns 130 without a stack trace', async () => {
 const child=Bun.spawn([process.execPath, new URL('./shell-process.ts',import.meta.url).pathname],{stdin:'pipe',stdout:'pipe',stderr:'pipe'});
 try {
  const reader=child.stdout.getReader();
  const ready=await reader.read();
  expect(new TextDecoder().decode(ready.value)).toBe('ready\n');
  child.kill('SIGINT');
  expect(await child.exited).toBe(130);
  expect(await new Response(child.stderr).text()).toBe('');
 } finally {child.stdin.end();child.kill();await child.exited;}
});

for (const fixture of contract.cases) test(`Go shell parity ${JSON.stringify(fixture.argv)}`,async()=>{
 expect(await run(fixture.argv)).toEqual({code:fixture.code,stdout:fixture.stdout,stderr:fixture.stderr});
});
test('known aliases reach their action boundary and shorthand help clusters work',async()=>{
 const result=await run(['t','show','ABC-1']);
 expect(result.code).toBe(1);
 expect(result.stderr).toContain('assign task show: command action is not available');
 expect((await run(['-hv'])).stdout).toBe(contract.nodes[0]!.help);
});
