import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Node } from "../lib/types";
import { NodesPage } from "./NodesPage";

const mocks = vi.hoisted(() => ({
  issueAgentEnrollmentToken: vi.fn(),
  nodes: vi.fn(),
  revokeNode: vi.fn(),
  toast: vi.fn(),
  writeText: vi.fn(),
}));

vi.mock("../lib/api", () => ({
  api: {
    issueAgentEnrollmentToken: mocks.issueAgentEnrollmentToken,
    nodes: mocks.nodes,
    revokeNode: mocks.revokeNode,
  },
}));

vi.mock("../app/App", () => ({
  useAppContext: () => ({
    session: { csrfToken: "csrf-token", user: { roles: ["platform_admin"] } },
    toast: mocks.toast,
  }),
}));

const node: Node = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  name: "node-1",
  condition: "available",
  version: "0.1.0",
  region: "local",
  address: "127.0.0.1",
  lastHeartbeatAt: "2026-08-14T08:00:00Z",
  cpuCores: 8,
  memoryBytes: 17_179_869_184,
  diskBytes: 107_374_182_400,
  allocatedMemoryBytes: 4_294_967_296,
  allocatedDiskBytes: 10_737_418_240,
  runningServers: 1,
  totalServers: 2,
  capabilities: ["console"],
};

describe("NodesPage enrollment and revocation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.nodes.mockResolvedValue([node]);
    mocks.issueAgentEnrollmentToken.mockResolvedValue({
      token: "a".repeat(64),
      expiresAt: "2026-08-15T08:00:00Z",
    });
    mocks.revokeNode.mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: mocks.writeText },
    });
    mocks.writeText.mockResolvedValue(undefined);
  });

  it("issues and displays a plaintext token only until the result is closed", async () => {
    render(<NodesPage />);
    await screen.findAllByText(node.name);

    fireEvent.click(screen.getByRole("button", { name: "Issue enrollment token" }));
    fireEvent.change(screen.getByLabelText(/^Node name hint/), { target: { value: "node-2" } });
    fireEvent.change(screen.getByLabelText(/^Lifetime/), { target: { value: "3600" } });
    fireEvent.click(screen.getByRole("button", { name: "Issue token" }));

    await waitFor(() => expect(mocks.issueAgentEnrollmentToken).toHaveBeenCalledWith({
      nodeNameHint: "node-2",
      ttlSeconds: 3600,
    }, "csrf-token"));
    expect(await screen.findByText("a".repeat(64))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await waitFor(() => expect(mocks.writeText).toHaveBeenCalledWith("a".repeat(64)));

    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByText("a".repeat(64))).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Issue enrollment token" }));
    expect(screen.queryByText("a".repeat(64))).not.toBeInTheDocument();
  });

  it("removes a node only after revocation succeeds", async () => {
    render(<NodesPage />);
    await screen.findAllByText(node.name);

    fireEvent.click(screen.getByRole("button", { name: `Revoke node ${node.name}` }));
    fireEvent.click(screen.getByRole("button", { name: /^Revoke node$/ }));

    await waitFor(() => expect(mocks.revokeNode).toHaveBeenCalledWith(node.id, "csrf-token"));
    await waitFor(() => expect(screen.queryByText(node.name)).not.toBeInTheDocument());
  });

  it("keeps the node and shows the server error when revocation fails", async () => {
    mocks.revokeNode.mockRejectedValue(new Error("Node still owns active workloads"));
    render(<NodesPage />);
    await screen.findAllByText(node.name);

    fireEvent.click(screen.getByRole("button", { name: `Revoke node ${node.name}` }));
    fireEvent.click(screen.getByRole("button", { name: /^Revoke node$/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Node still owns active workloads");
    expect(screen.getAllByText(node.name).length).toBeGreaterThan(1);
  });
});
