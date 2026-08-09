package domain

import (
	"errors"
	"sync"
	"time"
)

type PowerAction string

const (
	PowerStart   PowerAction = "start"
	PowerStop    PowerAction = "stop"
	PowerRestart PowerAction = "restart"
	PowerKill    PowerAction = "kill"
)

var ErrIdempotencyKeyReused = errors.New("idempotency key reused with a different request")

type Operation struct {
	ID             string          `json:"id"`
	ServerID       string          `json:"serverId"`
	NodeID         string          `json:"nodeId"`
	Type           PowerAction     `json:"type"`
	Status         string          `json:"status"`
	Progress       int             `json:"progress"`
	Generation     int64           `json:"generation"`
	Attempt        int             `json:"attempt"`
	MaxAttempts    int             `json:"maxAttempts"`
	LeaseOwner     *string         `json:"leaseOwner"`
	LeaseExpiresAt *time.Time      `json:"leaseExpiresAt"`
	Checkpoint     string          `json:"checkpoint"`
	Error          *OperationError `json:"error"`
	IdempotencyKey string          `json:"-"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// OperationError is the public, safe-to-display failure metadata for an
// asynchronous operation. It intentionally does not carry host paths,
// credentials, commands, or other internal implementation details.
type OperationError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// NewQueuedOperation gives every newly accepted operation the same baseline
// retry and checkpoint metadata. Stores may enrich its lease fields once a
// worker starts the operation.
func NewQueuedOperation(operationID string, serverID string, nodeID string, operationType PowerAction, generation int64, idempotencyKey string, now time.Time) Operation {
	return Operation{
		ID:             operationID,
		ServerID:       serverID,
		NodeID:         nodeID,
		Type:           operationType,
		Status:         "queued",
		Progress:       0,
		Generation:     generation,
		Attempt:        1,
		MaxAttempts:    1,
		Checkpoint:     "queued",
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

type PowerCoordinator struct {
	mu         sync.Mutex
	operations []Operation
	newID      func() string
	now        func() time.Time
}

func NewPowerCoordinator(newID func() string, now func() time.Time) *PowerCoordinator {
	return &PowerCoordinator{newID: newID, now: now}
}

func (c *PowerCoordinator) Request(serverID string, nodeID string, action PowerAction, idempotencyKey string) (Operation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, operation := range c.operations {
		if operation.ServerID != serverID || operation.IdempotencyKey != idempotencyKey {
			continue
		}
		if operation.Type != action {
			return Operation{}, ErrIdempotencyKeyReused
		}
		return operation, nil
	}

	now := c.now()
	operation := NewQueuedOperation(c.newID(), serverID, nodeID, action, 0, idempotencyKey, now)
	c.operations = append(c.operations, operation)
	return operation, nil
}
