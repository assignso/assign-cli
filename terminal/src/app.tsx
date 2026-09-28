import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Box, Text, useApp, useInput } from "ink";
import { useExternalStoreRuntime } from "@assistant-ui/core/react";
import { AssistantRuntimeProvider, AuiIf, ComposerPrimitive, LoadingPrimitive, ThreadPrimitive, useAuiState, type AppendMessage, type ThreadMessageLike } from "@assistant-ui/react-ink";
import { MarkdownText } from "@assistant-ui/react-ink-markdown";
import { DiscussAPI, type DiscussEnvironment } from "./api.js";
import type { DiscussMessage } from "./types.js";
import { messageWidgets, openInteraction } from "./widgets.js";
import { safeTerminalText } from "./sanitize.js";
import { applyDiscussText, mergeDiscussMessage, type ProvisionalText } from "./stream.js";

const messageText = (message: AppendMessage) => message.content.flatMap((part) => part.type === "text" ? [part.text] : []).join("\n").trim();

function Message({ byId, provisional, workspaceSlug, webOrigin }: { byId: ReadonlyMap<string, DiscussMessage>; provisional: ReadonlyMap<string, ProvisionalText>; workspaceSlug: string; webOrigin?: string }) {
  const message = useAuiState((state) => state.message);
  const source = message.id ? byId.get(message.id) : undefined;
  const streamed = message.id ? provisional.get(message.id)?.value : undefined;
  const value = safeTerminalText(streamed ?? message.content.flatMap((part) => part.type === "text" ? [part.text] : []).join(""));
  const user = message.role === "user";
  return <Box flexDirection="column" marginBottom={1}>
    <Text bold color={user ? "green" : "cyan"}>{user ? "You" : message.role === "assistant" ? "Discuss" : "Agent"}</Text>
    {user ? <Text wrap="wrap">{value}</Text> : <MarkdownText text={value || (source?.state === "pending" ? "Working…" : "")} />}
    {source ? messageWidgets(source, { workspaceSlug, ...(webOrigin ? { webOrigin } : {}) }).map((widget) => <React.Fragment key={widget.key}>{widget.node}</React.Fragment>) : null}
  </Box>;
}

function Thread({ byId, provisional, error, writable, workspaceSlug, webOrigin }: { byId: ReadonlyMap<string, DiscussMessage>; provisional: ReadonlyMap<string, ProvisionalText>; error: string; writable: boolean; workspaceSlug: string; webOrigin?: string }) {
  return <ThreadPrimitive.Root>
    <AuiIf condition={(state) => state.thread.isEmpty}><Text dimColor>No messages yet. Ask Discuss about your Workspace.</Text></AuiIf>
    <ThreadPrimitive.Messages>{() => <Message byId={byId} provisional={provisional} workspaceSlug={workspaceSlug} {...(webOrigin ? { webOrigin } : {})} />}</ThreadPrimitive.Messages>
    <LoadingPrimitive.Root gap={1}><LoadingPrimitive.Spinner variant="dots" /><LoadingPrimitive.Text /><LoadingPrimitive.ElapsedTime /></LoadingPrimitive.Root>
    {error ? <Text color="red">{error}</Text> : null}
    {writable ? <><Box borderStyle="round" borderColor="gray" paddingX={1} marginTop={1}>
      <Text color="gray">{"> "}</Text><ComposerPrimitive.Input submitOnEnter placeholder="Ask Discuss…" autoFocus />
      <AuiIf condition={(state) => state.thread.isRunning}><ComposerPrimitive.Cancel marginLeft={1}>{({ isFocused }) => <Text bold={isFocused} color="yellow">Stop</Text>}</ComposerPrimitive.Cancel></AuiIf>
    </Box><Text dimColor>Enter send · Stop cancels active work · Esc or Ctrl+C exit</Text></> : <Text dimColor>Read-only history · Esc or Ctrl+C exit</Text>}
  </ThreadPrimitive.Root>;
}

