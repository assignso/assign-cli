import { decodeJSONResponse } from "../transport.ts";

// Bun preserves the original number lexeme for these private int64 wire fields.
const exactJSON = JSON as unknown as {
  rawJSON(text: string): object;
  parse(text: string, reviver: (key: string, value: unknown, context: { source?: string }) => unknown): unknown;
};

function int64(value: bigint): bigint {
  if (value < -(2n ** 63n) || value >= 2n ** 63n) throw new RangeError("Worker integer exceeds signed 64-bit range");
  return value;
}

export function encodeWorkerJSON(value: unknown): string {
  return JSON.stringify(value, (_key, field: unknown) => {
    if (typeof field === "bigint") return exactJSON.rawJSON(int64(field).toString());
    if (typeof field === "number" && Number.isInteger(field) && !Number.isSafeInteger(field)) {
      throw new RangeError("Unsafe worker integer");
    }
    return field;
  });
}

export function decodeWorkerInt64Response<T>(response: Response, maxBytes: number, fields: readonly string[]): Promise<T> {
  return decodeJSONResponse(response, maxBytes, text => {
    const data = exactJSON.parse(text, (key, value, { source }) => {
      if (!fields.includes(key)) {
        if (typeof value === "number" && !Number.isSafeInteger(value)) throw new RangeError("Unsafe worker integer");
        return value;
      }
      if (typeof value !== "number" || !source || !/^-?\d+$/.test(source)) {
        throw new TypeError("Invalid worker int64");
      }
      return int64(BigInt(source));
    });
    if (!data || typeof data !== "object" || Array.isArray(data)) throw new TypeError("Invalid worker response");
    const record = data as Record<string, unknown>;
    if (fields.some(key => typeof record[key] !== "bigint")) throw new TypeError("Missing worker int64");
    return record as T;
  });
}
