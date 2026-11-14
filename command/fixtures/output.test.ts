import {test, expect} from 'bun:test';
import {Writable, Readable} from 'node:stream';
import {compactJSON, printJSON, printRows, printScalar, safeBlock, safeInline, truncateText, documentText} from '../src/output.ts';
import {renderRichText} from '../src/richtext.ts';
const t = (text: string, marks?: unknown[]) => ({type:'text',text,...(marks ? {marks} : {})});
const p = (...content: unknown[]) => ({type:'paragraph',content});
const doc = (...content: unknown[]) => JSON.stringify({schema_version:1,type:'doc',content});
test('exact streams, Go JSON ordering/escaping, decimal strings, null and newlines',async () => {
  let stdout='',stderr='';
  const io = {stdin:Readable.from([]),stdout:new Writable({write(c,_,done){stdout+=c;done();}}),stderr:new Writable({write(c,_,done){stderr+=c;done();}})};
  await printRows(io,[['ASG-1','界🙂e\u0301']]);
  await printJSON(io,{'2':null,'10':'9007199254740993',z:'<&>',a:[false,1]});
  await printScalar(io,null); await printScalar(io,'line\nvalue');
  expect(stdout).toBe('ASG-1  界🙂e\u0301\n{"10":"9007199254740993","2":null,"a":[false,1],"z":"\\u003c\\u0026\\u003e"}\nnull\nline value\n');
  expect(stderr).toBe('');
  expect(()=>compactJSON(9007199254740992)).toThrow();
  expect(()=>compactJSON(undefined)).toThrow();
});
test('terminal controls, ANSI, OSC and bidi are inert; Unicode stays unchanged',() => {
  expect(safeBlock('\x1b[31m\x1b]8;;url\x07X\u009b\u202e\r\n\t界🙂e\u0301')).toBe('�[31m�]8;;url�X��\n\t界🙂e\u0301');
  expect(safeInline('\t\r\n\x00\u2066')).toBe('     ');
  expect(truncateText('界🙂x',2)).toEqual({text:'界🙂',truncated:true});
  expect(truncateText('🙂',1)).toEqual({text:'🙂',truncated:false});
  expect(documentText('x'.repeat(65537))).toBe('x'.repeat(65536)+'\n\n[Document text truncated at 65,536 characters.]');
});
test('named callout, link and collision-safe code output',() => {
  expect(renderRichText(doc({type:'callout',attrs:{kind:'note',icon:''},content:[p(t('Hello'))]}))).toBe('> [!NOTE] none\n> Hello\n');
  expect(renderRichText(doc(p(t('go',[{type:'link',attrs:{href:'https://example.com/a (b)'}}]))))).toBe('[go](https://example.com/a%20\\(b\\))\n');
  expect(renderRichText(doc({type:'codeBlock',attrs:{language:'ts'},content:[t('```\nx')]}))).toBe('````ts\n```\nx\n````\n');
  expect(renderRichText(doc(p(t('`x`',[{type:'code'}]))))).toBe('`` `x` ``\n');
});
test('table Markdown and lossless complex fallback',() => {
  const cell = (type:string,s:string) => ({type,content:[p(t(s))]});
  const table = {type:'table',content:[{type:'tableRow',content:[cell('tableHeader','H')]},{type:'tableRow',content:[cell('tableCell','A|B')]}]};
  expect(renderRichText(JSON.stringify({schema_version:2,type:'doc',content:[table]}))).toBe('| H |\n| --- |\n| A\\|B |\n');
  (table.content[1]!.content[0] as any).attrs={colspan:2};
  expect(renderRichText(JSON.stringify({schema_version:2,type:'doc',content:[table]}))).toBe('```assign-table\n'+JSON.stringify(table)+'\n```\n');
});
test('unknown blocks/inline/marks and malformed, deep or oversized inputs fail explicitly',() => {
  for (const raw of [doc({type:'future'}),doc(p({type:'future'})),doc(p(t('x',[{type:'future'}]))),doc(p(t('x',[{type:'link',attrs:{href:'javascript:alert(1)'}}]))),'null',doc(p(t('x'.repeat(2*1024*1024))))]) expect(()=>renderRichText(raw)).toThrow();
  let nested:any=p(t('x')); for(let i=0;i<34;i++) nested={type:'blockquote',content:[nested]};
  expect(()=>renderRichText(doc(nested))).toThrow('complexity');
  expect(renderRichText(doc())).toBe('');
});

test('frozen Go renderer exact values', async () => {
  const cases = await Bun.file(new URL('./richtext-go.json',import.meta.url)).json();
  for (const fixture of cases) expect(renderRichText(JSON.stringify(fixture.input))).toBe(fixture.output);
});

test('relative durable links and lexical complex-table fallback',() => {
  expect(renderRichText(doc(p(t('local',[{type:'link',attrs:{href:'#section'}}]))))).toBe('[local](#section)\n');
  const raw = '{"schema_version":2,"type":"doc","content":[ { "type":"table", "content":[{"type":"tableRow","content":[{"type":"tableCell","content":[]}]}] } ]}';
  expect(renderRichText(raw)).toBe('```assign-table\n{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","content":[]}]}]}\n```\n');
  expect(()=>renderRichText('{"type":"doc","type":"doc","schema_version":1}')).toThrow('duplicate');
});

test('node and expanded output budgets fail before emitting partial content',() => {
  expect(()=>renderRichText(doc(...Array.from({length:50000},()=>p())))).toThrow('complexity');
  expect(()=>renderRichText(doc(p(t('<'.repeat(1100000)))))).toThrow('output too large');
  expect(compactJSON({'\u{10000}':1,'\ue000':2})).toBe('{"":2,"𐀀":1}');
});
