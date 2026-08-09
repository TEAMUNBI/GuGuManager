import { beforeEach, describe, expect, test, vi } from "vitest";
import { api } from "./api";
import { MockClient } from "./mock";
import type { Operation } from "./types";

const bootstrapToken = "mock-bootstrap-token-abcdefghijklmnopqrstuvwxyz";
const paperServerId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

describe("identity API contract", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  test("uses the declared setup, user, reset, and membership endpoints", async () => {
    const user = {
      id: "10000000-0000-4000-8000-000000000001",
      email: "operator@example.com",
      displayName: "Operator",
      roles: ["server_owner"],
      status: "active",
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    const resetToken = { token: "reset-token-abcdefghijklmnopqrstuvwxyz-1234", expiresAt: "2026-08-08T00:15:00.000Z" };
    const membership = {
      serverId: paperServerId,
      userId: user.id,
      permissions: ["servers.read", "servers.files.read"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ data: { required: true, bootstrapExpiresAt: "2026-08-08T00:15:00.000Z" } }))
      .mockResolvedValueOnce(jsonResponse({ data: { ...user, roles: ["platform_admin"] } }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: [user] }))
      .mockResolvedValueOnce(jsonResponse({ data: user }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: { ...user, status: "disabled" } }))
      .mockResolvedValueOnce(jsonResponse({ data: resetToken }, 201))
      .mockResolvedValueOnce(jsonResponse({ data: membership }))
      .mockResolvedValueOnce(jsonResponse({ data: { serverId: paperServerId, permissions: ["servers.files.read", "servers.read"] } }))
      .mockResolvedValueOnce(jsonResponse({ data: membership }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.setupStatus();
    await api.setupAdmin({ bootstrapToken, email: "admin@example.com", displayName: "Admin", password: "secure-password" });
    await api.users();
    await api.createUser({ email: user.email, displayName: user.displayName, password: "secure-password", roles: ["server_owner"] }, "csrf-token");
    await api.updateUser(user.id, { status: "disabled" }, "csrf-token");
    await api.issuePasswordResetToken(user.id, "csrf-token");
    await api.serverMembership(paperServerId, user.id);
    await expect(api.serverPermissions(paperServerId)).resolves.toEqual({
      serverId: paperServerId,
      permissions: ["servers.files.read", "servers.read"],
    });
    await api.putServerMembership(paperServerId, user.id, ["servers.read", "servers.files.read"], "csrf-token");
    await api.deleteServerMembership(paperServerId, user.id, "csrf-token");
    await api.resetPassword(resetToken.token, "replacement-password");

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/setup/status",
      "/api/v1/setup/admin",
      "/api/v1/users",
      "/api/v1/users",
      `/api/v1/users/${user.id}`,
      `/api/v1/users/${user.id}/password-reset-tokens`,
      `/api/v1/servers/${paperServerId}/members/${user.id}`,
      `/api/v1/servers/${paperServerId}/permissions`,
      `/api/v1/servers/${paperServerId}/members/${user.id}`,
      `/api/v1/servers/${paperServerId}/members/${user.id}`,
      "/api/v1/auth/password-reset",
    ]);
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe("POST");
    expect(new Headers((fetchMock.mock.calls[1][1] as RequestInit).headers).has("X-CSRF-Token")).toBe(false);
    expect((fetchMock.mock.calls[4][1] as RequestInit).method).toBe("PATCH");
    expect(new Headers((fetchMock.mock.calls[4][1] as RequestInit).headers).get("X-CSRF-Token")).toBe("csrf-token");
    expect((fetchMock.mock.calls[5][1] as RequestInit).body).toBeUndefined();
    expect((fetchMock.mock.calls[7][1] as RequestInit).method).toBeUndefined();
    expect(new Headers((fetchMock.mock.calls[7][1] as RequestInit).headers).has("X-CSRF-Token")).toBe(false);
    expect((fetchMock.mock.calls[8][1] as RequestInit).method).toBe("PUT");
    expect(JSON.parse(String((fetchMock.mock.calls[8][1] as RequestInit).body))).toEqual({ permissions: ["servers.read", "servers.files.read"] });
    expect((fetchMock.mock.calls[9][1] as RequestInit).method).toBe("DELETE");
    expect(new Headers((fetchMock.mock.calls[10][1] as RequestInit).headers).has("X-CSRF-Token")).toBe(false);
  });
});

