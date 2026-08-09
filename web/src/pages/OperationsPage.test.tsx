import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Operation, Server } from "../lib/types";
import { OperationsPage } from "./OperationsPage";

const mocks = vi.hoisted(() => ({
  operations: vi.fn(),
  servers: vi.fn(),
}));

vi.mock("../lib/api", () => ({
  api: {
    operations: mocks.operations,
    servers: mocks.servers,
  },
}));

const server: Server = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  name: "Skyline Survival",
  description: "",
  gameId: "io.gugumanager.papermc",
  gameBundleDigest: `sha256:${"ab".repeat(32)}`,
  gameDefinitionVersion: "1.0.0",
  gameName: "PaperMC",
  gameVersion: "1.21.8",
  nodeId: "11111111-1111-4111-8111-111111111111",
  nodeName: "north-1",
  lifecycleState: "ready",
  desiredPower: "running",
  observedPower: "running",
  nodeCondition: "available",
  healthCondition: "healthy",
  generation: 3,
  observedGeneration: 3,
  observedAt: "2026-08-08T08:00:00Z",
  allocation: "10.0.10.21:25565",
  ownerName: "GuGu Admin",
  metrics: { cpuPercent: 12, memoryBytes: 1_024, memoryLimitBytes: 4_096, diskBytes: 1_024, diskLimitBytes: 8_192 },
  metricHistory: [],
  updatedAt: "2026-08-08T08:00:00Z",
};

const activeOperation: Operation = {
  id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  serverId: server.id,
  nodeId: server.nodeId,
  type: "restart",
  status: "running",
  progress: 62,
  generation: 3,
  attempt: 1,
  maxAttempts: 1,
  leaseOwner: "development-memory-worker",
  leaseExpiresAt: "2026-08-08T08:01:00Z",
  checkpoint: "waiting-for-runtime",
  error: null,
  createdAt: "2026-08-08T08:00:00Z",
  updatedAt: "2026-08-08T08:00:20Z",
};

const failedOperation: Operation = {
  ...activeOperation,
  id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  type: "reconcile",
  status: "failed",
  progress: 100,
  checkpoint: "failed",
  leaseOwner: null,
  leaseExpiresAt: null,
  error: { code: "OPERATION_STALE", message: "The server target changed before reconciliation completed.", retryable: false },
  updatedAt: "2026-08-08T08:00:40Z",
};

describe("OperationsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.operations.mockResolvedValue([failedOperation, activeOperation]);
    mocks.servers.mockResolvedValue([server]);
  });

  it("separates running work from failures and links each operation to its server", async () => {
    render(
      <MemoryRouter initialEntries={["/operations"]}>
        <Routes>
          <Route path="/operations" element={<OperationsPage />} />
          <Route path="/servers/:serverId" element={<div>Server detail</div>} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByRole("heading", { name: "Task queue" })).toBeInTheDocument();
    expect(screen.getByText("Restart server")).toBeInTheDocument();
    expect(screen.getByText("The server target changed before reconciliation completed.")).toBeInTheDocument();
    const serverLinks = screen.getAllByRole("link", { name: /Open server Skyline Survival/ });
    expect(serverLinks).toHaveLength(2);
    serverLinks.forEach((link) => expect(link).toHaveAttribute("href", `/servers/${server.id}`));

    fireEvent.click(screen.getByRole("tab", { name: /Failures/ }));
    await waitFor(() => expect(screen.queryByText("Restart server")).not.toBeInTheDocument());
    expect(screen.getByText("Synchronize server state")).toBeInTheDocument();
  });

  it("classifies leased and canceled work and falls back to immutable server identifiers", async () => {
    const unknownServerId = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
    const leasedOperation: Operation = {
      ...activeOperation,
      id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
      serverId: unknownServerId,
      status: "leased",
      progress: 8,
      checkpoint: "lease-acquired",
    };
    const canceledOperation: Operation = {
      ...activeOperation,
      id: "ffffffff-ffff-4fff-8fff-ffffffffffff",
      serverId: unknownServerId,
      type: "delete",
      status: "canceled",
      progress: 20,
      checkpoint: "canceled",
      leaseOwner: null,
      leaseExpiresAt: null,
    };
    mocks.operations.mockResolvedValue([leasedOperation, canceledOperation]);
    mocks.servers.mockResolvedValue([]);

    render(<MemoryRouter><OperationsPage /></MemoryRouter>);

    expect(await screen.findByText("Leased")).toBeInTheDocument();
    const fallbackLinks = screen.getAllByRole("link", { name: /Open server dddddddd\.\.\.dddd/ });
    expect(fallbackLinks).toHaveLength(2);
    fallbackLinks.forEach((link) => expect(link).toHaveAttribute("href", `/servers/${unknownServerId}`));

    fireEvent.click(screen.getByRole("tab", { name: /Completed/ }));
    expect(screen.getByText("Delete server")).toBeInTheDocument();
    expect(screen.getByText("Canceled")).toBeInTheDocument();
    expect(screen.queryByText("Restart server")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: /Active/ }));
    expect(screen.getByText("Restart server")).toBeInTheDocument();
    expect(screen.queryByText("Delete server")).not.toBeInTheDocument();
  });

  it("keeps the last snapshot visible when a manual refresh fails", async () => {
    mocks.operations
      .mockResolvedValueOnce([activeOperation])
      .mockRejectedValueOnce(new Error("control plane unavailable"));

    render(<MemoryRouter><OperationsPage /></MemoryRouter>);
    expect(await screen.findByText("Restart server")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Refresh/ }));

    expect(await screen.findByText("Live refresh failed.")).toBeInTheDocument();
    expect(screen.getByText("control plane unavailable")).toBeInTheDocument();
    expect(screen.getByText("Restart server")).toBeInTheDocument();
    expect(screen.getByText("Stale snapshot")).toBeInTheDocument();
  });

  it("renders a stable empty state when no operation has been accepted", async () => {
    mocks.operations.mockResolvedValue([]);
    mocks.servers.mockResolvedValue([]);

    render(<MemoryRouter><OperationsPage /></MemoryRouter>);

    expect(await screen.findByRole("heading", { name: "Task queue" })).toBeInTheDocument();
    expect(screen.getByText("No operations in this view.")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /All/ })).toHaveAttribute("aria-selected", "true");
  });
});
