/// <reference types="vite/client" />

import { describe, expect, test, vi } from "vitest";
import factorioBundle from "../../../spec/game-definition/examples/factorio.json";
import paperMCBundle from "../../../spec/game-definition/examples/papermc.json";
import { MockClient } from "./mock";
import type { StartupValue } from "./types";

const serverId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

type MutableFixedBundleSpec = Record<string, unknown> & { variables?: unknown };
type MutableFixedBundle = Record<string, unknown> & { spec: MutableFixedBundleSpec };

async function clientWithRunnableCatalog(...gameIds: string[]): Promise<MockClient> {
  const defaults = new MockClient();
  const runnable = new Set(gameIds);
  const catalog = (await defaults.listGames()).map((game) => ({ ...game, runnable: runnable.has(game.id) }));
  return new MockClient({ catalog });
}

async function expectFixedBundleVariablesMutationRejected(
  bundle: unknown,
  targetServerId: string,
  mutate: (spec: MutableFixedBundle["spec"]) => void,
): Promise<void> {
  const spec = (bundle as MutableFixedBundle).spec;
  const original = spec.variables;
  try {
    spec.variables = structuredClone(original);
    mutate(spec);
    await expect(new MockClient().getStartup(targetServerId)).rejects.toThrow("PACKAGE_INCOMPATIBLE");
  } finally {
    spec.variables = original;
  }
}

function asRecord(value: unknown): Record<string, unknown> {
  return value as Record<string, unknown>;
}

function variableSchema(spec: MutableFixedBundle["spec"]): Record<string, unknown> {
  return asRecord(asRecord(spec.variables).schema);
}

function variableProperties(spec: MutableFixedBundle["spec"]): Record<string, unknown> {
  return asRecord(variableSchema(spec).properties);
}

describe("MockClient file listings", () => {
  test("returns only direct children of the requested directory", async () => {
    const client = new MockClient();

    const root = await client.getFiles(serverId);
    const config = await client.getFiles(serverId, "config");
    const world = await client.getFiles(serverId, "world");
    const region = await client.getFiles(serverId, "world/region");

    expect(root.map((entry) => entry.path)).not.toContain("config/paper-global.yml");
    expect(config.map((entry) => entry.path)).toEqual(["config/paper-global.yml"]);
    expect(world.map((entry) => entry.path)).toEqual(["world/region", "world/level.dat"]);
    expect(region.map((entry) => entry.path)).toEqual(["world/region/r.0.0.mca"]);
  });

  test("rejects absolute and escaping paths", async () => {
    const client = new MockClient();

    await expect(client.getFiles(serverId, "/world")).rejects.toThrow("PATH_ESCAPE_BLOCKED");
    await expect(client.getFiles(serverId, "../world")).rejects.toThrow("PATH_ESCAPE_BLOCKED");
  });

  test("persists create, write, move, read, and delete operations", async () => {
    const client = new MockClient();
    const directory = `panel-${crypto.randomUUID()}`;

    await client.createDirectory(serverId, directory);
    await client.writeFile(serverId, { path: `${directory}/notes.txt`, content: "save before upgrade", encoding: "utf-8" });
    await expect(client.getFileContent(serverId, `${directory}/notes.txt`)).resolves.toMatchObject({ content: "save before upgrade", encoding: "utf-8" });
    await client.moveFile(serverId, { source: `${directory}/notes.txt`, destination: `${directory}/upgrade.txt`, replace: false });
    await expect(client.getFileContent(serverId, `${directory}/notes.txt`)).rejects.toThrow("NOT_FOUND");
    await expect(client.getFileContent(serverId, `${directory}/upgrade.txt`)).resolves.toMatchObject({ content: "save before upgrade" });
    await client.deleteFile(serverId, directory, true);
    await expect(client.getFileContent(serverId, `${directory}/upgrade.txt`)).rejects.toThrow("NOT_FOUND");
  });
});

