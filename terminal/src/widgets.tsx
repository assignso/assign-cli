import React from "react";
import { Box, Text } from "ink";
import type { DiscussMessage, DiscussPart } from "./types.js";
import { safeTerminalText } from "./sanitize.js";

type Widget = { key: string; node: React.ReactNode };
export type WidgetContext = { readonly workspaceSlug: string; readonly webOrigin?: string };
type Renderer = (part: DiscussPart, message: DiscussMessage, index: number, context: WidgetContext) => Widget | null;

const text = (value: unknown, fallback = "") => safeTerminalText(typeof value === "string" ? value : fallback);
const integer = (value: unknown) => typeof value === "number" && Number.isSafeInteger(value) ? value : undefined;

function Frame({ title, children, color = "cyan" }: { title: string; children: React.ReactNode; color?: string }) {
  return <Box flexDirection="column" borderStyle="round" borderColor={color} paddingX={1} marginTop={1}>
    <Text bold color={color}>{title}</Text>
    {children}
  </Box>;
}

const webURL = (context: WidgetContext, path: string) => context.webOrigin && context.workspaceSlug ? `${context.webOrigin}/app/${encodeURIComponent(context.workspaceSlug)}${path}` : undefined;

const taskResult: Renderer = (part, _message, index, context) => {
  if (part.type !== "task_result" || typeof part.task_id !== "string" || typeof part.task_code !== "string" || typeof part.title !== "string" ||
    !integer(part.observed_revision) || !["read", "created", "updated"].includes(String(part.reason))) return null;
  const reason = part.reason === "created" ? "Created Task" : part.reason === "updated" ? "Updated Task" : "Current Task";
  const url = webURL(context, `/tasks/${encodeURIComponent(part.task_code)}`);
  return { key: `task:${part.task_code}:${index}`, node: <Frame title={`${text(part.task_code)} · ${text(part.title)}`}>
    <Text>{reason}{integer(part.observed_revision) ? ` · observed revision ${part.observed_revision}` : ""}</Text>
    {url ? <Text dimColor>Open: {url}</Text> : <Text dimColor>Find: assign search {part.task_code}</Text>}
  </Frame> };
};

const actionResult: Renderer = (part, _message, index) => {
  if (part.type !== "action_result" || part.status !== "completed") return null;
  const after = part.after_state && typeof part.after_state === "object" ? part.after_state as Record<string, unknown> : undefined;
  return { key: `action:${text(part.receipt_id, String(index))}`, node: <Frame title={`${text(part.task_code, "Task action")} completed`} color="green">
    {typeof after?.status_label === "string" ? <Text>Status: {after.status_label}</Text> : null}
    {part.reversible === true && typeof part.action_group_id === "string" ? <Text dimColor>Undo: /undo {part.action_group_id}</Text> : null}
    {typeof part.receipt_id === "string" ? <Text dimColor>Receipt {part.receipt_id.slice(0, 8)}</Text> : null}
  </Frame> };
};

