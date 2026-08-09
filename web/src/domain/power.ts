export type PowerAction = "start" | "stop" | "restart" | "kill";

export interface PowerOperation {
  id: string;
  serverId: string;
  nodeId: string;
  action: PowerAction;
  idempotencyKey: string;
  status: "queued";
}

export interface PowerState {
  operations: PowerOperation[];
}

export interface PowerCommand {
  serverId: string;
  nodeId: string;
  action: PowerAction;
  idempotencyKey: string;
}

export class IdempotencyKeyReuseError extends Error {}

export function isPowerControlLocked(
  observedPower: string,
  busyAction: string,
  nodeCondition: string,
): boolean {
  return busyAction !== "" || nodeCondition !== "available" || observedPower === "starting" || observedPower === "stopping";
}

export function requestPower(
  state: PowerState,
  command: PowerCommand,
  createId: () => string,
): { state: PowerState; operation: PowerOperation } {
  const existing = state.operations.find(
    (operation) =>
      operation.serverId === command.serverId &&
      operation.idempotencyKey === command.idempotencyKey,
  );

  if (existing) {
    if (existing.action !== command.action) {
      throw new IdempotencyKeyReuseError(
        "idempotency key was already used for another power action",
      );
    }

    return { state, operation: existing };
  }

  const operation: PowerOperation = {
    id: createId(),
    serverId: command.serverId,
    nodeId: command.nodeId,
    action: command.action,
    idempotencyKey: command.idempotencyKey,
    status: "queued",
  };

  return {
    state: { operations: [...state.operations, operation] },
    operation,
  };
}
