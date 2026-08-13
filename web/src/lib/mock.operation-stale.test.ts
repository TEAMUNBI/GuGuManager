import { afterEach, describe, expect, test, vi } from "vitest";
import type { MockClient as MockClientType } from "./mock";
import type { Operation } from "./types";

const runningServerId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const stoppedServerId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const alternateNodeId = "11111111-1111-4111-8111-111111111111";

afterEach(() => {
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.resetModules();
});

async function freshClient(withRunnableCatalog = false): Promise<MockClientType> {
  vi.useFakeTimers();
  vi.resetModules();
  const { MockClient } = await import("./mock");
  if (!withRunnableCatalog) return new MockClient();
  const defaults = new MockClient();
  const catalog = (await defaults.listGames()).map((game) => ({ ...game, runnable: true }));
  return new MockClient({ catalog });
}

async function expectStaleOperation(client: MockClientType, operation: Operation): Promise<void> {
  await expect(client.getOperation(operation.id)).resolves.toMatchObject({
    id: operation.id,
    serverId: operation.serverId,
    nodeId: operation.nodeId,
    status: "failed",
    progress: 100,
    leaseOwner: null,
    leaseExpiresAt: null,
    checkpoint: "failed",
    error: { code: "OPERATION_STALE", retryable: false },
  });
}

describe("MockClient operation completion fencing", () => {
  test("fails stale provision without marking the server ready", async () => {
    const client = await freshClient(true);
    const [game] = await client.listGames();
    const [node] = await client.listNodes();
    const operation = await client.createServer({
      name: "Stale provision target",
      gameDefinitionId: game.id,
      gameBundleDigest: game.bundleDigest,
      nodeId: node.id,
      memoryMb: 1024,
      diskGb: 5,
    }, "mock-stale-provision-0001");
    const server = await client.getServer(operation.serverId);
    server.generation++;
    const acceptedState = structuredClone(server);

    await vi.runAllTimersAsync();

    await expectStaleOperation(client, operation);
    await expect(client.getServer(operation.serverId)).resolves.toEqual(acceptedState);
    await expect(client.getServer(operation.serverId)).resolves.toMatchObject({
      lifecycleState: "provisioning",
      observedPower: "unknown",
      observedGeneration: 0,
    });
  });

  test("fails stale power without applying the observed power state", async () => {
    const client = await freshClient();
    const operation = await client.requestPower(stoppedServerId, "start", "mock-stale-power-0001");
    const server = await client.getServer(stoppedServerId);
    server.nodeId = alternateNodeId;
    const acceptedState = structuredClone(server);

    await vi.runAllTimersAsync();

    await expectStaleOperation(client, operation);
    await expect(client.getServer(stoppedServerId)).resolves.toEqual(acceptedState);
    await expect(client.getServer(stoppedServerId)).resolves.toMatchObject({
      desiredPower: "running",
      observedPower: "starting",
      healthCondition: "unknown",
    });
  });

  test("fails stale backup without creating a recovery point", async () => {
    const client = await freshClient();
    const before = structuredClone(await client.getBackups(stoppedServerId));
    const operation = await client.createBackup(stoppedServerId, "mock-stale-backup-0001");
    const server = await client.getServer(stoppedServerId);
    server.nodeId = alternateNodeId;

    await vi.runAllTimersAsync();

    await expectStaleOperation(client, operation);
    await expect(client.getBackups(stoppedServerId)).resolves.toEqual(before);
  });

  test("fails stale restore and returns its recovery point to ready", async () => {
    const client = await freshClient();
    const serverBefore = structuredClone(await client.getServer(stoppedServerId));
    const backupsBefore = structuredClone(await client.getBackups(stoppedServerId));
    const operation = await client.restoreBackup(stoppedServerId, backupsBefore[0].id, "mock-stale-restore-0001");
    const server = await client.getServer(stoppedServerId);
    server.nodeId = alternateNodeId;
    const acceptedState = structuredClone(server);

    await vi.runAllTimersAsync();

    await expectStaleOperation(client, operation);
    await expect(client.getBackups(stoppedServerId)).resolves.toEqual(backupsBefore);
    await expect(client.getServer(stoppedServerId)).resolves.toEqual(acceptedState);
    expect((await client.getServer(stoppedServerId)).observedGeneration).toBe(serverBefore.observedGeneration);
  });

  test("fails stale backup deletion and returns its recovery point to ready", async () => {
    const client = await freshClient();
    const backupsBefore = structuredClone(await client.getBackups(stoppedServerId));
    const operation = await client.deleteBackup(stoppedServerId, backupsBefore[0].id, "mock-stale-delete-0001");
    const server = await client.getServer(stoppedServerId);
    server.generation++;

    await vi.runAllTimersAsync();

    await expectStaleOperation(client, operation);
    await expect(client.getBackups(stoppedServerId)).resolves.toEqual(backupsBefore);
  });

  test("fails stale reconcile without advancing observed generation", async () => {
    const client = await freshClient();
    const before = await client.getServer(runningServerId);
    const node = (await client.listNodes()).find((item) => item.id === before.nodeId);
    const allocations = await client.getAllocations(runningServerId);
    const operation = await client.createAllocation(
      runningServerId,
      {
        bindIp: node?.address ?? "10.0.10.21",
        port: Math.max(...allocations.map((allocation) => allocation.port)) + 1,
        protocol: "tcp",
        primary: false,
      },
      before.generation,
      "mock-stale-reconcile-0001",
    );
    const server = await client.getServer(runningServerId);
    server.nodeId = "22222222-2222-4222-8222-222222222222";
    const acceptedState = structuredClone(server);

    await vi.runAllTimersAsync();

    await expectStaleOperation(client, operation);
    await expect(client.getServer(runningServerId)).resolves.toEqual(acceptedState);
  });
});