export function App({ environment }: { environment: DiscussEnvironment }) {
  const api = useMemo(() => new DiscussAPI(environment), [environment]);
  const [messages, setMessages] = useState<readonly DiscussMessage[]>([]);
  const [provisional, setProvisional] = useState<ReadonlyMap<string, ProvisionalText>>(new Map());
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [access, setAccess] = useState<"checking" | "full" | "read_only" | "preview">("checking");
  const cursor = useRef<string | undefined>(undefined);
  const { exit } = useApp();
  useInput((_input, key) => { if (key.escape) exit(); });

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const page = await api.list(signal);
    setMessages(page.messages);
    setProvisional((current) => new Map([...current].filter(([id]) => page.messages.some((message) => message.id === id && message.state === "pending"))));
  }, [api]);

  useEffect(() => {
    const controller = new AbortController();
    let stopped = false;
    const wait = (milliseconds: number) => new Promise<void>((resolve) => {
      const timer = setTimeout(resolve, milliseconds);
      controller.signal.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, { once: true });
    });
    const run = async () => {
      const availability = await api.availability(controller.signal);
      setAccess(availability.access);
      if (availability.access === "preview") {
        setError(`Discuss is included with ${availability.minimum_plan_label} and higher plans.`);
        return;
      }
      let failures = 0;
      while (!stopped) {
        try {
          const result = await api.stream({
            ...(cursor.current ? { cursor: cursor.current } : {}),
            onReady: async (value) => { cursor.current = value; await refresh(controller.signal); setError(""); },
            onMessage: (message, value) => {
              cursor.current = value;
              setMessages((current) => mergeDiscussMessage(current, message));
              if (message.state !== "pending") setProvisional((current) => { const next = new Map(current); next.delete(message.id); return next; });
            },
            onText: (frame) => setProvisional((current) => {
              const nextValue = applyDiscussText(current.get(frame.messageId), frame);
              if (!nextValue) {
                void refresh(controller.signal).catch(() => undefined);
                return current;
              }
              const next = new Map(current); next.set(frame.messageId, nextValue); return next;
            }),
            onResync: (value) => { cursor.current = value; setProvisional(new Map()); },
          }, controller.signal);
          cursor.current = result.cursor;
          failures = 0;
          await wait(Math.max(250, result.retryAfterMs));
        } catch (reason) {
          if (controller.signal.aborted) return;
          failures += 1;
          setError(reason instanceof Error ? `${reason.message}; reconnecting…` : "Discuss disconnected; reconnecting…");
          setProvisional(new Map());
          await refresh(controller.signal).catch(() => undefined);
          await wait(Math.min(10_000, 500 * 2 ** Math.min(failures - 1, 4)));
        }
      }
    };
    void run().catch((reason: unknown) => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : "Could not load Discuss."); });
    return () => { stopped = true; controller.abort(); setProvisional(new Map()); };
  }, [api, refresh]);

  const activeMessage = useMemo(() => [...messages].reverse().find((message) => message.role !== "user" &&
    (message.state === "pending" || message.run_state === "queued" || message.run_state === "running" || message.run_state === "cancel_requested")), [messages]);
  const running = submitting || Boolean(activeMessage);

  const onNew = useCallback(async (message: AppendMessage) => {
    const input = messageText(message);
    if (!input || running || access !== "full") return;
    setSubmitting(true); setError("");
    try {
      const waiting = openInteraction(messages);
      if (waiting) await api.decide(waiting, input);
      else {
        const turn = await api.send(input.startsWith("/undo ") ? `Undo action ${input.slice(6).trim()}` : input);
        setMessages((current) => mergeDiscussMessage(mergeDiscussMessage(current, turn.user), turn.response));
      }
      await refresh();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Discuss request failed.");
    } finally {
      setSubmitting(false);
    }
  }, [access, api, messages, refresh, running]);

  const onCancel = useCallback(async () => {
    if (!activeMessage) return;
    setError("");
    try {
      await api.cancel(activeMessage.id);
      await refresh();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Discuss could not stop the active response.");
    }
  }, [activeMessage, api, refresh]);

  const runtimeMessages = useMemo(() => messages.map((message): ThreadMessageLike => ({
    id: message.id,
    role: message.role === "user" ? "user" : "assistant",
    content: [{ type: "text", text: safeTerminalText(message.content) }],
    createdAt: new Date(message.created_at),
  })), [messages]);
  const runtime = useExternalStoreRuntime({ messages: runtimeMessages, convertMessage: (message) => message, isRunning: running, onNew, onCancel });
  const byId = useMemo(() => new Map(messages.map((message) => [message.id, message])), [messages]);

  return <AssistantRuntimeProvider runtime={runtime}>
    <Box flexDirection="column" paddingX={1}>
      <Text bold color="cyan">Assign Discuss · {environment.workspaceName}</Text>
      <Text dimColor>Hosted Workspace conversation · canonical history stays in Assign</Text>
      <Box flexDirection="column" marginTop={1}><Thread byId={byId} provisional={provisional} error={error} writable={access === "full"} workspaceSlug={environment.workspaceSlug} {...(environment.webOrigin ? { webOrigin: environment.webOrigin } : {})} /></Box>
    </Box>
  </AssistantRuntimeProvider>;
}
