import factorioBundle from "../../../spec/game-definition/examples/factorio.json";
import paperMCBundle from "../../../spec/game-definition/examples/papermc.json";
import vintageStoryBundle from "../../../spec/game-definition/examples/vintagestory.json";
import type {
  Allocation,
  AuditEvent,
  Backup,
  ConsoleLine,
  CreateAllocationInput,
  CreateServerInput,
  CreateUserInput,
  FileContent,
  FileEntry,
  GameDefinition,
  Node,
  Operation,
  Overview,
  Server,
  ServerMembership,
  ServerPermission,
  ServerPermissions,
  Session,
  SetupAdminInput,
  SetupStatus,
  Startup,
  StartupCommand,
  StartupValue,
  StartupVariable,
  UpdateUserInput,
  User,
} from "./types";

const now = Date.now();
const iso = (offsetMs = 0) => new Date(now - offsetMs).toISOString();
const id = (_prefix: string) => crypto.randomUUID();
const fixedBundleVariableIdentifier = /^[A-Za-z_][A-Za-z0-9_]*$/;

type QueuedOperationInput = Pick<Operation, "id" | "serverId" | "nodeId" | "type" | "generation" | "createdAt" | "updatedAt">;

function queuedOperation(input: QueuedOperationInput): Operation {
  return {
    ...input,
    status: "queued",
    progress: 0,
    attempt: 1,
    maxAttempts: 1,
    leaseOwner: null,
    leaseExpiresAt: null,
    checkpoint: "queued",
    error: null,
  };
}

function completedOperation(operation: Operation): Operation {
  return {
    ...operation,
    status: "succeeded",
    progress: 100,
    leaseOwner: null,
    leaseExpiresAt: null,
    checkpoint: "completed",
    error: null,
    updatedAt: new Date().toISOString(),
  };
}

function failedOperation(operation: Operation, error: Operation["error"]): Operation {
  return {
    ...operation,
    status: "failed",
    progress: 100,
    leaseOwner: null,
    leaseExpiresAt: null,
    checkpoint: "failed",
    error,
    updatedAt: new Date().toISOString(),
  };
}

type BundleScalar = string | number | boolean;

interface FixedBundleVariableProperty {
  type: StartupVariable["type"];
  default?: BundleScalar;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  enum?: string[];
  const?: BundleScalar;
}

interface FixedBundleBinding {
  variable: string;
  target: "environment" | "argument" | "file";
  name?: string;
  path?: string;
  template: string;
}

interface FixedBundleDocument {
  apiVersion: "gugumanager.io/games/v1alpha1";
  kind: "GameDefinition";
  metadata: { id: string; name: string; version: string };
  spec: {
    release: { version: string };
    compatibility: { platforms: string[] };
    capabilities: string[];
    variables?: {
      schema: {
        required?: string[];
        properties?: Record<string, FixedBundleVariableProperty>;
      };
      secrets?: string[];
      bindings?: FixedBundleBinding[];
    };
    runtime: {
      command: StartupCommand;
      ports: Array<{ name: string; protocol: "tcp" | "udp"; containerPort: number; role: "primary" | "query" | "rcon" | "additional" }>;
    };
  };
}

interface FixedBundleRecord {
  digest: string;
  document: FixedBundleDocument;
  presentation: Pick<GameDefinition, "summary" | "status" | "servers" | "icon" | "defaultMemoryMb" | "defaultDiskGb">;
}

const fixedBundles: FixedBundleRecord[] = [
  {
    digest: "sha256:412759ce8b7832b3762d1a6f34d076ecceebd4ecd7cd7d04f16ac0cff063285b",
    document: paperMCBundle as unknown as FixedBundleDocument,
    presentation: { summary: "高性能 Minecraft Java Dedicated Server", status: "approved", servers: 6, icon: "cube", defaultMemoryMb: 4096, defaultDiskGb: 25 },
  },
  {
    digest: "sha256:d2e03e9dac2b301923b6ebe0e949ceea33420bfb44767c07376d5ffb34c5b62e",
    document: factorioBundle as unknown as FixedBundleDocument,
    presentation: { summary: "稳定的工厂协作存档服务器", status: "approved", servers: 3, icon: "factory", defaultMemoryMb: 4096, defaultDiskGb: 20 },
  },
  {
    digest: "sha256:c2c2cdb82e9ba2cc69e17b9acc99ddd4e75a40dd091e39a19c987927273e7779",
    document: vintageStoryBundle as unknown as FixedBundleDocument,
    presentation: { summary: "强调探索与持久世界的独立服务器", status: "pending", servers: 1, icon: "mountain", defaultMemoryMb: 3072, defaultDiskGb: 18 },
  },
];

const games: GameDefinition[] = fixedBundles.map(({ digest, document, presentation }) => ({
  id: document.metadata.id,
  bundleDigest: digest,
  name: document.metadata.name,
  summary: presentation.summary,
  version: document.metadata.version,
  gameVersion: document.spec.release.version,
  status: presentation.status,
  signed: false,
  verified: false,
  runnable: false,
  supported: false,
  trustLevel: "L0_LOCAL",
  source: "embedded-v1alpha1",
  supportReasons: ["BUNDLE_SIGNATURE_UNVERIFIED", "RUNTIME_TARGET_UNAVAILABLE"],
  capabilities: [...document.spec.capabilities],
  platforms: [...document.spec.compatibility.platforms],
  servers: presentation.servers,
  icon: presentation.icon,
  defaultMemoryMb: presentation.defaultMemoryMb,
  defaultDiskGb: presentation.defaultDiskGb,
}));

const nodes: Node[] = [
  { id: "11111111-1111-4111-8111-111111111111", name: "nimbus-east-01", condition: "available", version: "agent 0.1.0-dev", region: "Shanghai / East", address: "10.0.10.21", lastHeartbeatAt: iso(8_000), cpuCores: 16, memoryBytes: 68_719_476_736, diskBytes: 1_099_511_627_776, allocatedMemoryBytes: 38_654_705_664, allocatedDiskBytes: 461_708_984_320, runningServers: 6, totalServers: 9, capabilities: ["container/v1", "console/v1", "backup/v1", "metrics/v1"] },
  { id: "22222222-2222-4222-8222-222222222222", name: "atlas-edge-02", condition: "available", version: "agent 0.1.0-dev", region: "Singapore / Edge", address: "10.0.20.14", lastHeartbeatAt: iso(12_000), cpuCores: 8, memoryBytes: 34_359_738_368, diskBytes: 549_755_813_888, allocatedMemoryBytes: 17_179_869_184, allocatedDiskBytes: 239_075_328_000, runningServers: 3, totalServers: 5, capabilities: ["container/v1", "console/v1", "metrics/v1"] },
  { id: "33333333-3333-4333-8333-333333333333", name: "harbor-lab-03", condition: "offline", version: "agent 0.0.9", region: "Tokyo / Lab", address: "10.0.30.18", lastHeartbeatAt: iso(180_000), cpuCores: 8, memoryBytes: 34_359_738_368, diskBytes: 549_755_813_888, allocatedMemoryBytes: 8_589_934_592, allocatedDiskBytes: 107_374_182_400, runningServers: 1, totalServers: 2, capabilities: ["container/v1", "console/v1"] },
];

const metricHistory = (cpu: number, memory: number) => Array.from({ length: 12 }, (_, index) => ({ timestamp: iso((11 - index) * 300_000), cpuPercent: Math.max(0, cpu + ((index * 7) % 13) - 6), memoryBytes: memory + (index % 4) * 83_000_000 }));

