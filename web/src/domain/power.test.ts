import {
  IdempotencyKeyReuseError,
  isPowerControlLocked,
  requestPower,
  type PowerState,
} from "./power";

describe("requestPower", () => {
  test("reuses the existing operation for an identical idempotent request", () => {
    const initial: PowerState = { operations: [] };
    const first = requestPower(
      initial,
      { serverId: "server-1", nodeId: "node-1", action: "start", idempotencyKey: "power-key-0001" },
      () => "operation-1",
    );
    const second = requestPower(
      first.state,
      { serverId: "server-1", nodeId: "node-2", action: "start", idempotencyKey: "power-key-0001" },
      () => "operation-2",
    );

    expect(second.operation.id).toBe("operation-1");
    expect(second.operation.nodeId).toBe("node-1");
    expect(second.state.operations).toHaveLength(1);
  });

  test("rejects reusing an idempotency key for a different power action", () => {
    const first = requestPower(
      { operations: [] },
      { serverId: "server-1", nodeId: "node-1", action: "start", idempotencyKey: "power-key-0001" },
      () => "operation-1",
    );

    expect(() =>
      requestPower(
        first.state,
        { serverId: "server-1", nodeId: "node-1", action: "stop", idempotencyKey: "power-key-0001" },
        () => "operation-2",
      ),
    ).toThrow(IdempotencyKeyReuseError);
  });
});

describe("isPowerControlLocked", () => {
  test("locks controls while observed power is transitioning", () => {
    expect(isPowerControlLocked("starting", "", "available")).toBe(true);
    expect(isPowerControlLocked("stopping", "", "available")).toBe(true);
    expect(isPowerControlLocked("running", "", "available")).toBe(false);
    expect(isPowerControlLocked("stopped", "", "available")).toBe(false);
  });
});
