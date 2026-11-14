import { CommandFailure } from "../main.ts";
import { decodeResponse } from "../transport.ts";
import { createWorkerTaskClaimsClient, type CLIWorkerReadyDelegationsV1, type WorkerTaskClaimReceiptV1, type WorkerTaskClaimTransport } from "./index.ts";
import { createWorkerTaskOperations, exactPositiveDecimal, sameID, workerUUID, WorkerTaskOperationFailure } from "./operations.ts";

type Client = ReturnType<typeof createWorkerTaskClaimsClient>;
type ClaimIntent = Readonly<Omit<Parameters<Client["claimWorkerTask"]>[0], "body"> & {
  body: Readonly<Parameters<Client["claimWorkerTask"]>[0]["body"]>;
}>;
type Connection = Omit<Parameters<Client["listCLIWorkerReadyDelegations"]>[0], "limit" | "cursor">;

type ClaimJournal = {
  // The existing supervisor owns durable persistence and process reconciliation.
  // Save must refuse to replace unreconciled state. Retain the original payload
  // for uncertain and committed claims until the supervisor resolves the run.
  save(intent: ClaimIntent): Promise<void>;
  refused(intent: ClaimIntent): Promise<void>;
};

type Selection =
  | { state: "claimed"; intent: ClaimIntent; receipt: WorkerTaskClaimReceiptV1 }
  | { state: "idle"; retryAfterMs: number }
  | { state: "reconciliation-required"; intent: ClaimIntent };

const pageSize = 25;
const maxPages = 4;
const passTimeoutMs = 15_000;
const malformed = () => new CommandFailure("Malformed worker ready feed", 1);
const id = (value: unknown): value is string => typeof value === "string" && workerUUID.test(value) &&
  value !== "00000000-0000-0000-0000-000000000000";

function readPage(raw: unknown, workerID: string): CLIWorkerReadyDelegationsV1 {
  const page = raw as Partial<CLIWorkerReadyDelegationsV1> | null;
  if (!page || !Array.isArray(page.data) || !Array.isArray(page.active_task_ids) ||
      page.data.length + page.active_task_ids.length > pageSize ||
      !page.active_task_ids.every(id) ||
      (page.next_cursor !== null && (typeof page.next_cursor !== "string" ||
        !page.next_cursor.length || page.next_cursor.length > 4096))) throw malformed();
  for (const item of page.data) {
    if (!item || item.state !== "queued" || !id(item.task_id) || !id(item.project_id) ||
        !id(item.id) || typeof item.assign_to_self !== "boolean" || !id(item.worker_id) || !sameID(item.worker_id, workerID) ||
        ![item.task_revision, item.saved_revision, item.applied_revision, item.revision].every(exactPositiveDecimal)) {
      throw malformed();
    }
  }
  return { data: page.data, active_task_ids: page.active_task_ids, next_cursor: page.next_cursor };
}

export function createExternalWorkerSelection(transport: WorkerTaskClaimTransport, journal: ClaimJournal) {
  const client = createWorkerTaskClaimsClient(transport);
  const { claim } = createWorkerTaskOperations(transport);

  function confirm(intent: ClaimIntent, receipt: WorkerTaskClaimReceiptV1, signal: AbortSignal): Selection {
    if (!signal.aborted && receipt.current && receipt.state === "active" && receipt.token &&
        BigInt(receipt.revision) > BigInt(intent.body.expected_revision) &&
        Date.parse(receipt.expires_at) > Date.now()) {
      return { state: "claimed", intent, receipt };
    }
    return { state: "reconciliation-required", intent };
  }

  async function reconcile(intent: ClaimIntent, signal?: AbortSignal): Promise<Selection> {
    const deadline = AbortSignal.timeout(passTimeoutMs);
    const bounded = signal ? AbortSignal.any([signal, deadline]) : deadline;
    try {
      return confirm(intent, await claim.reconcile(intent, bounded), bounded);
    } catch (error) {
      if (!(error instanceof WorkerTaskOperationFailure)) throw error;
      return { state: "reconciliation-required", intent };
    }
  }

  return {
    reconcile,
    async select(
      connection: Connection,
      report: (event: { taskID: string; state: "active" | "conflict" }) => void,
      previousIdlePasses: number,
      signal?: AbortSignal,
    ): Promise<Selection> {
      if (!exactPositiveDecimal(connection.epoch) || !id(connection.connection_intent) ||
          !Number.isSafeInteger(previousIdlePasses) || previousIdlePasses < 0) {
        throw new CommandFailure("Invalid worker selection input", 2);
      }
      const bound = Object.freeze({ ...connection });
      const { workspace_id, owner_id, device_id, worker_id } = bound;
      const deadline = AbortSignal.timeout(passTimeoutMs);
      const bounded = signal ? AbortSignal.any([signal, deadline]) : deadline;
      const cursors = new Set<string>();
      const observed = new Set<string>();
      let cursor: string | undefined;
      for (let index = 0; index < maxPages; index++) {
        bounded.throwIfAborted();
        const response = await client.listCLIWorkerReadyDelegations({ ...bound, limit: pageSize, cursor }, bounded);
        const page = readPage(await decodeResponse(response, 64 * 1024), worker_id);
        for (const taskID of page.active_task_ids) {
          const key = taskID.toLowerCase();
          if (observed.has(key)) continue;
          observed.add(key);
          report({ taskID, state: "active" });
        }
        for (const item of page.data) {
          const key = item.task_id.toLowerCase();
          if (observed.has(key)) continue;
          observed.add(key);
          bounded.throwIfAborted();
          const intent: ClaimIntent = Object.freeze({ workspace_id, owner_id, device_id, worker_id, body: Object.freeze({
            task_id: item.task_id, delegation_id: item.id, run_id: crypto.randomUUID(), operation_id: crypto.randomUUID(),
            expected_revision: item.task_revision, delegation_revision: item.revision,
            saved_revision: item.saved_revision, applied_revision: item.applied_revision,
            connection_epoch: bound.epoch, connection_intent: bound.connection_intent,
          }) });
          await journal.save(intent);
          try {
            return confirm(intent, await claim.apply(intent, bounded), bounded);
          } catch (error) {
            if (!(error instanceof WorkerTaskOperationFailure)) throw error;
            if (error.uncertain) return reconcile(intent, bounded);
            await journal.refused(intent);
            if (error.status !== 409) throw error;
            report({ taskID: item.task_id, state: "conflict" });
          }
        }
        if (page.next_cursor === null) break;
        if (cursors.has(page.next_cursor)) throw malformed();
        cursors.add(page.next_cursor);
        cursor = page.next_cursor;
      }
      bounded.throwIfAborted();
      return { state: "idle", retryAfterMs: Math.min(30_000, 1000 * 2 ** Math.min(previousIdlePasses, 5)) };
    },
  };
}
