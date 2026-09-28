import type { DiscussMessage } from "./types.js";

export type DiscussTextFrame = {
  readonly kind: "text_delta" | "text_snapshot";
  readonly runId: string;
  readonly messageId: string;
  readonly partId: string;
  readonly producerFence: number;
  readonly sequence: number;
  readonly generation: number;
  readonly revision: number;
  readonly offset: number;
  readonly text: string;
};

export type DiscussStreamOptions = {
  readonly cursor?: string;
  readonly signal?: AbortSignal;
  readonly onReady: (cursor: string) => Promise<void>;
  readonly onMessage: (message: DiscussMessage, cursor: string) => void;
  readonly onText: (frame: DiscussTextFrame) => void;
  readonly onResync: (cursor: string) => void;
};

const encodedLength = (value: string) => new TextEncoder().encode(value).length;

function readDiscussMessage(value: unknown): DiscussMessage {
  if (!value || typeof value !== "object") throw new Error("Invalid Discuss message");
  const raw = value as Record<string, unknown>;
  if (typeof raw.id !== "string" || !raw.id || typeof raw.sequence !== "number" || !Number.isSafeInteger(raw.sequence) || raw.sequence < 1 ||
    typeof raw.revision !== "number" || !Number.isSafeInteger(raw.revision) || raw.revision < 1 ||
    !["user", "assistant", "agent", "system"].includes(String(raw.role)) || typeof raw.kind !== "string" ||
    !["pending", "final", "failed", "cancelled"].includes(String(raw.state)) || typeof raw.content !== "string" ||
    !Array.isArray(raw.parts) || typeof raw.correlation_id !== "string" || typeof raw.created_at !== "string" ||
    !(raw.finalized_at === null || typeof raw.finalized_at === "string")) throw new Error("Invalid Discuss message");
  return raw as DiscussMessage;
}

export function readDiscussTextFrame(value: unknown): DiscussTextFrame {
  if (!value || typeof value !== "object") throw new Error("Invalid Discuss text frame");
  const raw = value as Record<string, unknown>;
  if (!(["text_delta", "text_snapshot"] as unknown[]).includes(raw.kind) ||
    ["run_id", "message_id", "part_id"].some((key) => typeof raw[key] !== "string" || !(raw[key] as string).length || (raw[key] as string).length > 128) ||
    ["producer_fence", "producer_sequence", "generation", "revision"].some((key) => typeof raw[key] !== "number" || !Number.isSafeInteger(raw[key]) || (raw[key] as number) < 1) ||
    typeof raw.offset !== "number" || !Number.isSafeInteger(raw.offset) || raw.offset < 0 || raw.offset > 65_536 ||
    typeof raw.text !== "string" || encodedLength(raw.text) > (raw.kind === "text_delta" ? 2_048 : 65_536)) {
    throw new Error("Invalid Discuss text frame");
  }
  return {
    kind: raw.kind as DiscussTextFrame["kind"], runId: raw.run_id as string,
    messageId: raw.message_id as string, partId: raw.part_id as string,
    producerFence: raw.producer_fence as number, sequence: raw.producer_sequence as number,
    generation: raw.generation as number, revision: raw.revision as number,
    offset: raw.offset, text: raw.text,
  };
}