const operationReceipts: Renderer = (part, _message, index, context) => {
  if (part.type !== "operation_receipts" || !Array.isArray(part.receipts)) return null;
  const labels: Record<string, string> = {
    task_create: "Task created",
    evidence_collection_create: "Evidence collection saved", evidence_collection_exclude: "Investigation exclusions saved",
    task_result_set_create: "Task results saved", task_selection_create: "Task selection saved",
    document_patch_create: "Document draft saved", change_set_create: "Changes proposed", change_set_apply: "Change outcome recorded",
  };
  const seen = new Set<string>();
  const items = part.receipts.flatMap((raw) => {
    if (!raw || typeof raw !== "object" || !("operation_id" in raw) || typeof raw.operation_id !== "string" ||
      !("tool" in raw) || typeof raw.tool !== "string" || !labels[raw.tool] || seen.has(raw.operation_id)) return [];
    seen.add(raw.operation_id);
    const state = "state" in raw && typeof raw.state === "string" ? ` · ${text(raw.state)}` : "";
    const resourceType = "resource_type" in raw && typeof raw.resource_type === "string" ? raw.resource_type : undefined;
    const resourceId = "resource_id" in raw && typeof raw.resource_id === "string" ? raw.resource_id : undefined;
    const revision = "revision" in raw && integer(raw.revision);
    const kind = resourceType === "change_set" ? "changes" : resourceType === "document_patch" ? "drafts" : resourceType === "evidence_collection" ? "evidence" : resourceType === "task_result_set" ? "tasks" : undefined;
    const url = kind && resourceId && revision ? webURL(context, `/discuss/work/${kind}/${encodeURIComponent(resourceId)}?version=${revision}`) : undefined;
    return [{ id: raw.operation_id, label: `${labels[raw.tool]}${state}`, url }];
  }).slice(0, 20);
  if (!items.length) return null;
  return { key: `receipts:${index}`, node: <Frame title="Work receipts">{items.map((item) => <React.Fragment key={item.id}><Text>✓ {item.label}</Text>{item.url ? <Text dimColor>  Open: {item.url}</Text> : null}</React.Fragment>)}</Frame> };
};

const interaction: Renderer = (part, _message, index) => {
  if (part.type !== "interaction" || (part.interaction_type !== "clarification" && part.interaction_type !== "approval")) return null;
  const options = Array.isArray(part.options) ? part.options.flatMap((raw) => raw && typeof raw === "object" && "label" in raw && typeof raw.label === "string" ? [{ label: raw.label, value: "value" in raw && typeof raw.value === "string" ? raw.value : raw.label }] : []) : [];
  const open = part.state === "open";
  return { key: `interaction:${text(part.id, String(index))}`, node: <Frame title={part.interaction_type === "approval" ? "Approval needed" : "Clarification needed"} color={open ? "yellow" : "gray"}>
    <Text>{text(part.prompt, "A response is required.")}</Text>
    {options.map((option, optionIndex) => <Text key={optionIndex}>{optionIndex + 1}. {option.label} <Text dimColor>({option.value})</Text></Text>)}
    <Text dimColor>{open ? "Type the answer in the composer and press Enter." : `Resolved${typeof part.decision === "string" ? `: ${part.decision}` : ""}`}</Text>
  </Frame> };
};

const evidence: Renderer = (part, _message, index) => {
  if (part.type !== "evidence_references" || !Array.isArray(part.references)) return null;
  const refs = part.references.slice(0, 20).flatMap((raw) => raw && typeof raw === "object" ? [raw as Record<string, unknown>] : []);
  if (!refs.length) return null;
  return { key: `evidence:${index}`, node: <Frame title="Code and test evidence">{refs.map((ref, refIndex) => {
    const kind = ref.kind === "verified_ci" ? "Verified CI" : ref.kind === "reported_test" ? "Reported test" : "Code";
    const location = typeof ref.path === "string" ? `${text(ref.path)}${integer(ref.line_start) ? `:${ref.line_start}` : ""}` : "";
    const summary = text(ref.summary, "Evidence reference");
    const commit = typeof ref.commit === "string" && ref.commit.length >= 7 ? ref.commit.slice(0, 7) : "";
    return <Text key={refIndex}>• {kind}: {summary}{location ? ` · ${location}` : ""}{commit ? ` · ${text(commit)}` : ""}</Text>;
  })}</Frame> };
};

