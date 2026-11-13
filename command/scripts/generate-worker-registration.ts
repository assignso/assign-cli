import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";

const root = new URL("../../../", import.meta.url);
const target = process.argv[3] ?? "worker-registration";
if (!["worker-registration", "worker-task-claims"].includes(target)) throw new Error("Unknown worker projection target");
const claims = target === "worker-task-claims";
const projectionPath = new URL(`../src/${target}/contract.json`, import.meta.url);
const bindingPath = new URL(`../src/${target}/index.ts`, import.meta.url);
const registrationOperations = [
  "registerCLIWorker",
  "listCLIWorkers",
  "showCLIWorker",
  "getCLIWorkerRegistrationReceipt",
  "readCLIWorkerPolicy",
  "updateCLIWorkerPolicy",
];

const readyOperation = "listCLIWorkerReadyDelegations";
const claimCredentials = {
  claimWorkerTask: "workerDeviceToken",
  readWorkerTaskClaimReceipt: "workerDeviceToken",
  renewWorkerTask: "workerDeviceToken",
  readWorkerTaskRenewReceipt: "workerDeviceToken",
  releaseWorkerTask: "workerRunToken",
  recordWorkerTaskEvidence: "workerRunToken",
  readWorkerTaskEvidenceReceipt: "workerDeviceToken",
  readWorkerTaskReleaseReceipt: "workerDeviceToken",
  finalizeWorkerTask: "workerRunToken",
  readWorkerTaskFinalizeReceipt: "workerDeviceToken",
  recoverWorkerTask: "developerAccessToken",
  readWorkerTaskRecoverReceipt: "developerAccessToken",
  reconcileWorkerTask: "developerAccessToken",
  readWorkerTaskReconcileReceipt: "developerAccessToken",
  issueWorkerTaskRunCredential: "workerDeviceToken",
  readWorkerTaskRunCredential: "workerDeviceToken",
} as const;
const operations = claims ? [...Object.keys(claimCredentials), readyOperation] : registrationOperations;
const authorities = {
  developerAccessToken: "cli_oauth_grant",
  workerDeviceToken: "registered_worker_device",
  workerRunToken: "registered_worker_run",
} as const;
type Credential = keyof typeof authorities;

type Schema = {
  $ref?: string;
  type?: string | string[];
  format?: string;
  const?: string | number | boolean;
  enum?: (string | number)[];
  oneOf?: Schema[];
  allOf?: Schema[];
  anyOf?: Schema[];
  not?: Schema;
  properties?: Record<string, Schema>;
  required?: string[];
  additionalProperties?: boolean | Schema;
  items?: Schema;
  minimum?: number;
  maximum?: number;
};
type Parameter = { name: string; in: "query" | "path"; required?: boolean; schema: Schema };
type Content = { content: Record<string, { schema: Schema }> };
type Operation = {
  operationId: string;
  security: Record<string, string[]>[];
  "x-assign-authority": { credentials: string[]; required_scope: string; client_selects_owner: boolean };
  parameters?: Parameter[];
  requestBody?: Content & { "x-assign-max-bytes": number };
  responses: Record<string, Content | { $ref: string }>;
};
type Components = Record<string, Record<string, unknown>>;
type Fragment = { paths?: Record<string, Record<string, Operation>>; components?: Components };
type Projection = {
  audience: "private-cli";
  sources: Record<string, string>;
  bearerPrefix: string;
  machineBearerPrefixes?: Record<"workerDeviceToken" | "workerRunToken", string>;
  paths: NonNullable<Fragment["paths"]>;
  components: Components;
};