/** Consume one bounded SSE connection. The caller owns reconnect and resync policy. */
export async function consumeDiscussStream(response: Response, options: DiscussStreamOptions): Promise<{ cursor: string; retryAfterMs: number }> {
  if (!response.ok) throw new Error(`Discuss stream failed (${response.status})`);
  if (!response.headers.get("content-type")?.startsWith("text/event-stream") || !response.body) throw new Error("Discuss stream unavailable");
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "";
  let ready = false;
  let cursor = options.cursor ?? "";
  let retryAfterMs = 500;
  let textReady = false;
  try {
    while (!options.signal?.aborted) {
      let timeout: ReturnType<typeof setTimeout> | undefined;
      const chunk = await Promise.race([
        reader.read(),
        new Promise<never>((_, reject) => { timeout = setTimeout(() => reject(new Error("Discuss stream heartbeat lost")), 45_000); }),
      ]).finally(() => clearTimeout(timeout));
      if (chunk.done) {
        buffer += decoder.decode();
        if (buffer.trim()) throw new Error("Incomplete Discuss stream frame");
        break;
      }
      buffer = (buffer + decoder.decode(chunk.value, { stream: true })).replaceAll("\r\n", "\n");
      if (encodedLength(buffer) > 524_288) throw new Error("Discuss stream frame too large");
      let boundary: number;
      while ((boundary = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const lines = frame.split("\n");
        const kind = lines.find((line) => line.startsWith("event: "))?.slice(7);
        const id = lines.find((line) => line.startsWith("id: "))?.slice(4);
        if (!kind) continue;
        const raw = lines.filter((line) => line.startsWith("data: ")).map((line) => line.slice(6)).join("\n");
        const data: unknown = JSON.parse(raw);
        if (!data || typeof data !== "object") throw new Error("Invalid Discuss stream frame");
        if (kind === "resync_required") {
          if (!("cursor" in data) || typeof data.cursor !== "string" || !data.cursor) throw new Error("Invalid Discuss resync");
          options.onResync(data.cursor);
          return { cursor: data.cursor, retryAfterMs: 0 };
        }
        if (kind === "hello") {
          if (ready || !("envelope_version" in data) || data.envelope_version !== 1 || !("cursor" in data) || typeof data.cursor !== "string" || !data.cursor ||
            !("stream_epoch" in data) || typeof data.stream_epoch !== "string" || !data.stream_epoch ||
            !("heartbeat_interval_ms" in data) || typeof data.heartbeat_interval_ms !== "number" || !Number.isSafeInteger(data.heartbeat_interval_ms) || data.heartbeat_interval_ms < 1 ||
            !("retry_after_ms" in data) || typeof data.retry_after_ms !== "number" || !Number.isSafeInteger(data.retry_after_ms) || data.retry_after_ms < 0) {
            throw new Error("Invalid Discuss stream negotiation");
          }
          ready = true;
          cursor = data.cursor;
          retryAfterMs = Math.min(10_000, data.retry_after_ms);
          textReady = "text_deltas" in data && data.text_deltas === true;
          await options.onReady(cursor);
        } else if (kind === "discuss_event") {
          if (!ready || !("stream_id" in data) || typeof data.stream_id !== "string") throw new Error("Invalid Discuss event");
          if ("text_delta" in data) {
            if (!textReady || id) throw new Error("Unnegotiated Discuss text");
            options.onText(readDiscussTextFrame(data.text_delta));
          } else {
            if (!id || !("sequence" in data) || typeof data.sequence !== "number" || !Number.isSafeInteger(data.sequence) || data.sequence < 1 || !("message" in data)) throw new Error("Invalid Discuss event");
            cursor = id;
            options.onMessage(readDiscussMessage(data.message), cursor);
          }
        } else if (kind !== "heartbeat") {
          throw new Error("Unsupported Discuss event");
        }
      }
    }
    if (!ready && !options.signal?.aborted) throw new Error("Discuss stream disconnected");
    return { cursor, retryAfterMs };
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}

export type ProvisionalText = DiscussTextFrame & { readonly value: string };

export function applyDiscussText(current: ProvisionalText | undefined, frame: DiscussTextFrame): ProvisionalText | undefined {
  if (frame.kind === "text_snapshot") {
    if (frame.offset !== 0) return undefined;
    return { ...frame, value: frame.text };
  }
  if (!current || current.messageId !== frame.messageId || current.partId !== frame.partId || current.runId !== frame.runId ||
    current.producerFence !== frame.producerFence || current.generation !== frame.generation || current.sequence + 1 !== frame.sequence ||
    encodedLength(current.value) !== frame.offset) return undefined;
  return { ...frame, value: current.value + frame.text };
}

export function mergeDiscussMessage(messages: readonly DiscussMessage[], incoming: DiscussMessage): readonly DiscussMessage[] {
  const index = messages.findIndex((message) => message.id === incoming.id);
  if (index < 0) return [...messages, incoming].sort((left, right) => left.sequence - right.sequence);
  const current = messages[index]!;
  if ((incoming.revision ?? 1) <= (current.revision ?? 1)) return messages;
  const next = [...messages];
  next[index] = incoming;
  return next;
}