const entityReferences: Renderer = (part, _message, index, context) => {
  if (part.type !== "entity_references" || !Array.isArray(part.references)) return null;
  const seen = new Set<string>();
  const references = part.references.flatMap((raw) => {
    if (!raw || typeof raw !== "object" || typeof raw.kind !== "string" || typeof raw.entity_id !== "string" || typeof raw.label !== "string" || typeof raw.locator !== "string") return [];
    const key = `${raw.kind}:${raw.entity_id}`;
    if (seen.has(key)) return [];
    seen.add(key);
    const path = raw.kind === "task" && /^[A-Za-z][A-Za-z0-9]*-\d+$/.test(raw.locator) ? `/tasks/${encodeURIComponent(raw.locator)}` : undefined;
    return [{ key, label: text(raw.label), locator: text(raw.locator), url: path ? webURL(context, path) : undefined }];
  }).slice(0, 20);
  if (!references.length) return null;
  return { key: `references:${index}`, node: <Frame title="Sources">{references.map((reference) => <React.Fragment key={reference.key}><Text>• {reference.locator} · {reference.label}</Text>{reference.url ? <Text dimColor>  Open: {reference.url}</Text> : null}</React.Fragment>)}</Frame> };
};

const toolActivities = (message: DiscussMessage): Widget | null => {
  const seen = new Set<string>();
  const activities = message.parts.flatMap((part) => {
    if (part.type !== "tool_activity" || typeof part.tool_call_id !== "string" || seen.has(part.tool_call_id) ||
      !["running", "completed", "failed", "cancelled"].includes(String(part.state))) return [];
    seen.add(part.tool_call_id);
    return [part];
  });
  if (!activities.length) return null;
  const title = activities.length > 1 ? `${activities.length}-step plan` : "Tool activity";
  return { key: "tool-plan", node: <Frame title={title}>{activities.slice(0, 20).map((activity) => <Text key={String(activity.tool_call_id)}>
    {activity.state === "completed" ? "✓" : activity.state === "failed" ? "✗" : activity.state === "cancelled" ? "−" : "◌"} {text(activity.summary, text(activity.label, text(activity.public_name, "Working")))} <Text dimColor>{text(activity.state)}</Text>
  </Text>)}</Frame> };
};

const responseOutcome: Renderer = (part, _message, index) => {
  if (part.type !== "response_outcome" || part.schema !== "assign.discuss.response_outcome.v1" || part.execution_state !== "partial") return null;
  const committed = Array.isArray(part.completed_action_ids) && part.completed_action_ids.length > 0;
  return {key: `outcome:${index}`, node: <Frame title={committed ? "Partially completed" : "Unfinished request"}>
    <Text>{committed ? "Review recorded results before requesting the remaining work. Committed changes remain in effect." : "Review any recorded results before requesting the remaining work."}</Text>
    {part.usage_state !== "settled" && part.usage_state !== "not_started" ? <Text>Check status before retrying; usage has not been confirmed.</Text> : null}
  </Frame>};
};
const renderers: readonly Renderer[] = [taskResult, actionResult, operationReceipts, responseOutcome, interaction, evidence, entityReferences];
const hiddenTypes = new Set(["runtime_text", "knowledge_answer", "entity_reference", "context_capsule", "tool_activity", "interaction_decision"]);

export function messageWidgets(message: DiscussMessage, context: WidgetContext = { workspaceSlug: "" }): readonly Widget[] {
  const widgets: Widget[] = [];
  const plan = toolActivities(message);
  if (plan) widgets.push(plan);
  message.parts.forEach((part, index) => {
    for (const renderer of renderers) {
      const rendered = renderer(part, message, index, context);
      if (rendered) { widgets.push(rendered); return; }
    }
    if (typeof part.type === "string" && !hiddenTypes.has(part.type)) {
      widgets.push({ key: `unsupported:${part.type}:${index}`, node: <Frame title="Additional result" color="gray"><Text>{text(part.type.replaceAll("_", " "), "Unsupported result")}</Text><Text dimColor>Open Discuss on the Web for the full interactive view.</Text></Frame> });
    }
  });
  return widgets;
}

export function openInteraction(messages: readonly DiscussMessage[]): DiscussMessage | undefined {
  return [...messages].reverse().find((message) => message.parts.some((part) => part.type === "interaction" && part.state === "open" && (part.interaction_type === "clarification" || part.interaction_type === "approval")));
}
