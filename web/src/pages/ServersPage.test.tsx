import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GameDefinition, Node, Operation, Server as ServerModel } from "../lib/types";
import { ServersPage } from "./ServersPage";

const mocks = vi.hoisted(() => ({
  createServer: vi.fn(),
  games: vi.fn(),
  nodes: vi.fn(),
  operation: vi.fn(),
  servers: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../lib/api", () => ({
  api: {
    createServer: mocks.createServer,
    games: mocks.games,
    nodes: mocks.nodes,
    operation: mocks.operation,
    servers: mocks.servers,
  },
}));

vi.mock("../app/App", () => ({
  useAppContext: () => ({
    session: { csrfToken: "csrf-token", user: { roles: ["platform_admin"] } },
    toast: mocks.toast,
  }),
}));

const game: GameDefinition = {
  id: "io.gugumanager.papermc",
  bundleDigest: `sha256:${"ab".repeat(32)}`,
  name: "PaperMC",
  summary: "Paper server",
  version: "1.0.0",
  gameVersion: "1.21.8",
  status: "approved",
  capabilities: [],
  platforms: ["linux/amd64"],
  servers: 0,
  icon: "server",
  defaultMemoryMb: 4096,
  defaultDiskGb: 25,
};

const node: Node = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  name: "node-1",
  condition: "available",
  version: "0.1.0",
  region: "local",
  address: "127.0.0.1",
  lastHeartbeatAt: "2026-08-08T08:00:00Z",
  cpuCores: 8,
  memoryBytes: 17_179_869_184,
  diskBytes: 107_374_182_400,
  allocatedMemoryBytes: 0,
  allocatedDiskBytes: 0,
  runningServers: 0,
  totalServers: 0,
  capabilities: [],
};

const queuedProvision: Operation = {
  id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  serverId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  nodeId: node.id,
  type: "provision",
  status: "queued",
  progress: 0,
  generation: 1,
  attempt: 1,
  maxAttempts: 1,
  leaseOwner: null,
  leaseExpiresAt: null,
  checkpoint: "queued",
  error: null,
  createdAt: "2026-08-08T08:00:00Z",
  updatedAt: "2026-08-08T08:00:00Z",
};

const listedServer: ServerModel = {
  id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
  name: "Aurora Isles",
  description: "Development server",
  gameId: game.id,
  gameBundleDigest: game.bundleDigest,
  gameDefinitionVersion: game.version,
  gameName: game.name,
  gameVersion: game.gameVersion,
  nodeId: node.id,
  nodeName: "nimbus-east-01",
  lifecycleState: "ready",
  desiredPower: "running",
  observedPower: "running",
  nodeCondition: "offline",
  healthCondition: "unhealthy",
  generation: 2,
  observedGeneration: 2,
  observedAt: "2026-08-08T08:00:00Z",
  allocation: "10.0.10.21:25565",
  ownerName: "GuGu Admin",
  metrics: {
    cpuPercent: 18,
    memoryBytes: 1_073_741_824,
    memoryLimitBytes: 4_294_967_296,
    diskBytes: 5_368_709_120,
    diskLimitBytes: 26_843_545_600,
  },
  metricHistory: [],
  updatedAt: "2026-08-08T08:00:00Z",
};

describe("ServersPage provisioning", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.servers.mockResolvedValue([]);
    mocks.games.mockResolvedValue([game]);
    mocks.nodes.mockResolvedValue([node]);
  });

  it("waits for the provision terminal state and reports its structured failure", async () => {
    mocks.createServer.mockResolvedValue(queuedProvision);
    mocks.operation.mockResolvedValue({
      ...queuedProvision,
      status: "failed",
      progress: 100,
      checkpoint: "failed",
      error: { code: "OPERATION_STALE", message: "The server state changed before provisioning completed.", retryable: false },
      updatedAt: "2026-08-08T08:00:01Z",
    } satisfies Operation);

    render(
      <MemoryRouter initialEntries={["/servers?create=1"]}>
        <Routes>
          <Route path="/servers" element={<ServersPage />} />
          <Route path="/servers/:serverId" element={<div>Server detail</div>} />
        </Routes>
      </MemoryRouter>,
    );

    fireEvent.change(await screen.findByPlaceholderText("e.g. Moonlit Valley"), { target: { value: "Failed provision" } });
    fireEvent.click(screen.getByRole("button", { name: /Create server/ }));

    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("Server creation accepted", "warning"));
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("The server state changed before provisioning completed.", "danger"), { timeout: 2_000 });
    expect(screen.queryByText("Server detail")).not.toBeInTheDocument();
  });

  it("renders node state and allocation as readable metadata instead of outlined tags", async () => {
    mocks.servers.mockResolvedValue([listedServer]);

    const { container } = render(
      <MemoryRouter initialEntries={["/servers"]}>
        <Routes>
          <Route path="/servers" element={<ServersPage />} />
        </Routes>
      </MemoryRouter>,
    );

    const nodeName = await screen.findByText("nimbus-east-01");
    const metadata = nodeName.closest(".server-runtime-meta");
    expect(metadata).not.toBeNull();
    expect(metadata?.querySelector("code")).toHaveTextContent("10.0.10.21:25565");
    expect(metadata?.querySelector(".node-offline")).toBeInTheDocument();
    expect(metadata).toHaveTextContent("Offline");
    expect(container.querySelector(".server-tags")).not.toBeInTheDocument();
  });

  it("counts unhealthy servers as needing attention and exposes the health state", async () => {
    mocks.servers.mockResolvedValue([{
      ...listedServer,
      nodeCondition: "available",
      healthCondition: "unhealthy",
    } satisfies ServerModel]);

    render(
      <MemoryRouter initialEntries={["/servers"]}>
        <Routes>
          <Route path="/servers" element={<ServersPage />} />
        </Routes>
      </MemoryRouter>,
    );

    const attentionLabel = await screen.findByText("need attention");
    expect(attentionLabel.closest(".summary-stamp")).toHaveTextContent("1");
    expect(screen.getByRole("tab", { name: /Needs attention 1/ })).toBeInTheDocument();
    expect(screen.getByText("Unhealthy")).toBeInTheDocument();
  });
});