describe("MockClient contract identities", () => {
  test("uses UUIDs for seeded and newly-created resource IDs", async () => {
    const client = await clientWithRunnableCatalog("io.gugumanager.papermc");
    const [game] = await client.listGames();
    const [node] = await client.listNodes();
    const [event] = await client.listAudit();

    expect(event.id).toMatch(uuidPattern);
    expect(event.operationId).toMatch(uuidPattern);

    const operation = await client.createServer({
      name: "UUID contract world",
      gameDefinitionId: game.id,
      gameBundleDigest: game.bundleDigest,
      nodeId: node.id,
      memoryMb: 1024,
      diskGb: 5,
    }, "mock-uuid-key-0001");

    expect(operation.id).toMatch(uuidPattern);
    expect(operation.serverId).toMatch(uuidPattern);
    expect(operation.nodeId).toBe(node.id);
    await expect(client.getServer(operation.serverId)).resolves.toMatchObject({ id: operation.serverId });
  });

  test("marks every default embedded catalog entry as unavailable and rejects creation", async () => {
    const client = new MockClient();
    const catalog = await client.listGames();
    const [game] = catalog;
    const [node] = await client.listNodes();

    for (const candidate of catalog) {
      expect(candidate).toMatchObject({
        signed: false,
        verified: false,
        runnable: false,
        supported: false,
        trustLevel: "L0_LOCAL",
        source: "embedded-v1alpha1",
        supportReasons: ["BUNDLE_SIGNATURE_UNVERIFIED", "RUNTIME_TARGET_UNAVAILABLE"],
      });
    }

    await expect(client.createServer({
      name: "Unavailable embedded world",
      gameDefinitionId: game.id,
      gameBundleDigest: game.bundleDigest,
      nodeId: node.id,
      memoryMb: 1024,
      diskGb: 5,
    }, "mock-unavailable-key-0001")).rejects.toThrow("PACKAGE_INCOMPATIBLE");
  });

  test("keeps the GameDefinition version separate from the upstream game version", async () => {
    const client = await clientWithRunnableCatalog("io.gugumanager.papermc");
    const [game] = await client.listGames();
    const [node] = await client.listNodes();

    expect(game).toMatchObject({
      id: "io.gugumanager.papermc",
      version: "1.0.0",
      gameVersion: "1.21.8",
    });

    const operation = await client.createServer({
      name: "Version contract world",
      gameDefinitionId: game.id,
      gameBundleDigest: game.bundleDigest,
      nodeId: node.id,
      memoryMb: 1024,
      diskGb: 5,
    }, "mock-version-key-0001");

    await expect(client.getServer(operation.serverId)).resolves.toMatchObject({
      gameDefinitionVersion: "1.0.0",
      gameVersion: "1.21.8",
    });
  });
});