let servers: Server[] = [
  { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", name: "雾港生存服", description: "Season 04 / whitelist", gameId: games[0].id, gameBundleDigest: games[0].bundleDigest, gameDefinitionVersion: games[0].version, gameName: "PaperMC", gameVersion: games[0].gameVersion, nodeId: nodes[0].id, nodeName: nodes[0].name, lifecycleState: "ready", desiredPower: "running", observedPower: "running", nodeCondition: "available", healthCondition: "healthy", generation: 12, observedGeneration: 12, observedAt: iso(11_000), allocation: "10.0.10.21:25565", ownerName: "Liang Chen", metrics: { cpuPercent: 42, memoryBytes: 3_221_225_472, memoryLimitBytes: 4_294_967_296, diskBytes: 18_420_000_000, diskLimitBytes: 26_843_545_600, playersOnline: 18, playersMax: 40 }, metricHistory: metricHistory(42, 3_221_225_472), updatedAt: iso(11_000) },
  { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "周五工厂", description: "Co-op / autosave enabled", gameId: games[1].id, gameBundleDigest: games[1].bundleDigest, gameDefinitionVersion: games[1].version, gameName: "Factorio", gameVersion: games[1].gameVersion, nodeId: nodes[1].id, nodeName: nodes[1].name, lifecycleState: "ready", desiredPower: "stopped", observedPower: "stopped", nodeCondition: "available", healthCondition: "unknown", generation: 7, observedGeneration: 7, observedAt: iso(120_000), allocation: "10.0.20.14:34197", ownerName: "Mina Wu", metrics: { cpuPercent: 0, memoryBytes: 1_020_000_000, memoryLimitBytes: 4_294_967_296, diskBytes: 12_500_000_000, diskLimitBytes: 21_474_836_480, playersOnline: 0, playersMax: 12 }, metricHistory: metricHistory(0, 1_020_000_000), updatedAt: iso(120_000) },
  { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", name: "Aurora Isles", description: "Experimental / node reconnect required", gameId: games[2].id, gameBundleDigest: games[2].bundleDigest, gameDefinitionVersion: games[2].version, gameName: "Vintage Story", gameVersion: games[2].gameVersion, nodeId: nodes[2].id, nodeName: nodes[2].name, lifecycleState: "ready", desiredPower: "running", observedPower: "unknown", nodeCondition: "offline", healthCondition: "unknown", generation: 3, observedGeneration: 2, observedAt: iso(180_000), allocation: "10.0.30.18:42420", ownerName: "Kai Zhou", metrics: { cpuPercent: 0, memoryBytes: 0, memoryLimitBytes: 3_221_225_472, diskBytes: 8_600_000_000, diskLimitBytes: 19_327_352_832, playersOnline: 0, playersMax: 8 }, metricHistory: metricHistory(16, 1_700_000_000), updatedAt: iso(180_000) },
];

let audit: AuditEvent[] = [
  { id: id("audit"), actorName: "GuGu Admin", action: "server.power.start", targetType: "server", targetName: "雾港生存服", result: "success", operationId: id("op"), createdAt: iso(11_000) },
  { id: id("audit"), actorName: "Mina Wu", action: "backup.create", targetType: "server", targetName: "周五工厂", result: "success", operationId: id("op"), createdAt: iso(21 * 60_000) },
  { id: id("audit"), actorName: "GuGu Admin", action: "catalog.approve", targetType: "game_definition", targetName: "PaperMC 1.0.0", result: "success", operationId: id("op"), createdAt: iso(2 * 60 * 60_000) },
  { id: id("audit"), actorName: "System", action: "server.reconcile", targetType: "server", targetName: "Aurora Isles", result: "failure", operationId: id("op"), createdAt: iso(3 * 60 * 60_000) },
];

const consoles: Record<string, ConsoleLine[]> = {
  [servers[0].id]: [
    { sequence: 1841, timestamp: iso(44_000), stream: "system", message: "[panel] attached to console stream (development adapter)" },
    { sequence: 1842, timestamp: iso(38_000), stream: "stdout", message: "[18:24:11 INFO]: Done (4.892s)! For help, type \"help\"" },
    { sequence: 1843, timestamp: iso(21_000), stream: "stdout", message: "[18:24:28 INFO]: There are 18 of a max of 40 players online" },
    { sequence: 1844, timestamp: iso(8_000), stream: "stdout", message: "[18:24:41 INFO]: Saving the game (this may take a moment!)" },
  ],
};

const files: Record<string, FileEntry[]> = {
  [servers[0].id]: [
    { name: "config", path: "config", kind: "directory", sizeBytes: 0, modifiedAt: iso(8 * 60 * 60_000) },
    { name: "logs", path: "logs", kind: "directory", sizeBytes: 0, modifiedAt: iso(11 * 60_000) },
    { name: "world", path: "world", kind: "directory", sizeBytes: 0, modifiedAt: iso(3 * 60_000) },
    { name: "server.properties", path: "server.properties", kind: "file", sizeBytes: 2940, modifiedAt: iso(2 * 60 * 60_000) },
    { name: "eula.txt", path: "eula.txt", kind: "file", sizeBytes: 11, modifiedAt: iso(4 * 60 * 60_000) },
    { name: "paper-global.yml", path: "config/paper-global.yml", kind: "file", sizeBytes: 8184, modifiedAt: iso(6 * 60 * 60_000) },
    { name: "level.dat", path: "world/level.dat", kind: "file", sizeBytes: 2371, modifiedAt: iso(3 * 60_000) },
    { name: "region", path: "world/region", kind: "directory", sizeBytes: 0, modifiedAt: iso(3 * 60_000) },
    { name: "r.0.0.mca", path: "world/region/r.0.0.mca", kind: "file", sizeBytes: 12_845_056, modifiedAt: iso(3 * 60_000) },
  ],
};

const fileContents: Record<string, Record<string, string>> = {
  [servers[0].id]: {
    "server.properties": "motd=GuGuManager development\nmax-players=40\n",
    "eula.txt": "eula=true\n",
    "config/paper-global.yml": "_version: 31\nproxies:\n  velocity:\n    enabled: false\n",
    "world/level.dat": "development-world-state\n",
    "world/region/r.0.0.mca": "development-region-fixture\n",
  },
  [servers[1].id]: {
    "server-settings.json": "{\n  \"name\": \"Friday Factory\"\n}\n",
    "saves/world.zip": "development-save-fixture\n",
  },
};

const backups: Record<string, Backup[]> = {
  [servers[0].id]: [
    { id: id("backup"), name: "pre-season-04", status: "ready", sizeBytes: 8_420_000_000, checksum: "sha256:5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a5c7a", createdAt: iso(25 * 60 * 60_000) },
    { id: id("backup"), name: "before-config-tune", status: "ready", sizeBytes: 8_190_000_000, checksum: "sha256:8a028a028a028a028a028a028a028a028a028a028a028a028a028a028a028a02", createdAt: iso(4 * 24 * 60 * 60_000) },
  ],
  [servers[1].id]: [{ id: id("backup"), name: "autosave-2026-08-06", status: "ready", sizeBytes: 7_420_000_000, checksum: "sha256:30c230c230c230c230c230c230c230c230c230c230c230c230c230c230c230c2", createdAt: iso(20 * 60 * 60_000) }],
};

const seededAllocations: Record<string, Allocation[]> = {
  [servers[0].id]: [{ id: "10000000-0000-4000-8000-000000000001", serverId: servers[0].id, nodeId: servers[0].nodeId, bindIp: "10.0.10.21", port: 25565, protocol: "tcp", primary: true, createdAt: iso(25 * 24 * 60 * 60_000), updatedAt: iso(11_000) }],
  [servers[1].id]: [{ id: "10000000-0000-4000-8000-000000000002", serverId: servers[1].id, nodeId: servers[1].nodeId, bindIp: "10.0.20.14", port: 34197, protocol: "udp", primary: true, createdAt: iso(18 * 24 * 60 * 60_000), updatedAt: iso(120_000) }],
  [servers[2].id]: [{ id: "10000000-0000-4000-8000-000000000003", serverId: servers[2].id, nodeId: servers[2].nodeId, bindIp: "10.0.30.18", port: 42420, protocol: "tcp", primary: true, createdAt: iso(7 * 24 * 60 * 60_000), updatedAt: iso(180_000) }],
};

type StartupVariableDefinition = Omit<StartupVariable, "hasValue" | "value">;

interface StoredStartup {
  values: Record<string, Exclude<StartupValue, null>>;
}

interface FixedStartup {
  command: StartupCommand;
  variables: StartupVariableDefinition[];
  bindings: FixedBundleBinding[];
}

const seededStartup: Record<string, StoredStartup> = {
  [servers[0].id]: createStoredStartup(servers[0], { rcon_password: "development-secret" }),
  [servers[1].id]: createStoredStartup(servers[1], { server_token: "development-token" }),
  [servers[2].id]: createStoredStartup(servers[2]),
};

export interface MockClientOptions {
  setupRequired?: boolean;
  bootstrapToken?: string;
  catalog?: readonly GameDefinition[];
}

interface StoredMockUser {
  user: User;
  password: string;
}

interface StoredResetToken {
  userId: string;
  expiresAt: number;
}

const defaultAdminId = "00000000-0000-4000-8000-000000000001";
const allowedRoles = new Set(["platform_admin", "server_owner"]);
const allowedPermissions = new Set<ServerPermission>([
  "servers.read",
  "servers.power",
  "servers.console",
  "servers.files.read",
  "servers.files.write",
  "servers.backups.read",
  "servers.backups.create",
  "servers.backups.restore",
  "servers.backups.delete",
  "servers.network.read",
  "servers.network.write",
  "servers.startup.read",
  "servers.startup.write",
]);

export class MockClient {
  private catalog: GameDefinition[];
  private users = new Map<string, StoredMockUser>();
  private userOrder: string[] = [];
  private currentUserId: string | null = null;
  private setupRequired: boolean;
  private bootstrapToken: string;
  private bootstrapExpiresAt: number;
  private resetTokens = new Map<string, StoredResetToken>();
  private memberships = new Map<string, ServerMembership>();
  private operations = new Map<string, Operation>();
  private idempotency = new Map<string, { operationId: string; signature: string }>();
  private requestDigestKey = crypto.getRandomValues(new Uint8Array(32));
  private allocations = cloneAllocations(seededAllocations);
  private startup = cloneStartup(seededStartup);

  constructor(options: MockClientOptions = {}) {
    this.catalog = (options.catalog ?? games).map((game) => ({
      ...game,
      supportReasons: [...game.supportReasons],
      capabilities: [...game.capabilities],
      platforms: [...game.platforms],
    }));
    this.setupRequired = options.setupRequired ?? false;
    this.bootstrapToken = options.bootstrapToken ?? "mock-bootstrap-token-abcdefghijklmnopqrstuvwxyz";
    this.bootstrapExpiresAt = Date.now() + 15 * 60 * 1000;
    if (!this.setupRequired) {
      const timestamp = new Date().toISOString();
      this.storeUser({
        id: defaultAdminId,
        email: "admin@gugu.local",
        displayName: "GuGu Admin",
        roles: ["platform_admin"],
        status: "active",
        createdAt: timestamp,
        updatedAt: timestamp,
      }, "gugu-dev-2026");
    }
  }

  async setupStatus(): Promise<SetupStatus> {
    return this.setupRequired
      ? { required: true, bootstrapExpiresAt: new Date(this.bootstrapExpiresAt).toISOString() }
      : { required: false };
  }

  async setupAdmin(input: SetupAdminInput): Promise<User> {
    if (!this.setupRequired || this.users.size > 0) throw new Error("SETUP_ALREADY_COMPLETE");
    if (Date.now() > this.bootstrapExpiresAt || input.bootstrapToken !== this.bootstrapToken) throw new Error("BOOTSTRAP_TOKEN_INVALID");
    const email = normalizeMockEmail(input.email);
    const displayName = validateMockDisplayName(input.displayName);
    validateMockPassword(input.password);
    const timestamp = new Date().toISOString();
    const user: User = {
      id: id("user"),
      email,
      displayName,
      roles: ["platform_admin"],
      status: "active",
      createdAt: timestamp,
      updatedAt: timestamp,
    };
    this.storeUser(user, input.password);
    this.setupRequired = false;
    this.bootstrapToken = "";
    return cloneMockUser(user);
  }

  async login(email: string, password: string): Promise<Session> {
    const normalized = email.trim().toLowerCase();
    const record = [...this.users.values()].find((candidate) => candidate.user.email === normalized);
    if (!record || record.user.status !== "active" || record.password !== password) throw new Error("AUTH_INVALID");
    this.currentUserId = record.user.id;
    return this.sessionFor(record.user);
  }
  async sessionInfo(): Promise<Session> {
    const record = this.currentUserId ? this.users.get(this.currentUserId) : undefined;
    if (!record || record.user.status !== "active") {
      this.currentUserId = null;
      throw new Error("AUTH_REQUIRED");
    }
    return this.sessionFor(record.user);
  }
  async logout(): Promise<void> { this.currentUserId = null; }

  async listUsers(): Promise<User[]> {
    this.requirePlatformAdmin();
    return this.userOrder.map((userId) => cloneMockUser(this.users.get(userId)!.user));
  }

  async createUser(input: CreateUserInput): Promise<User> {
    this.requirePlatformAdmin();
    const email = normalizeMockEmail(input.email);
    if ([...this.users.values()].some((record) => record.user.email === email)) throw new Error("EMAIL_CONFLICT");
    const roles = normalizeMockRoles(input.roles);
    const displayName = validateMockDisplayName(input.displayName);
    validateMockPassword(input.password);
    const timestamp = new Date().toISOString();
    const user: User = { id: id("user"), email, displayName, roles, status: "active", createdAt: timestamp, updatedAt: timestamp };
    this.storeUser(user, input.password);
    return cloneMockUser(user);
  }

  async updateUser(userId: string, input: UpdateUserInput): Promise<User> {
    this.requirePlatformAdmin();
    const record = this.users.get(userId);
    if (!record) throw new Error("NOT_FOUND");
    if (input.displayName === undefined && input.roles === undefined && input.status === undefined) throw new Error("VALIDATION_FAILED");
    const nextRoles = input.roles === undefined ? record.user.roles : normalizeMockRoles(input.roles);
    const nextStatus = input.status ?? record.user.status;
    if (nextStatus !== "active" && nextStatus !== "disabled") throw new Error("VALIDATION_FAILED");
    if (record.user.roles.includes("platform_admin") && record.user.status === "active" && (!nextRoles.includes("platform_admin") || nextStatus !== "active") && this.activeAdminCount() === 1) {
      throw new Error("VALIDATION_FAILED");
    }
    record.user = {
      ...record.user,
      ...(input.displayName === undefined ? {} : { displayName: validateMockDisplayName(input.displayName) }),
      roles: [...nextRoles],
      status: nextStatus,
      updatedAt: new Date().toISOString(),
    };
    if (record.user.status === "disabled" && this.currentUserId === userId) this.currentUserId = null;
    return cloneMockUser(record.user);
  }

  async issuePasswordResetToken(userId: string): Promise<{ token: string; expiresAt: string }> {
    this.requirePlatformAdmin();
    const record = this.users.get(userId);
    if (!record) throw new Error("NOT_FOUND");
    if (record.user.status !== "active") throw new Error("OPERATION_CONFLICT");
    const token = `mock-reset-${crypto.randomUUID()}-${crypto.randomUUID()}`;
    const expiresAt = Date.now() + 15 * 60 * 1000;
    this.resetTokens.set(token, { userId, expiresAt });
    return { token, expiresAt: new Date(expiresAt).toISOString() };
  }

  async resetPassword(token: string, password: string): Promise<void> {
    validateMockPassword(password);
    const reset = this.resetTokens.get(token);
    const record = reset ? this.users.get(reset.userId) : undefined;
    if (!reset || reset.expiresAt < Date.now() || !record || record.user.status !== "active") throw new Error("AUTH_INVALID_RESET_TOKEN");
    this.resetTokens.delete(token);
    record.password = password;
    record.user = { ...record.user, updatedAt: new Date().toISOString() };
    if (this.currentUserId === record.user.id) this.currentUserId = null;
  }

  async getServerMembership(serverId: string, userId: string): Promise<ServerMembership> {
    this.requirePlatformAdmin();
    this.requireServer(serverId);
    if (!this.users.has(userId)) throw new Error("NOT_FOUND");
    const membership = this.memberships.get(membershipKey(serverId, userId));
    if (!membership) throw new Error("NOT_FOUND");
    return cloneMembership(membership);
  }

  async putServerMembership(serverId: string, userId: string, permissions: ServerPermission[]): Promise<ServerMembership> {
    this.requirePlatformAdmin();
    this.requireServer(serverId);
    const user = this.users.get(userId);
    if (!user || user.user.status !== "active") throw new Error("NOT_FOUND");
    const normalized = [...new Set(permissions)];
    if (!normalized.includes("servers.read") || normalized.some((permission) => !allowedPermissions.has(permission))) throw new Error("VALIDATION_FAILED");
    const key = membershipKey(serverId, userId);
    const existing = this.memberships.get(key);
    const timestamp = new Date().toISOString();
    const membership: ServerMembership = {
      serverId,
      userId,
      permissions: normalized,
      createdAt: existing?.createdAt ?? timestamp,
      updatedAt: timestamp,
    };
    this.memberships.set(key, membership);
    return cloneMembership(membership);
  }

  async deleteServerMembership(serverId: string, userId: string): Promise<void> {
    this.requirePlatformAdmin();
    this.requireServer(serverId);
    if (!this.users.has(userId)) throw new Error("NOT_FOUND");
    if (!this.memberships.delete(membershipKey(serverId, userId))) throw new Error("NOT_FOUND");
  }

  async getServerPermissions(serverId: string): Promise<ServerPermissions> {
    const record = this.currentUserId ? this.users.get(this.currentUserId) : undefined;
    if (!record || record.user.status !== "active") throw new Error("AUTH_REQUIRED");
    this.requireServer(serverId);
    if (record.user.roles.includes("platform_admin")) {
      return { serverId, permissions: [...allowedPermissions].sort() };
    }
    const membership = this.memberships.get(membershipKey(serverId, record.user.id));
    if (!membership || !membership.permissions.includes("servers.read")) throw new Error("NOT_FOUND");
    return { serverId, permissions: [...membership.permissions].sort() };
  }
  async getOverview(): Promise<Overview> {
    return { environment: "development", serverCount: servers.length, runningServerCount: servers.filter((server) => server.observedPower === "running").length, onlineNodeCount: nodes.filter((node) => node.condition === "available").length, totalNodeCount: nodes.length, queuedOperationCount: [...this.operations.values()].filter((operation) => !["succeeded", "failed"].includes(operation.status)).length, cpuPercent: 42, memoryUsedBytes: 4_241_225_472, memoryTotalBytes: 103_079_215_104, recentActivity: audit.slice(0, 5) };
  }
  async listServers(query = ""): Promise<Server[]> {
    const lower = query.toLowerCase();
    const visible = this.visibleServers();
    return visible.filter((server) => `${server.name} ${server.gameName} ${server.nodeName}`.toLowerCase().includes(lower));
  }
  async getServer(serverId: string): Promise<Server> {
    const server = servers.find((item) => item.id === serverId);
    if (!server) throw new Error("NOT_FOUND");
    if (!this.canReadServer(serverId)) throw new Error("FORBIDDEN");
    return server;
  }
  async listNodes(): Promise<Node[]> { return nodes; }
  async listGames(): Promise<GameDefinition[]> { return this.catalog; }
  async listAudit(): Promise<AuditEvent[]> { return audit; }
  async listOperations(): Promise<Operation[]> {
    return [...this.operations.values()]
	  .filter((operation) => this.canReadServer(operation.serverId))
      .sort((left, right) => right.createdAt.localeCompare(left.createdAt) || left.id.localeCompare(right.id));
  }
  async getOperation(operationId: string): Promise<Operation> {
    const operation = this.operations.get(operationId);
    if (!operation) throw new Error("operation not found");
    if (!this.canReadServer(operation.serverId)) throw new Error("FORBIDDEN");
    return operation;
  }
  async requestPower(serverId: string, action: "start" | "stop" | "restart" | "kill", key: string): Promise<Operation> {
    const scope = `power:${serverId}:${key}`;
    const signature = JSON.stringify({ action });
    const record = this.idempotency.get(scope);
    if (record) {
      if (record.signature !== signature) throw new Error("IDEMPOTENCY_KEY_REUSED");
      const existing = this.operations.get(record.operationId);
      if (existing) return existing;
    }
    const server = servers.find((item) => item.id === serverId);
    if (!server) throw new Error("server not found");
    const node = nodes.find((item) => item.id === server.nodeId);
    if (!node || node.condition !== "available") throw new Error("NODE_OFFLINE");
    if (server.lifecycleState !== "ready") throw new Error("OPERATION_IN_PROGRESS");
    const active = [...this.operations.values()].find((item) => item.serverId === serverId && !["succeeded", "failed"].includes(item.status));
    if (active) {
      if (active.type !== action) throw new Error("OPERATION_IN_PROGRESS");
      this.idempotency.set(scope, { operationId: active.id, signature });
      return active;
    }
    if (action === "start" || action === "restart") {
      const startup = startupFromFixedBundle(server);
      const values = this.startup[serverId]?.values;
      if (!values || startup.variables.some((variable) => variable.required && !Object.hasOwn(values, variable.key))) {
        throw new Error("VALIDATION_FAILED");
      }
    }
    const generation = server.generation + 1;
    const operation = queuedOperation({ id: id("op"), serverId, nodeId: server.nodeId, type: action, generation, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() });
    this.operations.set(operation.id, operation);
    this.idempotency.set(scope, { operationId: operation.id, signature });
    servers = servers.map((item) => item.id === serverId ? { ...item, desiredPower: action === "stop" || action === "kill" ? "stopped" : action === "start" ? "running" : item.desiredPower, observedPower: action === "start" ? "starting" : "stopping", generation, updatedAt: new Date().toISOString() } : item);
    window.setTimeout(() => {
      const completedServer = this.completeOperationForServer(operation.id);
      if (!completedServer) return;
      servers = servers.map((item) => item.id === completedServer.id ? { ...item, observedPower: action === "start" || (action === "restart" && item.desiredPower === "running") ? "running" : "stopped", healthCondition: action === "start" || action === "restart" ? "healthy" : "unknown", observedGeneration: generation, observedAt: new Date().toISOString(), updatedAt: new Date().toISOString() } : item);
    }, 1100);
    return operation;
  }
  async createServer(input: CreateServerInput, key: string): Promise<Operation> {
    const scope = `server:create:${key}`;
    const signature = JSON.stringify(input);
    const record = this.idempotency.get(scope);
    if (record) {
      if (record.signature !== signature) throw new Error("IDEMPOTENCY_KEY_REUSED");
      const existing = this.operations.get(record.operationId);
      if (existing) return existing;
    }
    const node = nodes.find((item) => item.id === input.nodeId);
    const game = this.catalog.find((item) => item.id === input.gameDefinitionId);
    if (!node || !game) throw new Error("node or game not found");
    if (game.status !== "approved") throw new Error("GAME_DEFINITION_NOT_APPROVED");
    if (!game.runnable) throw new Error("PACKAGE_INCOMPATIBLE");
    if (game.bundleDigest !== input.gameBundleDigest) throw new Error("PACKAGE_INCOMPATIBLE");
    if (node.condition !== "available") throw new Error("NODE_OFFLINE");
    const [protocol, firstPort] = defaultAllocationSettings(game.id);
    const port = nextAvailableAllocationPort(this.allocations, node.id, node.address, protocol, firstPort);
    if (port === null) throw new Error("PORT_CONFLICT");
    const timestamp = new Date().toISOString();
    const serverId = id("server");
    const allocation: Allocation = {
      id: id("allocation"),
      serverId,
      nodeId: node.id,
      bindIp: node.address,
      port,
      protocol,
      primary: true,
      createdAt: timestamp,
      updatedAt: timestamp,
    };
    const server: Server = { id: serverId, name: input.name, description: "新建开发服务器", gameId: game.id, gameBundleDigest: game.bundleDigest, gameDefinitionVersion: game.version, gameName: game.name, gameVersion: game.gameVersion, nodeId: node.id, nodeName: node.name, lifecycleState: "provisioning", desiredPower: "stopped", observedPower: "unknown", nodeCondition: node.condition, healthCondition: "unknown", generation: 1, observedGeneration: 0, observedAt: timestamp, allocation: allocationAddress(allocation), ownerName: "GuGu Admin", metrics: { cpuPercent: 0, memoryBytes: 0, memoryLimitBytes: input.memoryMb * 1024 * 1024, diskBytes: 0, diskLimitBytes: input.diskGb * 1024 * 1024 * 1024 }, metricHistory: [], updatedAt: timestamp };
    const fixedStartup = startupFromFixedBundle(server);
    const startupOverrides: Record<string, StartupValue> = {};
    if (fixedStartup.variables.some((variable) => variable.key === "memory_mb")) startupOverrides.memory_mb = input.memoryMb;
    const storedStartup = createStoredStartup(server, startupOverrides);
    servers = [server, ...servers];
    this.allocations[server.id] = [allocation];
    this.startup[server.id] = storedStartup;
    const operation = queuedOperation({ id: id("op"), serverId: server.id, nodeId: node.id, type: "provision", generation: 1, createdAt: timestamp, updatedAt: timestamp });
    this.operations.set(operation.id, operation);
    this.idempotency.set(scope, { operationId: operation.id, signature });
    window.setTimeout(() => {
      const completedServer = this.completeOperationForServer(operation.id);
      if (!completedServer) return;
      servers = servers.map((item) => item.id === completedServer.id ? { ...item, lifecycleState: "ready", observedPower: "stopped", observedGeneration: operation.generation, observedAt: new Date().toISOString(), updatedAt: new Date().toISOString() } : item);
    }, 1100);
    return operation;
  }
  async getConsole(serverId: string): Promise<ConsoleLine[]> {
    this.requireServer(serverId);
    return consoles[serverId] ?? [];
  }
  async sendCommand(serverId: string, command: string): Promise<void> {
    const normalized = command.trim();
    if (!normalized || Array.from(normalized).length > 512) throw new Error("VALIDATION_FAILED");
    const server = this.requireServer(serverId);
    if (server.observedPower !== "running") throw new Error("OPERATION_CONFLICT");
    const lines = consoles[serverId] ?? [];
    const sequence = (lines.at(-1)?.sequence ?? 0) + 1;
    consoles[serverId] = [...lines, { sequence, timestamp: new Date().toISOString(), stream: "command", message: `> ${normalized}` }, { sequence: sequence + 1, timestamp: new Date().toISOString(), stream: "stdout", message: "[panel] command accepted by development adapter" }];
  }
  async getFiles(serverId: string, path = ""): Promise<FileEntry[]> {
    this.requireServer(serverId);
    const directory = normalizeRelativePath(path);
    const prefix = directory ? `${directory}/` : "";
    return (files[serverId] ?? []).filter((entry) => {
      if (!entry.path.startsWith(prefix)) return false;
      const childPath = entry.path.slice(prefix.length);
      return childPath.length > 0 && !childPath.includes("/");
    }).sort((left, right) => left.kind === right.kind ? left.name.localeCompare(right.name) : left.kind === "directory" ? -1 : 1);
  }
  async getFileContent(serverId: string, requestedPath: string): Promise<FileContent> {
    this.requireServer(serverId);
    const path = normalizeRelativePath(requestedPath);
    const entry = (files[serverId] ?? []).find((candidate) => candidate.path === path);
    if (!entry || entry.kind !== "file") throw new Error("NOT_FOUND");
    const content = fileContents[serverId]?.[path] ?? `# development fixture: ${path}\n`;
    return { path, content, encoding: "utf-8", sizeBytes: new TextEncoder().encode(content).length, modifiedAt: entry.modifiedAt };
  }
  async writeFile(serverId: string, input: { path: string; content: string; encoding?: "utf-8" | "base64" }): Promise<void> {
    this.requireServer(serverId);
    const path = normalizeRelativePath(input.path);
    if (!path) throw new Error("PATH_ESCAPE_BLOCKED");
    const parent = parentPath(path);
    if (parent && !(files[serverId] ?? []).some((entry) => entry.path === parent && entry.kind === "directory")) throw new Error("NOT_FOUND");
    const content = input.encoding === "base64" ? atob(input.content) : input.content;
    const now = new Date().toISOString();
    const current = files[serverId] ?? [];
    const existing = current.find((entry) => entry.path === path);
    if (existing?.kind === "directory") throw new Error("VALIDATION_FAILED");
    const next: FileEntry = { name: path.split("/").at(-1) ?? path, path, kind: "file", sizeBytes: new TextEncoder().encode(content).length, modifiedAt: now };
    files[serverId] = existing ? current.map((entry) => entry.path === path ? next : entry) : [...current, next];
    fileContents[serverId] = { ...(fileContents[serverId] ?? {}), [path]: content };
  }
  async createDirectory(serverId: string, requestedPath: string): Promise<void> {
    this.requireServer(serverId);
    const path = normalizeRelativePath(requestedPath);
    if (!path) throw new Error("PATH_ESCAPE_BLOCKED");
    const current = files[serverId] ?? [];
    if (current.some((entry) => entry.path === path)) throw new Error("OPERATION_CONFLICT");
    const parent = parentPath(path);
    if (parent && !current.some((entry) => entry.path === parent && entry.kind === "directory")) throw new Error("NOT_FOUND");
    files[serverId] = [...current, { name: path.split("/").at(-1) ?? path, path, kind: "directory", sizeBytes: 0, modifiedAt: new Date().toISOString() }];
  }
  async moveFile(serverId: string, input: { source: string; destination: string; replace?: boolean }): Promise<void> {
    this.requireServer(serverId);
    const source = normalizeRelativePath(input.source);
    const destination = normalizeRelativePath(input.destination);
    const current = files[serverId] ?? [];
    const sourceEntry = current.find((entry) => entry.path === source);
    if (!sourceEntry) throw new Error("NOT_FOUND");
    const destinationEntry = current.find((entry) => entry.path === destination);
    if (destinationEntry && !input.replace) throw new Error("OPERATION_CONFLICT");
    if (sourceEntry.kind === "directory" && (destination === source || destination.startsWith(`${source}/`))) throw new Error("VALIDATION_FAILED");
    const parent = parentPath(destination);
    if (parent && !current.some((entry) => entry.path === parent && entry.kind === "directory" && !entry.path.startsWith(`${source}/`))) throw new Error("NOT_FOUND");
    const moved = current
      .filter((entry) => !destinationEntry || entry.path !== destination)
      .map((entry) => entry.path === source || entry.path.startsWith(`${source}/`) ? {
        ...entry,
        path: destination + entry.path.slice(source.length),
        name: entry.path === source ? destination.split("/").at(-1) ?? destination : entry.name,
        modifiedAt: new Date().toISOString(),
      } : entry);
    files[serverId] = moved;
    const contents = fileContents[serverId] ?? {};
    const nextContents = { ...contents };
    Object.entries(contents).forEach(([path, content]) => {
      if (path === source || path.startsWith(`${source}/`)) {
        delete nextContents[path];
        nextContents[destination + path.slice(source.length)] = content;
      }
    });
    fileContents[serverId] = nextContents;
  }
  async deleteFile(serverId: string, requestedPath: string, recursive: boolean): Promise<void> {
    this.requireServer(serverId);
    const path = normalizeRelativePath(requestedPath);
    if (!path) throw new Error("PATH_ESCAPE_BLOCKED");
    const current = files[serverId] ?? [];
    const target = current.find((entry) => entry.path === path);
    if (!target) throw new Error("NOT_FOUND");
    const hasChildren = current.some((entry) => entry.path.startsWith(`${path}/`));
    if (target.kind === "directory" && hasChildren && !recursive) throw new Error("OPERATION_CONFLICT");
    files[serverId] = current.filter((entry) => entry.path !== path && !(recursive && entry.path.startsWith(`${path}/`)));
    const contents = { ...(fileContents[serverId] ?? {}) };
    Object.keys(contents).forEach((entryPath) => {
      if (entryPath === path || recursive && entryPath.startsWith(`${path}/`)) delete contents[entryPath];
    });
    fileContents[serverId] = contents;
  }
  async getBackups(serverId: string): Promise<Backup[]> {
    this.requireServer(serverId);
    return backups[serverId] ?? [];
  }
  async createBackup(serverId: string, key: string): Promise<Operation> {
    const server = servers.find((item) => item.id === serverId);
    if (!server) throw new Error("server not found");
    const signature = "{}";
    const scope = `backup:create:${serverId}:${key}`;
    const record = this.idempotency.get(scope);
    if (record) {
      if (record.signature !== signature) throw new Error("IDEMPOTENCY_KEY_REUSED");
      const existing = this.operations.get(record.operationId);
      if (existing) return existing;
    }
    const active = [...this.operations.values()].find((item) => item.serverId === serverId && !["succeeded", "failed"].includes(item.status));
    if (active) throw new Error("OPERATION_IN_PROGRESS");
    const operation = queuedOperation({ id: id("op"), serverId, nodeId: server.nodeId, type: "backup", generation: server.generation, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() });
    this.operations.set(operation.id, operation);
    this.idempotency.set(scope, { operationId: operation.id, signature });
    window.setTimeout(() => {
      if (!this.completeOperationForServer(operation.id)) return;
      backups[serverId] = [{ id: id("backup"), name: "manual-development", status: "ready", sizeBytes: 8_420_000_000, checksum: `sha256:${"de".repeat(32)}`, createdAt: new Date().toISOString() }, ...(backups[serverId] ?? [])];
    }, 1100);
    return operation;
  }
  async restoreBackup(serverId: string, backupId: string, key: string): Promise<Operation> {
    const server = this.requireServer(serverId);
    if (server.observedPower !== "stopped") throw new Error("RESTORE_LOCKED");
    const backup = (backups[serverId] ?? []).find((item) => item.id === backupId);
    if (!backup) throw new Error("NOT_FOUND");
    if (backup.status !== "ready") throw new Error("RESTORE_LOCKED");
    const scope = `backup:restore:${serverId}:${backupId}:${key}`;
    const record = this.idempotency.get(scope);
    if (record) return this.operations.get(record.operationId) as Operation;
    const active = [...this.operations.values()].find((item) => item.serverId === serverId && !["succeeded", "failed"].includes(item.status));
    if (active) throw new Error("OPERATION_IN_PROGRESS");
    const operation = queuedOperation({ id: id("op"), serverId, nodeId: server.nodeId, type: "restore", generation: server.generation + 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() });
    this.operations.set(operation.id, operation);
    this.idempotency.set(scope, { operationId: operation.id, signature: backupId });
    backup.status = "restoring";
    servers = servers.map((item) => item.id === serverId ? { ...item, generation: operation.generation, updatedAt: new Date().toISOString() } : item);
    window.setTimeout(() => {
      const completedServer = this.completeOperationForServer(operation.id);
      backup.status = "ready";
      if (!completedServer) return;
      servers = servers.map((item) => item.id === completedServer.id ? { ...item, observedGeneration: operation.generation, desiredPower: "stopped", observedPower: "stopped", updatedAt: new Date().toISOString(), observedAt: new Date().toISOString() } : item);
    }, 1100);
    return operation;
  }
  async deleteBackup(serverId: string, backupId: string, key: string): Promise<Operation> {
    const server = this.requireServer(serverId);
    const backup = (backups[serverId] ?? []).find((item) => item.id === backupId);
    if (!backup) throw new Error("NOT_FOUND");
    if (backup.status !== "ready" && backup.status !== "failed") throw new Error("RESTORE_LOCKED");
    const previousStatus = backup.status;
    const scope = `backup:delete:${serverId}:${backupId}:${key}`;
    const record = this.idempotency.get(scope);
    if (record) return this.operations.get(record.operationId) as Operation;
    const active = [...this.operations.values()].find((item) => item.serverId === serverId && !["succeeded", "failed"].includes(item.status));
    if (active) throw new Error("OPERATION_IN_PROGRESS");
    const operation = queuedOperation({ id: id("op"), serverId, nodeId: server.nodeId, type: "backup-delete", generation: server.generation, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() });
    this.operations.set(operation.id, operation);
    this.idempotency.set(scope, { operationId: operation.id, signature: backupId });
    backup.status = "deleting";
    window.setTimeout(() => {
      if (!this.completeOperationForServer(operation.id)) {
        const retained = (backups[serverId] ?? []).find((item) => item.id === backupId);
        if (retained?.status === "deleting") retained.status = previousStatus;
        return;
      }
      backups[serverId] = (backups[serverId] ?? []).filter((item) => item.id !== backupId);
    }, 1100);
    return operation;
  }

  async getAllocations(serverId: string): Promise<Allocation[]> {
    this.requireServer(serverId);
    return (this.allocations[serverId] ?? []).map((allocation) => ({ ...allocation }));
  }

  async createAllocation(serverId: string, input: CreateAllocationInput, expectedGeneration: number, key: string): Promise<Operation> {
    const server = this.requireServer(serverId);
    const bindIp = normalizeBindIp(input.bindIp);
    const protocol = input.protocol.trim().toLowerCase();
    if (!bindIp || input.port < 1 || input.port > 65535 || !Number.isInteger(input.port) || !["tcp", "udp"].includes(protocol)) throw new Error("VALIDATION_FAILED");
    const normalizedInput: CreateAllocationInput = { ...input, bindIp, protocol: protocol as CreateAllocationInput["protocol"] };
    const signature = JSON.stringify({ expectedGeneration, input: normalizedInput });
    return this.reconcileConfiguration(server, `allocation:create:${serverId}:${key}`, signature, expectedGeneration, () => {
      const conflict = Object.values(this.allocations).flat().some((allocation) => allocation.nodeId === server.nodeId && allocation.bindIp === bindIp && allocation.port === input.port && allocation.protocol === protocol);
      if (conflict) throw new Error("PORT_CONFLICT");
      const timestamp = new Date().toISOString();
      const current = this.allocations[serverId] ?? [];
      const primary = input.primary || current.length === 0;
      const next = current.map((allocation) => primary ? { ...allocation, primary: false, updatedAt: timestamp } : allocation);
      this.allocations[serverId] = [...next, { id: id("allocation"), serverId, nodeId: server.nodeId, bindIp, port: input.port, protocol: protocol as "tcp" | "udp", primary, createdAt: timestamp, updatedAt: timestamp }];
    });
  }

  async setPrimaryAllocation(serverId: string, allocationId: string, expectedGeneration: number, key: string): Promise<Operation> {
    const server = this.requireServer(serverId);
    const signature = JSON.stringify({ expectedGeneration, allocationId, primary: true });
    return this.reconcileConfiguration(server, `allocation:primary:${serverId}:${allocationId}:${key}`, signature, expectedGeneration, () => {
      const target = (this.allocations[serverId] ?? []).find((allocation) => allocation.id === allocationId);
      if (!target) throw new Error("NOT_FOUND");
      const timestamp = new Date().toISOString();
      this.allocations[serverId] = (this.allocations[serverId] ?? []).map((allocation) => ({ ...allocation, primary: allocation.id === allocationId, updatedAt: timestamp }));
    });
  }

  async deleteAllocation(serverId: string, allocationId: string, expectedGeneration: number, key: string): Promise<Operation> {
    const server = this.requireServer(serverId);
    const signature = JSON.stringify({ expectedGeneration, allocationId });
    return this.reconcileConfiguration(server, `allocation:delete:${serverId}:${allocationId}:${key}`, signature, expectedGeneration, () => {
      const current = this.allocations[serverId] ?? [];
      const target = current.find((allocation) => allocation.id === allocationId);
      if (!target) throw new Error("NOT_FOUND");
      if (target.primary || current.length <= 1) throw new Error("OPERATION_CONFLICT");
      this.allocations[serverId] = current.filter((allocation) => allocation.id !== allocationId);
    });
  }

  async getStartup(serverId: string): Promise<Startup> {
    const server = this.requireServer(serverId);
    const stored = this.startup[serverId];
    if (!stored) throw new Error("NOT_FOUND");
    const startup = startupFromFixedBundle(server);
    return {
      serverId: server.id,
      generation: server.generation,
      command: resolveStartupCommand(startup, stored.values),
      variables: startup.variables.map((variable) => {
        const hasValue = Object.hasOwn(stored.values, variable.key);
        if (variable.secret) {
          return {
            key: variable.key,
            type: variable.type,
            secret: true,
            required: variable.required,
            hasValue,
            ...(variable.minimum !== undefined ? { minimum: variable.minimum } : {}),
            ...(variable.maximum !== undefined ? { maximum: variable.maximum } : {}),
            ...(variable.minLength !== undefined ? { minLength: variable.minLength } : {}),
            ...(variable.maxLength !== undefined ? { maxLength: variable.maxLength } : {}),
          };
        }
        return {
          ...variable,
          enumValues: variable.enumValues ? [...variable.enumValues] : undefined,
          hasValue,
          ...(hasValue ? { value: stored.values[variable.key] } : {}),
        };
      }),
    };
  }

  async updateStartup(serverId: string, values: Record<string, StartupValue>, expectedGeneration: number, key: string): Promise<Operation> {
    const server = this.requireServer(serverId);
    const stored = this.startup[serverId];
    if (!stored) throw new Error("NOT_FOUND");
    if (Object.keys(values).length === 0) throw new Error("VALIDATION_FAILED");
    const startup = startupFromFixedBundle(server);
    const signature = await keyedDigest(this.requestDigestKey, stableStringify({ generation: expectedGeneration, variables: values }));
    return this.reconcileConfiguration(server, `startup:update:${serverId}:${key}`, signature, expectedGeneration, () => {
      const next = Object.assign(
        Object.create(null) as Record<string, Exclude<StartupValue, null>>,
        stored.values,
      );
      for (const [name, value] of Object.entries(values)) {
        const variable = startup.variables.find((candidate) => candidate.key === name);
        if (!variable || !validStartupValue(variable, value)) throw new Error("VALIDATION_FAILED");
        if (value === null) delete next[name];
        else next[name] = value;
      }
      this.startup[serverId] = { values: next };
    });
  }

  private reconcileConfiguration(server: Server, scope: string, signature: string, expectedGeneration: number, mutate: () => void): Operation {
    const record = this.idempotency.get(scope);
    if (record) {
      if (record.signature !== signature) throw new Error("IDEMPOTENCY_KEY_REUSED");
      const existing = this.operations.get(record.operationId);
      if (existing) return existing;
    }
    const currentServer = this.requireServer(server.id);
    if (currentServer.generation !== expectedGeneration) throw new Error("PRECONDITION_FAILED");
    const node = nodes.find((item) => item.id === currentServer.nodeId);
    if (!node || node.condition !== "available") throw new Error("NODE_OFFLINE");
    const active = [...this.operations.values()].find((item) => item.serverId === server.id && !["succeeded", "failed"].includes(item.status));
    if (active) throw new Error("OPERATION_IN_PROGRESS");

    const timestamp = new Date().toISOString();
    const generation = currentServer.generation + 1;
    mutate();
    const primary = (this.allocations[server.id] ?? []).find((allocation) => allocation.primary);
    servers = servers.map((item) => item.id === server.id ? { ...item, generation, allocation: primary ? allocationAddress(primary) : item.allocation, updatedAt: timestamp } : item);
    const operation = queuedOperation({ id: id("operation"), serverId: server.id, nodeId: node.id, type: "reconcile", generation, createdAt: timestamp, updatedAt: timestamp });
    this.operations.set(operation.id, operation);
    this.idempotency.set(scope, { operationId: operation.id, signature });
    window.setTimeout(() => {
      const completedServer = this.completeOperationForServer(operation.id);
      if (!completedServer) return;
      servers = servers.map((item) => item.id === completedServer.id ? { ...item, observedGeneration: generation, observedAt: new Date().toISOString(), updatedAt: new Date().toISOString() } : item);
    }, 1100);
    return operation;
  }

  private completeOperationForServer(operationId: string): Server | null {
    const operation = this.operations.get(operationId);
    if (!operation || ["succeeded", "failed"].includes(operation.status)) return null;
    const server = servers.find((item) => item.id === operation.serverId);
    if (!server || server.generation !== operation.generation || server.nodeId !== operation.nodeId) {
      this.operations.set(operation.id, failedOperation(operation, {
        code: "OPERATION_STALE",
        message: "The server target changed before the operation completed.",
        retryable: false,
      }));
      return null;
    }
    this.operations.set(operation.id, completedOperation(operation));
    return server;
  }

  private storeUser(user: User, password: string): void {
    this.users.set(user.id, { user: cloneMockUser(user), password });
    this.userOrder.push(user.id);
  }

  private sessionFor(user: User): Session {
    return {
      user: cloneMockUser(user),
      csrfToken: "mock-csrf-token-abcdefghijklmnopqrstuvwxyz",
      environment: "development",
    };
  }

  private requirePlatformAdmin(): User {
    const record = this.currentUserId ? this.users.get(this.currentUserId) : undefined;
    if (!record || record.user.status !== "active") throw new Error("AUTH_REQUIRED");
    if (!record.user.roles.includes("platform_admin")) throw new Error("FORBIDDEN");
    return record.user;
  }

  private activeAdminCount(): number {
    return [...this.users.values()].filter((record) => record.user.status === "active" && record.user.roles.includes("platform_admin")).length;
  }

  private visibleServers(): Server[] {
    if (!this.currentUserId) return servers;
    const record = this.users.get(this.currentUserId);
    if (!record || record.user.status !== "active") return [];
    if (record.user.roles.includes("platform_admin")) return servers;
    return servers.filter((server) => this.memberships.get(membershipKey(server.id, record.user.id))?.permissions.includes("servers.read"));
  }

  private canReadServer(serverId: string): boolean {
    if (!this.currentUserId) return true;
    const record = this.users.get(this.currentUserId);
    if (!record || record.user.status !== "active") return false;
    if (record.user.roles.includes("platform_admin")) return true;
    return this.memberships.get(membershipKey(serverId, record.user.id))?.permissions.includes("servers.read") ?? false;
  }

  private requireServer(serverId: string): Server {
    const server = servers.find((item) => item.id === serverId);
    if (!server) throw new Error("NOT_FOUND");
    return server;
  }
}

function membershipKey(serverId: string, userId: string): string {
  return `${serverId}:${userId}`;
}

function cloneMockUser(user: User): User {
  return { ...user, roles: [...user.roles] };
}

function cloneMembership(membership: ServerMembership): ServerMembership {
  return { ...membership, permissions: [...membership.permissions] };
}

function normalizeMockEmail(value: string): string {
  const email = value.trim().toLowerCase();
  if (email.length > 254 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) throw new Error("VALIDATION_FAILED");
  return email;
}

function validateMockDisplayName(value: string): string {
  const displayName = value.trim();
  const length = [...displayName].length;
  if (length < 1 || length > 64) throw new Error("VALIDATION_FAILED");
  return displayName;
}

function validateMockPassword(value: string): void {
  const length = [...value].length;
  if (length < 8 || length > 1024) throw new Error("VALIDATION_FAILED");
}

function normalizeMockRoles(roles: string[]): string[] {
  const normalized = [...new Set(roles)];
  if (normalized.length === 0 || normalized.some((role) => !allowedRoles.has(role))) throw new Error("VALIDATION_FAILED");
  return normalized;
}

function cloneAllocations(source: Record<string, Allocation[]>): Record<string, Allocation[]> {
  return Object.fromEntries(Object.entries(source).map(([serverId, values]) => [serverId, values.map((value) => ({ ...value }))]));
}

function cloneStartup(source: Record<string, StoredStartup>): Record<string, StoredStartup> {
  return Object.fromEntries(Object.entries(source).map(([serverId, value]) => [serverId, { values: { ...value.values } }]));
}

interface ValidatedFixedBundleVariables {
  properties: Record<string, FixedBundleVariableProperty>;
  required: Set<string>;
  secrets: Set<string>;
  bindings: FixedBundleBinding[];
}

function incompatibleFixedBundle(): never {
  throw new Error("PACKAGE_INCOMPATIBLE");
}

function isJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasOnlyKeys(value: Record<string, unknown>, allowed: ReadonlySet<string>): boolean {
  return Object.keys(value).every((key) => allowed.has(key));
}

function readFixedBundleStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) incompatibleFixedBundle();
  const result: string[] = [];
  for (const entry of value) {
    if (typeof entry !== "string") incompatibleFixedBundle();
    result.push(entry);
  }
  return result;
}

