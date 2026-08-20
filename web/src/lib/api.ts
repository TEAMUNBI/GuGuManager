import { MockClient } from "./mock";
import type { AgentEnrollmentToken, Allocation, APIToken, APITokenCredential, AuditEvent, Backup, ConsoleConnectionCredential, ConsoleLine, CreateAllocationInput, CreateServerInput, CreateUserInput, FileContent, FileEntry, GameDefinition, IssueAgentEnrollmentTokenInput, Node, Operation, Overview, PasswordResetToken, Server, ServerMembership, ServerPermission, ServerPermissions, Session, SetupAdminInput, SetupStatus, Startup, StartupValue, UpdateUserInput, User } from "./types";
import { getActiveLocale, type Locale } from "../i18n/I18n";

const DEV_PROXY_ERROR_HEADER = "X-GuGuManager-Proxy-Error";
const DEV_PROXY_UPSTREAM_UNAVAILABLE = "upstream-unavailable";

const apiFallbackCopy: Record<Locale, {
  invalidResponse: (status: number) => string;
  requestFailed: string;
  notFound: string;
  invalidGeneration: string;
}> = {
  "zh-CN": {
    invalidResponse: (status) => `管理服务返回了无法解析的响应（HTTP ${status}）`,
    requestFailed: "请求失败",
    notFound: "资源不存在",
    invalidGeneration: "If-Match 版本号无效",
  },
  en: {
    invalidResponse: (status) => `The control plane returned an invalid response (HTTP ${status}).`,
    requestFailed: "Request failed",
    notFound: "Resource not found",
    invalidGeneration: "Invalid If-Match generation",
  },
  ja: {
    invalidResponse: (status) => `管理サービスから無効な応答が返されました（HTTP ${status}）。`,
    requestFailed: "リクエストに失敗しました",
    notFound: "リソースが見つかりません",
    invalidGeneration: "If-Match の構成バージョンが無効です",
  },
  ko: {
    invalidResponse: (status) => `관리 서비스에서 유효하지 않은 응답을 반환했습니다(HTTP ${status}).`,
    requestFailed: "요청에 실패했습니다",
    notFound: "리소스를 찾을 수 없습니다",
    invalidGeneration: "If-Match 구성 버전이 올바르지 않습니다",
  },
};

function apiFallback() {
  return apiFallbackCopy[getActiveLocale()];
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public retryable = false,
    public operationId: string | null = null,
    public traceId: string | null = null,
    public details: Record<string, unknown> = {},
  ) { super(message); }
}

const mock = new MockClient();
let adapterMode: "undecided" | "http" | "mock" = "undecided";
let adapterFetch: typeof fetch | undefined;

function mockAdapterActive(): boolean {
  return adapterMode === "mock";
}

interface ApiPayload<T> {
  data?: T;
  page?: { nextCursor: string | null };
  error?: {
    code: string;
    message: string;
    retryable?: boolean;
    operationId?: string | null;
    traceId?: string;
    details?: Record<string, unknown>;
  };
}

async function requestPayload<T>(path: string, init: RequestInit = {}): Promise<ApiPayload<T>> {
  const currentFetch = globalThis.fetch;
  if (adapterFetch !== currentFetch) {
    adapterFetch = currentFetch;
    adapterMode = "undecided";
  }
  if (mockAdapterActive()) {
    try {
      return { data: await mockRequest<T>(path, init) };
    } catch (error) {
      throw normalizeMockError(error);
    }
  }
  try {
    const response = await currentFetch(`/api/v1${path}`, { credentials: "include", ...init });
    if (mockAdapterActive()) {
      try {
        return { data: await mockRequest<T>(path, init) };
      } catch (mockError) {
        throw normalizeMockError(mockError);
      }
    }
    if (
      adapterMode === "undecided" &&
      response.headers.get(DEV_PROXY_ERROR_HEADER) === DEV_PROXY_UPSTREAM_UNAVAILABLE
    ) {
      adapterMode = "mock";
      try {
        return { data: await mockRequest<T>(path, init) };
      } catch (mockError) {
        throw normalizeMockError(mockError);
      }
    }
    adapterMode = "http";
    const body = response.status === 204 ? "" : await response.text();
    let payload: ApiPayload<T> = {};
    if (body) {
      try {
        payload = JSON.parse(body) as ApiPayload<T>;
      } catch {
        throw new ApiError(
          response.status,
          "INVALID_RESPONSE",
          apiFallback().invalidResponse(response.status),
        );
      }
    }
    if (!response.ok) throw new ApiError(
      response.status,
      payload.error?.code ?? "INTERNAL_ERROR",
      payload.error?.message ?? apiFallback().requestFailed,
      payload.error?.retryable,
      payload.error?.operationId,
      payload.error?.traceId,
      payload.error?.details,
    );
    return payload;
  } catch (error) {
    if (error instanceof TypeError && adapterMode === "undecided") {
      adapterMode = "mock";
      try {
        return { data: await mockRequest<T>(path, init) };
      } catch (mockError) {
        throw normalizeMockError(mockError);
      }
    }
    throw error;
  }
}

