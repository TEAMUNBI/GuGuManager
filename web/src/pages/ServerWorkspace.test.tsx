import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FileEntry, Operation, Server, ServerPermission } from "../lib/types";
import { ServerWorkspace } from "./ServerWorkspace";

const mocks = vi.hoisted(() => ({
  files: vi.fn(),
  operation: vi.fn(),
  operations: vi.fn(),
  power: vi.fn(),
  server: vi.fn(),
  serverPermissions: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: {
    files: mocks.files,
    operation: mocks.operation,
    operations: mocks.operations,
    power: mocks.power,
    server: mocks.server,
    serverPermissions: mocks.serverPermissions,
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
  name: "File test server",
  gameName: "PaperMC",
  gameVersion: "1.21.8",
  nodeName: "node-1",
  nodeCondition: "available",
  allocation: "127.0.0.1:25565",
  observedPower: "stopped",
  desiredPower: "stopped",
  lifecycleState: "ready",
  healthCondition: "healthy",
  generation: 1,
  observedGeneration: 1,
  observedAt: "2026-08-08T08:00:00Z",
  ownerName: "GuGu Admin",
  gameId: "io.gugumanager.papermc",
  gameDefinitionVersion: "1.0.0",
  metrics: {
    cpuPercent: 0,
    memoryBytes: 0,
    memoryLimitBytes: 4_294_967_296,
    diskBytes: 0,
    diskLimitBytes: 26_843_545_600,
  },
  updatedAt: "2026-08-08T08:00:00Z",
} as Server;

const rootEntries: FileEntry[] = [
  { name: "config", path: "config", kind: "directory", sizeBytes: 0, modifiedAt: "2026-08-07T12:00:00Z" },
  { name: "Old settings", path: "old.txt", kind: "file", sizeBytes: 12, modifiedAt: "2026-08-07T12:00:00Z" },
];

const queuedPowerOperation: Operation = {
  id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  serverId: server.id,
  nodeId: "11111111-1111-4111-8111-111111111111",
  type: "start",
  status: "queued",
  progress: 0,
  generation: 2,
  attempt: 1,
  maxAttempts: 1,
  leaseOwner: null,
  leaseExpiresAt: null,
  checkpoint: "queued",
  error: null,
  createdAt: "2026-08-08T08:00:00Z",
  updatedAt: "2026-08-08T08:00:00Z",
};

const allPermissions: ServerPermission[] = [
  "servers.backups.create", "servers.backups.delete", "servers.backups.read", "servers.backups.restore",
  "servers.console", "servers.files.read", "servers.files.write", "servers.network.read", "servers.network.write",
  "servers.power", "servers.read", "servers.startup.read", "servers.startup.write",
];

function WorkspaceWithLocation() {
  const location = useLocation();
  return <><ServerWorkspace /><output data-testid="workspace-location">{location.pathname}</output></>;
}

describe("ServerWorkspace file listings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    window.localStorage.setItem("gugu.locale", "en");
    mocks.server.mockResolvedValue(server);
    mocks.serverPermissions.mockResolvedValue({ serverId: server.id, permissions: allPermissions });
    mocks.operations.mockResolvedValue([]);
  });

  it("exposes every authorized page in a labelled selector and navigates to the selected route", async () => {
    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<WorkspaceWithLocation />} />
        </Routes>
      </MemoryRouter>,
    );

    const selector = await screen.findByRole("combobox", { name: "Server pages" });
    expect(selector).toHaveValue("overview");
    expect(within(selector).getAllByRole("option").map((option) => ({
      label: option.textContent,
      value: option.getAttribute("value"),
    }))).toEqual([
      { label: "Overview", value: "overview" },
      { label: "Console", value: "console" },
      { label: "Files", value: "files" },
      { label: "Backups", value: "backups" },
      { label: "Network", value: "network" },
      { label: "Startup", value: "startup" },
      { label: "Task history", value: "activity" },
      { label: "Server details", value: "settings" },
    ]);

    fireEvent.change(selector, { target: { value: "activity" } });

    await waitFor(() => expect(screen.getByTestId("workspace-location")).toHaveTextContent(`/servers/${server.id}/activity`));
    expect(selector).toHaveValue("activity");
  });

  it("keeps every permission-authorized destination discoverable in the selector", async () => {
    mocks.serverPermissions.mockResolvedValue({
      serverId: server.id,
      permissions: ["servers.read", "servers.console", "servers.files.read"],
    });

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    const selector = await screen.findByRole("combobox", { name: "Server pages" });
    expect(within(selector).getAllByRole("option").map((option) => option.getAttribute("value"))).toEqual([
      "overview",
      "console",
      "files",
      "activity",
      "settings",
    ]);
  });

  it("retains roving keyboard navigation in the desktop tablist", async () => {
    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<WorkspaceWithLocation />} />
        </Routes>
      </MemoryRouter>,
    );

    const tablist = await screen.findByRole("tablist", { name: "Server pages" });
    const overviewTab = within(tablist).getByRole("tab", { name: "Overview" });
    const settingsTab = within(tablist).getByRole("tab", { name: "Server details" });
    expect(overviewTab).toHaveAttribute("tabindex", "0");
    expect(settingsTab).toHaveAttribute("tabindex", "-1");

    overviewTab.focus();
    fireEvent.keyDown(overviewTab, { key: "End" });

    await waitFor(() => expect(screen.getByTestId("workspace-location")).toHaveTextContent(`/servers/${server.id}/settings`));
    expect(settingsTab).toHaveFocus();
    expect(settingsTab).toHaveAttribute("aria-selected", "true");
    expect(settingsTab).toHaveAttribute("tabindex", "0");
  });

  it("uses informational memory tones and a non-ring player capacity meter", async () => {
    mocks.server.mockResolvedValue({
      ...server,
      metrics: { ...server.metrics, playersOnline: 3, playersMax: 12 },
    } satisfies Server);

    const { container } = render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    const memoryTrend = await screen.findByRole("img", { name: "Memory trend over the last hour" });
    expect(memoryTrend).toHaveClass("bars-blue");
    expect(memoryTrend).not.toHaveClass("bars-orange");
    expect(container.querySelector(".player-radar")).not.toBeInTheDocument();
    expect(container.querySelector(".player-readout")).not.toBeInTheDocument();
    expect(container.querySelector(".player-capacity")).toHaveTextContent("25%");
  });

  it("shows unavailable instead of a zero player count when telemetry is missing", async () => {
    mocks.server.mockResolvedValue({
      ...server,
      metrics: {
        cpuPercent: server.metrics.cpuPercent,
        memoryBytes: server.metrics.memoryBytes,
        memoryLimitBytes: server.metrics.memoryLimitBytes,
        diskBytes: server.metrics.diskBytes,
        diskLimitBytes: server.metrics.diskLimitBytes,
      },
    } satisfies Server);

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    const playersLabel = await screen.findByText("Players");
    const playerMetric = playersLabel.closest(".workspace-metric");
    expect(playerMetric).toHaveTextContent("Player telemetry unavailable");
    expect(playerMetric).not.toHaveTextContent("0 / 0");
  });

  it("does not fabricate trend samples when metric history is unavailable", async () => {
    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    const cpuTrend = await screen.findByRole("img", { name: "CPU trend over the last hour" });
    const memoryTrend = screen.getByRole("img", { name: "Memory trend over the last hour" });
    expect(cpuTrend.querySelectorAll("span")).toHaveLength(0);
    expect(memoryTrend.querySelectorAll("span")).toHaveLength(0);
  });

  it("labels maintenance without reporting the node as offline", async () => {
    mocks.server.mockResolvedValue({ ...server, nodeCondition: "maintenance" } satisfies Server);

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Maintenance")).toBeInTheDocument();
    expect(screen.queryByText("Node offline")).not.toBeInTheDocument();
  });

  it("surfaces an unhealthy server condition alongside power state", async () => {
    mocks.server.mockResolvedValue({ ...server, observedPower: "running", healthCondition: "unhealthy" } satisfies Server);

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Running")).toBeInTheDocument();
    expect(screen.getByText("Unhealthy")).toBeInTheDocument();
  });

  it("reports a structured terminal power failure instead of treating acceptance as success", async () => {
    mocks.power.mockResolvedValue(queuedPowerOperation);
    mocks.operation.mockResolvedValue({
      ...queuedPowerOperation,
      status: "failed",
      progress: 100,
      checkpoint: "failed",
      error: { code: "OPERATION_STALE", message: "The server state changed before power was applied.", retryable: false },
      updatedAt: "2026-08-08T08:00:01Z",
    } satisfies Operation);

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "Start" }));

    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("Start request accepted", "warning"));
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("The server state changed before power was applied.", "danger"), { timeout: 2_000 });
    expect(mocks.toast).not.toHaveBeenCalledWith("Start request accepted", "success");
  });

  it("does not expose entries or mutations from the previous path after a directory load fails", async () => {
    mocks.files
      .mockResolvedValueOnce(rootEntries)
      .mockRejectedValueOnce(new Error("directory unavailable"));

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}/files`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Old settings")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New file" })).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: "Open config" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("directory unavailable");
    expect(screen.queryByText("Old settings")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Old settings" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New file" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
    await waitFor(() => expect(mocks.files).toHaveBeenNthCalledWith(2, server.id, "config"));
  });

  it("clears the current entries when a refresh fails", async () => {
    mocks.files
      .mockResolvedValueOnce(rootEntries)
      .mockRejectedValueOnce(new Error("refresh unavailable"));

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}/files`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Old settings")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Refresh files" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("refresh unavailable");
    expect(screen.queryByText("Old settings")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New file" })).toBeDisabled();
  });
  it("loads server activity from the resource-authorized operation list", async () => {
    mocks.operations.mockResolvedValue([
      queuedPowerOperation,
      {
        ...queuedPowerOperation,
        id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
        serverId: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
        type: "stop",
      } satisfies Operation,
    ]);

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}/activity`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Start server")).toBeInTheDocument();
    expect(screen.queryByText("Stop server")).not.toBeInTheDocument();
    expect(mocks.operations).toHaveBeenCalledTimes(1);
  });

  it("does not mount a protected deep link or request its data without permission", async () => {
    mocks.serverPermissions.mockResolvedValue({ serverId: server.id, permissions: ["servers.read"] });

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}/files`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(/permission to use this server page|无权访问此服务器页面/i);
    expect(screen.queryByRole("tab", { name: "Files" })).not.toBeInTheDocument();
    expect(mocks.files).not.toHaveBeenCalled();
  });

  it("keeps the file browser read-only without the file write permission", async () => {
    mocks.serverPermissions.mockResolvedValue({
      serverId: server.id,
      permissions: ["servers.read", "servers.files.read"],
    });
    mocks.files.mockResolvedValue(rootEntries);

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}/files`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Old settings")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New file" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New directory" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Move Old settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Old settings" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByText("Old settings"));
    await waitFor(() => expect(mocks.files).toHaveBeenCalledWith(server.id, ""));
  });

  it("hides power controls without the power permission", async () => {
    mocks.serverPermissions.mockResolvedValue({ serverId: server.id, permissions: ["servers.read"] });

    render(
      <MemoryRouter initialEntries={[`/servers/${server.id}`]}>
        <Routes>
          <Route path="/servers/:serverId/*" element={<ServerWorkspace />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("heading", { name: server.name })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Force terminate" })).not.toBeInTheDocument();
    expect(mocks.power).not.toHaveBeenCalled();
  });
});