function validateFixedBundleVariables(declaration: unknown): ValidatedFixedBundleVariables {
  if (declaration === undefined) {
    return { properties: {}, required: new Set(), secrets: new Set(), bindings: [] };
  }
  if (!isJsonObject(declaration) || !hasOnlyKeys(declaration, new Set(["schema", "secrets", "bindings"])) || !Object.hasOwn(declaration, "schema")) {
    incompatibleFixedBundle();
  }

  const schema = declaration.schema;
  if (!isJsonObject(schema) || !hasOnlyKeys(schema, new Set(["type", "additionalProperties", "required", "properties"])) || schema.type !== "object" || !Object.hasOwn(schema, "properties")) {
    incompatibleFixedBundle();
  }
  if (Object.hasOwn(schema, "additionalProperties") && schema.additionalProperties !== false) incompatibleFixedBundle();
  if (!isJsonObject(schema.properties)) incompatibleFixedBundle();

  const properties: Record<string, FixedBundleVariableProperty> = Object.create(null) as Record<string, FixedBundleVariableProperty>;
  for (const [key, rawProperty] of Object.entries(schema.properties)) {
    if (!fixedBundleVariableIdentifier.test(key)) incompatibleFixedBundle();
    properties[key] = validateFixedBundleVariableProperty(rawProperty);
  }

  const requiredValues = Object.hasOwn(schema, "required") ? readFixedBundleStringArray(schema.required) : [];
  const required = new Set<string>();
  for (const key of requiredValues) {
    if (!fixedBundleVariableIdentifier.test(key) || required.has(key) || !Object.hasOwn(properties, key)) incompatibleFixedBundle();
    required.add(key);
  }

  const secretValues = Object.hasOwn(declaration, "secrets") ? readFixedBundleStringArray(declaration.secrets) : [];
  const secrets = new Set<string>();
  for (const key of secretValues) {
    if (!fixedBundleVariableIdentifier.test(key) || secrets.has(key) || !Object.hasOwn(properties, key)) incompatibleFixedBundle();
    const property = properties[key];
    if (["default", "const", "enum"].some((keyword) => Object.hasOwn(property, keyword))) incompatibleFixedBundle();
    secrets.add(key);
  }

  const bindings = Object.hasOwn(declaration, "bindings")
    ? validateFixedBundleBindings(declaration.bindings, properties)
    : [];
  return { properties, required, secrets, bindings };
}

