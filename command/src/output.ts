import type { CommandIO } from './main.ts';
import { writeStdout } from './main.ts';

// navigation.go safeInline/safeBlock, extended with terminal's accepted bidi set.
const controls = /[\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu;
export function safeInline(value: string): string { return value.replace(controls, ' '); }
export function safeBlock(value: string): string {
  return value.replaceAll('\r\n', '\n').replaceAll('\r', '\n').replace(controls, c => c === '\n' || c === '\t' ? c : '�');
}
export function truncateText(value: string, limit = 65536): { text: string; truncated: boolean } {
  if (!Number.isSafeInteger(limit) || limit < 0) throw new Error('invalid text limit');
  const chars = Array.from(value);
  return { text: chars.slice(0, limit).join(''), truncated: chars.length > limit };
}
// Sort recursively to match encoding/json, rather than insertion order. Reject
// lossy JS numbers; callers retain API decimal strings as strings.
export function compactJSON(value: unknown): string {
  const visit = (v: unknown): unknown => {
    if (v === null || typeof v === 'string' || typeof v === 'boolean') return v;
    if (typeof v === 'number' && Number.isFinite(v) && (!Number.isInteger(v) || Number.isSafeInteger(v))) return v;
    if (Array.isArray(v)) return v.map(visit);
    if (v && typeof v === 'object' && Object.getPrototypeOf(v) === Object.prototype) {
      return Object.fromEntries(Object.keys(v).sort((a,b) => Buffer.compare(Buffer.from(a),Buffer.from(b))).map(k => [k, visit((v as Record<string, unknown>)[k])]));
    }
    throw new Error('value is not lossless JSON');
  };
  // JSON.stringify reorders integer keys. Encode objects explicitly.
  const encode = (v: any): string => Array.isArray(v) ? '[' + v.map(encode).join(',') + ']' : v && typeof v === 'object' ? '{' + Object.keys(v).sort((a,b) => Buffer.compare(Buffer.from(a),Buffer.from(b))).map(k => JSON.stringify(k)+':'+encode(v[k])).join(',') + '}' : Object.is(v, -0) ? '-0' : JSON.stringify(v);
  return encode(visit(value)).replace(/[<>&\u2028\u2029]/g, c => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
}
export async function printJSON(io: CommandIO, value: unknown): Promise<void> { await writeStdout(io, compactJSON(value) + '\n'); }
export async function printScalar(io: CommandIO, value: string | number | boolean | null): Promise<void> {
  await writeStdout(io, (typeof value === 'string' ? safeInline(value) : compactJSON(value)) + '\n');
}
export async function printRows(io: CommandIO, rows: readonly (readonly [string, string])[]): Promise<void> {
  await writeStdout(io, rows.map(([key, title]) => `${safeInline(key)}  ${safeInline(title)}\n`).join(''));
}
export function documentText(value: string): string {
  const result = truncateText(value);
  return safeBlock(result.text) + (result.truncated ? '\n\n[Document text truncated at 65,536 characters.]' : '');
}