function normalizeMockError(error: unknown): ApiError {
  if (error instanceof ApiError) return error;
  const code = error instanceof Error ? error.message : "INTERNAL_ERROR";
  const normalizedCode = code === "AUTH_INVALID" ? "AUTH_INVALID_CREDENTIALS" : code;
  const status = ["AUTH_REQUIRED", "AUTH_INVALID_CREDENTIALS", "AUTH_INVALID_RESET_TOKEN", "BOOTSTRAP_TOKEN_INVALID"].includes(normalizedCode) ? 401
    : ["CSRF_FAILED", "FORBIDDEN"].includes(normalizedCode) ? 403
    : normalizedCode === "NOT_FOUND" ? 404
      : normalizedCode === "PRECONDITION_FAILED" ? 412
        : ["VALIDATION_FAILED", "PATH_ESCAPE_BLOCKED", "INSUFFICIENT_RESOURCE", "GAME_DEFINITION_NOT_APPROVED", "PACKAGE_INCOMPATIBLE", "BACKUP_INTEGRITY_FAILED"].includes(normalizedCode) ? 422
          : normalizedCode === "RATE_LIMITED" ? 429
          : normalizedCode === "NODE_OFFLINE" ? 503
            : ["PORT_CONFLICT", "OPERATION_CONFLICT", "OPERATION_IN_PROGRESS", "IDEMPOTENCY_KEY_REUSED", "EMAIL_CONFLICT", "SETUP_ALREADY_COMPLETE"].includes(normalizedCode) ? 409
              : 500;
  const retryable = normalizedCode === "NODE_OFFLINE" || normalizedCode === "OPERATION_IN_PROGRESS";
  return new ApiError(status, normalizedCode, error instanceof Error && error.message !== normalizedCode ? error.message : normalizedCode, retryable);
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const payload = await requestPayload<T>(path, init);
  return payload.data as T;
}