function validateFixedBundleVariableProperty(value: unknown): FixedBundleVariableProperty {
  if (!isJsonObject(value) || typeof value.type !== "string") incompatibleFixedBundle();

  let allowed: ReadonlySet<string>;
  switch (value.type) {
    case "string":
      allowed = new Set(["type", "default", "const", "enum", "minLength", "maxLength"]);
      break;
    case "integer":
      allowed = new Set(["type", "default", "const", "minimum", "maximum"]);
      break;
    case "boolean":
      allowed = new Set(["type", "default", "const"]);
      break;
    default:
      incompatibleFixedBundle();
  }
  if (!hasOnlyKeys(value, allowed)) incompatibleFixedBundle();

  const property: FixedBundleVariableProperty = { type: value.type };
  if (value.type === "string") {
    if (Object.hasOwn(value, "minLength")) {
      if (!Number.isSafeInteger(value.minLength) || (value.minLength as number) < 0) incompatibleFixedBundle();
      property.minLength = value.minLength as number;
    }
    if (Object.hasOwn(value, "maxLength")) {
      if (!Number.isSafeInteger(value.maxLength) || (value.maxLength as number) < 0) incompatibleFixedBundle();
      property.maxLength = value.maxLength as number;
    }
    if (property.minLength !== undefined && property.maxLength !== undefined && property.minLength > property.maxLength) incompatibleFixedBundle();
    if (Object.hasOwn(value, "enum")) {
      const enumValues = readFixedBundleStringArray(value.enum);
      if (enumValues.length === 0 || new Set(enumValues).size !== enumValues.length) incompatibleFixedBundle();
      const minimum = property.minLength;
      const maximum = property.maxLength;
      if (enumValues.some((candidate) => {
        const length = Array.from(candidate).length;
        return minimum !== undefined && length < minimum || maximum !== undefined && length > maximum;
      })) incompatibleFixedBundle();
      property.enum = enumValues;
    }
  }
  if (value.type === "integer") {
    if (Object.hasOwn(value, "minimum")) {
      if (!Number.isSafeInteger(value.minimum)) incompatibleFixedBundle();
      property.minimum = value.minimum as number;
    }
    if (Object.hasOwn(value, "maximum")) {
      if (!Number.isSafeInteger(value.maximum)) incompatibleFixedBundle();
      property.maximum = value.maximum as number;
    }
    if (property.minimum !== undefined && property.maximum !== undefined && property.minimum > property.maximum) incompatibleFixedBundle();
  }

  const definition: StartupVariableDefinition = {
    key: "validated",
    type: property.type,
    secret: false,
    required: false,
    ...(property.minimum !== undefined ? { minimum: property.minimum } : {}),
    ...(property.maximum !== undefined ? { maximum: property.maximum } : {}),
    ...(property.minLength !== undefined ? { minLength: property.minLength } : {}),
    ...(property.maxLength !== undefined ? { maxLength: property.maxLength } : {}),
    ...(property.enum !== undefined ? { enumValues: [...property.enum] } : {}),
  };
  if (Object.hasOwn(value, "const")) {
    if (value.const === null || !validStartupValue(definition, value.const as StartupValue)) incompatibleFixedBundle();
    property.const = value.const as BundleScalar;
    definition.constValue = property.const;
  }
  if (Object.hasOwn(value, "default")) {
    if (value.default === null || !validStartupValue(definition, value.default as StartupValue)) incompatibleFixedBundle();
    property.default = value.default as BundleScalar;
  }
  return property;
}

