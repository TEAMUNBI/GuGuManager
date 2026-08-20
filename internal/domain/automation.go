package domain

import "time"

type Schedule struct {
	ID                string     `json:"id"`
	ServerID          string     `json:"serverId,omitempty"`
	Name              string     `json:"name"`
	Action            string     `json:"action"`
	CronExpression    string     `json:"cronExpression"`
	Timezone          string     `json:"timezone"`
	Enabled           bool       `json:"enabled"`
	MissedRunPolicy   string     `json:"missedRunPolicy"`
	ConcurrencyPolicy string     `json:"concurrencyPolicy"`
	NextRunAt         *time.Time `json:"nextRunAt"`
	LastScheduledAt   *time.Time `json:"lastScheduledAt"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type ScheduleInput struct {
	ServerID          string `json:"serverId,omitempty"`
	Name              string `json:"name"`
	Action            string `json:"action"`
	CronExpression    string `json:"cronExpression"`
	Timezone          string `json:"timezone"`
	Enabled           *bool  `json:"enabled,omitempty"`
	MissedRunPolicy   string `json:"missedRunPolicy,omitempty"`
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`
}

type ScheduleRun struct {
	ID           string     `json:"id"`
	ScheduleID   string     `json:"scheduleId"`
	ScheduledFor time.Time  `json:"scheduledFor"`
	Status       string     `json:"status"`
	OperationID  string     `json:"operationId,omitempty"`
	FailureCode  string     `json:"failureCode,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

type BackupPolicy struct {
	ServerID      string    `json:"serverId"`
	RetentionDays int       `json:"retentionDays"`
	MaxCount      int       `json:"maxCount"`
	ProtectManual bool      `json:"protectManual"`
	Enabled       bool      `json:"enabled"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type BackupPolicyInput struct {
	RetentionDays int  `json:"retentionDays"`
	MaxCount      int  `json:"maxCount"`
	ProtectManual bool `json:"protectManual"`
	Enabled       bool `json:"enabled"`
}

type Notification struct {
	ID             string         `json:"id"`
	Severity       string         `json:"severity"`
	Category       string         `json:"category"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	TargetType     string         `json:"targetType"`
	TargetID       string         `json:"targetId"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"createdAt"`
	AcknowledgedAt *time.Time     `json:"acknowledgedAt,omitempty"`
}

type WebhookEndpoint struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type WebhookInput struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type WebhookCredential struct {
	WebhookEndpoint
	Secret string `json:"secret"`
}

type Quota struct {
	MaxNodes             int       `json:"maxNodes"`
	MaxServers           int       `json:"maxServers"`
	MaxMemoryBytes       int64     `json:"maxMemoryBytes"`
	MaxDiskBytes         int64     `json:"maxDiskBytes"`
	MaxRunningServers    int       `json:"maxRunningServers"`
	MaxConcurrentCreates int       `json:"maxConcurrentCreates"`
	MaxConcurrentBackups int       `json:"maxConcurrentBackups"`
	MaxConcurrentUploads int       `json:"maxConcurrentUploads"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type Capacity struct {
	NodeCount            int   `json:"nodeCount"`
	ServerCount          int   `json:"serverCount"`
	RunningServerCount   int   `json:"runningServerCount"`
	TotalMemoryBytes     int64 `json:"totalMemoryBytes"`
	AllocatedMemoryBytes int64 `json:"allocatedMemoryBytes"`
	TotalDiskBytes       int64 `json:"totalDiskBytes"`
	AllocatedDiskBytes   int64 `json:"allocatedDiskBytes"`
	Quota                Quota `json:"quota"`
}

type OutboxDeadLetter struct {
	ID              string         `json:"id"`
	AggregateType   string         `json:"aggregateType"`
	AggregateID     string         `json:"aggregateId"`
	EventType       string         `json:"eventType"`
	EventVersion    int            `json:"eventVersion"`
	Payload         map[string]any `json:"payload"`
	PublishAttempts int            `json:"publishAttempts"`
	LastError       string         `json:"lastError"`
	BusinessAt      time.Time      `json:"businessAt"`
	DeadLetteredAt  time.Time      `json:"deadLetteredAt"`
}