async function downloadBackup(serverId: string, backupId: string): Promise<void> {
  const response = await globalThis.fetch(`/api/v1/servers/${serverId}/backups/${backupId}/download`, { credentials: "include" });
  if (!response.ok) {
    let code = "INTERNAL_ERROR";
    let message = apiFallback().requestFailed;
    try {
      const payload = JSON.parse(await response.text()) as { error?: { code?: string; message?: string } };
      if (payload.error?.code) code = payload.error.code;
      if (payload.error?.message) message = payload.error.message;
    } catch {
      // Non-JSON error body; fall back to the generic message.
    }
    throw new ApiError(response.status, code, message);
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `${backupId}.tar.gz`;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function listServers(query: string): Promise<Server[]> {
  const servers: Server[] = [];
  let cursor: string | null = null;
  do {
    const search = new URLSearchParams({ query, limit: "100" });
    if (cursor) search.set("cursor", cursor);
    const payload = await requestPayload<Server[]>(`/servers?${search}`);
    servers.push(...(payload.data ?? []));
    cursor = payload.page?.nextCursor ?? null;
  } while (cursor);
  return servers;
}

async function listOperations(): Promise<Operation[]> {
  const operations: Operation[] = [];
  let cursor: string | null = null;
  do {
    const search = new URLSearchParams({ limit: "100" });
    if (cursor) search.set("cursor", cursor);
    const payload = await requestPayload<Operation[]>(`/operations?${search}`);
    operations.push(...(payload.data ?? []));
    cursor = payload.page?.nextCursor ?? null;
  } while (cursor);
  return operations;
}

async function mockRequest<T>(path: string, init: RequestInit): Promise<T> {
  const method = init.method ?? "GET";
  const [pathname, queryString = ""] = path.split("?");
  if (pathname === "/auth/login" && method === "POST") {
    const credentials = JSON.parse(String(init.body)) as { email: string; password: string };
    return mock.login(credentials.email, credentials.password) as Promise<T>;
  }
  if (pathname === "/setup/status" && method === "GET") return mock.setupStatus() as Promise<T>;
  if (pathname === "/setup/admin" && method === "POST") return mock.setupAdmin(JSON.parse(String(init.body))) as Promise<T>;
  if (pathname === "/auth/password-reset" && method === "POST") {
    const body = JSON.parse(String(init.body)) as { token: string; password: string };
    return mock.resetPassword(body.token, body.password) as Promise<T>;
  }
  if (pathname === "/auth/session") return mock.sessionInfo() as Promise<T>;
  if (pathname === "/auth/logout") return mock.logout() as Promise<T>;
  if (pathname === "/users" && method === "GET") return mock.listUsers() as Promise<T>;
  if (pathname === "/users" && method === "POST") return mock.createUser(JSON.parse(String(init.body))) as Promise<T>;
	if (pathname === "/api-tokens" && method === "GET") return Promise.resolve([] as APIToken[]) as Promise<T>;
	if (pathname === "/api-tokens" && method === "POST") {
		const input = JSON.parse(String(init.body)) as { name: string; scopes: string[]; expiresAt?: string };
		return Promise.resolve({ id: crypto.randomUUID(), name: input.name, scopes: input.scopes, expiresAt: input.expiresAt ?? null, lastUsedAt: null, createdAt: new Date().toISOString(), token: crypto.randomUUID() } as APITokenCredential) as Promise<T>;
	}
	if (/^\/api-tokens\/[^/]+$/.test(pathname) && method === "DELETE") return Promise.resolve(undefined) as Promise<T>;
  if (/^\/users\/[^/]+\/password-reset-tokens$/.test(pathname) && method === "POST") return mock.issuePasswordResetToken(pathname.split("/")[2]) as Promise<T>;
  if (/^\/users\/[^/]+$/.test(pathname) && method === "PATCH") return mock.updateUser(pathname.split("/")[2], JSON.parse(String(init.body))) as Promise<T>;
  if (pathname === "/overview") return mock.getOverview() as Promise<T>;
  if (pathname === "/servers" && method === "GET") return mock.listServers(new URLSearchParams(queryString).get("query") ?? "") as Promise<T>;
  if (pathname === "/servers" && method === "POST") return mock.createServer(JSON.parse(String(init.body)), String(init.headers && new Headers(init.headers).get("Idempotency-Key"))) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/power")) return mock.requestPower(pathname.split("/")[2], JSON.parse(String(init.body)).action, String(init.headers && new Headers(init.headers).get("Idempotency-Key"))) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/console")) return mock.getConsole(pathname.split("/")[2]) as Promise<T>;
	if (pathname.startsWith("/servers/") && pathname.endsWith("/console-tokens") && method === "POST") return Promise.resolve({ token: crypto.randomUUID(), expiresAt: new Date(Date.now() + 60_000).toISOString() } as ConsoleConnectionCredential) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/console/commands")) return mock.sendCommand(pathname.split("/")[2], JSON.parse(String(init.body)).command) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/files/content") && method === "GET") return mock.getFileContent(pathname.split("/")[2], new URLSearchParams(queryString).get("path") ?? "") as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/files/content") && method === "PUT") return mock.writeFile(pathname.split("/")[2], JSON.parse(String(init.body))) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/files/directories") && method === "POST") return mock.createDirectory(pathname.split("/")[2], JSON.parse(String(init.body)).path) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/files/moves") && method === "POST") return mock.moveFile(pathname.split("/")[2], JSON.parse(String(init.body))) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/files") && method === "DELETE") return mock.deleteFile(pathname.split("/")[2], new URLSearchParams(queryString).get("path") ?? "", new URLSearchParams(queryString).get("recursive") === "true") as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/files") && method === "GET") return mock.getFiles(pathname.split("/")[2], new URLSearchParams(queryString).get("path") ?? "") as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/backups") && method === "GET") return mock.getBackups(pathname.split("/")[2]) as Promise<T>;
  if (pathname.startsWith("/servers/") && pathname.endsWith("/backups") && method === "POST") return mock.createBackup(pathname.split("/")[2], String(init.headers && new Headers(init.headers).get("Idempotency-Key"))) as Promise<T>;
  if (/^\/servers\/[^/]+\/backups\/[^/]+\/restore$/.test(pathname) && method === "POST") return mock.restoreBackup(pathname.split("/")[2], pathname.split("/")[4], String(init.headers && new Headers(init.headers).get("Idempotency-Key"))) as Promise<T>;
  if (/^\/servers\/[^/]+\/backups\/[^/]+$/.test(pathname) && method === "DELETE") return mock.deleteBackup(pathname.split("/")[2], pathname.split("/")[4], String(init.headers && new Headers(init.headers).get("Idempotency-Key"))) as Promise<T>;
  if (/^\/servers\/[^/]+\/allocations$/.test(pathname) && method === "GET") return mock.getAllocations(pathname.split("/")[2]) as Promise<T>;
  if (/^\/servers\/[^/]+\/allocations$/.test(pathname) && method === "POST") return mock.createAllocation(pathname.split("/")[2], JSON.parse(String(init.body)), generationFrom(init), idempotencyKeyFrom(init)) as Promise<T>;
  if (/^\/servers\/[^/]+\/allocations\/[^/]+$/.test(pathname) && method === "PATCH") return mock.setPrimaryAllocation(pathname.split("/")[2], pathname.split("/")[4], generationFrom(init), idempotencyKeyFrom(init)) as Promise<T>;
  if (/^\/servers\/[^/]+\/allocations\/[^/]+$/.test(pathname) && method === "DELETE") return mock.deleteAllocation(pathname.split("/")[2], pathname.split("/")[4], generationFrom(init), idempotencyKeyFrom(init)) as Promise<T>;
  if (/^\/servers\/[^/]+\/startup$/.test(pathname) && method === "GET") return mock.getStartup(pathname.split("/")[2]) as Promise<T>;
  if (/^\/servers\/[^/]+\/startup$/.test(pathname) && method === "PUT") return mock.updateStartup(pathname.split("/")[2], JSON.parse(String(init.body)).variables, generationFrom(init), idempotencyKeyFrom(init)) as Promise<T>;
  if (/^\/servers\/[^/]+\/members\/[^/]+$/.test(pathname) && method === "GET") return mock.getServerMembership(pathname.split("/")[2], pathname.split("/")[4]) as Promise<T>;
  if (/^\/servers\/[^/]+\/members\/[^/]+$/.test(pathname) && method === "PUT") return mock.putServerMembership(pathname.split("/")[2], pathname.split("/")[4], JSON.parse(String(init.body)).permissions) as Promise<T>;
  if (/^\/servers\/[^/]+\/members\/[^/]+$/.test(pathname) && method === "DELETE") return mock.deleteServerMembership(pathname.split("/")[2], pathname.split("/")[4]) as Promise<T>;
  if (/^\/servers\/[^/]+\/permissions$/.test(pathname) && method === "GET") return mock.getServerPermissions(pathname.split("/")[2]) as Promise<T>;
  if (/^\/servers\/[^/]+$/.test(pathname)) return mock.getServer(pathname.split("/")[2]) as Promise<T>;
  if (pathname === "/nodes" && method === "GET") return mock.listNodes() as Promise<T>;
  if (/^\/nodes\/[^/]+$/.test(pathname) && method === "DELETE") return mock.revokeNode(pathname.split("/")[2]) as Promise<T>;
  if (pathname === "/agent-enrollment-tokens" && method === "POST") return mock.issueAgentEnrollmentToken(JSON.parse(String(init.body))) as Promise<T>;
  if (pathname === "/game-definitions") return mock.listGames() as Promise<T>;
  if (pathname === "/audit-events") return mock.listAudit() as Promise<T>;
  if (pathname === "/operations") return mock.listOperations() as Promise<T>;
  if (pathname.startsWith("/operations/")) return mock.getOperation(pathname.split("/")[2]) as Promise<T>;
  throw new ApiError(404, "NOT_FOUND", apiFallback().notFound);
}