const sha = (content: string) => createHash("sha256").update(content).digest("hex");
const json = (value: unknown) => JSON.stringify(value, null, 2) + "\n";
function rows(paths: Projection["paths"]) {
  return Object.entries(paths).flatMap(([path, methods]) =>
    Object.entries(methods).map(([method, operation]) => ({ path, method, operation })));
}
function verifyOperations(paths: Projection["paths"]) {
  const selected = rows(paths).map(({ operation }) => operation.operationId).sort();
  if (json(selected) !== json([...operations].sort())) throw new Error("Worker operation allowlist differs");
  for (const { path, operation, method } of rows(paths)) {
    if (claims && operation.operationId === readyOperation) {
      if (path !== "/api/v1/cli/worker-devices/{workspace_id}/{owner_id}/{device_id}/{worker_id}/ready-delegations" ||
          method !== "get" || operation.requestBody ||
          json(operation.security) !== json([{ workerDeviceBearerV1: [] }]) ||
          json(operation["x-assign-authority"].credentials) !== json(["worker_device_credential"]) ||
          operation["x-assign-authority"].client_selects_owner !== false) {
        throw new Error("Worker ready feed authority differs");
      }
      continue;
    }
    if (claims) {
      const family = claimCredentials[operation.operationId as keyof typeof claimCredentials];
      const prefix = "/api/v1/cli/worker-devices/{workspace_id}/{owner_id}/{device_id}/{worker_id}/task-claims/";
      if (!path.startsWith(prefix) || method !== "post" || !family ||
          json(operation.security) !== json([{ [family]: [] }]) ||
          json(operation["x-assign-authority"].credentials) !== json([authorities[family]]) ||
          operation["x-assign-authority"].client_selects_owner !== false ||
          operation.requestBody?.["x-assign-max-bytes"] !== 16 * 1024) {
        throw new Error(`Worker claim authority or limit differs for ${operation.operationId}`);
      }
      continue;
    }
    if (!/^\/api\/v1\/cli\/workers(?:\/[-\w{}]+)*$/.test(path) || !["get", "post", "patch"].includes(method)) {
      throw new Error(`Unexpected CLI worker route ${method} ${path}`);
    }
    const authority = operation["x-assign-authority"];
    if (json(operation.security) !== json([{ developerAccessToken: [] }]) ||
        json(authority.credentials) !== json(["cli_oauth_grant"]) || authority.client_selects_owner !== false ||
        authority.required_scope !== (method === "get" ? "assign:read" : "assign:write")) {
      throw new Error(`Worker authority differs for ${operation.operationId}`);
    }
  }
}