describe("MockClient API parity", () => {
  test("issues validated enrollment tokens and revokes nodes once", async () => {
    const client = new MockClient();
    await client.login("admin@gugu.local", "gugu-dev-2026");
    const [node] = await client.listNodes();

    const issued = await client.issueAgentEnrollmentToken({ nodeNameHint: " node-2 ", ttlSeconds: 60 });
    expect(issued.token).toMatch(/^[0-9a-f]{64}$/);
    expect(new Date(issued.expiresAt).getTime()).toBeGreaterThan(Date.now());
    await expect(client.issueAgentEnrollmentToken({ ttlSeconds: 0 })).rejects.toThrow("VALIDATION_FAILED");
    await expect(client.issueAgentEnrollmentToken({ nodeNameHint: "x".repeat(101) })).rejects.toThrow("VALIDATION_FAILED");

    await expect(client.revokeNode(node.id)).resolves.toBeUndefined();
    expect((await client.listNodes()).some((candidate) => candidate.id === node.id)).toBe(false);
    await expect(client.revokeNode(node.id)).rejects.toThrow("NOT_FOUND");
  });

  test("checks development credentials", async () => {
    const client = new MockClient();

    await expect(client.login("admin@gugu.local", "wrong-password")).rejects.toThrow("AUTH_INVALID");
    await expect(client.login("admin@gugu.local", "gugu-dev-2026")).resolves.toMatchObject({
      user: { email: "admin@gugu.local" },
    });
  });

  test("rejects power operations when the assigned node is offline", async () => {
    const client = new MockClient();

    await expect(client.requestPower(
      "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      "start",
      "mock-offline-power-key-0001",
    )).rejects.toThrow("NODE_OFFLINE");
  });

  test.each(["start", "restart"] as const)("rejects %s before mutating a server with missing required startup values", async (action) => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const internal = client as unknown as {
        startup: Record<string, { values: Record<string, Exclude<StartupValue, null>> }>;
        operations: Map<string, unknown>;
        idempotency: Map<string, unknown>;
      };
      delete internal.startup[serverId].values.accept_eula;
      const before = await client.getServer(serverId);
      const operationCount = internal.operations.size;
      const idempotencyCount = internal.idempotency.size;

      await expect(client.requestPower(
        serverId,
        action,
        `mock-required-${action}-0001`,
      )).rejects.toThrow("VALIDATION_FAILED");

      await expect(client.getServer(serverId)).resolves.toEqual(before);
      expect(internal.operations.size).toBe(operationCount);
      expect(internal.idempotency.size).toBe(idempotencyCount);
    } finally {
      vi.runAllTimers();
      vi.useRealTimers();
    }
  });

  test("starts a seeded server when every required startup value is present", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const before = await client.getServer(serverId);

      const operation = await client.requestPower(serverId, "start", "mock-required-start-positive-0001");

      expect(operation).toMatchObject({ serverId, type: "start", status: "queued", generation: before.generation + 1 });
      await expect(client.getServer(serverId)).resolves.toMatchObject({ desiredPower: "running", observedPower: "starting" });
      vi.runAllTimers();
      await expect(client.getOperation(operation.id)).resolves.toMatchObject({ status: "succeeded", progress: 100 });
      await expect(client.getServer(serverId)).resolves.toMatchObject({ desiredPower: "running", observedPower: "running" });
    } finally {
      vi.useRealTimers();
    }
  });

  test("rejects commands for stopped or missing servers", async () => {
    const client = new MockClient();

    await expect(client.sendCommand(
      "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      "status",
    )).rejects.toThrow("OPERATION_CONFLICT");
    await expect(client.sendCommand(
      "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
      "status",
    )).rejects.toThrow("NOT_FOUND");
  });

  test("returns not found instead of empty collections for unknown servers", async () => {
    const client = new MockClient();
    const missing = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";

    await expect(client.getConsole(missing)).rejects.toThrow("NOT_FOUND");
    await expect(client.getFiles(missing)).rejects.toThrow("NOT_FOUND");
    await expect(client.getBackups(missing)).rejects.toThrow("NOT_FOUND");
  });

  test("locks a stopped server restore and asynchronously deletes the backup", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const stoppedServer = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
      const [backup] = await client.getBackups(stoppedServer);

      const restore = await client.restoreBackup(stoppedServer, backup.id, "mock-restore-key-0001");
      expect(restore.type).toBe("restore");
      await expect(client.getBackups(stoppedServer)).resolves.toEqual(expect.arrayContaining([expect.objectContaining({ id: backup.id, status: "restoring" })]));
      vi.runAllTimers();
      expect((await client.getOperation(restore.id)).status).toBe("succeeded");

      const deletion = await client.deleteBackup(stoppedServer, backup.id, "mock-delete-key-00001");
      expect(deletion.type).toBe("backup-delete");
      vi.runAllTimers();
      expect((await client.getOperation(deletion.id)).status).toBe("succeeded");
      expect((await client.getBackups(stoppedServer)).some((item) => item.id === backup.id)).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  test("cleans up a failed backup through the deletion operation", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const [backup] = await client.getBackups(serverId);
      backup.status = "failed";
      backup.failureCode = "BACKUP_INTEGRITY_FAILED";
      backup.failureMessage = "Backup manifest validation failed";

      const cleanup = await client.deleteBackup(serverId, backup.id, "mock-cleanup-key-0001");
      expect(cleanup.type).toBe("backup-delete");
      await expect(client.getBackups(serverId)).resolves.toEqual(expect.arrayContaining([
        expect.objectContaining({ id: backup.id, status: "deleting" }),
      ]));

      await vi.runAllTimersAsync();

      expect((await client.getOperation(cleanup.id)).status).toBe("succeeded");
      expect((await client.getBackups(serverId)).some((item) => item.id === backup.id)).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("MockClient network and startup reconciliation", () => {
  test("publishes the canonical digests of the fixed bundles", async () => {
    const client = new MockClient();
    const games = new Map((await client.listGames()).map((game) => [game.id, game]));

    for (const [gameId, digest] of [
      ["io.gugumanager.papermc", "sha256:a0118b857dacc2ffd27a56bcdd9cdfcd27f699a5d55ca424bffc447b0572fbfa"],
      ["io.gugumanager.factorio", "sha256:9234f21256ca3fd1a0886a4ea57f75e30ac6e1f22d1fee7cc7fbe1dc5731b0d7"],
      ["io.gugumanager.vintagestory", "sha256:f85cd736d3428f3ee81c3261a6ddb36e43df950915209f12659a44e53b9e768f"],
    ] as const) {
      expect(games.get(gameId)?.bundleDigest).toBe(digest);
    }
  });

  test("derives seeded startup declarations and resolved commands from the fixed bundles", async () => {
    const client = new MockClient();

    const paper = await client.getStartup(serverId);
    expect(paper.command).toEqual({ executable: "/image/scripts/start", args: [] });
    expect(paper.variables.map((variable) => variable.key)).toEqual(["accept_eula", "memory_mb", "rcon_password"]);
    expect(paper.variables.find((variable) => variable.key === "memory_mb")).toMatchObject({
      default: 2048,
      value: 2048,
      hasValue: true,
    });

    const factorio = await client.getStartup("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb");
    expect(factorio.variables.map((variable) => variable.key)).toEqual([
      "autosave_interval",
      "difficulty",
      "public_listing",
      "server_name",
      "server_token",
    ]);

    const vintageStory = await client.getStartup("cccccccc-cccc-4ccc-8ccc-cccccccccccc");
    expect(vintageStory.variables.map((variable) => variable.key)).toEqual(["max_clients", "world_name"]);
  });

  test.each(["variaables", "Variables"] as const)("rejects an unknown case-sensitive fixed Bundle spec key named %s", async (key) => {
    const document = paperMCBundle as unknown as MutableFixedBundle;
    const original = document.spec;
    try {
      document.spec = structuredClone(original);
      document.spec[key] = structuredClone(document.spec.variables);

      await expect(new MockClient().getStartup(serverId)).rejects.toThrow("PACKAGE_INCOMPATIBLE");
    } finally {
      document.spec = original;
    }
  });

  test("rejects an unknown fixed Bundle document key", async () => {
    const document = paperMCBundle as unknown as MutableFixedBundle;
    try {
      document.unexpected = true;
      await expect(new MockClient().getStartup(serverId)).rejects.toThrow("PACKAGE_INCOMPATIBLE");
    } finally {
      delete document.unexpected;
    }
  });

  test("accepts a fixed Bundle whose variables declaration is genuinely omitted", async () => {
    const document = paperMCBundle as unknown as MutableFixedBundle;
    const original = document.spec;
    try {
      document.spec = structuredClone(original);
      delete document.spec.variables;

      await expect(new MockClient().getStartup(serverId)).resolves.toMatchObject({ variables: [] });
    } finally {
      document.spec = original;
    }
  });

  test("creates the initial primary allocation and startup state from the selected fixed bundle", async () => {
    vi.useFakeTimers();
    try {
      const client = await clientWithRunnableCatalog("io.gugumanager.papermc");
      const game = (await client.listGames()).find((candidate) => candidate.id === "io.gugumanager.papermc");
      const node = (await client.listNodes()).find((candidate) => candidate.id === "11111111-1111-4111-8111-111111111111");
      expect(game).toBeDefined();
      expect(node).toBeDefined();

      const operation = await client.createServer({
        name: "Bundle-derived Paper",
        gameDefinitionId: game?.id ?? "",
        gameBundleDigest: game?.bundleDigest ?? "",
        nodeId: node?.id ?? "",
        memoryMb: 8192,
        diskGb: 25,
      }, "mock-bundle-create-0001");

      await expect(client.getAllocations(operation.serverId)).resolves.toEqual([
        expect.objectContaining({
          serverId: operation.serverId,
          nodeId: node?.id,
          bindIp: node?.address,
          port: 25566,
          protocol: "tcp",
          primary: true,
        }),
      ]);
      await expect(client.getServer(operation.serverId)).resolves.toMatchObject({ allocation: "10.0.10.21:25566" });
      await expect(client.getStartup(operation.serverId)).resolves.toMatchObject({
        command: { executable: "/image/scripts/start", args: [] },
        variables: expect.arrayContaining([
          expect.objectContaining({ key: "memory_mb", value: 8192, hasValue: true }),
          expect.objectContaining({ key: "rcon_password", hasValue: false }),
        ]),
      });
    } finally {
      vi.runAllTimers();
      vi.useRealTimers();
    }
  });

  test("creates Factorio network and startup state without inheriting seeded secrets", async () => {
    vi.useFakeTimers();
    try {
      const client = await clientWithRunnableCatalog("io.gugumanager.factorio");
      const game = (await client.listGames()).find((candidate) => candidate.id === "io.gugumanager.factorio");
      const node = (await client.listNodes()).find((candidate) => candidate.id === "22222222-2222-4222-8222-222222222222");

      const operation = await client.createServer({
        name: "Bundle-derived Factorio",
        gameDefinitionId: game?.id ?? "",
        gameBundleDigest: game?.bundleDigest ?? "",
        nodeId: node?.id ?? "",
        memoryMb: 4096,
        diskGb: 20,
      }, "mock-factorio-create-0001");

      await expect(client.getAllocations(operation.serverId)).resolves.toEqual([
        expect.objectContaining({ port: 34198, protocol: "udp", primary: true }),
      ]);
      expect((await client.getStartup(operation.serverId)).variables.find((variable) => variable.key === "server_token")).toMatchObject({
        hasValue: false,
      });
    } finally {
      vi.runAllTimers();
      vi.useRealTimers();
    }
  });

  test("preserves a required __proto__ startup default through update and start", async () => {
    vi.useFakeTimers();
    const variables = factorioBundle.spec.variables as unknown as {
      schema: { properties: Record<string, unknown>; required?: string[] };
    };
    const originalRequired = variables.schema.required;
    try {
      Object.defineProperty(variables.schema.properties, "__proto__", {
        configurable: true,
        enumerable: true,
        value: { type: "string", default: "default-world" },
        writable: true,
      });
      variables.schema.required = [...(originalRequired ?? []), "__proto__"];

      const client = await clientWithRunnableCatalog("io.gugumanager.factorio");
      const game = (await client.listGames()).find((candidate) => candidate.id === "io.gugumanager.factorio");
      const node = (await client.listNodes()).find((candidate) => candidate.id === "22222222-2222-4222-8222-222222222222");
      const provision = await client.createServer({
        name: "Prototype-safe Factorio",
        gameDefinitionId: game?.id ?? "",
        gameBundleDigest: game?.bundleDigest ?? "",
        nodeId: node?.id ?? "",
        memoryMb: 4096,
        diskGb: 20,
      }, "mock-proto-create-0001");
      vi.runAllTimers();

      expect((await client.getStartup(provision.serverId)).variables.find((variable) => variable.key === "__proto__")).toMatchObject({
        required: true,
        hasValue: true,
        value: "default-world",
        default: "default-world",
      });

      const internal = client as unknown as {
        startup: Record<string, { values: Record<string, Exclude<StartupValue, null>> }>;
      };
      delete internal.startup[provision.serverId].values.__proto__;
      const updateValues = Object.create(null) as Record<string, StartupValue>;
      updateValues.__proto__ = "updated-world";
      const beforeUpdate = await client.getServer(provision.serverId);
      await client.updateStartup(provision.serverId, updateValues, beforeUpdate.generation, "mock-proto-update-0001");
      vi.runAllTimers();

      expect((await client.getStartup(provision.serverId)).variables.find((variable) => variable.key === "__proto__")).toMatchObject({
        required: true,
        hasValue: true,
        value: "updated-world",
      });
      await expect(client.requestPower(provision.serverId, "start", "mock-proto-start-0001")).resolves.toMatchObject({
        serverId: provision.serverId,
        type: "start",
        status: "queued",
      });
    } finally {
      delete variables.schema.properties.__proto__;
      variables.schema.required = originalRequired;
      vi.runAllTimers();
      vi.useRealTimers();
    }
  });

  test("rejects wildcard allocation bind addresses", async () => {
    const client = new MockClient();
    const current = await client.getServer(serverId);

    for (const bindIp of ["0.0.0.0", "::"]) {
      await expect(client.createAllocation(
        serverId,
        { bindIp, port: 25570, protocol: "tcp", primary: false },
        current.generation,
        `mock-allocation-wildcard-${bindIp}`,
      )).rejects.toThrow("VALIDATION_FAILED");
    }
  });

  test("keeps one primary allocation and rejects a duplicate node endpoint", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const before = await client.getServer(serverId);
      const seeded = await client.getAllocations(serverId);

      expect(seeded).toHaveLength(1);
      expect(seeded[0]).toMatchObject({ primary: true, nodeId: before.nodeId });

      const created = await client.createAllocation(
        serverId,
        { bindIp: "10.0.10.21", port: 25566, protocol: "tcp", primary: false },
        before.generation,
        "mock-allocation-create-0001",
      );
      expect(created.type).toBe("reconcile");
      expect((await client.getServer(serverId)).generation).toBe(before.generation + 1);
      await expect(client.createAllocation(
        serverId,
        { bindIp: "10.0.10.21", port: 25566, protocol: "tcp", primary: false },
        before.generation,
        "mock-allocation-create-0001",
      )).resolves.toMatchObject({ id: created.id, type: "reconcile" });
      const secondary = (await client.getAllocations(serverId)).find((allocation) => allocation.port === 25566);
      expect(secondary).toBeDefined();
      await expect(client.setPrimaryAllocation(
        serverId,
        secondary?.id ?? "missing",
        before.generation,
        "mock-allocation-primary-stale",
      )).rejects.toThrow("PRECONDITION_FAILED");
      vi.runAllTimers();

      const current = await client.getServer(serverId);
      await expect(client.createAllocation(
        serverId,
        { bindIp: "10.0.10.21", port: 25566, protocol: "tcp", primary: false },
        current.generation,
        "mock-allocation-create-0002",
      )).rejects.toThrow("PORT_CONFLICT");

      await expect(client.createAllocation(
        serverId,
        { bindIp: "::ffff:10.0.10.21", port: 25565, protocol: "tcp", primary: false },
        current.generation,
        "mock-allocation-mapped-ipv6-0001",
      )).rejects.toThrow("PORT_CONFLICT");

      const switched = await client.setPrimaryAllocation(
        serverId,
        secondary?.id ?? "missing",
        current.generation,
        "mock-allocation-primary-0001",
      );
      expect(switched.type).toBe("reconcile");
      vi.runAllTimers();
      expect((await client.getAllocations(serverId)).filter((allocation) => allocation.primary)).toEqual([
        expect.objectContaining({ id: secondary?.id }),
      ]);

      const afterSwitch = await client.getServer(serverId);
      const original = (await client.getAllocations(serverId)).find((allocation) => allocation.id !== secondary?.id);
      const deletion = await client.deleteAllocation(
        serverId,
        original?.id ?? "missing",
        afterSwitch.generation,
        "mock-allocation-delete-0001",
      );
      vi.runAllTimers();
      expect(await client.getAllocations(serverId)).toHaveLength(1);
      await expect(client.deleteAllocation(
        serverId,
        original?.id ?? "missing",
        afterSwitch.generation,
        "mock-allocation-delete-0001",
      )).resolves.toMatchObject({ id: deletion.id, type: "reconcile" });
    } finally {
      vi.useRealTimers();
    }
  });

  test("validates startup variables and never returns secret values", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const before = await client.getServer(serverId);
      const startup = await client.getStartup(serverId);
      const secret = startup.variables.find((variable) => variable.secret);

      expect(startup).toMatchObject({ serverId, generation: before.generation });
      expect(secret).toMatchObject({ key: "rcon_password", hasValue: true });
      for (const forbiddenField of ["value", "default", "constValue", "enumValues"]) {
        expect(secret).not.toHaveProperty(forbiddenField);
      }

      const update = await client.updateStartup(
        serverId,
        { memory_mb: 6144, rcon_password: "replacement-secret" },
        before.generation,
        "mock-startup-update-0001",
      );
      expect(update.type).toBe("reconcile");
      expect((await client.getServer(serverId)).generation).toBe(before.generation + 1);
      const updated = await client.getStartup(serverId);
      expect(updated.variables.find((variable) => variable.key === "memory_mb")).toMatchObject({ value: 6144, hasValue: true });
      expect(updated.variables.find((variable) => variable.key === "rcon_password")).not.toHaveProperty("value");
      await expect(client.updateStartup(
        serverId,
        { memory_mb: 8192 },
        before.generation,
        "mock-startup-update-stale",
      )).rejects.toThrow("PRECONDITION_FAILED");
      vi.runAllTimers();

      const current = await client.getServer(serverId);
      await expect(client.updateStartup(
        serverId,
        { undeclared: true },
        current.generation,
        "mock-startup-update-0002",
      )).rejects.toThrow("VALIDATION_FAILED");
      const cleared = await client.updateStartup(
        serverId,
        { rcon_password: null },
        current.generation,
        "mock-startup-update-0003",
      );
      expect(cleared.type).toBe("reconcile");
      expect((await client.getStartup(serverId)).variables.find((variable) => variable.key === "rcon_password")).toMatchObject({ hasValue: false });
    } finally {
      vi.useRealTimers();
    }
  });

  test.each([
    ["default", "development-secret"],
    ["const", "development-secret"],
    ["enum", ["development-secret"]],
  ] as const)("rejects a fixed bundle whose secret declares %s", async (keyword, value) => {
    const property = (paperMCBundle.spec.variables.schema.properties.rcon_password as Record<string, unknown>);
    const hadOriginal = Object.hasOwn(property, keyword);
    const original = property[keyword];

    try {
      property[keyword] = value;
      await expect(new MockClient().getStartup(serverId)).rejects.toThrow("PACKAGE_INCOMPATIBLE");
    } finally {
      if (hadOriginal) property[keyword] = original;
      else delete property[keyword];
    }
  });

  test.each([
    ["variables is null", (spec: MutableFixedBundle["spec"]) => { spec.variables = null; }],
    ["variables is an array", (spec: MutableFixedBundle["spec"]) => { spec.variables = []; }],
    ["schema is missing", (spec: MutableFixedBundle["spec"]) => { delete asRecord(spec.variables).schema; }],
    ["schema is null", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).schema = null; }],
    ["schema is an array", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).schema = []; }],
    ["properties is null", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).properties = null; }],
    ["properties is an array", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).properties = []; }],
    ["required is null", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).required = null; }],
    ["required is an object", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).required = {}; }],
    ["secrets is null", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).secrets = null; }],
    ["secrets is an object", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).secrets = {}; }],
    ["bindings is null", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).bindings = null; }],
    ["bindings is an object", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).bindings = {}; }],
  ] as const)("rejects a malformed fixed Bundle variable container when %s", async (_name, mutate) => {
    await expectFixedBundleVariablesMutationRejected(paperMCBundle, serverId, mutate);
  });

  test.each([
    ["schema has an unknown keyword", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).pattern = "ignored"; }],
    ["property has an unknown keyword", (spec: MutableFixedBundle["spec"]) => { asRecord(variableProperties(spec).rcon_password).pattern = "^safe$"; }],
    ["property key is invalid", (spec: MutableFixedBundle["spec"]) => { variableProperties(spec)["bad-key"] = { type: "string" }; }],
    ["required entry is undeclared", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).required = ["accept_eula", "missing_variable"]; }],
    ["required entry is duplicated", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).required = ["accept_eula", "accept_eula"]; }],
    ["required entry is invalid", (spec: MutableFixedBundle["spec"]) => { variableSchema(spec).required = ["bad-key"]; }],
    ["string declares minimum", (spec: MutableFixedBundle["spec"]) => { asRecord(variableProperties(spec).rcon_password).minimum = 1; }],
    ["string length is negative", (spec: MutableFixedBundle["spec"]) => { asRecord(variableProperties(spec).rcon_password).minLength = -1; }],
    ["string length range is contradictory", (spec: MutableFixedBundle["spec"]) => {
      Object.assign(asRecord(variableProperties(spec).rcon_password), { minLength: 9, maxLength: 8 });
    }],
    ["integer range is contradictory", (spec: MutableFixedBundle["spec"]) => {
      const property = asRecord(variableProperties(spec).memory_mb);
      delete property.default;
      Object.assign(property, { minimum: 2, maximum: 1 });
    }],
    ["minimum is unsafe", (spec: MutableFixedBundle["spec"]) => {
      asRecord(variableProperties(spec).memory_mb).minimum = Number.MIN_SAFE_INTEGER - 1;
    }],
    ["enum is null", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_name = { type: "string", enum: null };
    }],
    ["enum is empty", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_name = { type: "string", enum: [] };
    }],
    ["enum contains duplicates", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_name = { type: "string", enum: ["safe", "safe"] };
    }],
    ["enum candidate violates length", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_name = { type: "string", minLength: 5, enum: ["tiny"] };
    }],
    ["default is null", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_listing = { type: "boolean", default: null };
    }],
    ["const is null", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_listing = { type: "boolean", const: null };
    }],
    ["const has the wrong type", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_listing = { type: "boolean", const: "true" };
    }],
    ["const violates its range", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.player_limit = { type: "integer", const: 65, maximum: 64 };
    }],
    ["default has the wrong type", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_listing = { type: "boolean", default: "true" };
    }],
    ["default violates its enum", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_name = { type: "string", default: "hard", enum: ["normal"] };
    }],
    ["default differs from const", (spec: MutableFixedBundle["spec"]) => {
      const properties = variableProperties(spec);
      properties.public_listing = { type: "boolean", default: false, const: true };
    }],
  ] as const)("rejects a fixed Bundle variable schema when %s", async (_name, mutate) => {
    await expectFixedBundleVariablesMutationRejected(paperMCBundle, serverId, mutate);
  });

  test.each([
    ["secrets contains an undeclared key", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).secrets = ["missing_secret"]; }],
    ["secrets contains duplicates", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).secrets = ["rcon_password", "rcon_password"]; }],
    ["secrets contains an invalid key", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).secrets = ["bad-key"]; }],
    ["a binding item is null", (spec: MutableFixedBundle["spec"]) => { asRecord(spec.variables).bindings = [null]; }],
    ["a binding has an unknown field", (spec: MutableFixedBundle["spec"]) => {
      asRecord(spec.variables).bindings = [{ variable: "memory_mb", target: "argument", template: "{{ value }}", extra: true }];
    }],
    ["a binding variable is undeclared", (spec: MutableFixedBundle["spec"]) => {
      asRecord(spec.variables).bindings = [{ variable: "missing", target: "argument", template: "{{ value }}" }];
    }],
    ["an environment binding omits name", (spec: MutableFixedBundle["spec"]) => {
      asRecord(spec.variables).bindings = [{ variable: "memory_mb", target: "environment", template: "{{ value }}" }];
    }],
    ["a file binding omits path", (spec: MutableFixedBundle["spec"]) => {
      asRecord(spec.variables).bindings = [{ variable: "memory_mb", target: "file", template: "{{ value }}" }];
    }],
    ["a binding template has the wrong type", (spec: MutableFixedBundle["spec"]) => {
      asRecord(spec.variables).bindings = [{ variable: "memory_mb", target: "argument", template: null }];
    }],
  ] as const)("rejects malformed fixed Bundle secrets or bindings when %s", async (_name, mutate) => {
    await expectFixedBundleVariablesMutationRejected(paperMCBundle, serverId, mutate);
  });

  test.each([
    ["parent traversal", "../eula.txt"],
    ["absolute path", "/abs"],
    ["backslash separator", "a\\b"],
    ["non-canonical dot segment", "a/./b"],
    ["more than 1024 Unicode code points", ["🧭".repeat(255), "🧭".repeat(255), "🧭".repeat(255), "🧭".repeat(255), "🧭"].join("/")],
    ["a segment longer than 255 Unicode code points", "🧭".repeat(256)],
  ] as const)("rejects a fixed Bundle file binding path containing %s", async (_name, path) => {
    await expectFixedBundleVariablesMutationRejected(paperMCBundle, serverId, (spec) => {
      asRecord(spec.variables).bindings = [{
        variable: "memory_mb",
        target: "file",
        path,
        template: "{{ value }}",
      }];
    });
  });

  test.each([
    ["an ordinary canonical path", "config/server.properties"],
    ["exactly 1024 Unicode code points", ["🧭".repeat(255), "🧭".repeat(255), "🧭".repeat(255), "🧭".repeat(254), "🧭"].join("/")],
    ["a segment of exactly 255 Unicode code points", "🧭".repeat(255)],
  ] as const)("accepts a fixed Bundle file binding path with %s", async (_name, path) => {
    const spec = (paperMCBundle as unknown as MutableFixedBundle).spec;
    const original = spec.variables;
    try {
      spec.variables = structuredClone(original);
      asRecord(spec.variables).bindings = [{
        variable: "memory_mb",
        target: "file",
        path,
        template: "memory={{ value }}",
      }];

      await expect(new MockClient().getStartup(serverId)).resolves.toMatchObject({ serverId });
    } finally {
      spec.variables = original;
    }
  });

  test.each(["default", "const"] as const)("rejects an integer %s outside the JavaScript safe domain while materializing a fixed bundle", async (keyword) => {
    const property = factorioBundle.spec.variables.schema.properties.autosave_interval as Record<string, unknown>;
    const original = { ...property };

    try {
      delete property.default;
      delete property.const;
      delete property.minimum;
      delete property.maximum;
      property[keyword] = Number.MAX_SAFE_INTEGER + 1;
      await expect(new MockClient().getStartup("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")).rejects.toThrow("PACKAGE_INCOMPATIBLE");
    } finally {
      for (const name of Object.keys(property)) delete property[name];
      Object.assign(property, original);
    }
  });

  test.each([
    ["lower", Number.MIN_SAFE_INTEGER],
    ["upper", Number.MAX_SAFE_INTEGER],
  ] as const)("accepts the JavaScript safe integer %s boundary", async (_boundary, value) => {
    vi.useFakeTimers();
    const property = factorioBundle.spec.variables.schema.properties.autosave_interval as Record<string, unknown>;
    const minimum = property.minimum;
    const maximum = property.maximum;
    try {
      delete property.minimum;
      delete property.maximum;
      const client = new MockClient();
      const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
      const current = await client.getServer(factorioId);

      await expect(client.updateStartup(
        factorioId,
        { autosave_interval: value },
        current.generation,
        `mock-safe-integer-${_boundary}-0001`,
      )).resolves.toMatchObject({ type: "reconcile" });
    } finally {
      property.minimum = minimum;
      property.maximum = maximum;
      vi.runAllTimers();
      vi.useRealTimers();
    }
  });

  test.each([
    ["lower", Number.MIN_SAFE_INTEGER - 1],
    ["upper", Number.MAX_SAFE_INTEGER + 1],
  ] as const)("rejects an integer beyond the JavaScript safe %s boundary", async (_boundary, value) => {
    const property = factorioBundle.spec.variables.schema.properties.autosave_interval as Record<string, unknown>;
    const minimum = property.minimum;
    const maximum = property.maximum;
    try {
      delete property.minimum;
      delete property.maximum;
      const client = new MockClient();
      const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
      const current = await client.getServer(factorioId);

      await expect(client.updateStartup(
        factorioId,
        { autosave_interval: value },
        current.generation,
        `mock-unsafe-integer-${_boundary}-0001`,
      )).rejects.toThrow("VALIDATION_FAILED");
    } finally {
      property.minimum = minimum;
      property.maximum = maximum;
    }
  });

  test("enforces fixed bundle types, enums, constants, and numeric bounds", async () => {
    const client = new MockClient();
    const paper = await client.getServer(serverId);
    const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const factorio = await client.getServer(factorioId);

    const invalidFactorioValues: Array<Record<string, StartupValue>> = [
      { difficulty: "unsupported" },
      { autosave_interval: 0 },
      { autosave_interval: 1.5 },
      { public_listing: "true" },
    ];
    for (const [index, values] of invalidFactorioValues.entries()) {
      await expect(client.updateStartup(
        factorioId,
        values,
        factorio.generation,
        `mock-factorio-validation-${index}`,
      )).rejects.toThrow("VALIDATION_FAILED");
    }

    const invalidPaperValues: Array<Record<string, StartupValue>> = [
      { accept_eula: false },
      { accept_eula: null },
      { memory_mb: 1023 },
      { memory_mb: 32769 },
      { memory_mb: 1024.5 },
    ];
    for (const [index, values] of invalidPaperValues.entries()) {
      await expect(client.updateStartup(
        serverId,
        values,
        paper.generation,
        `mock-paper-validation-${index}`,
      )).rejects.toThrow("VALIDATION_FAILED");
    }
  });

  test("replays idempotent startup clears for optional non-secret variables", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
      const before = await client.getServer(factorioId);
      const first = await client.updateStartup(factorioId, { public_listing: null }, before.generation, "mock-startup-clear-0001");
      await expect(client.updateStartup(factorioId, { public_listing: null }, before.generation, "mock-startup-clear-0001")).resolves.toEqual(first);
      expect((await client.getStartup(factorioId)).variables.find((variable) => variable.key === "public_listing")).toMatchObject({ hasValue: false });
    } finally {
      vi.useRealTimers();
    }
  });

  test("rejects empty startup updates like the Go handler and store", async () => {
    const client = new MockClient();
    const current = await client.getServer(serverId);

    await expect(client.updateStartup(
      serverId,
      {},
      current.generation,
      "mock-startup-empty-0001",
    )).rejects.toThrow("VALIDATION_FAILED");
  });

  test("validates string lengths by Unicode code points", async () => {
    vi.useFakeTimers();
    try {
      const client = new MockClient();
      const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
      const current = await client.getServer(factorioId);
      const fortyCodePoints = "🎮".repeat(40);

      await expect(client.updateStartup(
        factorioId,
        { server_name: fortyCodePoints },
        current.generation,
        "mock-startup-unicode-0001",
      )).resolves.toMatchObject({ type: "reconcile" });
    } finally {
      vi.runAllTimers();
      vi.useRealTimers();
    }
  });

  test("canonicalizes startup variables before checking idempotency", async () => {
    const client = new MockClient();
    const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const current = await client.getServer(factorioId);
    const key = "mock-startup-digest-0001";
    const firstValues = { server_name: "Canonical Factory", public_listing: true };
    const first = await client.updateStartup(factorioId, firstValues, current.generation, key);

    await expect(client.updateStartup(
      factorioId,
      { public_listing: true, server_name: "Canonical Factory" },
      current.generation,
      key,
    )).resolves.toEqual(first);
  });

  test("uses a keyed digest for the exact canonical startup payload", async () => {
    const client = new MockClient();
    const factorioId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const current = await client.getServer(factorioId);
    const key = "mock-startup-keyed-digest-0001";
    const variables = { server_token: "replacement-secret" };
    await client.updateStartup(factorioId, variables, current.generation, key);

    const records = (client as unknown as {
      idempotency: Map<string, { operationId: string; signature: string }>;
    }).idempotency;
    const record = records.get(`startup:update:${factorioId}:${key}`);
    const plainDigest = await sha256(JSON.stringify({ generation: current.generation, variables }));
    expect(record?.signature).not.toBe(plainDigest);

    await expect(client.updateStartup(
      factorioId,
      { server_token: "different-secret" },
      current.generation,
      key,
    )).rejects.toThrow("IDEMPOTENCY_KEY_REUSED");
  });
});

async function sha256(value: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}