function validateFixedBundleBindings(value: unknown, properties: Record<string, FixedBundleVariableProperty>): FixedBundleBinding[] {
  if (!Array.isArray(value)) incompatibleFixedBundle();
  return value.map((rawBinding): FixedBundleBinding => {
    const allowed = new Set(["variable", "target", "name", "path", "template"]);
    if (!isJsonObject(rawBinding) || !hasOnlyKeys(rawBinding, allowed)) incompatibleFixedBundle();
    if (typeof rawBinding.variable !== "string" || !fixedBundleVariableIdentifier.test(rawBinding.variable) || !Object.hasOwn(properties, rawBinding.variable)) incompatibleFixedBundle();
    if (rawBinding.target !== "environment" && rawBinding.target !== "argument" && rawBinding.target !== "file") incompatibleFixedBundle();
    if (typeof rawBinding.template !== "string") incompatibleFixedBundle();
    if (Object.hasOwn(rawBinding, "name") && (typeof rawBinding.name !== "string" || rawBinding.name.length === 0)) incompatibleFixedBundle();
    if (Object.hasOwn(rawBinding, "path") && (typeof rawBinding.path !== "string" || rawBinding.path.length === 0)) incompatibleFixedBundle();
    if (rawBinding.target === "environment" && typeof rawBinding.name !== "string") incompatibleFixedBundle();
    if (rawBinding.target === "file" && (typeof rawBinding.path !== "string" || !isCanonicalFixedBundleRelativePath(rawBinding.path))) incompatibleFixedBundle();
    return {
      variable: rawBinding.variable,
      target: rawBinding.target,
      template: rawBinding.template,
      ...(typeof rawBinding.name === "string" ? { name: rawBinding.name } : {}),
      ...(typeof rawBinding.path === "string" ? { path: rawBinding.path } : {}),
    };
  });
}

