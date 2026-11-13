// Generated constructor snapshot is the shell grammar authority during migration.
import type { Command } from 'commander';
import contract from './shell-contract.json';
import { CommandFailure, writeStdout } from './main.ts';
import type { CommandIO } from './main.ts';

type Flag = { name: string; short: string; type: string; default: string; annotations: Record<string, string[] | undefined> | null };
type Node = { path: string; aliases: string[] | null; help: string; flags: Flag[]; args: string[]; runnable: boolean };
const nodes: Node[] = contract.nodes;
const children = (node: Node) => nodes.filter(n => n.path.slice(0, n.path.lastIndexOf(' ')) === node.path);
const quote = (s: string) => JSON.stringify(s);
const fail = (s: string): never => { throw new CommandFailure(s, 2); };

function validateValue(flag: Flag, value: string, spelling: string) {
 if (flag.type==='stringSlice' && value) {
  let quoted=false; let fieldStart=true;
  for(let i=0;i<value.length;i++) {
   const char=value[i]!;
   if(char==='"') {
    if(quoted && value[i+1]==='"'){i++;continue;}
    const closing=quoted;
    if(!quoted && !fieldStart) csvError(i,'bare " in non-quoted-field');
    quoted=!quoted;fieldStart=false;
    if(closing && i+1<value.length && ![',','\r','\n'].includes(value[i+1]!)) csvError(i+1,'extraneous or missing " in quoted-field');
   } else if(!quoted && char===',') fieldStart=true;
   else if(!quoted && (char==='\n' || char==='\r')) break;
   else fieldStart=false;
  }
  if(quoted) csvError(value.length,'extraneous or missing " in quoted-field');
  function csvError(index: number, reason: string): never {
   const prefix=value.slice(0,index);const line=prefix.split('\n').length;
   const column=Buffer.byteLength(prefix.slice(prefix.lastIndexOf('\n')+1))+1;
   return fail(`invalid argument ${quote(value)} for ${quote(spelling)} flag: parse error on line ${line}, column ${column}: ${reason}`);
  }
 }
 if (flag.type==='int' || flag.type==='int64') {
  let reason='';
  try {
   if (!/^[+-]?(?:0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO]?[0-7_]+|[1-9][0-9_]*|0)$/.test(value)) throw Error();
   const sign=value.startsWith('-')?-1n:1n;let digits=value.replace(/^[+-]/,'').replaceAll('_','');
   if (/^0[0-7]+$/.test(digits)) digits='0o'+digits.slice(1);
   const v=sign*BigInt(digits);if(v < -(1n<<63n) || v >= (1n<<63n)) reason='value out of range';
  } catch {reason='invalid syntax';}
  if(reason) fail(`invalid argument ${quote(value)} for ${quote(spelling)} flag: strconv.ParseInt: parsing ${quote(value)}: ${reason}`);
 }
 if(flag.type==='duration') {
  let rest=value.replace(/^[+-]/,''); let total=0n; let reason='';
  const units:Record<string,bigint>={ns:1n,us:1000n,'µs':1000n,'μs':1000n,ms:1000000n,s:1000000000n,m:60000000000n,h:3600000000000n};
  if(rest!=='0') {
   if(!rest) reason=`invalid duration ${quote(value)}`;
   while(rest && !reason) {
    const number=/^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)/.exec(rest)?.[0];
    if(!number){reason=`invalid duration ${quote(value)}`;break;}
    rest=rest.slice(number.length);
    const unit=/^[^0-9.]+/.exec(rest)?.[0] ?? '';
    if(!unit){reason=`missing unit in duration ${quote(value)}`;break;}
    if(!units[unit]){reason=`unknown unit ${quote(unit)} in duration ${quote(value)}`;break;}
    const [whole,fraction='']=number.split('.');
    total+=BigInt(whole || '0')*units[unit]! + BigInt(fraction || '0')*units[unit]!/(10n**BigInt(fraction.length));
    rest=rest.slice(unit.length);
   }
  }
  if(total>(value.startsWith('-')?1n<<63n:(1n<<63n)-1n)) reason=`invalid duration ${quote(value)}`;
  if(reason) fail(`invalid argument ${quote(value)} for ${quote(spelling)} flag: time: ${reason}`);
 }

}

