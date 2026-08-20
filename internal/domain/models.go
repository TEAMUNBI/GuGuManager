package domain

import "time"

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Roles       []string  `json:"roles"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type SetupStatus struct {
	Required           bool       `json:"required"`
	BootstrapExpiresAt *time.Time `json:"bootstrapExpiresAt,omitempty"`
}

type SetupAdminInput struct {
	BootstrapToken string `json:"bootstrapToken"`
	Email          string `json:"email"`
	DisplayName    string `json:"displayName"`
	Password       string `json:"password"`
}

type CreateUserInput struct {
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	Password    string   `json:"password"`
	Roles       []string `json:"roles"`
}

type UpdateUserInput struct {
	DisplayName *string   `json:"displayName,omitempty"`
	Status      *string   `json:"status,omitempty"`
	Roles       *[]string `json:"roles,omitempty"`
}

type PasswordResetToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ServerMembership struct {
	ServerID    string    `json:"serverId"`
	UserID      string    `json:"userId"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ServerPermissions struct {
	ServerID    string   `json:"serverId"`
	Permissions []string `json:"permissions"`
}

type SessionView struct {
	User        User   `json:"user"`
	CSRFToken   string `json:"csrfToken"`
	Environment string `json:"environment"`
}

type Session struct {
	Token     string
	CSRFToken string
	UserID    string
	User      User
	ExpiresAt time.Time
}

// APIToken is the public, non-secret metadata for a persistent bearer token.
// The plaintext credential is returned only once in APITokenCredential.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type CreateAPITokenInput struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type APITokenCredential struct {
	APIToken
	Token string `json:"token"`
}

type APITokenPrincipal struct {
	User        User
	Scopes      []string
	APITokenID  string
	Environment string
}

type ConsoleConnectionCredential struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ConsoleConnectionPrincipal struct {
	UserID    string
	ServerID  string
	ExpiresAt time.Time
}

type ResourceMetrics struct {
	CPUPercent      float64 `json:"cpuPercent"`
	MemoryBytes     int64   `json:"memoryBytes"`
	MemoryLimit     int64   `json:"memoryLimitBytes"`
	DiskBytes       int64   `json:"diskBytes"`
	DiskLimit       int64   `json:"diskLimitBytes"`
	NetworkRxBytes  int64   `json:"networkRxBytes"`
	NetworkTxBytes  int64   `json:"networkTxBytes"`
	PlayersOnline   int     `json:"playersOnline"`
	PlayersCapacity int     `json:"playersMax"`
}

type MetricPoint struct {
	Timestamp   time.Time `json:"timestamp"`
	CPUPercent  float64   `json:"cpuPercent"`
	MemoryBytes int64     `json:"memoryBytes"`
}

// ServerMetrics 是 Agent 通过 MetricsBatch 上报的单台服务器实时指标，
// 由控制面合并进 Server.Metrics（ResourceMetrics）与 MetricHistory。
type ServerMetrics struct {
	ServerID           string    `json:"serverId"`
	ObservedGeneration int64     `json:"observedGeneration"`
	CPUPercent         float64   `json:"cpuPercent"`
	MemoryBytes        int64     `json:"memoryBytes"`
	MemoryLimitBytes   int64     `json:"memoryLimitBytes"`
	DiskBytes          int64     `json:"diskBytes"`
	DiskLimitBytes     int64     `json:"diskLimitBytes"`
	NetworkRxBytes     int64     `json:"networkRxBytes"`
	NetworkTxBytes     int64     `json:"networkTxBytes"`
	PlayersOnline      int       `json:"playersOnline"`
	PlayersMax         int       `json:"playersMax"`
	ObservedAt         time.Time `json:"observedAt"`
}

type Server struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Description           string            `json:"description"`
	GameID                string            `json:"gameId"`
	GameBundleDigest      string            `json:"gameBundleDigest"`
	GameDefinitionVersion string            `json:"gameDefinitionVersion"`
	GameName              string            `json:"gameName"`
	GameVersion           string            `json:"gameVersion"`
	NodeID                string            `json:"nodeId"`
	NodeName              string            `json:"nodeName"`
	LifecycleState        string            `json:"lifecycleState"`
	DesiredPower          string            `json:"desiredPower"`
	ObservedPower         string            `json:"observedPower"`
	NodeCondition         string            `json:"nodeCondition"`
	HealthCondition       string            `json:"healthCondition"`
	Generation            int64             `json:"generation"`
	ObservedGeneration    int64             `json:"observedGeneration"`
	ObservedAt            time.Time         `json:"observedAt"`
	Allocation            string            `json:"allocation"`
	OwnerName             string            `json:"ownerName"`
	Metrics               ResourceMetrics   `json:"metrics"`
	MetricHistory         []MetricPoint     `json:"metricHistory"`
	Metadata              map[string]string `json:"metadata,omitempty"` // Internal metadata (e.g., containerID)
	UpdatedAt             time.Time         `json:"updatedAt"`
}

