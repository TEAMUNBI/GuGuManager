import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import type { Operation, Server } from "./types";

describe("API response handling", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("accepts an empty successful response for console commands", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.command("server-1", "say hello", "csrf-token")).resolves.toBeUndefined();
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(new Headers(init.headers).has("Idempotency-Key")).toBe(false);
  });

  it("sends logout without an undeclared body or idempotency key", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.logout("csrf-token");
    const logoutInit = fetchMock.mock.calls[0][1] as RequestInit;
    expect(logoutInit.body).toBeUndefined();
    expect(new Headers(logoutInit.headers).has("Idempotency-Key")).toBe(false);
  });

  it("sends enrollment and node revocation mutations with CSRF protection", async () => {
    const issued = { token: "a".repeat(64), expiresAt: "2026-08-15T08:00:00Z" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ data: issued }, 201))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.issueAgentEnrollmentToken({ nodeNameHint: "node-2", ttlSeconds: 3600 }, "csrf-token")).resolves.toEqual(issued);
    await expect(api.revokeNode("node-1", "csrf-token")).resolves.toBeUndefined();

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/agent-enrollment-tokens",
      "/api/v1/nodes/node-1",
    ]);
    const issueInit = fetchMock.mock.calls[0][1] as RequestInit;
    expect(issueInit.method).toBe("POST");
    expect(JSON.parse(String(issueInit.body))).toEqual({ nodeNameHint: "node-2", ttlSeconds: 3600 });
    expect(new Headers(issueInit.headers).get("X-CSRF-Token")).toBe("csrf-token");
    const revokeInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(revokeInit.method).toBe("DELETE");
    expect(revokeInit.body).toBeUndefined();
    expect(new Headers(revokeInit.headers).get("X-CSRF-Token")).toBe("csrf-token");
  });

  it("sends backup creation without a body while retaining idempotency", async () => {
    const operation = { id: "00000000-0000-4000-8000-000000000001" };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ data: operation }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.createBackup("server-1", "csrf-token")).resolves.toEqual(operation);
    const backupInit = fetchMock.mock.calls[0][1] as RequestInit;
    expect(backupInit.body).toBeUndefined();
    expect(new Headers(backupInit.headers).has("Idempotency-Key")).toBe(true);
  });

  it("sends file writes and moves to their declared endpoints with CSRF protection", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.writeFile("server-1", "config/server.properties", "motd=GuGu", "csrf-token");
    await api.moveFile("server-1", "config/server.properties", "server.properties", false, "csrf-token");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/servers/server-1/files/content");
    const writeInit = fetchMock.mock.calls[0][1] as RequestInit;
    expect(writeInit.method).toBe("PUT");
    expect(new Headers(writeInit.headers).get("X-CSRF-Token")).toBe("csrf-token");
    expect(new Headers(writeInit.headers).has("Idempotency-Key")).toBe(false);
    expect(JSON.parse(String(writeInit.body))).toEqual({
      path: "config/server.properties",
      content: "motd=GuGu",
      encoding: "utf-8",
    });

    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/servers/server-1/files/moves");
    const moveInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(moveInit.method).toBe("POST");
    expect(new Headers(moveInit.headers).get("X-CSRF-Token")).toBe("csrf-token");
    expect(new Headers(moveInit.headers).has("Idempotency-Key")).toBe(false);
    expect(JSON.parse(String(moveInit.body))).toEqual({
      source: "config/server.properties",
      destination: "server.properties",
      replace: false,
    });
  });

  it("sends backup restore and deletion with CSRF and operation idempotency keys", async () => {
    const operation = { id: "00000000-0000-4000-8000-000000000004" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ data: operation }))
      .mockResolvedValueOnce(jsonResponse({ data: operation }));
    vi.stubGlobal("fetch", fetchMock);

    await api.restoreBackup("server-1", "backup-1", "csrf-token");
    await api.deleteBackup("server-1", "backup-1", "csrf-token");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/servers/server-1/backups/backup-1/restore");
    const restoreInit = fetchMock.mock.calls[0][1] as RequestInit;
    expect(restoreInit.method).toBe("POST");
    expect(restoreInit.body).toBeUndefined();
    expect(new Headers(restoreInit.headers).get("X-CSRF-Token")).toBe("csrf-token");
    expect(new Headers(restoreInit.headers).get("Idempotency-Key")).toMatch(/^restore-backup-1-/);

    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/servers/server-1/backups/backup-1");
    const deleteInit = fetchMock.mock.calls[1][1] as RequestInit;
    expect(deleteInit.method).toBe("DELETE");
    expect(deleteInit.body).toBeUndefined();
    expect(new Headers(deleteInit.headers).get("X-CSRF-Token")).toBe("csrf-token");
    expect(new Headers(deleteInit.headers).get("Idempotency-Key")).toMatch(/^delete-backup-1-/);
  });

  it("sends allocation mutations with generation preconditions and idempotency", async () => {
    const operation = { id: "00000000-0000-4000-8000-000000000005" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ data: operation }))
      .mockResolvedValueOnce(jsonResponse({ data: operation }))
      .mockResolvedValueOnce(jsonResponse({ data: operation }));
    vi.stubGlobal("fetch", fetchMock);

    await api.createAllocation("server-1", { bindIp: "0.0.0.0", port: 25566, protocol: "tcp", primary: false }, 12, "csrf-token");
    await api.setPrimaryAllocation("server-1", "allocation-2", 13, "csrf-token");
    await api.deleteAllocation("server-1", "allocation-1", 14, "csrf-token");

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/servers/server-1/allocations",
      "/api/v1/servers/server-1/allocations/allocation-2",
      "/api/v1/servers/server-1/allocations/allocation-1",
    ]);
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBe("POST");
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("PATCH");
    expect((fetchMock.mock.calls[2][1] as RequestInit).method).toBe("DELETE");
    expect(JSON.parse(String((fetchMock.mock.calls[1][1] as RequestInit).body))).toEqual({ primary: true });
    for (const [index, call] of fetchMock.mock.calls.entries()) {
      const headers = new Headers((call[1] as RequestInit).headers);
      expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
      expect(headers.get("If-Match")).toBe(String(12 + index));
      expect(headers.get("Idempotency-Key")).toBeTruthy();
    }
  });

  it("updates declared startup variables with a generation precondition", async () => {
    const operation = { id: "00000000-0000-4000-8000-000000000006" };
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse({ data: operation }));
    vi.stubGlobal("fetch", fetchMock);

    await api.updateStartup("server-1", { memory_mb: 4096, accept_eula: true, rcon_password: null }, 21, "csrf-token");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/servers/server-1/startup");
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("PUT");
    expect(JSON.parse(String(init.body))).toEqual({ variables: { memory_mb: 4096, accept_eula: true, rcon_password: null } });
    const headers = new Headers(init.headers);
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(headers.get("If-Match")).toBe("21");
    expect(headers.get("Idempotency-Key")).toMatch(/^startup-server-1-/);
  });

  it("reads the current actor's effective server permissions without a mutation header", async () => {
    const permissions = {
      serverId: "00000000-0000-4000-8000-000000000001",
      permissions: ["servers.files.read", "servers.read"],
    };
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ data: permissions }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.serverPermissions(permissions.serverId)).resolves.toEqual(permissions);

    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/servers/${permissions.serverId}/permissions`);
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBeUndefined();
    expect(new Headers(init.headers).has("X-CSRF-Token")).toBe(false);
  });

  it("follows server list cursors until all pages are loaded", async () => {
    const first = { id: "server-1" } as Server;
    const second = { id: "server-2" } as Server;
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ data: [first], page: { nextCursor: "next-page" } }))
      .mockResolvedValueOnce(jsonResponse({ data: [second], page: { nextCursor: null } }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.servers("paper")).resolves.toEqual([first, second]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1][0])).toContain("cursor=next-page");
  });

  it("loads the visible operation list from the collection endpoint", async () => {
	const first = { id: "00000000-0000-4000-8000-000000000007" } as Operation;
	const second = { id: "00000000-0000-4000-8000-000000000008" } as Operation;
	const fetchMock = vi.fn()
		.mockResolvedValueOnce(jsonResponse({ data: [first], page: { nextCursor: "next-operation-page" } }))
		.mockResolvedValueOnce(jsonResponse({ data: [second], page: { nextCursor: null } }));
	vi.stubGlobal("fetch", fetchMock);

	await expect(api.operations()).resolves.toEqual([first, second]);
	expect(String(fetchMock.mock.calls[0][0])).toContain("/api/v1/operations?limit=100");
	expect(String(fetchMock.mock.calls[1][0])).toContain("cursor=next-operation-page");
  });

  it("preserves structured error context from the API envelope", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      error: {
        code: "OPERATION_IN_PROGRESS",
        message: "operation already running",
        retryable: true,
        operationId: "00000000-0000-4000-8000-000000000002",
        traceId: "00000000-0000-4000-8000-000000000003",
        details: { operationType: "start" },
      },
    }, 409));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.power("server-1", "stop", "csrf-token")).rejects.toMatchObject({
      code: "OPERATION_IN_PROGRESS",
      retryable: true,
      operationId: "00000000-0000-4000-8000-000000000002",
      traceId: "00000000-0000-4000-8000-000000000003",
      details: { operationType: "start" },
    });
  });

  it("reports a non-JSON HTTP error without exposing a parser failure", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("404 page not found\n", {
      status: 404,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.setupStatus()).rejects.toMatchObject({
      status: 404,
      code: "INVALID_RESPONSE",
      message: "The control plane returned an invalid response (HTTP 404).",
    });
  });

  it("switches the initial Vite proxy connection failure to the mock adapter", async () => {
    vi.resetModules();
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, {
      status: 500,
      headers: { "X-GuGuManager-Proxy-Error": "upstream-unavailable" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const { api: offlineApi } = await import("./api");

    await expect(offlineApi.setupStatus()).resolves.toEqual({ required: false });
    await expect(offlineApi.session()).rejects.toMatchObject({
      status: 401,
      code: "AUTH_REQUIRED",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("keeps concurrent initial Vite proxy failures on the mock adapter", async () => {
    vi.resetModules();
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, {
      status: 500,
      headers: { "X-GuGuManager-Proxy-Error": "upstream-unavailable" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const { api: offlineApi } = await import("./api");

    await expect(Promise.all([
      offlineApi.setupStatus(),
      offlineApi.setupStatus(),
    ])).resolves.toEqual([{ required: false }, { required: false }]);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    await expect(offlineApi.session()).rejects.toMatchObject({
      status: 401,
      code: "AUTH_REQUIRED",
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("forwards cancellation to operation status requests", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ data: { id: "operation-1" } }));
    vi.stubGlobal("fetch", fetchMock);
    const controller = new AbortController();

    await api.operation("operation-1", controller.signal);

    expect((fetchMock.mock.calls[0][1] as RequestInit).signal).toBe(controller.signal);
  });

  it("does not switch an established HTTP session to the mock adapter after a network failure", async () => {
    vi.resetModules();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ data: { serverCount: 3 } }))
      .mockRejectedValueOnce(new TypeError("network unavailable"));
    vi.stubGlobal("fetch", fetchMock);
    const { api: connectedApi } = await import("./api");

    await connectedApi.overview();

    await expect(connectedApi.operation("operation-1")).rejects.toBeInstanceOf(TypeError);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("forwards the generated backup idempotency key to the offline adapter", async () => {
    vi.resetModules();
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));
    const { api: offlineApi } = await import("./api");

    const serverId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const first = await offlineApi.createBackup(serverId, "csrf-token");
    await vi.advanceTimersByTimeAsync(1200);
    const second = await offlineApi.createBackup(serverId, "csrf-token");

    expect(second.id).not.toBe(first.id);
  });

  it("routes network and startup reads through the offline adapter after the initial probe", async () => {
    vi.resetModules();
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("offline"));
    vi.stubGlobal("fetch", fetchMock);
    const { api: offlineApi } = await import("./api");
    const serverId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

    await expect(offlineApi.allocations(serverId)).resolves.toEqual([
      expect.objectContaining({ serverId, primary: true }),
    ]);
    await expect(offlineApi.startup(serverId)).resolves.toMatchObject({
      command: { executable: "/image/scripts/start" },
      variables: expect.arrayContaining([expect.objectContaining({ key: "memory_mb" })]),
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("preserves structured error codes when the offline adapter rejects a mutation", async () => {
    vi.resetModules();
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));
    const { api: offlineApi, ApiError } = await import("./api");

    await expect(offlineApi.createAllocation(
      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      { bindIp: "0.0.0.0", port: 25570, protocol: "tcp", primary: false },
      12,
      "csrf-token",
    )).rejects.toMatchObject({ status: 422, code: "VALIDATION_FAILED" });
    await expect(offlineApi.createAllocation(
      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      { bindIp: "10.0.10.21", port: 25565, protocol: "tcp", primary: false },
      12,
      "csrf-token",
    )).rejects.toBeInstanceOf(ApiError);

    await expect(offlineApi.createServer({
      name: "Unapproved bundle",
      gameDefinitionId: "io.gugumanager.vintagestory",
      gameBundleDigest: "sha256:c2c2cdb82e9ba2cc69e17b9acc99ddd4e75a40dd091e39a19c987927273e7779",
      nodeId: "11111111-1111-4111-8111-111111111111",
      memoryMb: 3072,
      diskGb: 18,
    }, "csrf-token")).rejects.toMatchObject({ status: 422, code: "GAME_DEFINITION_NOT_APPROVED" });

    await expect(offlineApi.createServer({
      name: "Mismatched bundle",
      gameDefinitionId: "io.gugumanager.papermc",
      gameBundleDigest: "sha256:invalid",
      nodeId: "11111111-1111-4111-8111-111111111111",
      memoryMb: 4096,
      diskGb: 25,
    }, "csrf-token")).rejects.toMatchObject({ status: 422, code: "PACKAGE_INCOMPATIBLE" });
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
