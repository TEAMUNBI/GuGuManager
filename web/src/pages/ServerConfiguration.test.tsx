import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Allocation, Operation, Server, Startup, StartupValue } from "../lib/types";
import { ServerWorkspace } from "./ServerWorkspace";

const mocks = vi.hoisted(() => ({
  allocations: vi.fn(),
  operation: vi.fn(),
  server: vi.fn(),
  serverPermissions: vi.fn(),
  setPrimaryAllocation: vi.fn(),
  startup: vi.fn(),
  toast: vi.fn(),
  updateStartup: vi.fn(),
}));

vi.mock("../lib/api", () => ({
  ApiError: class ApiError extends Error {
    constructor(public status: number, public code: string, message: string) { super(message); }
  },
  api: {
    allocations: mocks.allocations,
    operation: mocks.operation,
    server: mocks.server,
    serverPermissions: mocks.serverPermissions,
    setPrimaryAllocation: mocks.setPrimaryAllocation,
    startup: mocks.startup,
    updateStartup: mocks.updateStartup,
  },
}));

vi.mock("../app/App", () => ({
  useAppContext: () => ({
    session: { csrfToken: "csrf-token" },
    toast: mocks.toast,
  }),
}));

const server = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  name: "Configuration test server",
  gameName: "PaperMC",
  gameVersion: "1.21.8",
  nodeName: "node-1",
  nodeCondition: "available",
  allocation: "10.0.10.21:25565",
  observedPower: "stopped",
  desiredPower: "stopped",
  generation: 12,
} as Server;

const allocations: Allocation[] = [
  { id: "allocation-1", serverId: server.id, nodeId: "node-1", bindIp: "10.0.10.21", port: 25565, protocol: "tcp", primary: true, createdAt: "2026-08-07T12:00:00Z", updatedAt: "2026-08-07T12:00:00Z" },
  { id: "allocation-2", serverId: server.id, nodeId: "node-1", bindIp: "10.0.10.21", port: 25566, protocol: "tcp", primary: false, createdAt: "2026-08-07T12:00:00Z", updatedAt: "2026-08-07T12:00:00Z" },
];

const startup: Startup = {
  serverId: server.id,
  generation: 19,
  command: { executable: "java", args: ["-Xmx4096M", "-jar", "paper.jar", "--nogui"] },
  variables: [
    { key: "memory_mb", type: "integer", secret: false, required: true, hasValue: true, value: 4096, minimum: 1024, maximum: 32768 },
    { key: "server_mode", type: "string", secret: false, required: false, hasValue: true, value: "survival", enumValues: ["survival", "creative"] },
    { key: "fixed_mode", type: "string", secret: false, required: false, hasValue: true, value: "survival", constValue: "survival", enumValues: ["survival", "creative"] },
    { key: "rcon_password", type: "string", secret: true, required: true, hasValue: true, minLength: 12, maxLength: 128 },
  ],
};

const completedOperation = {
  id: "operation-1",
  serverId: server.id,
  nodeId: "node-1",
  type: "reconcile",
  status: "succeeded",
  progress: 100,
  generation: 13,
  attempt: 1,
  maxAttempts: 1,
  leaseOwner: null,
  leaseExpiresAt: null,
  checkpoint: "completed",
  error: null,
  createdAt: "2026-08-07T12:00:00Z",
  updatedAt: "2026-08-07T12:00:01Z",
} as Operation;

