const unsafeTerminal = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F-\u009F\u061C\u200E\u200F\u202A-\u202E\u2066-\u2069]/gu;

export function safeTerminalText(value: string): string {
  return value.replaceAll("\r\n", "\n").replaceAll("\r", "\n").replace(unsafeTerminal, "�");
}
