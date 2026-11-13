import { SchemaVersion, TableSchemaVersion, MaxDepth, MaxNodes } from './richtext-limits.ts';
import { safeBlock } from './output.ts';

// Read-only projection of Core's JSON interchange, not an editor/document model.
// Source identity is pinned in richtext-source.json; regenerate with the script.
type ObjectValue = Record<string, any>;
function object(v: unknown): ObjectValue {
  if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('invalid rich-text structure');
  return v as ObjectValue;
}
function children(n: ObjectValue): ObjectValue[] {
  if (n.content === undefined) return [];
  if (!Array.isArray(n.content)) throw new Error('invalid rich-text children');
  return n.content.map(object);
}
function escapeText(s: string): string { return s.replace(/[\\`*_{}\[\]<>#+!~$&)\-]|(?<=\d)\.(?=\s|$)/gu, '\\$&'); }
function destination(s: string): string {
  const href = s.trim();
  if (!/^[/#?]/.test(href)) {
    let url: URL; try { url = new URL(href); } catch { throw new Error('unsupported rich-text link'); }
    if (!(['http:', 'https:'].includes(url.protocol) && url.host || url.protocol === 'mailto:' && url.pathname)) throw new Error('unsupported rich-text link');
  }
  return s.replace(/[\\() <>]/g, c => ({' ':'%20','<':'%3C','>':'%3E'}[c] ?? '\\'+c));
}
function run(s: string): number { return Math.max(0, ...Array.from(s.matchAll(/`+/g), m => m[0].length)); }
function attrs(n: ObjectValue): ObjectValue { return n.attrs === undefined ? {} : object(n.attrs); }
function text(v: unknown): string { if (typeof v !== 'string') throw new Error('invalid rich-text text'); return v; }
function inline(nodes: ObjectValue[]): string {
  return nodes.map(n => {
    const a = attrs(n);
    if (n.type === 'hardBreak') return '  \n';
    if (n.type === 'image' || n.type === 'file') return attachment(n);
    const ref = ({userMention:'user',agentMention:'agent',taskReference:'task',projectReference:'project',documentReference:'document'} as Record<string,string>)[n.type];
    if (ref) return `[${escapeText(text(a.label))}](assign:${ref}/${text(a.id)})`;
    if (n.type !== 'text') throw new Error(`unsupported rich-text inline: ${n.type}`);
    let value = text(n.text);
    const marks = n.marks === undefined ? [] : n.marks;
    if (!Array.isArray(marks)) throw new Error('invalid rich-text marks');
    if (marks.some(m => m.type === 'code')) {
      const fence = '`'.repeat(run(value)+1);
      const padding = value.startsWith('`') || value.endsWith('`') || (value.startsWith(' ') && value.endsWith(' ') && value.trim()) ? ' ' : '';
      value = fence+padding+value+padding+fence;
    } else value = escapeText(value);
    for (const mark of marks) {
      const delimiters: Record<string,string> = {bold:'**',italic:'*',strike:'~~',underline:'++'};
      if (delimiters[mark.type]) value = delimiters[mark.type]+value+delimiters[mark.type];
      else if (mark.type === 'link') value = `[${value}](${destination(text(attrs(object(mark)).href))})`;
      else if (mark.type !== 'code') throw new Error(`unsupported rich-text mark: ${mark.type}`);
    }
    return value;
  }).join('');
}
function attachment(n: ObjectValue): string {
  const a = attrs(n);
  return `${n.type === 'image' ? '!' : ''}[${escapeText(text(a[n.type === 'image' ? 'alt' : 'name']))}](assign:attachment/${text(a.attachmentId)})`;
}
function blocks(nodes: ObjectValue[]): string { return nodes.map(block).filter(s => s.trim()).join('\n\n'); }
function block(n: ObjectValue): string {
  const a = attrs(n), c = children(n);
  switch(n.type) {
    case 'paragraph': return inline(c);
    case 'heading': return '#'.repeat(Number.isInteger(a.level) && a.level >= 1 && a.level <= 6 ? a.level : 1)+' '+inline(c);
    case 'horizontalRule': return '---';
    case 'image': case 'file': return attachment(n);
    case 'blockMath': return '$$\n'+text(a.latex)+'\n$$';
    case 'codeBlock': {
      const value = c.map(n => text(n.text)).join(''), fence = '`'.repeat(Math.max(3,run(value)+1));
      const language = a.language ?? '';
      if (!/^[a-zA-Z0-9_+-]*$/.test(language)) throw new Error('unsupported code language');
      return fence+language+'\n'+value+'\n'+fence;
    }
    case 'blockquote': return blocks(c).split('\n').map(s => '> '+s).join('\n');
    case 'callout': return '> [!'+text(a.kind).toUpperCase()+']'+(a.icon == null ? '' : ' '+(text(a.icon) || 'none'))+'\n'+blocks(c).split('\n').map(s => s ? '> '+s : '>').join('\n');
    case 'bulletList': case 'orderedList': case 'taskList': return c.map((item,i) => {
      if (!['listItem','taskItem'].includes(item.type)) throw new Error('invalid list item');
      const marker = n.type === 'orderedList' ? `${(a.start ?? 1)+i}. ` : n.type === 'taskList' ? `- [${attrs(item).checked ? 'x' : ' '}] ` : '- ';
      return blocks(children(item)).split('\n').map((s,j) => (j ? ' '.repeat(n.type === 'taskList' ? 2 : marker.length) : marker)+s).join('\n');
    }).join('\n');
    case 'table': return table(n);
    default: throw new Error(`unsupported rich-text block: ${n.type}`);
  }
}
function table(n: ObjectValue): string {
  const rows = children(n).map(children);
  if (!rows.length || rows.some(r => !r.length || r.length !== rows[0]!.length)) throw new Error('invalid table');
  let simple = rows.length >= 2;
  const lines = rows.map((row,ri) => '| '+row.map((cell,ci) => {
    const a = attrs(cell), c = children(cell);
    if ((a.colspan ?? 1) !== 1 || (a.rowspan ?? 1) !== 1 || a.colwidth != null || cell.type !== (ri ? 'tableCell' : 'tableHeader') || c.length !== 1 || c[0]!.type !== 'paragraph' || a.align !== attrs(rows[0]![ci]!).align) simple = false;
    return c.length === 1 && c[0]!.type === 'paragraph' ? inline(children(c[0]!)).replaceAll('|','\\|').replaceAll('  \n','<br>') : '';
  }).join(' | ')+' |');
  if (!simple) return '```assign-table\n'+(n as any)[rawNode]+'\n```';
  lines.splice(1,0,'| '+rows[0]!.map(cell => ({left:':---',right:'---:',center:':---:'}[attrs(cell).align as string] ?? '---')).join(' | ')+' |');
  return lines.join('\n');
}
// Keep lexical JSON for complex tables: Core compacts RawMessage without
// sorting its keys or rounding number tokens. Never reconstruct this fallback.
const rawNode = Symbol('raw JSON');
function parseInput(raw: string): unknown {
  JSON.parse(raw); // Validate complete JSON syntax before the bounded span walk.
  let offset = 0;
  const space = () => { while (/\s/.test(raw[offset] ?? '') && offset < raw.length) offset++; };
  const stringEnd = () => {
    offset++;
    while (offset < raw.length) { if (raw[offset++] === '\\') offset++; else if (raw[offset-1] === '"') break; }
  };
  const compact = (s: string) => {
    let quoted = false, escaped = false, out = '';
    for (const c of s) {
      if (quoted) {
        out += /[<>&\u2028\u2029]/.test(c) ? '\\u'+c.charCodeAt(0).toString(16).padStart(4,'0') : c;
        if (escaped) escaped=false; else if(c === '\\') escaped=true; else if(c === '"') quoted=false;
      } else if(c === '"') {quoted=true;out+=c;} else if(!/\s/.test(c)) out+=c;
    }
    return out;
  };
  const value = (depth: number): any => {
    if(depth > MaxDepth*3+8) throw new Error('rich-text complexity limit');
    space(); const start=offset, first=raw[offset];
    if(first === '{') {
      offset++; space(); const result: ObjectValue = {};
      while(raw[offset] !== '}') {
        const keyStart=offset; stringEnd(); const key=JSON.parse(raw.slice(keyStart,offset));
        if(Object.hasOwn(result,key)) throw new Error('duplicate rich-text JSON field');
        space(); offset++; const child=value(depth+1);
        Object.defineProperty(result,key,{value:child,enumerable:true,writable:true});
        space(); if(raw[offset] !== ',') break; offset++; space();
      }
      offset++; Object.defineProperty(result,rawNode,{value:compact(raw.slice(start,offset))}); return result;
    }
    if(first === '[') {
      offset++; space(); const result=[];
      while(raw[offset] !== ']') {result.push(value(depth+1));space();if(raw[offset] !== ',') break;offset++;}
      offset++; return result;
    }
    if(first === '"') stringEnd(); else while(offset < raw.length && !/[\s,}\]]/.test(raw[offset]!)) offset++;
    return JSON.parse(raw.slice(start,offset));
  };
  return value(0);
}
export function renderRichText(raw: string): string {
  if (Buffer.byteLength(raw) > 2*1024*1024) throw new Error('rich-text input too large');
  const doc = object(parseInput(raw));
  if (doc.type !== 'doc' || ![SchemaVersion,TableSchemaVersion].includes(doc.schema_version)) throw new Error('unsupported rich-text document');
  let count = 0;
  const check = (n: ObjectValue, depth: number): void => {
    if (++count > MaxNodes || depth > MaxDepth) throw new Error('rich-text complexity limit');
    children(n).forEach(c => check(c,depth+1));
  };
  check(doc,0);
  const body = safeBlock(blocks(children(doc)));
  const result = body ? body+'\n' : '';
  if (Buffer.byteLength(result) > 2*1024*1024) throw new Error('rich-text output too large');
  return result;
}