describe("ServerWorkspace Network and Startup tabs", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.setItem("gugu.locale", "en");
    mocks.server.mockResolvedValue(server);
    mocks.serverPermissions.mockResolvedValue({ serverId: server.id, permissions: [
      "servers.backups.create", "servers.backups.delete", "servers.backups.read", "servers.backups.restore",
      "servers.console", "servers.files.read", "servers.files.write", "servers.network.read", "servers.network.write",
      "servers.power", "servers.read", "servers.startup.read", "servers.startup.write",
    ] });
    mocks.allocations.mockResolvedValue(allocations);
    mocks.startup.mockResolvedValue(startup);
    mocks.operation.mockResolvedValue(completedOperation);
    mocks.setPrimaryAllocation.mockResolvedValue(completedOperation);
    mocks.updateStartup.mockResolvedValue(completedOperation);
  });

  it("switches the primary allocation using the current server generation", async () => {
    renderWorkspace("network");

    expect(await screen.findByRole("heading", { name: "Network allocations" })).toBeInTheDocument();
    const setPrimary = await screen.findByRole("button", { name: "Set 10.0.10.21:25566 as primary" });
    fireEvent.click(setPrimary);

    await waitFor(() => expect(mocks.setPrimaryAllocation).toHaveBeenCalledWith(
      server.id,
      "allocation-2",
       12,
      "csrf-token",
    ));
  });

  it("renders schema-driven startup controls without exposing the stored secret", async () => {
    renderWorkspace("startup");

    expect(await screen.findByRole("heading", { name: "Startup configuration" })).toBeInTheDocument();
    expect(screen.getByText("java -Xmx4096M -jar paper.jar --nogui")).toBeInTheDocument();
    const secret = screen.getByLabelText("rcon_password") as HTMLInputElement;
    expect(secret).toHaveValue("");
    expect(secret).toHaveAttribute("placeholder", "Configured. Enter a new value to replace it");

    fireEvent.change(screen.getByLabelText("memory_mb"), { target: { value: "6144" } });
    fireEvent.change(secret, { target: { value: "replacement-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save startup" }));

    await waitFor(() => expect(mocks.updateStartup).toHaveBeenCalledWith(
      server.id,
      { memory_mb: 6144, rcon_password: "replacement-secret" },
       19,
      "csrf-token",
    ));
    expect(screen.queryByDisplayValue("development-secret")).not.toBeInTheDocument();
  });

  it("clears optional non-secret variables and locks constant enum controls", async () => {
    renderWorkspace("startup");

    expect(await screen.findByRole("heading", { name: "Startup configuration" })).toBeInTheDocument();
    expect(screen.getByLabelText("fixed_mode")).toBeDisabled();
    fireEvent.click(screen.getAllByRole("button", { name: "Clear" })[0]);
    fireEvent.click(screen.getByRole("button", { name: "Save startup" }));

    await waitFor(() => expect(mocks.updateStartup).toHaveBeenCalledWith(
      server.id,
      { server_mode: null },
      19,
      "csrf-token",
    ));
  });

  it("prefills missing constant values and submits string, zero, and false constants", async () => {
    mocks.startup.mockResolvedValue({
      ...startup,
      variables: [
        { key: "locked_name", type: "string", secret: false, required: true, hasValue: false, constValue: "Friday Factory" },
        { key: "locked_slots", type: "integer", secret: false, required: true, hasValue: false, constValue: 0 },
        { key: "locked_public", type: "boolean", secret: false, required: true, hasValue: false, constValue: false },
      ],
    } satisfies Startup);
    renderWorkspace("startup");

    expect(await screen.findByRole("heading", { name: "Startup configuration" })).toBeInTheDocument();
    expect(screen.getByLabelText("locked_name")).toHaveValue("Friday Factory");
    expect(screen.getByLabelText("locked_slots")).toHaveValue(0);
    expect(screen.getByLabelText("locked_public")).not.toBeChecked();
    expect(screen.getByLabelText("locked_name")).toBeDisabled();
    expect(screen.getByLabelText("locked_slots")).toBeDisabled();
    expect(screen.getByLabelText("locked_public")).toBeDisabled();

    const save = screen.getByRole("button", { name: "Save startup" });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() => expect(mocks.updateStartup).toHaveBeenCalledWith(
      server.id,
      { locked_name: "Friday Factory", locked_slots: 0, locked_public: false },
      19,
      "csrf-token",
    ));
  });

  it("preserves a required __proto__ startup key from its default through an update", async () => {
    mocks.startup.mockResolvedValue({
      ...startup,
      variables: [
        { key: "__proto__", type: "string", secret: false, required: true, hasValue: false, default: "default-world" },
      ],
    } satisfies Startup);
    renderWorkspace("startup");

    const input = await screen.findByLabelText("__proto__");
    expect(input).toHaveValue("default-world");
    fireEvent.change(input, { target: { value: "updated-world" } });
    fireEvent.click(screen.getByRole("button", { name: "Save startup" }));

    await waitFor(() => expect(mocks.updateStartup).toHaveBeenCalled());
    const [targetServerId, values, generation, csrfToken] = mocks.updateStartup.mock.calls[0] as [string, Record<string, StartupValue>, number, string];
    expect(targetServerId).toBe(server.id);
    expect(Object.keys(values)).toEqual(["__proto__"]);
    expect(Object.hasOwn(values, "__proto__")).toBe(true);
    expect(values.__proto__).toBe("updated-world");
    expect(generation).toBe(19);
    expect(csrfToken).toBe("csrf-token");
  });

  it("validates startup string limits by Unicode code points", async () => {
    mocks.startup.mockResolvedValue({
      ...startup,
      variables: [
        { key: "server_name", type: "string", secret: false, required: true, hasValue: true, value: "Friday Factory", minLength: 1, maxLength: 64 },
      ],
    } satisfies Startup);
    renderWorkspace("startup");

    expect(await screen.findByRole("heading", { name: "Startup configuration" })).toBeInTheDocument();
    const fortyCodePoints = "🎮".repeat(40);
    fireEvent.change(screen.getByLabelText("server_name"), { target: { value: fortyCodePoints } });
    fireEvent.click(screen.getByRole("button", { name: "Save startup" }));

    await waitFor(() => expect(mocks.updateStartup).toHaveBeenCalledWith(
      server.id,
      { server_name: fortyCodePoints },
      19,
      "csrf-token",
    ));
  });

  it("rejects fractional or unsafe startup integers before calling the API", async () => {
		mocks.startup.mockResolvedValue({
			...startup,
			variables: [
				{ key: "player_slots", type: "integer", secret: false, required: true, hasValue: true, value: 15 },
			],
		} satisfies Startup);
		renderWorkspace("startup");

		expect(await screen.findByRole("heading", { name: "Startup configuration" })).toBeInTheDocument();
		const input = screen.getByLabelText("player_slots");
		for (const invalid of ["15.0000000000000000000000000000000001", "9007199254740992"]) {
			fireEvent.change(input, { target: { value: invalid } });
			fireEvent.click(screen.getByRole("button", { name: "Save startup" }));
			expect(await screen.findByText("player_slots is outside its allowed integer range.")).toBeInTheDocument();
			expect(mocks.updateStartup).not.toHaveBeenCalled();
		}
	});
});

function renderWorkspace(tab: "network" | "startup") {
  return render(
    <MemoryRouter initialEntries={[`/servers/${server.id}/${tab}`]}>
      <Routes>
        <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
      </Routes>
    </MemoryRouter>,
  );
}
