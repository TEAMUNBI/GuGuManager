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
	ID             string   `json:"id"`
	BundleDigest   string   `json:"bundleDigest"`
	Name           string   `json:"name"`
	Summary        string   `json:"summary"`
	Version        string   `json:"version"`
	GameVersion    string   `json:"gameVersion"`
	Status         string   `json:"status"`
	Capabilities   []string `json:"capabilities"`
	Platforms      []string `json:"platforms"`
	Servers        int      `json:"servers"`
	Icon           string   `json:"icon"`
	DefaultMemory  int      `json:"defaultMemoryMb"`
	DefaultDisk    int      `json:"defaultDiskGb"`
	BundleDocument string   `json:"-"`
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

type ConsoleLine struct {
	Sequence  int64     `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"`
	Message   string    `json:"message"`
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
	ID        string    `json:"id"`
	ServerID  string    `json:"serverId"`
	NodeID    string    `json:"nodeId"`
	BindIP    string    `json:"bindIp"`
	Port      int       `json:"port"`
	Protocol  string    `json:"protocol"`
	Primary   bool      `json:"primary"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CreateAllocationInput struct {
	BindIP   string `json:"bindIp"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Primary  bool   `json:"primary"`
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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	SizeBytes int64     `json:"sizeBytes"`
	Checksum  string    `json:"checksum"`
	CreatedAt time.Time `json:"createdAt"`
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