function isCanonicalFixedBundleRelativePath(path: string): boolean {
  if (path.includes("\\")) return false;
  if (Array.from(path).length > 1024 || path.split("/").some((component) => Array.from(component).length > 255)) return false;
  try {
    const normalized = normalizeRelativePath(path);
    return normalized.length > 0 && normalized === path;
  } catch {
    return false;
  }
}

function validateFixedBundleDocumentShape(document: unknown): asserts document is FixedBundleDocument {
  const documentKeys = new Set(["apiVersion", "kind", "metadata", "spec"]);
  if (!isJsonObject(document) || !hasOnlyKeys(document, documentKeys) || document.apiVersion !== "gugumanager.io/games/v1alpha1" || document.kind !== "GameDefinition") {
    incompatibleFixedBundle();
  }
  const specKeys = new Set(["release", "compatibility", "capabilities", "variables", "runtime", "install", "lifecycle"]);
  if (!isJsonObject(document.spec) || !hasOnlyKeys(document.spec, specKeys)) incompatibleFixedBundle();
}

function startupFromFixedBundle(server: Server): FixedStartup {
  const bundle = fixedBundles.find((candidate) => candidate.document.metadata.id === server.gameId);
  const game = games.find((candidate) => candidate.id === server.gameId);
  if (!bundle || !game) incompatibleFixedBundle();
  validateFixedBundleDocumentShape(bundle.document);
  if (server.gameDefinitionVersion !== bundle.document.metadata.version || server.gameBundleDigest !== bundle.digest || game.version !== bundle.document.metadata.version || game.bundleDigest !== bundle.digest) {
    throw new Error("PACKAGE_INCOMPATIBLE");
  }

  const { properties, required, secrets, bindings } = validateFixedBundleVariables(bundle.document.spec.variables);

  const variables = Object.keys(properties).sort().map((key): StartupVariableDefinition => {
    const property = properties[key];
    const variable: StartupVariableDefinition = {
      key,
      type: property.type,
      secret: secrets.has(key),
      required: required.has(key),
      ...(property.minimum !== undefined ? { minimum: property.minimum } : {}),
      ...(property.maximum !== undefined ? { maximum: property.maximum } : {}),
      ...(property.minLength !== undefined ? { minLength: property.minLength } : {}),
      ...(property.maxLength !== undefined ? { maxLength: property.maxLength } : {}),
      ...(property.enum ? { enumValues: [...property.enum] } : {}),
    };
    if (Object.hasOwn(property, "const")) {
      variable.constValue = property.const;
      if (!validStartupValue(variable, property.const ?? null)) incompatibleFixedBundle();
    }
    if (Object.hasOwn(property, "default")) {
      variable.default = property.default;
      if (!validStartupValue(variable, property.default ?? null)) incompatibleFixedBundle();
    }
    return variable;
  });

  const command = {
    executable: bundle.document.spec.runtime.command.executable,
    args: [...bundle.document.spec.runtime.command.args],
  };
  const byKey = new Map(variables.map((variable) => [variable.key, variable]));
  for (const binding of bindings) {
    const variable = byKey.get(binding.variable);
    if (!variable) incompatibleFixedBundle();
    if (binding.target === "argument") {
      if (variable.secret || !command.args.includes(`{{ ${binding.variable} }}`) || !binding.template.includes("{{ value }}")) incompatibleFixedBundle();
    }
  }
  return { command, variables, bindings };
}

