export type Environment = "development" | "production";
export type NodeCondition = "available" | "offline" | "maintenance";
export type ObservedPower = "unknown" | "stopped" | "starting" | "running" | "stopping";
export type DesiredPower = "running" | "stopped";
export type UserStatus = "active" | "disabled";
export type GlobalRole = "platform_admin" | "server_owner";

export interface User {
  id: string;
  email: string;
  displayName: string;
  roles: string[];
  status: UserStatus;
  createdAt: string;
  updatedAt: string;
}

export interface SetupStatus {
  required: boolean;
  bootstrapExpiresAt?: string;
}

export interface SetupAdminInput {
  bootstrapToken: string;
  email: string;
  displayName: string;
  password: string;
}

export interface CreateUserInput {
  email: string;
  displayName: string;
  password: string;
  roles: GlobalRole[];
}

export interface UpdateUserInput {
  displayName?: string;
  status?: UserStatus;
  roles?: GlobalRole[];
}

export interface PasswordResetToken {
  token: string;
  expiresAt: string;
}

export type ServerPermission =
  | "servers.read"
  | "servers.power"
  | "servers.console"
  | "servers.files.read"
  | "servers.files.write"
  | "servers.backups.read"
  | "servers.backups.create"
  | "servers.backups.restore"
  | "servers.backups.delete"
  | "servers.network.read"
  | "servers.network.write"
  | "servers.startup.read"
  | "servers.startup.write";

export interface ServerMembership {
  serverId: string;
  userId: string;
  permissions: ServerPermission[];
  createdAt: string;
  updatedAt: string;
}

export interface ServerPermissions {
  serverId: string;
  permissions: ServerPermission[];
}

export interface Session {
  user: User;
  csrfToken: string;
  environment: Environment;
}

export interface Metrics {
  cpuPercent: number;
  memoryBytes: number;
  memoryLimitBytes: number;
  diskBytes: number;
  diskLimitBytes: number;
  networkRxBytes?: number;
  networkTxBytes?: number;
  playersOnline?: number;
  playersMax?: number;
}

export interface MetricPoint {
  timestamp: string;
  cpuPercent: number;
  memoryBytes: number;
}

export interface Server {
  id: string;
  name: string;
  description: string;
  gameId: string;
  gameBundleDigest: string;
  gameDefinitionVersion: string;
  gameName: string;
  gameVersion: string;
  nodeId: string;
  nodeName: string;
  lifecycleState: "provisioning" | "ready" | "deleting" | "deleted";
  desiredPower: DesiredPower;
  observedPower: ObservedPower;
  nodeCondition: NodeCondition;
  healthCondition: "unknown" | "healthy" | "unhealthy";
  generation: number;
  observedGeneration: number;
  observedAt: string;
  allocation: string;
  ownerName: string;
  metrics: Metrics;
  metricHistory: MetricPoint[];
  updatedAt: string;
}

export interface Node {
  id: string;
  name: string;
  condition: NodeCondition;
  version: string;
  region: string;
  address: string;
  lastHeartbeatAt: string;
  cpuCores: number;
  memoryBytes: number;
  diskBytes: number;
  allocatedMemoryBytes: number;
  allocatedDiskBytes: number;
  runningServers: number;
  totalServers: number;
  capabilities: string[];
}

export interface GameDefinition {
  id: string;
  bundleDigest: string;
  name: string;
  summary: string;
  version: string;
  gameVersion: string;
  status: "approved" | "pending" | "rejected";
  capabilities: string[];
  platforms: string[];
  servers: number;
  icon: string;
  defaultMemoryMb: number;
  defaultDiskGb: number;
}

export interface Operation {
  id: string;
  serverId: string;
  nodeId: string;
  type: "provision" | "start" | "stop" | "restart" | "kill" | "backup" | "restore" | "backup-delete" | "delete" | "reconcile";
  status: "queued" | "leased" | "dispatched" | "running" | "succeeded" | "failed" | "canceled";
  progress: number;
  generation: number;
  attempt: number;
  maxAttempts: number;
  leaseOwner: string | null;
  leaseExpiresAt: string | null;
  checkpoint: string;
  error: OperationError | null;
  createdAt: string;
  updatedAt: string;
}

export interface OperationError {
  code: string;
  message: string;
  retryable: boolean;
}

export interface AuditEvent {
  id: string;
  actorName: string;
  action: string;
  targetType: string;
  targetName: string;
  result: "accepted" | "success" | "failure";
  operationId: string | null;
  createdAt: string;
}

export interface ConsoleLine {
  sequence: number;
  timestamp: string;
  stream: "system" | "stdout" | "stderr" | "command";
  message: string;
}

export interface FileEntry {
  name: string;
  path: string;
  kind: "file" | "directory";
  sizeBytes: number;
  modifiedAt: string;
}

export interface FileContent {
  path: string;
  content: string;
  encoding: "utf-8" | "base64";
  sizeBytes: number;
  modifiedAt: string;
}

export interface Backup {
  id: string;
  name: string;
  status: "creating" | "ready" | "failed" | "restoring" | "deleting";
  sizeBytes?: number | null;
  checksum?: string | null;
  storageLocation?: string | null;
  retentionUntil?: string | null;
  createdAt: string;
  completedAt?: string | null;
}

export interface Allocation {
  id: string;
  serverId: string;
  nodeId: string;
  bindIp: string;
  port: number;
  protocol: "tcp" | "udp";
  primary: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface CreateAllocationInput {
  bindIp: string;
  port: number;
  protocol: "tcp" | "udp";
  primary: boolean;
}

export interface StartupCommand {
  executable: string;
  args: string[];
}

export interface StartupVariable {
  key: string;
  type: "string" | "integer" | "boolean";
  secret: boolean;
  required: boolean;
  hasValue: boolean;
  default?: string | number | boolean;
  value?: string | number | boolean;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  constValue?: string | number | boolean;
  enumValues?: string[];
}

export interface Startup {
  serverId: string;
  generation: number;
  command: StartupCommand;
  variables: StartupVariable[];
}

export type StartupValue = string | number | boolean | null;

export interface Overview {
  environment: Environment;
  serverCount: number;
  runningServerCount: number;
  onlineNodeCount: number;
  totalNodeCount: number;
  queuedOperationCount: number;
  cpuPercent: number;
  memoryUsedBytes: number;
  memoryTotalBytes: number;
  recentActivity: AuditEvent[];
}

export interface CreateServerInput {
  name: string;
  gameDefinitionId: string;
  gameBundleDigest: string;
  nodeId: string;
  memoryMb: number;
  diskGb: number;
}
