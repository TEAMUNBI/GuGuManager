import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import { App } from "./App";
import { api, ApiError } from "../lib/api";
import type { Server, ServerMembership, Session } from "../lib/types";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.localStorage.clear();
  window.history.replaceState({}, "", "/");
  document.documentElement.style.overflow = "";
  document.body.style.overflow = "";
});

function expectFocusWithin(container: HTMLElement) {
  const activeElement = document.activeElement;
  expect(activeElement).toBeInstanceOf(HTMLElement);
  if (!(activeElement instanceof HTMLElement)) throw new Error("Expected focus to remain on an HTML element inside the dialog.");
  expect(container).toContainElement(activeElement);
}

function mockAuthenticatedServerOwner() {
  const ownerSession: Session = {
    user: {
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner@example.com",
      displayName: "World Owner",
      roles: ["server_owner"],
      status: "active",
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    },
    csrfToken: "csrf-token",
    environment: "development",
  };
  vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
  vi.spyOn(api, "session").mockResolvedValue(ownerSession);
  vi.spyOn(api, "servers").mockResolvedValue([]);
  vi.spyOn(api, "games").mockResolvedValue([]);
  return ownerSession;
}

describe("application identity routing", () => {
  test("switches the interface language immediately and persists the choice", async () => {
    window.localStorage.setItem("gugu.locale", "en");
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockRejectedValue(new ApiError(401, "AUTH_REQUIRED", "Authentication required"));
    const user = userEvent.setup();

    const view = render(<App />);

    const language = await screen.findByRole("combobox", { name: "Interface language" });
    expect(screen.getByRole("heading", { name: /Welcome back, operator\./i })).toBeInTheDocument();

    await user.selectOptions(language, "zh-CN");

    expect(screen.getByRole("heading", { name: /欢迎回来，\s*运维人员。/ })).toBeInTheDocument();
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(window.localStorage.getItem("gugu.locale")).toBe("zh-CN");

    view.unmount();
    render(<App />);

    expect(await screen.findByRole("heading", { name: /欢迎回来，\s*运维人员。/ })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "界面语言" })).toHaveValue("zh-CN");
  });

  test("shows first-administrator setup before checking a session", async () => {
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: true, bootstrapExpiresAt: "2026-08-08T00:15:00.000Z" });
    const session = vi.spyOn(api, "session").mockRejectedValue(new Error("AUTH_REQUIRED"));

    render(<App />);

    expect(await screen.findByRole("heading", { name: /first administrator/i })).toBeInTheDocument();
    expect(session).not.toHaveBeenCalled();
  });

  test("submits the first administrator setup and continues to sign in", async () => {
    const bootstrapToken = "bootstrap-token-abcdefghijklmnopqrstuvwxyz-1234";
    const createdAdmin = {
      id: "00000000-0000-4000-8000-000000000001",
      email: "first-admin@example.com",
      displayName: "First Admin",
      roles: ["platform_admin"],
      status: "active" as const,
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: true, bootstrapExpiresAt: "2026-08-08T00:15:00.000Z" });
    const setupAdmin = vi.spyOn(api, "setupAdmin").mockResolvedValue(createdAdmin);
    const user = userEvent.setup();

    render(<App />);
    await user.type(await screen.findByLabelText("Bootstrap token"), bootstrapToken);
    await user.type(screen.getByLabelText("Email"), "first-admin@example.com");
    await user.type(screen.getByLabelText("Display name"), "First Admin");
    await user.type(screen.getByLabelText("Password"), "first-admin-password");
    await user.type(screen.getByLabelText("Confirm password"), "first-admin-password");
    await user.click(screen.getByRole("button", { name: "Create administrator" }));

    await waitFor(() => expect(setupAdmin).toHaveBeenCalledWith({
      bootstrapToken,
      email: "first-admin@example.com",
      displayName: "First Admin",
      password: "first-admin-password",
    }));
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /first administrator/i })).not.toBeInTheDocument();
  });

  test("keeps the password reset route available without a session", async () => {
    window.history.replaceState({}, "", "/reset-password?token=reset-token-abcdefghijklmnopqrstuvwxyz-1234");
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockRejectedValue(new ApiError(401, "AUTH_REQUIRED", "Authentication required"));

    render(<App />);

    expect(await screen.findByRole("heading", { name: /choose a new password/i })).toBeInTheDocument();
    expect(screen.getByLabelText(/reset token/i)).toHaveValue("reset-token-abcdefghijklmnopqrstuvwxyz-1234");
  });

  test("resets a password anonymously and reports that existing sessions were revoked", async () => {
    const resetToken = "reset-token-abcdefghijklmnopqrstuvwxyz-1234";
    window.history.replaceState({}, "", `/reset-password?token=${resetToken}`);
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockRejectedValue(new ApiError(401, "AUTH_REQUIRED", "Authentication required"));
    const resetPassword = vi.spyOn(api, "resetPassword").mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(<App />);
    await user.type(await screen.findByLabelText("New password"), "replacement-password");
    await user.type(screen.getByLabelText("Confirm new password"), "replacement-password");
    await user.click(screen.getByRole("button", { name: "Reset password" }));

    await waitFor(() => expect(resetPassword).toHaveBeenCalledWith(resetToken, "replacement-password"));
    expect(await screen.findByRole("status")).toHaveTextContent("All other sessions for this account have been signed out");
  });

  test("reports a session probe failure instead of presenting it as a signed-out state", async () => {
    const setupStatus = vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session")
      .mockRejectedValueOnce(new ApiError(503, "DEPENDENCY_UNAVAILABLE", "Control plane session check failed", true))
      .mockRejectedValueOnce(new ApiError(401, "AUTH_REQUIRED", "Authentication required"));
    const user = userEvent.setup();

    render(<App />);

    expect(await screen.findByText("Control plane session check failed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^sign in$/i })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /retry|重试/i }));

    expect(await screen.findByRole("button", { name: /^sign in$/i })).toBeInTheDocument();
    expect(setupStatus).toHaveBeenCalledTimes(2);
  });

  test("routes a server owner to assigned servers without admin-only navigation or node requests", async () => {
    const ownerSession: Session = {
      user: {
        id: "10000000-0000-4000-8000-000000000001",
        email: "owner@example.com",
        displayName: "World Owner",
        roles: ["server_owner"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(ownerSession);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    vi.spyOn(api, "games").mockResolvedValue([]);
    const nodes = vi.spyOn(api, "nodes").mockResolvedValue([]);

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Servers" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Task queue" })).toHaveAttribute("href", "/operations");
    expect(screen.queryByText("Overview")).not.toBeInTheDocument();
    expect(screen.queryByText("Nodes")).not.toBeInTheDocument();
    expect(screen.queryByText("Activity log")).not.toBeInTheDocument();
    expect(screen.queryByText("Users")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /new server/i })).not.toBeInTheDocument();
    await waitFor(() => expect(nodes).not.toHaveBeenCalled());
  });

  test("moves focus into the mobile navigation and returns it to the opener when closed", async () => {
    mockAuthenticatedServerOwner();
    const user = userEvent.setup();

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Servers" })).toBeInTheDocument();
    const openNavigation = await screen.findByRole("button", { name: "Open navigation" });
    await user.click(openNavigation);

    const closeNavigation = screen.getByRole("button", { name: "Close navigation" });
    await waitFor(() => expect(closeNavigation).toHaveFocus());

    await user.click(closeNavigation);

    await waitFor(() => expect(openNavigation).toHaveFocus());
  });

  test("locks background scrolling while mobile navigation is open and restores it after Escape", async () => {
    mockAuthenticatedServerOwner();
    document.documentElement.style.overflow = "clip";
    document.body.style.overflow = "auto";
    const user = userEvent.setup();

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Servers" })).toBeInTheDocument();
    const openNavigation = await screen.findByRole("button", { name: "Open navigation" });
    await user.click(openNavigation);

    expect(document.documentElement.style.overflow).toBe("hidden");
    expect(document.body.style.overflow).toBe("hidden");

    await user.keyboard("{Escape}");

    await waitFor(() => expect(openNavigation).toHaveFocus());
    expect(document.documentElement.style.overflow).toBe("clip");
    expect(document.body.style.overflow).toBe("auto");
  });

  test("does not expose a previous user's membership actions while the next membership is loading", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const ownerA = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner-a@example.com",
      displayName: "Owner A",
      roles: ["server_owner"],
    };
    const ownerB = {
      ...ownerA,
      id: "10000000-0000-4000-8000-000000000002",
      email: "owner-b@example.com",
      displayName: "Owner B",
    };
    const ownerC = {
      ...ownerA,
      id: "10000000-0000-4000-8000-000000000003",
      email: "owner-c@example.com",
      displayName: "Owner C",
    };
    const server = {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      name: "Identity test server",
      gameName: "PaperMC",
    } as Server;
    const membershipA: ServerMembership = {
      serverId: server.id,
      userId: ownerA.id,
      permissions: ["servers.read", "servers.power"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    let resolveOwnerB!: (membership: ServerMembership) => void;
    const ownerBMembership = new Promise<ServerMembership>((resolve) => { resolveOwnerB = resolve; });

    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, ownerA, ownerB, ownerC]);
    vi.spyOn(api, "servers").mockResolvedValue([server]);
    vi.spyOn(api, "serverMembership").mockImplementation((_serverId, userId) => {
      if (userId === ownerA.id) return Promise.resolve(membershipA);
      if (userId === ownerB.id) return ownerBMembership;
      if (userId === ownerC.id) return Promise.reject(new Error("membership lookup failed"));
      return Promise.reject(new Error("unexpected membership lookup"));
    });
    const deleteMembership = vi.spyOn(api, "deleteServerMembership").mockResolvedValue(undefined);
    const putMembership = vi.spyOn(api, "putServerMembership").mockResolvedValue(membershipA);
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /Owner A/ }));
    expect(await screen.findByRole("button", { name: "Revoke" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: /Owner B/ }));

    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
    expect(deleteMembership).not.toHaveBeenCalled();

    resolveOwnerB({ ...membershipA, userId: ownerB.id });
    expect(await screen.findByRole("button", { name: "Revoke" })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: /Owner C/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("membership lookup failed");
    expect(screen.getByRole("button", { name: "Save access" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
    expect(putMembership).not.toHaveBeenCalled();
  });

  test("grants a server membership with the selected permissions and CSRF token", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const owner = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner@example.com",
      displayName: "World Owner",
      roles: ["server_owner"],
    };
    const server = {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      name: "Survival Realm",
      gameName: "PaperMC",
    } as Server;
    const grantedMembership: ServerMembership = {
      serverId: server.id,
      userId: owner.id,
      permissions: ["servers.read", "servers.power", "servers.console"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, owner]);
    vi.spyOn(api, "servers").mockResolvedValue([server]);
    vi.spyOn(api, "serverMembership").mockRejectedValue(new ApiError(404, "NOT_FOUND", "Membership not found"));
    const putMembership = vi.spyOn(api, "putServerMembership").mockResolvedValue(grantedMembership);
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /World Owner/ }));
    expect(await screen.findByRole("button", { name: "Save access" })).toBeEnabled();
    await user.click(screen.getByRole("checkbox", { name: /Power controls/ }));
    await user.click(screen.getByRole("checkbox", { name: /Console/ }));
    await user.click(screen.getByRole("button", { name: "Save access" }));

    await waitFor(() => expect(putMembership).toHaveBeenCalledWith(
      server.id,
      owner.id,
      ["servers.read", "servers.power", "servers.console"],
      "csrf-token",
    ));
    expect(await screen.findByRole("status")).toHaveTextContent("Server access saved");
  });

  test("does not apply a completed membership save to a newly selected server", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const owner = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner@example.com",
      displayName: "World Owner",
      roles: ["server_owner"],
    };
    const serverA = {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      name: "Realm A",
      gameName: "PaperMC",
    } as Server;
    const serverB = {
      id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      name: "Realm B",
      gameName: "PaperMC",
    } as Server;
    const savedA: ServerMembership = {
      serverId: serverA.id,
      userId: owner.id,
      permissions: ["servers.read", "servers.power"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    let resolveSave!: (membership: ServerMembership) => void;
    const pendingSave = new Promise<ServerMembership>((resolve) => { resolveSave = resolve; });
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, owner]);
    vi.spyOn(api, "servers").mockResolvedValue([serverA, serverB]);
    const readMembership = vi.spyOn(api, "serverMembership").mockRejectedValue(new ApiError(404, "NOT_FOUND", "Membership not found"));
    const putMembership = vi.spyOn(api, "putServerMembership").mockReturnValue(pendingSave);
    const user = userEvent.setup();

    render(<App />);
    const ownerRow = await screen.findByRole("button", { name: /World Owner/ });
    await user.click(ownerRow);
    const serverSelect = await screen.findByLabelText("Membership server");
    expect(await screen.findByRole("button", { name: "Save access" })).toBeEnabled();
    await user.click(screen.getByRole("checkbox", { name: /Power controls/ }));
    await user.click(screen.getByRole("button", { name: "Save access" }));

    await waitFor(() => expect(putMembership).toHaveBeenCalledWith(
      serverA.id,
      owner.id,
      ["servers.read", "servers.power"],
      "csrf-token",
    ));
    const controlsWereLocked = ownerRow.hasAttribute("disabled") && (serverSelect as HTMLSelectElement).disabled;
    fireEvent.change(serverSelect, { target: { value: serverB.id } });
    await waitFor(() => expect(readMembership).toHaveBeenCalledWith(serverB.id, owner.id));

    await act(async () => {
      resolveSave(savedA);
      await pendingSave;
    });

    expect(serverSelect).toHaveValue(serverB.id);
    expect(screen.getByRole("checkbox", { name: /Power controls/ })).not.toBeChecked();
    expect(screen.queryByRole("button", { name: "Revoke" })).not.toBeInTheDocument();
    expect(controlsWereLocked).toBe(true);
  });

  test("requires an explicit impact confirmation before disabling a local user", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const operator = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "operator@example.com",
      displayName: "Operator",
      roles: ["server_owner"],
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, operator]);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    const updateUser = vi.spyOn(api, "updateUser").mockResolvedValue({ ...operator, status: "disabled" });
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /Operator/ }));
    await user.click(screen.getByRole("button", { name: "Disable" }));

    expect(updateUser).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog", { name: "Disable local user?" });
    expect(within(dialog).getByText("operator@example.com")).toBeInTheDocument();
    expect(within(dialog).getByText(/existing sessions are revoked immediately/i)).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Disable user" }));
    await waitFor(() => expect(updateUser).toHaveBeenCalledWith(
      operator.id,
      { status: "disabled" },
      "csrf-token",
    ));
  });

  test("keeps a failed user disable actionable inside its confirmation dialog", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const operator = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "operator@example.com",
      displayName: "Operator",
      roles: ["server_owner"],
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, operator]);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    const updateUser = vi.spyOn(api, "updateUser").mockRejectedValue(new ApiError(503, "IDENTITY_UPDATE_FAILED", "Unable to disable Operator", true));
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /Operator/ }));
    await user.click(screen.getByRole("button", { name: "Disable" }));
    const dialog = await screen.findByRole("dialog", { name: "Disable local user?" });
    await user.click(within(dialog).getByRole("button", { name: "Disable user" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Unable to disable Operator");
    expect(updateUser).toHaveBeenCalledTimes(1);
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Disable user" })).toBeEnabled();
    expectFocusWithin(dialog);
  });

  test("requires an explicit impact confirmation before revoking a server membership", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const owner = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner@example.com",
      displayName: "World Owner",
      roles: ["server_owner"],
    };
    const server = {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      name: "Survival Realm",
      gameName: "PaperMC",
    } as Server;
    const membership: ServerMembership = {
      serverId: server.id,
      userId: owner.id,
      permissions: ["servers.read", "servers.power"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, owner]);
    vi.spyOn(api, "servers").mockResolvedValue([server]);
    vi.spyOn(api, "serverMembership").mockResolvedValue(membership);
    const deleteMembership = vi.spyOn(api, "deleteServerMembership").mockResolvedValue(undefined);
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /World Owner/ }));
    await user.click(await screen.findByRole("button", { name: "Revoke" }));

    expect(deleteMembership).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog", { name: "Revoke server access?" });
    expect(within(dialog).getByText("owner@example.com")).toBeInTheDocument();
    expect(within(dialog).getByText(/Survival Realm/)).toBeInTheDocument();
    expect(within(dialog).getByText(/immediately lose access/i)).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Revoke server access" }));
    await waitFor(() => expect(deleteMembership).toHaveBeenCalledWith(server.id, owner.id, "csrf-token"));
  });

  test("keeps a failed membership revoke actionable inside its confirmation dialog", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const owner = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner@example.com",
      displayName: "World Owner",
      roles: ["server_owner"],
    };
    const server = {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      name: "Survival Realm",
      gameName: "PaperMC",
    } as Server;
    const membership: ServerMembership = {
      serverId: server.id,
      userId: owner.id,
      permissions: ["servers.read", "servers.power"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, owner]);
    vi.spyOn(api, "servers").mockResolvedValue([server]);
    vi.spyOn(api, "serverMembership").mockResolvedValue(membership);
    const deleteMembership = vi.spyOn(api, "deleteServerMembership").mockRejectedValue(new ApiError(503, "MEMBERSHIP_REVOKE_FAILED", "Unable to revoke Survival Realm access", true));
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /World Owner/ }));
    await user.click(await screen.findByRole("button", { name: "Revoke" }));
    const dialog = await screen.findByRole("dialog", { name: "Revoke server access?" });
    await user.click(within(dialog).getByRole("button", { name: "Revoke server access" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Unable to revoke Survival Realm access");
    expect(deleteMembership).toHaveBeenCalledTimes(1);
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Revoke server access" })).toBeEnabled();
    expectFocusWithin(dialog);
  });

  test("does not apply a failed membership revoke to a newly selected server", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const owner = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "owner@example.com",
      displayName: "World Owner",
      roles: ["server_owner"],
    };
    const serverA = {
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      name: "Realm A",
      gameName: "PaperMC",
    } as Server;
    const serverB = {
      id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      name: "Realm B",
      gameName: "PaperMC",
    } as Server;
    const membershipA: ServerMembership = {
      serverId: serverA.id,
      userId: owner.id,
      permissions: ["servers.read", "servers.power"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    const membershipB: ServerMembership = {
      serverId: serverB.id,
      userId: owner.id,
      permissions: ["servers.read", "servers.files.read"],
      createdAt: "2026-08-08T00:00:00.000Z",
      updatedAt: "2026-08-08T00:00:00.000Z",
    };
    let rejectDelete!: (reason: Error) => void;
    const pendingDelete = new Promise<void>((_resolve, reject) => { rejectDelete = reject; });
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, owner]);
    vi.spyOn(api, "servers").mockResolvedValue([serverA, serverB]);
    const readMembership = vi.spyOn(api, "serverMembership").mockImplementation((serverId) => Promise.resolve(serverId === serverA.id ? membershipA : membershipB));
    const deleteMembership = vi.spyOn(api, "deleteServerMembership").mockReturnValue(pendingDelete);
    const user = userEvent.setup();

    render(<App />);
    const ownerRow = await screen.findByRole("button", { name: /World Owner/ });
    await user.click(ownerRow);
    const serverSelect = await screen.findByLabelText("Membership server");
    await user.click(await screen.findByRole("button", { name: "Revoke" }));
    const dialog = await screen.findByRole("dialog", { name: "Revoke server access?" });
    await user.click(within(dialog).getByRole("button", { name: "Revoke server access" }));

    await waitFor(() => expect(deleteMembership).toHaveBeenCalledWith(serverA.id, owner.id, "csrf-token"));
    const controlsWereLocked = ownerRow.hasAttribute("disabled") && (serverSelect as HTMLSelectElement).disabled;
    fireEvent.change(serverSelect, { target: { value: serverB.id } });
    await waitFor(() => expect(readMembership).toHaveBeenCalledWith(serverB.id, owner.id));

    await act(async () => {
      rejectDelete(new Error("Realm A revoke failed"));
      await pendingDelete.catch(() => undefined);
    });
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

    expect(serverSelect).toHaveValue(serverB.id);
    expect(screen.getByRole("checkbox", { name: /Read files/ })).toBeChecked();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(controlsWereLocked).toBe(true);
  });

  test("requires an explicit impact confirmation before demoting a platform administrator", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const targetAdmin = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "second-admin@example.com",
      displayName: "Second Admin",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, targetAdmin]);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    const updateUser = vi.spyOn(api, "updateUser").mockResolvedValue({ ...targetAdmin, roles: ["server_owner"] });
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /Second Admin/ }));
    await user.click(screen.getByRole("button", { name: "Server owner" }));

    expect(updateUser).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("dialog", { name: "Remove platform administrator access?" });
    expect(within(dialog).getByText("second-admin@example.com")).toBeInTheDocument();
    expect(within(dialog).getByText(/lose global user, node, catalog, and audit administration/i)).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Remove admin access" }));
    await waitFor(() => expect(updateUser).toHaveBeenCalledWith(
      targetAdmin.id,
      { roles: ["server_owner"] },
      "csrf-token",
    ));
  });

  test("keeps a failed administrator demotion actionable inside its confirmation dialog", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const targetAdmin = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "second-admin@example.com",
      displayName: "Second Admin",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, targetAdmin]);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    const updateUser = vi.spyOn(api, "updateUser").mockRejectedValue(new ApiError(503, "ROLE_UPDATE_FAILED", "Unable to remove Second Admin access", true));
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /Second Admin/ }));
    await user.click(screen.getByRole("button", { name: "Server owner" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove platform administrator access?" });
    await user.click(within(dialog).getByRole("button", { name: "Remove admin access" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Unable to remove Second Admin access");
    expect(updateUser).toHaveBeenCalledTimes(1);
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Remove admin access" })).toBeEnabled();
    expectFocusWithin(dialog);
  });

  test("prevents the current administrator from changing their own global role", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const secondAdmin = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "second-admin@example.com",
      displayName: "Second Admin",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, secondAdmin]);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    const updateUser = vi.spyOn(api, "updateUser").mockResolvedValue({ ...adminSession.user, roles: ["server_owner"] });
    const user = userEvent.setup();

    render(<App />);
    const serverOwnerButton = await screen.findByRole("button", { name: "Server owner" });
    await user.click(serverOwnerButton);
    const confirmation = screen.queryByRole("dialog", { name: "Remove platform administrator access?" });
    if (confirmation) {
      await user.click(within(confirmation).getByRole("button", { name: "Remove admin access" }));
    }

    expect(updateUser).not.toHaveBeenCalled();
    expect(serverOwnerButton).toBeDisabled();
    expect(screen.getByRole("button", { name: "Platform admin" })).toBeDisabled();
    expect(screen.getByText(/sign in as a different platform administrator to change this account's global role/i)).toBeInTheDocument();
  });

  test("exposes the selected global role as a pressed button state", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user]);
    vi.spyOn(api, "servers").mockResolvedValue([]);

    render(<App />);

    expect(await screen.findByRole("button", { name: "Platform admin" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Server owner" })).toHaveAttribute("aria-pressed", "false");
  });

  test("provides text alternatives for active and disabled user status dots", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const activeOwner = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000001",
      email: "active-owner@example.com",
      displayName: "Active Owner",
      roles: ["server_owner"],
    };
    const disabledOwner = {
      ...activeOwner,
      id: "10000000-0000-4000-8000-000000000002",
      email: "disabled-owner@example.com",
      displayName: "Disabled Owner",
      status: "disabled" as const,
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user, activeOwner, disabledOwner]);
    vi.spyOn(api, "servers").mockResolvedValue([]);

    render(<App />);

    const activeRow = await screen.findByRole("button", { name: /Active Owner/ });
    const disabledRow = screen.getByRole("button", { name: /Disabled Owner/ });
    expect(within(activeRow).getByText("Status: active")).toBeInTheDocument();
    expect(within(disabledRow).getByText("Status: disabled")).toBeInTheDocument();
  });

  test("lets an administrator create a local user and issue a one-time reset token", async () => {
    window.history.replaceState({}, "", "/users");
    const adminSession: Session = {
      user: {
        id: "00000000-0000-4000-8000-000000000001",
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: "2026-08-08T00:00:00.000Z",
        updatedAt: "2026-08-08T00:00:00.000Z",
      },
      csrfToken: "csrf-token",
      environment: "development",
    };
    const created = {
      ...adminSession.user,
      id: "10000000-0000-4000-8000-000000000002",
      email: "operator@example.com",
      displayName: "Operator",
      roles: ["server_owner"],
    };
    vi.spyOn(api, "setupStatus").mockResolvedValue({ required: false });
    vi.spyOn(api, "session").mockResolvedValue(adminSession);
    vi.spyOn(api, "users").mockResolvedValue([adminSession.user]);
    vi.spyOn(api, "servers").mockResolvedValue([]);
    const createUser = vi.spyOn(api, "createUser").mockResolvedValue(created);
    const issueToken = vi.spyOn(api, "issuePasswordResetToken").mockResolvedValue({ token: "reset-token-abcdefghijklmnopqrstuvwxyz-1234", expiresAt: "2026-08-08T00:15:00.000Z" });
    const user = userEvent.setup();

    render(<App />);
    await user.click(await screen.findByRole("button", { name: /new user/i }));
    await user.type(screen.getByLabelText("Email"), "operator@example.com");
    await user.type(screen.getByLabelText("Display name"), "Operator");
    await user.type(screen.getByLabelText("Temporary password"), "operator-password");
    await user.click(screen.getByRole("button", { name: /^create user$/i }));

    await waitFor(() => expect(createUser).toHaveBeenCalledWith({
      email: "operator@example.com",
      displayName: "Operator",
      password: "operator-password",
      roles: ["server_owner"],
    }, "csrf-token"));
    await user.click(await screen.findByRole("button", { name: /issue reset token/i }));
    const dialog = await screen.findByRole("dialog", { name: /one-time reset token/i });
    expect(screen.getByText("reset-token-abcdefghijklmnopqrstuvwxyz-1234")).toBeInTheDocument();
    expect(issueToken).toHaveBeenCalledWith(created.id, "csrf-token");

    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByText("reset-token-abcdefghijklmnopqrstuvwxyz-1234")).not.toBeInTheDocument());
    expect(screen.queryByRole("dialog", { name: /one-time reset token/i })).not.toBeInTheDocument();
  });
});
