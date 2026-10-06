import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { DiscussMessage } from "./types.js";
import { messageWidgets, openInteraction } from "./widgets.js";
import { safeTerminalText } from "./sanitize.js";

const message = (parts: DiscussMessage["parts"]): DiscussMessage => ({ id: "m", sequence: 1, role: "assistant", kind: "result", state: "final", content: "Done", parts, correlation_id: "c", created_at: "2026-09-20T00:00:00Z", finalized_at: "2026-09-20T00:00:01Z" });

describe("terminal Discuss widgets", () => {
  it("registers Task, receipt, plan, interaction, and evidence widgets", () => {
    const value = message([
      { type: "task_result", task_id: "task", task_code: "ASG-42", title: "Ship CLI", observed_revision: 3, reason: "updated" },
      { type: "action_result", status: "completed", task_code: "ASG-42", receipt_id: "receipt", reversible: true, action_group_id: "group" },
      { type: "tool_activity", tool_call_id: "one", state: "completed", summary: "Read Task" },
      { type: "tool_activity", tool_call_id: "two", state: "running", summary: "Update Task" },
      { type: "interaction", id: "question", revision: 1, state: "open", interaction_type: "clarification", prompt: "Which Project?" },
      { type: "evidence_references", references: [{ kind: "verified_ci", summary: "CLI test passed", commit: "1234567890abcdef", record_id: "run", record_revision: "1" }] },
    ]);
    const widgets = messageWidgets(value, { workspaceSlug: "assign", webOrigin: "https://app.assign.so" });
    const keys = widgets.map((widget) => widget.key);
    for (const expected of ["tool-plan", "task:ASG-42:0", "action:receipt", "interaction:question", "evidence:5"]) assert.ok(keys.includes(expected));
    assert.equal(openInteraction([value]), value);
    assert.match(JSON.stringify(widgets), /https:\/\/app\.assign\.so\/app\/assign\/tasks\/ASG-42/);
  });

  it("uses a safe fallback for unknown parts without dumping payload data", () => {
    const widgets = messageWidgets(message([{ type: "future_private_widget\u001b", secret: "never render" }]));
    assert.equal(widgets.length, 1);
    assert.equal(widgets[0]?.key, "unsupported:future_private_widget\u001b:0");
    assert.equal(JSON.stringify(widgets[0]).includes("never render"), false);
  });

  it("neutralizes terminal and bidirectional control characters", () => {
    assert.equal(safeTerminalText("safe\u001b]8;;https://evil.invalid\u0007link\u202E"), "safe�]8;;https://evil.invalid�link�");
  });

  it("keeps a human wait and retires its decision controls after canonical invalidation", () => {
    const waiting = message([{ type: "interaction", id: "q", revision: 2, state: "open",
      interaction_type: "clarification", intent_id: "intent", intent_revision: 3, intent_state: "waiting" }]);
    assert.equal(openInteraction([waiting]), waiting);
    for (const state of ["stale", "expired", "resolved", "superseded"]) {
      const closed = { ...waiting, revision: 4, parts: [{ ...waiting.parts[0], state }] };
      assert.equal(openInteraction([closed]), undefined);
    }
  });
});

it("keeps the Core managed partial result and receipt visible", async () => {
  const prepared = await import("../../../assign-core/internal/discuss/testdata/kr08_managed_partial_response.json");
  const widgets = messageWidgets({...message(prepared.default.parts), state: "failed"}, { workspaceSlug: "assign", webOrigin: "https://app.assign.so" });
  assert.ok(widgets.some(widget => widget.key.startsWith("outcome:")));
  assert.ok(widgets.some(widget => widget.key.startsWith("receipts:")));
});