function createStoredStartup(server: Server, overrides: Record<string, StartupValue> = {}): StoredStartup {
  const startup = startupFromFixedBundle(server);
  const values = Object.create(null) as Record<string, Exclude<StartupValue, null>>;
  for (const variable of startup.variables) {
    if (Object.hasOwn(variable, "default")) values[variable.key] = variable.default as Exclude<StartupValue, null>;
  }
  for (const [key, value] of Object.entries(overrides)) {
    const variable = startup.variables.find((candidate) => candidate.key === key);
    if (!variable || !validStartupValue(variable, value)) throw new Error("VALIDATION_FAILED");
    if (value === null) delete values[key];
    else values[key] = value;
  }
  return { values };
}

function resolveStartupCommand(startup: FixedStartup, values: Record<string, Exclude<StartupValue, null>>): StartupCommand {
  const result = { executable: startup.command.executable, args: [...startup.command.args] };
  const definitions = new Map(startup.variables.map((variable) => [variable.key, variable]));
  for (const binding of startup.bindings) {
    const variable = definitions.get(binding.variable);
    if (!variable || variable.secret || binding.target !== "argument" || !Object.hasOwn(values, binding.variable)) continue;
    const rendered = String(values[binding.variable]);
    const placeholder = `{{ ${binding.variable} }}`;
    result.args = result.args.map((argument) => argument === placeholder ? binding.template.replaceAll("{{ value }}", rendered) : argument);
  }
  return result;
}