function project(): Projection {
  const sources: Record<string, string> = {};
  const read = (path: string) => {
    const input = readFileSync(new URL(path, root), "utf8");
    sources[path] = sha(input);
    return input;
  };
  const source = JSON.parse(read(`assign-core/contracts/api/src/paths/${claims ? target : "worker-catalog"}.v1.json`)) as Fragment;
  const core = read("assign-core/internal/cliauth/service.go");
  const bearerPrefix = /^\s*accessPrefix\s*=\s*"([a-z_]+)"/m.exec(core)?.[1];
  if (!bearerPrefix) throw new Error("Missing CLI access-token family");
  const catalog = read(`assign-core/internal/httpapi/${claims ? "worker_task_claims" : "worker_catalog"}.go`);
  const machine = claims ? read("assign-core/internal/mcpauth/worker_credentials.go") : "";
  const devicePrefix = /^\s*workerDevicePrefix\s*=\s*"([a-z_]+)"/m.exec(machine)?.[1];
  const runPrefix = /^\s*workerRunPrefix\s*=\s*"([a-z_]+)"/m.exec(machine)?.[1];
  if (claims && (!devicePrefix || !runPrefix)) throw new Error("Missing Core worker credential family");

  const paths: Projection["paths"] = {};
  const readySource = claims ? JSON.parse(read("assign-core/contracts/api/src/paths/worker-catalog.v1.json")) as Fragment : undefined;
  if (claims && !read("assign-core/internal/httpapi/worker_connections.go").includes('r.Get(workerDevicePath+"/ready-delegations", routes.ready)')) {
    throw new Error("Missing Core ready feed route");
  }
  for (const { path, method, operation } of rows({ ...source.paths, ...readySource?.paths })) {
    if (!operations.includes(operation.operationId)) continue;
    if (claims && operation.operationId !== readyOperation) {
      const action = path.split("/").at(-1);
      if (!(catalog.includes(`"${action}"`) || catalog.includes(`"/${action}"`)) || !catalog.includes("decodeJSONBody(r, 16*1024, true,")) {
        throw new Error(`Missing Core claim route or body limit for ${operation.operationId}`);
      }
    } else if (operation.requestBody) {
      const handler = { registerCLIWorker: "register", updateCLIWorkerPolicy: "updatePolicy" }[operation.operationId];
      const body = catalog.split(`func (routes *WorkerCatalogRoutes) ${handler}(`)[1]?.split("\nfunc ")[0];
      const cap = /decodeJSONBody\(r, (\d+)\*(\d+), true, /.exec(body ?? "");
      if (!cap) throw new Error(`Missing canonical JSON limit for ${operation.operationId}`);
      operation.requestBody["x-assign-max-bytes"] = Number(cap[1]) * Number(cap[2]);
    }
    (paths[path] ??= {})[method] = operation;
  }
  verifyOperations(paths);

  const pool = structuredClone(source.components ?? {});
  if (readySource) Object.assign(pool.schemas ??= {}, readySource.components?.schemas);
  for (const path of [
    "assign-core/contracts/api/src/components/shared.yaml",
    ...["comments", "contract", "errors", "problems", "shared"].map(name => `openapi-spec/src/components/${name}.yaml`),
  ]) {
    const fragment = Bun.YAML.parse(read(path)) as Fragment;
    for (const [section, values] of Object.entries(fragment.components ?? {})) {
      const target = pool[section] ??= {};
      for (const [key, value] of Object.entries(values)) {
        if (Object.hasOwn(target, key)) throw new Error(`Ambiguous component ${section}/${key}`);
        target[key] = value;
      }
    }
  }
  const components: Components = {};
  const include = (section: string, key: string) => {
    const target = components[section] ??= {};
    if (Object.hasOwn(target, key)) return;
    const value = pool[section]?.[key];
    if (!value) throw new Error(`Missing component ${section}/${key}`);
    target[key] = value;
    visit(value);
  };
  const visit = (node: unknown) => {
    if (Array.isArray(node)) {
      node.forEach(visit);
    } else if (node && typeof node === "object") {
      for (const [key, value] of Object.entries(node)) {
        if (key === "$ref") {
          const match = /^#\/components\/(\w+)\/(\w+)$/.exec(value);
          if (!match?.[1] || !match[2]) throw new Error(`Unsupported worker reference ${value}`);
          include(match[1], match[2]);
        } else {
          visit(value);
        }
      }
    }
  };
  visit(paths);
  const scheme = pool.securitySchemes?.developerAccessToken as { type?: string; scheme?: string };
  if (scheme?.type !== "http" || scheme.scheme !== "bearer") throw new Error("Missing Core bearer security scheme");
  components.securitySchemes = {
    developerAccessToken: { type: scheme.type, scheme: scheme.scheme, bearerFormat: "Assign CLI OAuth access token" },
    ...(claims ? source.components?.securitySchemes : {}),
  };
  return {
    audience: "private-cli", sources, bearerPrefix,
    ...(devicePrefix && runPrefix ? { machineBearerPrefixes: { workerDeviceToken: devicePrefix, workerRunToken: runPrefix } } : {}),
    paths, components,
  };
}

function schemaType(schema: Schema): string {
  if (schema.$ref) {
    if (!/^#\/components\/schemas\/\w+$/.test(schema.$ref)) throw new Error(`Unsupported schema ${schema.$ref}`);
    return schema.$ref.split("/").at(-1)!;
  }
  if (schema.const !== undefined) return JSON.stringify(schema.const);
  if (schema.enum) return schema.enum.map(value => JSON.stringify(value)).join(" | ");
  if (schema.allOf) return schema.allOf.map(part => `(${schemaType(part)})`).join(" & ");
  if (Array.isArray(schema.type)) return schema.type.map(type => schemaType({ ...schema, type })).join(" | ");
  if (schema.properties) {
    const properties = schema.properties;
    const fields = Object.entries(properties).map(([key, value]) =>
      `${key}${schema.required?.includes(key) ? "" : "?"}: ${schemaType(value)};`);
    if (schema.additionalProperties === true) fields.push("[key: string]: unknown;");
    const base = `{\n  ${fields.join("\n  ")}\n}`;
    if (!schema.oneOf) return base;

    // This fragment uses presence constraints to separate profile edits from control actions.
    const branches = schema.oneOf.flatMap(branch => {
      const required = branch.anyOf?.map(option => option.required ?? []) ?? [branch.required ?? []];
      const forbidden = branch.not?.anyOf?.flatMap(option => option.required ?? []) ?? branch.not?.required ?? [];
      return required.map(keys => `{ ${[
        ...keys.map(key => `${key}: ${schemaType(properties[key]!)};`),
        ...forbidden.map(key => `${key}?: never;`),
      ].join(" ")} }`);
    });
    if (branches.some(branch => branch === "{  }")) throw new Error("Unsupported object union");
    return `${base} & (${branches.join(" | ")})`;
  }
  if (schema.oneOf) return schema.oneOf.map(part => `(${schemaType(part)})`).join(" | ");
  switch (schema.type) {
    case "array": return `Array<${schemaType(schema.items!)}>`;
    case "object": return `Record<string, ${schema.additionalProperties && schema.additionalProperties !== true
      ? schemaType(schema.additionalProperties) : "unknown"}>`;
    case "integer": return "number";
    case "string": case "boolean": case "number": case "null": return schema.type;
    default: throw new Error(`Unsupported schema ${JSON.stringify(schema)}`);
  }
}

function generate(projection: Projection): string {
  const credentials = claims ? Object.keys(authorities) : ["developerAccessToken"];
  if (projection.audience !== "private-cli" ||
      json(Object.keys(projection.components.securitySchemes ?? {})) !== json(credentials) ||
      (claims && !projection.machineBearerPrefixes)) {
    throw new Error("Private CLI projection required");
  }
  verifyOperations(projection.paths);
  const name = claims ? "WorkerTaskClaims" : "WorkerRegistration";
  const requestModule = claims ? "../worker-registration/request.ts" : "./request.ts";
  const lines = [
    "// Generated by command/scripts/generate-worker-registration.ts. Do not edit.",
    `import { createWorkerRequest } from "${requestModule}";`,
    `import type { WorkerTransport } from "${requestModule}";`,
    "",
    `export const ${claims ? "workerTaskClaimsIdentity" : "workerRegistrationIdentity"} = {`,
    `  audience: "private-cli",`,
    `  projectionSHA256: "${sha(json(projection))}",`,
    `  generatorSHA256: "${sha(readFileSync(new URL(import.meta.url), "utf8"))}",`,
    "} as const;",
    "",
  ];
  const schemas = projection.components.schemas as Record<string, Schema>;
  for (const [name, schema] of Object.entries(schemas)) lines.push(`export type ${name} = ${schemaType(schema)};`);
  lines.push("", `export interface ${name}Success {`);
  for (const { operation } of rows(projection.paths)) {
    const response = operation.responses["200"];
    if (!response || !("content" in response)) throw new Error("Missing JSON success response");
    lines.push(`  ${operation.operationId}: ${schemaType(response.content["application/json"]!.schema)};`);
  }
  lines.push("}", "", `export interface ${name}Problems {`);
  for (const { operation } of rows(projection.paths)) {
    lines.push(`  ${operation.operationId}: {`);
    for (const [status, entry] of Object.entries(operation.responses)) {
      if (status === "200") continue;
      const response = "$ref" in entry ? projection.components.responses?.[entry.$ref.split("/").at(-1)!] as Content : entry;
      const schema = response?.content["application/problem+json"]?.schema;
      if (!schema) throw new Error(`Missing problem schema for ${operation.operationId} (${status})`);
      lines.push(`    ${status}: ${schemaType(schema)};`);
    }
    lines.push("  };");
  }
  lines.push("}", "");
  if (claims) {
    lines.push("export type WorkerTaskClaimTransport = Omit<WorkerTransport, \"accessToken\"> & {",
      ...credentials.map(key => `  ${key}?: WorkerTransport["accessToken"];`), "};", "",
      "export function createWorkerTaskClaimsClient(transport: WorkerTaskClaimTransport) {");
    const prefixes = { developerAccessToken: projection.bearerPrefix, ...projection.machineBearerPrefixes };
    for (const [key, prefix] of Object.entries(prefixes)) {
      lines.push(`  const ${key} = createWorkerRequest({ ...transport, accessToken: async () => {`,
        `    if (!transport.${key}) throw new TypeError("Missing worker credential for this operation");`,
        `    return transport.${key}();`, `  } }, ${JSON.stringify(prefix)}, "the bound credential for this operation");`);
    }
  } else {
    lines.push("export function createWorkerRegistrationClient(transport: WorkerTransport) {",
      `  const request = createWorkerRequest(transport, ${JSON.stringify(projection.bearerPrefix)});`);
  }
  lines.push("  return {");
  for (const { path, method, operation } of rows(projection.paths)) {
    const parameters = operation.parameters ?? [];
    const fields = parameters.map(parameter =>
      `${parameter.name}${parameter.required ? "" : "?"}: ${schemaType(parameter.schema)}`);
    const body = operation.requestBody;
    if (body) fields.push(`body: ${schemaType(body.content["application/json"]!.schema)}`);
    const optionalInput = !body && !parameters.some(parameter => parameter.required);
    const args = fields.length ? `input: { ${fields.join("; ")} }${optionalInput ? " = {}" : ""}, ` : "";
    const url = path.replace(/\{(\w+)\}/g, (_, key: string) => {
      const parameter = parameters.find(p => p.name === key && p.in === "path");
      if (parameter?.schema.format !== "uuid" || !parameter.required) throw new Error(`Expected required UUID path parameter ${key}`);
      return "${pathSegment(input." + key + ")}";
    });
    lines.push(`    ${operation.operationId}(${args}signal?: AbortSignal) {`);
    for (const { name, in: location, schema } of parameters) {
      if (location === "query" && schema.type === "integer") {
        if (schema.minimum === undefined || schema.maximum === undefined) throw new Error("Missing query bounds");
        lines.push(`      if (input.${name} !== undefined && (!Number.isInteger(input.${name}) ||`,
          `          input.${name} < ${schema.minimum} || input.${name} > ${schema.maximum})) throw new RangeError("Invalid worker ${name}");`);
      }
    }
    const query = parameters.filter(parameter => parameter.in === "query");
    if (query.length) {
      lines.push("      const query = new URLSearchParams();");
      for (const { name } of query) {
        lines.push(`      if (input.${name} !== undefined) query.set("${name}", String(input.${name}));`);
      }
    }
    if (body && !Number.isSafeInteger(body["x-assign-max-bytes"])) throw new Error("Missing canonical body limit");
    const request = operation.operationId === readyOperation ? "workerDeviceToken" :
      claims ? Object.keys(operation.security[0]!)[0] as Credential : "request";
    lines.push(`      return ${request}("${method.toUpperCase()}", \`${url}\`, signal${query.length ? ", { query }" :
      body ? `, { body: input.body, maxBytes: ${body["x-assign-max-bytes"]} }` : ""});`, "    },");
  }
  lines.push("  };", "}", "", "function pathSegment(value: string): string {",
    '  if (!/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(value)) throw new TypeError("Invalid worker identifier");',
    "  return value;", "}", "");
  return lines.join("\n");
}

const mode = process.argv[2];
if (!["project", "generate", "check", "check-source"].includes(mode ?? "")) {
  throw new Error("Use project, generate, check, or check-source");
}
if (mode === "project" || mode === "check-source") {
  const expected = json(project());
  if (mode === "project") writeFileSync(projectionPath, expected);
  else if (readFileSync(projectionPath, "utf8") !== expected) throw new Error("Worker projection differs from Core source");
} else {
  const projection = JSON.parse(readFileSync(projectionPath, "utf8")) as Projection;
  const expected = generate(projection);
  if (mode === "generate") writeFileSync(bindingPath, expected);
  else if (readFileSync(bindingPath, "utf8") !== expected) throw new Error("Worker binding needs regeneration");
}
