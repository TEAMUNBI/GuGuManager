import { afterEach, describe, expect, it, vi } from "vitest";
import { snapshotLabel } from "./format";

describe("snapshotLabel", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("distinguishes a stale snapshot from a successful refresh", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-07T12:02:00Z"));

    expect(snapshotLabel("2026-08-07T12:00:00Z", false)).toMatch(/^Updated /);
    expect(snapshotLabel("2026-08-07T12:00:00Z", true)).toMatch(/^Stale · /);
  });

  it("reports that the first snapshot is still pending", () => {
    expect(snapshotLabel(null, false)).toBe("Awaiting sync");
  });
});
