import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GameDefinition } from "../lib/types";
import { GamesPage } from "./GamesPage";

const mocks = vi.hoisted(() => ({ games: vi.fn() }));

vi.mock("../lib/api", () => ({
  api: { games: mocks.games },
}));

const untrustedGame: GameDefinition = {
  id: "io.gugumanager.papermc",
  bundleDigest: `sha256:${"ab".repeat(32)}`,
  name: "PaperMC",
  summary: "Paper server",
  version: "1.0.0",
  gameVersion: "1.21.8",
  status: "approved",
  signed: false,
  verified: false,
  runnable: false,
  supported: false,
  trustLevel: "L0_LOCAL",
  source: "embedded-v1alpha1",
  supportReasons: ["BUNDLE_SIGNATURE_UNVERIFIED", "RUNTIME_TARGET_UNAVAILABLE"],
  capabilities: ["console"],
  platforms: ["linux/amd64"],
  servers: 0,
  icon: "server",
  defaultMemoryMb: 4096,
  defaultDiskGb: 25,
};

describe("GamesPage catalog trust", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.games.mockResolvedValue([untrustedGame]);
  });

  it("shows recorded trust evidence and excludes an approved-but-unrunnable bundle from Available", async () => {
    render(<GamesPage />);

    const heading = await screen.findByRole("heading", { name: "PaperMC 1.21.8" });
    const card = heading.closest("article");
    expect(card).not.toBeNull();
    const catalogEntry = within(card as HTMLElement);

    expect(screen.getByText("Catalog trust follows recorded evidence")).toBeInTheDocument();
    expect(screen.queryByText("Runtime package verification is active")).not.toBeInTheDocument();
    expect(screen.queryByText("Signed runtime package")).not.toBeInTheDocument();
    expect(catalogEntry.getByText("Approved")).toBeInTheDocument();
    expect(catalogEntry.getByText("Unsigned")).toBeInTheDocument();
    expect(catalogEntry.getByText("Unverified")).toBeInTheDocument();
    expect(catalogEntry.getByText("Not runnable")).toBeInTheDocument();
    expect(catalogEntry.getByText("Unsupported")).toBeInTheDocument();
    expect(catalogEntry.getByText("Bundle signature has not been verified")).toBeInTheDocument();
    expect(catalogEntry.getByText("No runnable target is available")).toBeInTheDocument();
    expect(catalogEntry.getByText("Trust L0_LOCAL · Source embedded-v1alpha1")).toBeInTheDocument();

    const runnableFilter = screen.getByRole("tab", { name: "Runnable 0" });
    expect(screen.getByRole("tab", { name: "Unavailable 1" })).toBeInTheDocument();
    fireEvent.click(runnableFilter);
    await waitFor(() => expect(screen.queryByRole("heading", { name: "PaperMC 1.21.8" })).not.toBeInTheDocument());
    expect(screen.getByText("No game templates match.")).toBeInTheDocument();
  });
});
