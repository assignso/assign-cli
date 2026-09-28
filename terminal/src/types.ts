export type DiscussPart = Readonly<Record<string, unknown>> & { readonly type?: unknown };

export type DiscussMessage = {
  readonly id: string;
  readonly revision?: number;
  readonly sequence: number;
  readonly role: "user" | "assistant" | "agent" | "system";
  readonly kind: string;
  readonly state: "pending" | "final" | "failed" | "cancelled";
  readonly run_state?: "queued" | "running" | "cancel_requested" | "completed" | "failed" | "cancelled";
  readonly content: string;
  readonly parts: readonly DiscussPart[];
  readonly correlation_id: string;
  readonly created_at: string;
  readonly finalized_at: string | null;
};

export type DiscussPage = {
  readonly event_cursor?: number;
  readonly stream_id: string;
  readonly messages: readonly DiscussMessage[];
  readonly next_before: number | null;
  readonly unread_count: number;
  readonly last_sequence: number;
  readonly last_read_sequence: number;
};

export type DiscussTurn = {
  readonly stream_id: string;
  readonly user: DiscussMessage;
  readonly response: DiscussMessage;
  readonly replayed: boolean;
};

export type DiscussAvailability = {
  readonly feature: "discuss";
  readonly access: "full" | "read_only" | "preview";
  readonly reason: string;
  readonly history_present: boolean;
  readonly minimum_plan_label: string;
};