describe("MockClient identity lifecycle", () => {
  test("requires one valid bootstrap token and creates the first administrator", async () => {
    const client = new MockClient({ setupRequired: true, bootstrapToken });

    await expect(client.setupStatus()).resolves.toMatchObject({ required: true });
    await expect(client.setupAdmin({ bootstrapToken: "wrong-bootstrap-token-abcdefghijklmnopqrstuvwxyz", email: "admin@example.com", displayName: "Admin", password: "secure-password" })).rejects.toThrow("BOOTSTRAP_TOKEN_INVALID");
    await expect(client.setupAdmin({ bootstrapToken, email: "admin@example.com", displayName: "Admin", password: "secure-password" })).resolves.toMatchObject({ roles: ["platform_admin"], status: "active" });
    await expect(client.setupStatus()).resolves.toEqual({ required: false });
    await expect(client.setupAdmin({ bootstrapToken, email: "second@example.com", displayName: "Second", password: "secure-password" })).rejects.toThrow("SETUP_ALREADY_COMPLETE");
  });

  test("filters servers by membership and revokes access immediately", async () => {
    const client = new MockClient();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    const user = await client.createUser({ email: "member@example.com", displayName: "Member", password: "member-password", roles: ["server_owner"] });
    await client.logout();
    await client.login(user.email, "member-password");
    await expect(client.listServers()).resolves.toEqual([]);

    await client.logout();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    await client.putServerMembership(paperServerId, user.id, ["servers.read", "servers.files.read"]);
    await client.logout();
    await client.login(user.email, "member-password");
    await expect(client.listServers()).resolves.toEqual([expect.objectContaining({ id: paperServerId })]);

    await client.logout();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    await client.deleteServerMembership(paperServerId, user.id);
    await client.logout();
    await client.login(user.email, "member-password");
    await expect(client.getServer(paperServerId)).rejects.toThrow("FORBIDDEN");
  });

  test("returns sorted effective permissions for administrators and members, with hidden unauthorized resources", async () => {
    const client = new MockClient();
    await expect(client.getServerPermissions(paperServerId)).rejects.toThrow("AUTH_REQUIRED");

    await client.login("admin@gugu.local", "gugu-dev-2026");
    const adminPermissions = await client.getServerPermissions(paperServerId);
    expect(adminPermissions.serverId).toBe(paperServerId);
    expect(adminPermissions.permissions).toEqual([...adminPermissions.permissions].sort());
    expect(adminPermissions.permissions).toHaveLength(13);

    const user = await client.createUser({ email: "permissions-member@example.com", displayName: "Permissions Member", password: "permissions-password", roles: ["server_owner"] });
    await client.putServerMembership(paperServerId, user.id, ["servers.files.read", "servers.read"]);
    await client.logout();
    await client.login(user.email, "permissions-password");

    const memberPermissions = await client.getServerPermissions(paperServerId);
    expect(memberPermissions.permissions).toEqual(["servers.files.read", "servers.read"]);
    memberPermissions.permissions.push("servers.power");
    await expect(client.getServerPermissions(paperServerId)).resolves.toMatchObject({ permissions: ["servers.files.read", "servers.read"] });

    await client.logout();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    await client.deleteServerMembership(paperServerId, user.id);
    await client.logout();
    await client.login(user.email, "permissions-password");
    await expect(client.getServerPermissions(paperServerId)).rejects.toThrow("NOT_FOUND");
    await expect(client.getServerPermissions("ffffffff-ffff-4fff-8fff-ffffffffffff")).rejects.toThrow("NOT_FOUND");
  });

  test("revokes access to an existing operation with its server membership", async () => {
    const client = new MockClient();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    const user = await client.createUser({
      email: "operation-member@example.com",
      displayName: "Operation Member",
      password: "operation-member-password",
      roles: ["server_owner"],
    });
    const timestamp = new Date().toISOString();
    const operation: Operation = {
      id: "90000000-0000-4000-8000-000000000001",
      serverId: paperServerId,
      nodeId: "11111111-1111-4111-8111-111111111111",
      type: "backup",
      status: "succeeded",
      progress: 100,
      generation: 12,
      attempt: 1,
      maxAttempts: 1,
      leaseOwner: null,
      leaseExpiresAt: null,
      checkpoint: "completed",
      error: null,
      createdAt: timestamp,
      updatedAt: timestamp,
    };
    const internal = client as unknown as { operations: Map<string, Operation> };
    internal.operations.set(operation.id, operation);
    await client.putServerMembership(paperServerId, user.id, ["servers.read"]);

    await client.logout();
    await client.login(user.email, "operation-member-password");
    await expect(client.getOperation(operation.id)).resolves.toEqual(operation);
	await expect(client.listOperations()).resolves.toEqual([operation]);

    await client.logout();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    await client.deleteServerMembership(paperServerId, user.id);
    await client.logout();
    await client.login(user.email, "operation-member-password");
    await expect(client.getOperation(operation.id)).rejects.toThrow("FORBIDDEN");
	await expect(client.listOperations()).resolves.toEqual([]);
  });

  test("rejects granting server membership to a disabled local user", async () => {
    const client = new MockClient();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    const user = await client.createUser({
      email: "disabled-member@example.com",
      displayName: "Disabled Member",
      password: "disabled-member-password",
      roles: ["server_owner"],
    });
    await client.updateUser(user.id, { status: "disabled" });

    await expect(client.putServerMembership(
      paperServerId,
      user.id,
      ["servers.read"],
    )).rejects.toThrow("NOT_FOUND");
  });

  test("rejects issuing a password reset token to a disabled local user", async () => {
    const client = new MockClient();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    const user = await client.createUser({
      email: "disabled-reset@example.com",
      displayName: "Disabled Reset User",
      password: "disabled-reset-password",
      roles: ["server_owner"],
    });
    await client.updateUser(user.id, { status: "disabled" });

    await expect(client.issuePasswordResetToken(user.id)).rejects.toThrow("OPERATION_CONFLICT");
  });

  test("consumes reset tokens once and invalidates the target session", async () => {
    const client = new MockClient();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    const user = await client.createUser({ email: "reset@example.com", displayName: "Reset User", password: "old-password", roles: ["server_owner"] });
    const issued = await client.issuePasswordResetToken(user.id);
    await client.logout();
    await client.login(user.email, "old-password");

    await client.resetPassword(issued.token, "new-password");

    await expect(client.sessionInfo()).rejects.toThrow("AUTH_REQUIRED");
    await expect(client.resetPassword(issued.token, "another-password")).rejects.toThrow("AUTH_INVALID_RESET_TOKEN");
    await expect(client.login(user.email, "old-password")).rejects.toThrow("AUTH_INVALID");
    await expect(client.login(user.email, "new-password")).resolves.toMatchObject({ user: { id: user.id } });
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}
