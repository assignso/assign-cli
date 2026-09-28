import type { DiscussAvailability, DiscussMessage, DiscussPage, DiscussTurn } from "./types.js";
import { consumeDiscussStream, type DiscussStreamOptions } from "./stream.js";

export type DiscussEnvironment = {
  broker: string;
  session: string;
  workspaceId: string;
  workspaceName: string;
  workspaceSlug: string;
  webOrigin?: string;
};

export function readEnvironment(source: NodeJS.ProcessEnv = process.env): DiscussEnvironment {
  const values = {
    broker: source.ASSIGN_DISCUSS_BROKER,
    session: source.ASSIGN_DISCUSS_SESSION,
    workspaceId: source.ASSIGN_DISCUSS_WORKSPACE_ID,
    workspaceName: source.ASSIGN_DISCUSS_WORKSPACE_NAME,
    workspaceSlug: source.ASSIGN_DISCUSS_WORKSPACE_SLUG,
  };
  for (const [name, value] of Object.entries(values)) {
    if (!value) throw new Error(`Missing terminal Discuss ${name}`);
  }
  return { ...values, ...(source.ASSIGN_DISCUSS_WEB_ORIGIN ? { webOrigin: source.ASSIGN_DISCUSS_WEB_ORIGIN } : {}) } as DiscussEnvironment;
}

export class DiscussAPI {
  readonly #environment: DiscussEnvironment;
  constructor(environment: DiscussEnvironment) { this.#environment = environment; }

  async availability(signal?: AbortSignal): Promise<DiscussAvailability> {
    return this.#request<DiscussAvailability>("GET", "/availability", undefined, signal);
  }

  async list(signal?: AbortSignal): Promise<DiscussPage> {
    return this.#request<DiscussPage>("GET", "/messages?limit=100", undefined, signal);
  }

  async send(content: string, signal?: AbortSignal): Promise<DiscussTurn> {
    return this.#request<DiscussTurn>("POST", "/turns", { content, parts: [] }, signal);
  }

  async cancel(messageId: string, signal?: AbortSignal): Promise<DiscussMessage> {
    return this.#request<DiscussMessage>("POST", `/messages/${encodeURIComponent(messageId)}/cancel`, undefined, signal);
  }

  async decide(message: DiscussMessage, decision: string, signal?: AbortSignal): Promise<void> {
    const interaction = message.parts.find((part) => part.type === "interaction" && part.state === "open");
    if (!interaction || typeof interaction.id !== "string" || typeof interaction.revision !== "number") {
      throw new Error("The interaction is no longer open.");
    }
    await this.#request("POST", `/interactions/${encodeURIComponent(interaction.id)}/decision`, {
      expected_revision: interaction.revision,
      decision,
    }, signal);
  }

  async stream(options: Omit<DiscussStreamOptions, "signal">, signal: AbortSignal): Promise<{ cursor: string; retryAfterMs: number }> {
    const response = await fetch(`${this.#environment.broker}/api/v1/workspaces/${encodeURIComponent(this.#environment.workspaceId)}/discuss/events/stream?envelope_version=1&text_deltas=true`, {
      cache: "no-store", signal,
      headers: {
        "Accept": "text/event-stream",
        "X-Assign-Discuss-Session": this.#environment.session,
        ...(options.cursor ? { "Last-Event-ID": options.cursor } : {}),
      },
    });
    return consumeDiscussStream(response, { ...options, signal });
  }

  async #request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const response = await fetch(`${this.#environment.broker}/api/v1/workspaces/${encodeURIComponent(this.#environment.workspaceId)}/discuss${path}`, {
      method,
	  ...(signal ? { signal } : {}),
      headers: {
        "Accept": "application/json",
        "Content-Type": "application/json",
        "Idempotency-Key": crypto.randomUUID(),
        "X-Assign-Discuss-Session": this.#environment.session,
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const text = await response.text();
    if (!response.ok) {
      let message = `Discuss request failed (${response.status})`;
      try {
        const parsed = JSON.parse(text) as { message?: unknown };
        if (typeof parsed.message === "string") message = parsed.message;
      } catch { /* bounded server text is intentionally not displayed */ }
      throw new Error(message);
    }
    return (text ? JSON.parse(text) : undefined) as T;
  }
}
