import { CommandFailure } from "../main.ts";
import { decodeResponse, responseFailure } from "../transport.ts";
import {
  createWorkerTaskClaimsClient,
  type WorkerTaskClaimReceiptV1,
  type WorkerTaskClaimTransport,
} from "./index.ts";

export const workerUUID = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;
export const sameID = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();
export const exactPositiveDecimal = (value: unknown): value is string =>
  typeof value === "string" && /^[1-9][0-9]{0,18}$/.test(value) && BigInt(value) <= 9223372036854775807n;

export class WorkerTaskOperationFailure extends CommandFailure {
  constructor(readonly operationID: string, readonly uncertain: boolean, exitCode: number, readonly status?: number) {
    super(
      uncertain
        ? "Worker Task outcome is uncertain. Reconcile the original operation before continuing."
        : status === undefined ? "Invalid worker Task operation" : responseFailure(status).message,
      exitCode,
    );
  }
}

type OperationInput = {
  body: { task_id: string; delegation_id: string; run_id: string; operation_id: string };
};
type Request<Input extends OperationInput> = (input: Input, signal?: AbortSignal) => Promise<Response>;

async function readReceipt(
  response: Response,
  input: OperationInput,
  replacementRun?: string,
): Promise<WorkerTaskClaimReceiptV1> {
  const value = await decodeResponse(response, 16 * 1024);
  const row = value as Partial<WorkerTaskClaimReceiptV1> | null;
  const { body } = input;
  if (!row || typeof row !== "object" ||
      typeof row.task_id !== "string" || !sameID(row.task_id, body.task_id) ||
      typeof row.operation_id !== "string" || !sameID(row.operation_id, body.operation_id) ||
      typeof row.run_id !== "string" || !sameID(row.run_id, replacementRun ?? body.run_id) ||
      typeof row.delegation_id !== "string" || !workerUUID.test(row.delegation_id) ||
      (replacementRun ? sameID(row.delegation_id, body.delegation_id) : !sameID(row.delegation_id, body.delegation_id)) ||
      (row.state === "active" ? typeof row.token !== "string" || !workerUUID.test(row.token) : row.token !== undefined) ||
      !exactPositiveDecimal(row.generation) || !exactPositiveDecimal(row.revision) ||
      typeof row.expires_at !== "string" || !Number.isFinite(Date.parse(row.expires_at)) ||
      (row.state !== "active" && row.state !== "released") ||
      typeof row.current !== "boolean" || (row.current && row.state !== "active")) {
    throw new Error("Invalid worker Task receipt");
  }
  return {
    task_id: row.task_id, operation_id: row.operation_id, run_id: row.run_id,
    delegation_id: row.delegation_id, ...(row.token === undefined ? {} : { token: row.token }), generation: row.generation, revision: row.revision,
    expires_at: row.expires_at, state: row.state, current: row.current,
  };
}

function operation<Input extends OperationInput>(
  apply: Request<Input>,
  lookup: Request<Input>,
  replacement?: (input: Input) => string,
) {
  const execute = async (input: Input, reconcile: boolean, signal?: AbortSignal) => {
    // Callers persist this exact intent before invoking apply. Receipt lookup
    // may be used after a restart with the same persisted payload.
    const id = input.body.operation_id;
    if (!workerUUID.test(id) || id === "00000000-0000-0000-0000-000000000000") {
      throw new WorkerTaskOperationFailure(id, reconcile, 2);
    }

    let response: Response;
    try {
      response = await (reconcile ? lookup : apply)(input, signal);
    } catch {
      throw new WorkerTaskOperationFailure(id, true, signal?.aborted ? 130 : 1);
    }
    if (!response.ok) {
      const status = response.status;
      let conflict = false;
      if (!reconcile && status === 409) {
        try {
          const problem = await decodeResponse(new Response(response.body), 16 * 1024) as { reason?: string } | null;
          conflict = problem?.reason === "worker_claim_conflict";
        } catch {
          // A generic 409 is not a confirmed refusal by the claim owner.
        }
      } else {
        void response.body?.cancel().catch(() => {});
      }
      const uncertain = reconcile || status >= 500 || status === 408 || status === 429 || (status === 409 && !conflict);
      throw new WorkerTaskOperationFailure(id, uncertain, responseFailure(status).exitCode, status);
    }
    try {
      if (response.status !== 200) throw new Error("Unexpected worker Task response status");
      return await readReceipt(response, input, replacement?.(input));
    } catch {
      throw new WorkerTaskOperationFailure(id, true, signal?.aborted ? 130 : 1);
    }
  };
  return {
    apply: (input: Input, signal?: AbortSignal) => execute(input, false, signal),
    reconcile: (input: Input, signal?: AbortSignal) => execute(input, true, signal),
  };
}

/** Private CLI operation results. Keep receipts and tokens out of shell and Task output. */
export function createWorkerTaskOperations(transport: WorkerTaskClaimTransport) {
  const client = createWorkerTaskClaimsClient(transport);
  return {
    claim: operation(client.claimWorkerTask, client.readWorkerTaskClaimReceipt),
    renew: operation(client.renewWorkerTask, client.readWorkerTaskRenewReceipt),
    release: operation(client.releaseWorkerTask, client.readWorkerTaskReleaseReceipt),
    evidence: operation(client.recordWorkerTaskEvidence, client.readWorkerTaskEvidenceReceipt),
    finalize: operation(client.finalizeWorkerTask, client.readWorkerTaskFinalizeReceipt),
    recover: operation(client.recoverWorkerTask, client.readWorkerTaskRecoverReceipt, input => input.body.new_run_id),
    reconcile: operation(client.reconcileWorkerTask, client.readWorkerTaskReconcileReceipt),
    issueRunCredential: client.issueWorkerTaskRunCredential,
    readRunCredential: client.readWorkerTaskRunCredential,
  };
}
