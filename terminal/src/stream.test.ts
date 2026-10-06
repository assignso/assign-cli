import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { applyDiscussText, consumeDiscussStream, mergeDiscussMessage, type DiscussStreamOptions } from "./stream.js";
import type { DiscussMessage } from "./types.js";

const message = (revision: number, content = "Saved"): DiscussMessage => ({
  id: "message", revision, sequence: 2, role: "assistant", kind: "message", state: revision > 2 ? "final" : "pending",
  content, parts: [], correlation_id: "turn", created_at: "2026-09-20T00:00:00Z", finalized_at: null,
});

const hello = 'event: hello\ndata: {"envelope_version":1,"cursor":"initial","stream_epoch":"epoch","heartbeat_interval_ms":15000,"retry_after_ms":500,"text_deltas":true}\n\n';

describe("terminal Discuss stream", () => {
  it("negotiates, applies provisional UTF-8 text, and advances only durable cursors", async () => {
    const delta = { kind: "text_snapshot", run_id: "run", message_id: "message", part_id: "part", producer_fence: 1, producer_sequence: 1, generation: 1, revision: 1, offset: 0, text: "Hello 🌍" };
    const raw = hello + `event: discuss_event\ndata: ${JSON.stringify({ stream_id: "stream", text_delta: delta })}\n\n` +
      `id: durable-next\nevent: discuss_event\ndata: ${JSON.stringify({ stream_id: "stream", sequence: 2, message: message(3) })}\n\n`;
    const bytes = new TextEncoder().encode(raw);
    const body = new ReadableStream<Uint8Array>({ start(controller) { for (const byte of bytes) controller.enqueue(new Uint8Array([byte])); controller.close(); } });
    const seen: string[] = [];
    const options: DiscussStreamOptions = {
      cursor: "saved", onReady: async (cursor) => { seen.push(`ready:${cursor}`); },
      onMessage: (_message, cursor) => { seen.push(`message:${cursor}`); },
      onText: (frame) => { seen.push(`text:${frame.text}`); }, onResync: () => assert.fail("unexpected resync"),
    };
    const result = await consumeDiscussStream(new Response(body, { headers: { "content-type": "text/event-stream" } }), options);
    assert.deepEqual(seen, ["ready:initial", "text:Hello 🌍", "message:durable-next"]);
    assert.equal(result.cursor, "durable-next");
  });

  it("stops at resync and ignores trailing events", async () => {
    const raw = 'event: resync_required\ndata: {"cursor":"replacement"}\n\n' + hello;
    const resync: string[] = [];
    await consumeDiscussStream(new Response(raw, { headers: { "content-type": "text/event-stream" } }), {
      onReady: async () => assert.fail("unexpected hello"), onMessage: () => assert.fail("unexpected message"),
      onText: () => assert.fail("unexpected text"), onResync: (cursor) => resync.push(cursor),
    });
    assert.deepEqual(resync, ["replacement"]);
  });

  it("detects delta gaps and replaces only with newer canonical revisions", () => {
    const snapshot = applyDiscussText(undefined, { kind: "text_snapshot", runId: "run", messageId: "message", partId: "part", producerFence: 1, sequence: 1, generation: 1, revision: 1, offset: 0, text: "Hi" });
    assert.equal(snapshot?.value, "Hi");
    assert.equal(applyDiscussText(snapshot, { ...snapshot!, kind: "text_delta", sequence: 2, offset: 2, text: "!" })?.value, "Hi!");
    assert.equal(applyDiscussText(snapshot, { ...snapshot!, kind: "text_delta", sequence: 3, offset: 2, text: "gap" }), undefined);
    assert.equal(mergeDiscussMessage([message(2)], message(1))[0]?.revision, 2);
    assert.equal(mergeDiscussMessage([message(2)], message(3))[0]?.state, "final");
  });

  it("keeps one canonical failed or cancelled answer when old streaming events arrive late", () => {
    for (const state of ["failed", "cancelled"] as const) {
      const terminal: DiscussMessage = { ...message(4), state, run_state: state };
      const merged = mergeDiscussMessage([message(2)], terminal);
      assert.equal(merged.length, 1);
      assert.equal(merged[0]?.state, state);
      const late = mergeDiscussMessage(merged, message(3, "Stale completed answer"));
      assert.equal(late.length, 1);
      assert.equal(late[0], terminal);
    }
  });
});