function validStartupValue(variable: StartupVariableDefinition, value: StartupValue): boolean {
  if (value === null) return !variable.required;
  if (variable.type === "string" && typeof value !== "string") return false;
  if (variable.type === "integer" && (typeof value !== "number" || !Number.isSafeInteger(value))) return false;
  if (variable.type === "boolean" && typeof value !== "boolean") return false;
  const stringLength = typeof value === "string" ? Array.from(value).length : 0;
  if (typeof value === "string" && variable.minLength !== undefined && stringLength < variable.minLength) return false;
  if (typeof value === "string" && variable.maxLength !== undefined && stringLength > variable.maxLength) return false;
  if (typeof value === "number" && variable.minimum !== undefined && value < variable.minimum) return false;
  if (typeof value === "number" && variable.maximum !== undefined && value > variable.maximum) return false;
  if (variable.constValue !== undefined && value !== variable.constValue) return false;
  if (typeof value === "string" && variable.enumValues && !variable.enumValues.includes(value)) return false;
  return true;
}

function defaultAllocationSettings(gameId: string): ["tcp" | "udp", number] {
  const bundle = fixedBundles.find((candidate) => candidate.document.metadata.id === gameId);
  const port = bundle?.document.spec.runtime.ports[0];
  if (!port || !Number.isInteger(port.containerPort) || port.containerPort < 1 || port.containerPort > 65535) throw new Error("PACKAGE_INCOMPATIBLE");
  return [port.protocol, port.containerPort];
}

function nextAvailableAllocationPort(source: Record<string, Allocation[]>, nodeId: string, bindIp: string, protocol: "tcp" | "udp", firstPort: number): number | null {
  const allocations = Object.values(source).flat();
  for (let port = firstPort; port <= 65535; port += 1) {
    if (!allocations.some((allocation) => allocation.nodeId === nodeId && allocation.bindIp === bindIp && allocation.port === port && allocation.protocol === protocol)) return port;
  }
  return null;
}

function allocationAddress(allocation: Pick<Allocation, "bindIp" | "port">): string {
  return allocation.bindIp.includes(":") ? `[${allocation.bindIp}]:${allocation.port}` : `${allocation.bindIp}:${allocation.port}`;
}

function normalizeBindIp(value: string): string | null {
  const trimmed = value.trim();
  if (!trimmed || trimmed === "0.0.0.0" || trimmed === "::") return null;
  if (trimmed.includes(":")) {
    if (!/^[0-9a-f:.]+$/i.test(trimmed)) return null;
    try {
      const hostname = new URL(`http://[${trimmed}]/`).hostname.replace(/^\[|\]$/g, "").toLowerCase();
      if (hostname === "::") return null;
      return ipv4MappedAddress(hostname) ?? hostname;
    } catch {
      return null;
    }
  }
  const octets = trimmed.split(".");
  if (octets.length !== 4 || octets.some((part) => !/^\d{1,3}$/.test(part) || part.length > 1 && part.startsWith("0"))) return null;
  const parsed = octets.map(Number);
  if (parsed.some((part) => part < 0 || part > 255)) return null;
  return parsed.join(".");
}

function ipv4MappedAddress(hostname: string): string | null {
  const groups = parseIPv6Groups(hostname);
  if (!groups || groups.length !== 8 || !groups.slice(0, 5).every((group) => group === 0) || groups[5] !== 0xffff) return null;
  return [groups[6] >> 8, groups[6] & 0xff, groups[7] >> 8, groups[7] & 0xff].join(".");
}

function parseIPv6Groups(value: string): number[] | null {
  const sections = value.split("::");
  if (sections.length > 2) return null;
  const parseSection = (section: string): number[] | null => {
    if (!section) return [];
    const parts = section.split(":");
    const groups: number[] = [];
    for (const part of parts) {
      if (part.includes(".")) {
        if (part !== parts[parts.length - 1]) return null;
        const octets = part.split(".");
        if (octets.length !== 4 || octets.some((octet) => !/^\d{1,3}$/.test(octet) || Number(octet) > 255)) return null;
        groups.push((Number(octets[0]) << 8) | Number(octets[1]), (Number(octets[2]) << 8) | Number(octets[3]));
      } else {
        if (!/^[0-9a-f]{1,4}$/i.test(part)) return null;
        groups.push(Number.parseInt(part, 16));
      }
    }
    return groups;
  };
  const left = parseSection(sections[0]);
  const right = parseSection(sections.length === 2 ? sections[1] : "");
  if (!left || !right) return null;
  if (sections.length === 1) return left.length === 8 ? left : null;
  const missing = 8 - left.length - right.length;
  return missing > 0 ? [...left, ...Array.from({ length: missing }, () => 0), ...right] : null;
}

function stableStringify(value: unknown): string {
  if (value === null || typeof value === "string" || typeof value === "boolean") return JSON.stringify(value);
  if (typeof value === "number") {
    if (!Number.isFinite(value)) throw new Error("VALIDATION_FAILED");
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
  if (typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(record[key])}`).join(",")}}`;
  }
  throw new Error("VALIDATION_FAILED");
}

async function keyedDigest(secret: Uint8Array, value: string): Promise<string> {
  const key = await crypto.subtle.importKey("raw", new Uint8Array(secret).buffer, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const digest = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(value));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function normalizeRelativePath(requested: string): string {
  if (requested.includes("\0") || requested.startsWith("/") || /^[A-Za-z]:/.test(requested)) {
    throw new Error("PATH_ESCAPE_BLOCKED");
  }
  const parts: string[] = [];
  for (const part of requested.replaceAll("\\", "/").split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      throw new Error("PATH_ESCAPE_BLOCKED");
    }
    parts.push(part);
  }
  return parts.join("/");
}

function parentPath(path: string): string {
  return path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "";
}
