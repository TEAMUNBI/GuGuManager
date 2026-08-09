import { describe, expect, it, vi } from "vitest";
import type { Operation } from "./types";
import { operationFailureMessage, pollOperation } from "./operations";

const operation = (status: Operation["status"], progress: number): Operation => ({
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  serverId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  nodeId: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  type: "backup",
  status,
  progress,
  generation: 4,
  attempt: 1,
  maxAttempts: 1,
  leaseOwner: null,
  leaseExpiresAt: null,
  checkpoint: status === "succeeded" ? "completed" : status,
  error: null,
  createdAt: "2026-08-07T12:00:00Z",
  updatedAt: "2026-08-07T12:00:00Z",
});

describe("pollOperation", () => {
  it("prefers the API's safe structured error message and falls back when absent", () => {
    expect(operationFailureMessage({
      ...operation("failed", 100),
      checkpoint: "failed",
      error: { code: "OPERATION_STALE", message: "The server state changed before restore completed.", retryable: false },
    }, "Restore failed")).toBe("The server state changed before restore completed.");
    expect(operationFailureMessage(operation("failed", 100), "Restore failed")).toBe("Restore failed");
  });

  it("does not resolve until the operation reaches a terminal state", async () => {
    const read = vi.fn()
      .mockResolvedValueOnce(operation("running", 60))
      .mockResolvedValueOnce(operation("succeeded", 100));
    const wait = vi.fn().mockResolvedValue(undefined);
    const onUpdate = vi.fn();

    const completed = await pollOperation(operation("queued", 0), read, wait, onUpdate);

    expect(completed.status).toBe("succeeded");
    expect(read).toHaveBeenCalledTimes(2);
    expect(wait).toHaveBeenCalledTimes(2);
    expect(onUpdate).toHaveBeenNthCalledWith(1, expect.objectContaining({ status: "running" }));
    expect(onUpdate).toHaveBeenNthCalledWith(2, expect.objectContaining({ status: "succeeded" }));
  });

  it("keeps polling after a transient status read failure", async () => {
    const read = vi.fn()
      .mockRejectedValueOnce(new TypeError("network unavailable"))
      .mockResolvedValueOnce(operation("succeeded", 100));
    const wait = vi.fn().mockResolvedValue(undefined);

    const completed = await pollOperation(operation("queued", 0), read, wait);

    expect(completed.status).toBe("succeeded");
    expect(read).toHaveBeenCalledTimes(2);
    expect(wait).toHaveBeenCalledTimes(2);
  });

  it("rejects after the configured consecutive status read failure budget", async () => {
    const read = vi.fn().mockRejectedValue(new TypeError("network unavailable"));
    const wait = vi.fn()
      .mockResolvedValueOnce(undefined)
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("polling did not stop"));

    await expect(pollOperation(
      operation("queued", 0),
      read,
      wait,
      undefined,
      { maxConsecutiveFailures: 2 },
    )).rejects.toThrow("network unavailable");
    expect(read).toHaveBeenCalledTimes(2);
  });

  it("does not start another status read after cancellation", async () => {
    const controller = new AbortController();
    controller.abort();
    const read = vi.fn();
    const wait = vi.fn().mockRejectedValue(new Error("wait should not start"));

    await expect(pollOperation(
      operation("queued", 0),
      read,
      wait,
      undefined,
      { signal: controller.signal },
    )).rejects.toMatchObject({ name: "AbortError" });
    expect(wait).not.toHaveBeenCalled();
    expect(read).not.toHaveBeenCalled();
  });

  it("cancels the default wait before another status read starts", async () => {
    vi.useFakeTimers();
    try {
      const controller = new AbortController();
      const read = vi.fn();
      const pending = pollOperation(
        operation("queued", 0),
        read,
        undefined,
        undefined,
        { signal: controller.signal },
      );

      controller.abort();

      await expect(pending).rejects.toMatchObject({ name: "AbortError" });
      expect(read).not.toHaveBeenCalled();
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("forwards cancellation to the active status read", async () => {
    const controller = new AbortController();
    const read = vi.fn().mockResolvedValue(operation("succeeded", 100));
    const wait = vi.fn().mockResolvedValue(undefined);

    await pollOperation(
      operation("queued", 0),
      read,
      wait,
      undefined,
      { signal: controller.signal },
    );

    expect(read).toHaveBeenCalledWith(operation("queued", 0).id, controller.signal);
  });

  it("does not publish a status read that completes after cancellation", async () => {
    const controller = new AbortController();
    const read = vi.fn().mockImplementation(async () => {
      controller.abort();
      return operation("succeeded", 100);
    });
    const wait = vi.fn().mockResolvedValue(undefined);
    const onUpdate = vi.fn();

    await expect(pollOperation(
      operation("queued", 0),
      read,
      wait,
      onUpdate,
      { signal: controller.signal },
    )).rejects.toMatchObject({ name: "AbortError" });
    expect(onUpdate).not.toHaveBeenCalled();
  });
});