type Node struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Condition            string    `json:"condition"`
	Version              string    `json:"version"`
	Region               string    `json:"region"`
	Architecture         string    `json:"architecture"`
	Draining             bool      `json:"draining"`
	DrainReason          string    `json:"drainReason,omitempty"`
	Address              string    `json:"address"`
	LastHeartbeatAt      time.Time `json:"lastHeartbeatAt"`
	CPUCores             int       `json:"cpuCores"`
	MemoryBytes          int64     `json:"memoryBytes"`
	DiskBytes            int64     `json:"diskBytes"`
	AllocatedMemoryBytes int64     `json:"allocatedMemoryBytes"`
	AllocatedDiskBytes   int64     `json:"allocatedDiskBytes"`
	RunningServers       int       `json:"runningServers"`
	TotalServers         int       `json:"totalServers"`
	Capabilities         []string  `json:"capabilities"`
}

type GameDefinition struct {
	ID             string             `json:"id"`
	BundleDigest   string             `json:"bundleDigest"`
	Name           string             `json:"name"`
	Summary        string             `json:"summary"`
	Version        string             `json:"version"`
	GameVersion    string             `json:"gameVersion"`
	Status         string             `json:"status"`
	Capabilities   []string           `json:"capabilities"`
	Platforms      []string           `json:"platforms"`
	Servers        int                `json:"servers"`
	Icon           string             `json:"icon"`
	DefaultMemory  int                `json:"defaultMemoryMb"`
	DefaultDisk    int                `json:"defaultDiskGb"`
	Signed         bool               `json:"signed"`
	Verified       bool               `json:"verified"`
	Runnable       bool               `json:"runnable"`
	Supported      bool               `json:"supported"`
	TrustLevel     string             `json:"trustLevel"`
	Source         string             `json:"source"`
	SupportReasons []string           `json:"supportReasons"`
	RuntimeTarget  *GameRuntimeTarget `json:"runtimeTarget,omitempty"`
	BundleDocument string             `json:"-"`
}

type GameRuntimeTarget struct {
	Digest      string                 `json:"digest"`
	Adapter     string                 `json:"adapter"`
	Image       string                 `json:"image"`
	User        string                 `json:"user"`
	WorkingDir  string                 `json:"workingDir"`
	Command     StartupCommand         `json:"command"`
	Environment map[string]string      `json:"environment,omitempty"`
	DataMounts  []RuntimeDataMount     `json:"dataMounts"`
	Ports       []RuntimePort          `json:"ports"`
	Stop        RuntimeStop            `json:"stop"`
	Health      RuntimeHealth          `json:"health"`
	Console     *RuntimeConsoleAdapter `json:"console,omitempty"`
}

type RuntimeDataMount struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Backup bool   `json:"backup"`
}

type RuntimePort struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"containerPort"`
	Role          string `json:"role"`
}

type RuntimeStop struct {
	Method         string `json:"method"`
	Value          string `json:"value"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type RuntimeHealth struct {
	Type             string `json:"type"`
	PortRef          string `json:"portRef,omitempty"`
	IntervalSeconds  int    `json:"intervalSeconds"`
	TimeoutSeconds   int    `json:"timeoutSeconds"`
	FailureThreshold int    `json:"failureThreshold"`
}

type RuntimeConsoleAdapter struct {
	Adapter string `json:"adapter"`
	Port    int    `json:"port"`
}

type AuditEvent struct {
	ID          string    `json:"id"`
	ActorName   string    `json:"actorName"`
	Action      string    `json:"action"`
	TargetType  string    `json:"targetType"`
	TargetName  string    `json:"targetName"`
	Result      string    `json:"result"`
	OperationID string    `json:"operationId"`
	CreatedAt   time.Time `json:"createdAt"`
}

type AuditQuery struct {
	Cursor     string
	Limit      int
	Actor      string
	Action     string
	TargetType string
	Result     string
	From       *time.Time
	To         *time.Time
}

type AuditEventPage struct {
	Items      []AuditEvent `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type ConsoleLine struct {
	Sequence  int64     `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"`
	Message   string    `json:"message"`
}

// ConsoleCommandResult is the runtime-neutral result returned by an Agent for
// one console command dispatch. RequestID and ServerID are correlation fields,
// not user-controlled display data.
type ConsoleCommandResult struct {
	RequestID string
	ServerID  string
	Succeeded bool
	ErrorCode string
	Retryable bool
}

type FileEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type FileContent struct {
	Path       string    `json:"path"`
	Content    string    `json:"content"`
	Encoding   string    `json:"encoding"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Allocation struct {
	ID            string    `json:"id"`
	ServerID      string    `json:"serverId"`
	NodeID        string    `json:"nodeId"`
	BindIP        string    `json:"bindIp"`
	Port          int       `json:"port"`
	Protocol      string    `json:"protocol"`
	PortRef       string    `json:"portRef,omitempty"`
	ContainerPort int       `json:"containerPort"`
	Role          string    `json:"role"`
	Primary       bool      `json:"primary"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type CreateAllocationInput struct {
	BindIP        string `json:"bindIp"`
	Port          int    `json:"port"`
	Protocol      string `json:"protocol"`
	PortRef       string `json:"portRef,omitempty"`
	ContainerPort int    `json:"containerPort,omitempty"`
	Role          string `json:"role,omitempty"`
	Primary       bool   `json:"primary"`
}

type StartupCommand struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

type StartupVariable struct {
	Key        string   `json:"key"`
	Type       string   `json:"type"`
	Secret     bool     `json:"secret"`
	Required   bool     `json:"required"`
	Default    any      `json:"default,omitempty"`
	Value      any      `json:"value,omitempty"`
	HasValue   bool     `json:"hasValue"`
	Minimum    *int64   `json:"minimum,omitempty"`
	Maximum    *int64   `json:"maximum,omitempty"`
	MinLength  *int     `json:"minLength,omitempty"`
	MaxLength  *int     `json:"maxLength,omitempty"`
	EnumValues []string `json:"enumValues,omitempty"`
	ConstValue any      `json:"constValue,omitempty"`
}

type StartupBinding struct {
	Variable string `json:"variable"`
	Target   string `json:"target"`
	Name     string `json:"name,omitempty"`
	Path     string `json:"path,omitempty"`
	Template string `json:"template"`
}

type Startup struct {
	ServerID   string            `json:"serverId"`
	Generation int64             `json:"generation"`
	Command    StartupCommand    `json:"command"`
	Variables  []StartupVariable `json:"variables"`
	Bindings   []StartupBinding  `json:"-"`
}

type Backup struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	SizeBytes       *int64     `json:"sizeBytes"`
	Checksum        *string    `json:"checksum"`
	ManifestDigest  *string    `json:"manifestDigest"`
	StorageLocation *string    `json:"storageLocation"`
	RetentionUntil  *time.Time `json:"retentionUntil"`
	CreatedAt       time.Time  `json:"createdAt"`
	CompletedAt     *time.Time `json:"completedAt"`
	FailureCode     *string    `json:"failureCode"`
	FailureMessage  *string    `json:"failureMessage"`
	DeletedAt       *time.Time `json:"deletedAt"`
}

// BackupContent 是备份下载的返回内容。Content 按 Base64 标记为原文或 base64 编码。
type BackupContent struct {
	Content   []byte
	Base64    bool
	SizeBytes int64
	Filename  string
}

type Overview struct {
	Environment          string       `json:"environment"`
	ServerCount          int          `json:"serverCount"`
	RunningServerCount   int          `json:"runningServerCount"`
	OnlineNodeCount      int          `json:"onlineNodeCount"`
	TotalNodeCount       int          `json:"totalNodeCount"`
	QueuedOperationCount int          `json:"queuedOperationCount"`
	CPUPercent           float64      `json:"cpuPercent"`
	MemoryUsedBytes      int64        `json:"memoryUsedBytes"`
	MemoryTotalBytes     int64        `json:"memoryTotalBytes"`
	RecentActivity       []AuditEvent `json:"recentActivity"`
}

type CreateServerInput struct {
	Name             string `json:"name"`
	GameDefinitionID string `json:"gameDefinitionId"`
	GameBundleDigest string `json:"gameBundleDigest"`
	NodeID           string `json:"nodeId"`
	MemoryMB         int    `json:"memoryMb"`
	DiskGB           int    `json:"diskGb"`
}

// ServerObserved 是 Agent 对某个服务器实际状态的回报。
type ServerObserved struct {
	ServerID           string
	ObservedGeneration int64
	ObservedPower      string // unknown|stopped|starting|running|stopping
	HealthCondition    string // unknown|healthy|unhealthy
	RuntimeID          string
	BundleDigest       string
	Detail             string
	ObservedAt         time.Time
}

// NodeCapability 描述节点支持的一项能力及其版本。
type NodeCapability struct {
	Name    string
	Version string
}

// RunningOperation 是 Agent 正在执行的操作快照。
type RunningOperation struct {
	OperationID string
	Checkpoint  string
	Attempt     int
	ServerID    string
}

// Heartbeat 是 Agent 周期性上报的节点资源与运行状态。
type Heartbeat struct {
	NodeID               string
	MemoryTotalBytes     int64
	MemoryAvailableBytes int64
	DiskTotalBytes       int64
	DiskAvailableBytes   int64
	CPULoad              float64
	AgentVersion         string
	ObservedAt           time.Time
	RunningOperations    []RunningOperation
}