export async function shellDispatch(argv: string[], io: CommandIO, versionText: string): Promise<{ handled: boolean; args: string[] }> {
  let node: Node = nodes[0]!;
  let args = [...argv];
  // Cobra completion scripts re-enter this private, offline protocol.
  if (args[0] === '__complete' || args[0] === '__completeNoDesc') {
    args.shift();
    const prefix = args.pop() ?? '';
    for (const part of args) {
      const child = children(node).find(n => n.path.split(' ').at(-1) === part || n.aliases?.includes(part));
      if (!child) break;
      node = child;
    }
    const candidates = prefix.startsWith('-') ? node.flags.map(f => `--${f.name}`)
      : children(node).flatMap(n => [n.path.split(' ').at(-1)!, ...(n.aliases ?? [])]);
    await writeStdout(io, candidates.filter(s => s.startsWith(prefix)).join('\n') + '\n:4\n');
    return { handled: true, args: [] };
  }
  if (args[0] === 'help' && !args.includes('--help') && !args.includes('-h')) { args = [...args.slice(1), '--help']; }
  const normalized: string[] = [];
  const positionals: string[] = [];
  const changed = new Set<string>();
  let help = false;
  let version = false;
  let literal = false;
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]!;
    if (arg === '--' && !literal) { literal = true; normalized.push(arg); continue; }
    if (!literal && /^-[^-]/.test(arg) && !arg.includes('=') && arg.length>2) {
      const first=node.flags.find(f=>f.short===arg[1]);
      if (first) {
        const head='-'+arg[1]; const rest=arg.slice(2);
        args.splice(i,1,first.type==='bool'?head:head+'='+rest,...(first.type==='bool'?['-'+rest]:[]));
        i--; continue;
      }
    }
    if (!literal && arg.startsWith('-') && arg !== '-') {
      const [name, ...valueParts] = arg.replace(/^--?/, '').split('=');
      const short = !arg.startsWith('--');
      const flag = node.flags.find(f => short ? f.short === name : f.name === name);
      if (!flag) return fail(short ? `unknown shorthand flag: '${arg.slice(1,2)}' in ${arg}` : `unknown flag: --${name}`);
      changed.add(flag.name);
      normalized.push(arg);
      if (flag.type === 'bool') {
        if (valueParts.length && !['true','false','1','0','t','f','TRUE','FALSE','T','F','True','False'].includes(valueParts.join('='))) fail(`invalid argument ${quote(valueParts.join('='))} for "--${flag.name}" flag: strconv.ParseBool: parsing ${quote(valueParts.join('='))}: invalid syntax`);
        if (flag.name === 'version') version = !valueParts.length || ['true','1','t','TRUE','T','True'].includes(valueParts.join('='));
        if (flag.name === 'help') help = !valueParts.length || ['true','1','t','TRUE','T','True'].includes(valueParts.join('='));
      } else if (!valueParts.length) {
        const value = args[++i];
        if (value === undefined) return fail(`flag needs an argument: ${short ? '-' + name : '--' + name}`);
        normalized.push(value);
        validateValue(flag,value,short ? '-' + name : '--' + name);
      }
      if (flag.type !== 'bool' && valueParts.length) validateValue(flag,valueParts.join('='),short ? '-' + name : '--' + name);
      if (flag.name === 'help' || flag.name === 'version') normalized.pop();
      continue;
    }
    const child = !literal && !positionals.length && children(node).find(n => n.path.split(' ').at(-1) === arg || n.aliases?.includes(arg));
    if (child) { node = child; normalized.push(node.path.split(' ').at(-1)!); }
    else { positionals.push(arg); normalized.push(arg); }
  }
  if (help) { await writeStdout(io, node.help); return {handled:true,args:normalized}; }
  if (version) { await writeStdout(io, versionText.split('\n')[0]!.replace(/^assign /,'assign version ') + '\n'); return {handled:true,args:normalized}; }
  if (!node.runnable) { await writeStdout(io,node.help); return {handled:true,args:normalized}; }
  const validation = node.args[Math.min(positionals.length, node.args.length - 1)];
  if (validation) fail(validation.replace(/"argument"/g, quote(positionals[0] ?? '')));
  for (const flag of node.flags) {
    for (const group of flag.annotations?.['cobra_annotation_mutually_exclusive'] ?? []) {
      const names = group.split(' ');
      const set = names.filter(n => changed.has(n)).sort();
      if (set.length > 1) throw new CommandFailure(`if any flags in the group [${group}] are set none of the others can be; [${set.join(' ')}] were all set`, 1);
    }
  }
  if (node.path === 'assign completion') {
    const shell = positionals[0]!;
    if (!Object.hasOwn(contract.scripts, shell)) fail(`unsupported shell ${quote(shell)}; expected bash, zsh, or fish`);
    await writeStdout(io, contract.scripts[shell as keyof typeof contract.scripts]);
    return {handled:true,args:normalized};
  }
  if (node.path === 'assign aliases') {
    await writeStdout(io, contract.aliases);
    return {handled:true,args:normalized};
  }
  if (!node.runnable) { await writeStdout(io, node.help); return {handled:true,args:normalized}; }
  return {handled:false,args:normalized};
}

// Cohort actions replace these explicit unavailable handlers as they are ported.
// A recognized grammar path must never become an unknown-command argument error.
export function registerShellGrammar(program: Command) {
 for (const node of nodes.slice(1)) {
  const parts=node.path.split(' ').slice(1);
  let parent=program;
  for(const part of parts) {
   let command=parent.commands.find(c=>c.name()===part);
   if(!command) command=parent.command(part);
   parent=command;
  }
  for(const flag of node.flags) {
   if(flag.name==='help' || flag.name==='version' || flag.name==='host' || parent.options.some(o=>o.long==='--'+flag.name)) continue;
   parent.option(`--${flag.name}${flag.type==='bool'?'':' <value>'}`);
  }
  if(node.runnable && !parent.registeredArguments.length && !['assign config','assign config show','assign config set host','assign config unset host','assign version'].includes(node.path)) {
   parent.argument('[args...]').action(()=>{throw new CommandFailure(`${node.path}: command action is not available in this candidate`,1);});
  }
 }
}