function generationFrom(init: RequestInit): number {
  const raw = new Headers(init.headers).get("If-Match")?.trim() ?? "";
  // Keep the development adapter's parser aligned with Go's strconv.ParseInt:
  // decimal integers only, without exponent, fraction, sign, or whitespace.
  if (!/^[1-9]\d*$/.test(raw)) throw new ApiError(422, "VALIDATION_FAILED", apiFallback().invalidGeneration);
  const value = Number(raw);
  if (!Number.isSafeInteger(value)) throw new ApiError(422, "VALIDATION_FAILED", apiFallback().invalidGeneration);
  return value;
}

function idempotencyKeyFrom(init: RequestInit): string {
  return new Headers(init.headers).get("Idempotency-Key") ?? "";
}

const json = (body: unknown): RequestInit => ({ method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
const csrfMutation = (body: unknown, csrfToken: string): RequestInit => ({ ...json(body), headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken } });
const idempotentMutation = (body: unknown, csrfToken: string, key: string): RequestInit => ({ ...csrfMutation(body, csrfToken), headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken, "Idempotency-Key": key } });
const bodylessMutation = (csrfToken: string, key?: string): RequestInit => ({ method: "POST", headers: key ? { "X-CSRF-Token": csrfToken, "Idempotency-Key": key } : { "X-CSRF-Token": csrfToken } });
const csrfDelete = (csrfToken: string, key?: string): RequestInit => ({ method: "DELETE", headers: key ? { "X-CSRF-Token": csrfToken, "Idempotency-Key": key } : { "X-CSRF-Token": csrfToken } });
const generationMutation = (method: "POST" | "PATCH" | "PUT" | "DELETE", body: unknown, csrfToken: string, key: string, generation: number): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken, "Idempotency-Key": key, "If-Match": String(generation) },
  ...(body === undefined ? {} : { body: JSON.stringify(body) }),
});

export const api = {
  setupStatus: () => request<SetupStatus>("/setup/status"),
  setupAdmin: (input: SetupAdminInput) => request<User>("/setup/admin", json(input)),
  login: (email: string, password: string) => request<Session>("/auth/login", json({ email, password })),
  resetPassword: (token: string, password: string) => request<void>("/auth/password-reset", json({ token, password })),
  session: () => request<Session>("/auth/session"),
  logout: (csrfToken: string) => request<void>("/auth/logout", bodylessMutation(csrfToken)),
  users: () => request<User[]>("/users"),
  createUser: (input: CreateUserInput, csrfToken: string) => request<User>("/users", csrfMutation(input, csrfToken)),
  updateUser: (userId: string, input: UpdateUserInput, csrfToken: string) => request<User>(`/users/${userId}`, { ...csrfMutation(input, csrfToken), method: "PATCH" }),
  issuePasswordResetToken: (userId: string, csrfToken: string) => request<PasswordResetToken>(`/users/${userId}/password-reset-tokens`, bodylessMutation(csrfToken)),
	apiTokens: () => request<APIToken[]>("/api-tokens"),
	createAPIToken: (input: { name: string; scopes: string[]; expiresAt?: string }, csrfToken: string) => request<APITokenCredential>("/api-tokens", csrfMutation(input, csrfToken)),
	revokeAPIToken: (tokenId: string, csrfToken: string) => request<void>(`/api-tokens/${tokenId}`, csrfDelete(csrfToken)),
  overview: () => request<Overview>("/overview"),
  servers: (query = "") => listServers(query),
  server: (serverId: string) => request<Server>(`/servers/${serverId}`),
  createServer: (input: CreateServerInput, csrfToken: string) => request<Operation>("/servers", idempotentMutation(input, csrfToken, `create-${crypto.randomUUID()}`)),
  power: (serverId: string, action: "start" | "stop" | "restart" | "kill", csrfToken: string) => request<Operation>(`/servers/${serverId}/power`, idempotentMutation({ action }, csrfToken, `power-${serverId}-${action}-${crypto.randomUUID()}`)),
  operations: () => listOperations(),
  operation: (operationId: string, signal?: AbortSignal) => request<Operation>(`/operations/${operationId}`, { signal }),
  nodes: () => request<Node[]>("/nodes"),
  issueAgentEnrollmentToken: (input: IssueAgentEnrollmentTokenInput, csrfToken: string) => request<AgentEnrollmentToken>("/agent-enrollment-tokens", csrfMutation(input, csrfToken)),
  revokeNode: (nodeId: string, csrfToken: string) => request<void>(`/nodes/${nodeId}`, csrfDelete(csrfToken)),
  games: () => request<GameDefinition[]>("/game-definitions"),
  audit: () => request<AuditEvent[]>("/audit-events"),
  console: (serverId: string) => request<ConsoleLine[]>(`/servers/${serverId}/console`),
	consoleToken: (serverId: string, csrfToken: string) => request<ConsoleConnectionCredential>(`/servers/${serverId}/console-tokens`, bodylessMutation(csrfToken)),
  // consoleStreamPath 返回控制台实时 WebSocket 的路径（不含协议与 host），
  // 由调用方按 location.protocol 派生 ws/wss。
	consoleStreamPath: (serverId: string, token: string, after = 0) => `/api/v1/servers/${serverId}/console/stream?token=${encodeURIComponent(token)}&after=${after}`,
  command: (serverId: string, command: string, csrfToken: string) => request<void>(`/servers/${serverId}/console/commands`, csrfMutation({ command }, csrfToken)),
  files: (serverId: string, path = "") => request<FileEntry[]>(`/servers/${serverId}/files?path=${encodeURIComponent(path)}`),
  fileContent: (serverId: string, path: string) => request<FileContent>(`/servers/${serverId}/files/content?path=${encodeURIComponent(path)}`),
  writeFile: (serverId: string, path: string, content: string, csrfToken: string) => request<void>(`/servers/${serverId}/files/content`, { ...csrfMutation({ path, content, encoding: "utf-8" }, csrfToken), method: "PUT" }),
  createDirectory: (serverId: string, path: string, csrfToken: string) => request<void>(`/servers/${serverId}/files/directories`, csrfMutation({ path }, csrfToken)),
  moveFile: (serverId: string, source: string, destination: string, replace: boolean, csrfToken: string) => request<void>(`/servers/${serverId}/files/moves`, csrfMutation({ source, destination, replace }, csrfToken)),
  deleteFile: (serverId: string, path: string, recursive: boolean, csrfToken: string) => request<void>(`/servers/${serverId}/files?path=${encodeURIComponent(path)}&recursive=${recursive}`, csrfDelete(csrfToken)),
  backups: (serverId: string) => request<Backup[]>(`/servers/${serverId}/backups`),
  createBackup: (serverId: string, csrfToken: string) => request<Operation>(`/servers/${serverId}/backups`, bodylessMutation(csrfToken, `backup-${serverId}-${crypto.randomUUID()}`)),
  restoreBackup: (serverId: string, backupId: string, csrfToken: string) => request<Operation>(`/servers/${serverId}/backups/${backupId}/restore`, bodylessMutation(csrfToken, `restore-${backupId}-${crypto.randomUUID()}`)),
  deleteBackup: (serverId: string, backupId: string, csrfToken: string) => request<Operation>(`/servers/${serverId}/backups/${backupId}`, csrfDelete(csrfToken, `delete-${backupId}-${crypto.randomUUID()}`)),
  downloadBackup: (serverId: string, backupId: string) => downloadBackup(serverId, backupId),
  allocations: (serverId: string) => request<Allocation[]>(`/servers/${serverId}/allocations`),
  createAllocation: (serverId: string, input: CreateAllocationInput, generation: number, csrfToken: string) => request<Operation>(`/servers/${serverId}/allocations`, generationMutation("POST", input, csrfToken, `allocation-create-${serverId}-${crypto.randomUUID()}`, generation)),
  setPrimaryAllocation: (serverId: string, allocationId: string, generation: number, csrfToken: string) => request<Operation>(`/servers/${serverId}/allocations/${allocationId}`, generationMutation("PATCH", { primary: true }, csrfToken, `allocation-primary-${allocationId}-${crypto.randomUUID()}`, generation)),
  deleteAllocation: (serverId: string, allocationId: string, generation: number, csrfToken: string) => request<Operation>(`/servers/${serverId}/allocations/${allocationId}`, generationMutation("DELETE", undefined, csrfToken, `allocation-delete-${allocationId}-${crypto.randomUUID()}`, generation)),
  startup: (serverId: string) => request<Startup>(`/servers/${serverId}/startup`),
  updateStartup: (serverId: string, variables: Record<string, StartupValue>, generation: number, csrfToken: string) => request<Operation>(`/servers/${serverId}/startup`, generationMutation("PUT", { variables }, csrfToken, `startup-${serverId}-${crypto.randomUUID()}`, generation)),
  serverMembership: (serverId: string, userId: string) => request<ServerMembership>(`/servers/${serverId}/members/${userId}`),
  serverPermissions: (serverId: string) => request<ServerPermissions>(`/servers/${serverId}/permissions`),
  putServerMembership: (serverId: string, userId: string, permissions: ServerPermission[], csrfToken: string) => request<ServerMembership>(`/servers/${serverId}/members/${userId}`, { ...csrfMutation({ permissions }, csrfToken), method: "PUT" }),
  deleteServerMembership: (serverId: string, userId: string, csrfToken: string) => request<void>(`/servers/${serverId}/members/${userId}`, csrfDelete(csrfToken)),
};
