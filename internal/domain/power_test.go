package domain

import (
	"errors"
	"testing"
	"time"
)

func TestPowerRequestReusesIdenticalIdempotentOperation(t *testing.T) {
	const firstNodeID = "11111111-1111-4111-8111-111111111111"
	const secondNodeID = "22222222-2222-4222-8222-222222222222"
	ids := []string{"operation-1", "operation-2"}
	coordinator := NewPowerCoordinator(func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}, func() time.Time { return time.Unix(0, 0).UTC() })

	first, err := coordinator.Request("server-1", firstNodeID, PowerStart, "power-key-0001")
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	second, err := coordinator.Request("server-1", secondNodeID, PowerStart, "power-key-0001")
	if err != nil {
		t.Fatalf("duplicate request failed: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("duplicate operation ID = %q, want %q", second.ID, first.ID)
	}
	if first.NodeID != firstNodeID || second.NodeID != first.NodeID {
		t.Fatalf("operation node IDs = %q and %q, want immutable %s", first.NodeID, second.NodeID, firstNodeID)
	}
	if first.Attempt != 1 || first.MaxAttempts != 1 {
		t.Fatalf("operation attempts = %d/%d, want 1/1", first.Attempt, first.MaxAttempts)
	}
	if first.Checkpoint != "queued" {
		t.Fatalf("operation checkpoint = %q, want queued", first.Checkpoint)
	}
	if first.LeaseOwner != nil || first.LeaseExpiresAt != nil {
		t.Fatalf("queued operation unexpectedly has a lease: %+v", first)
	}
	if first.Error != nil {
		t.Fatalf("queued operation error = %+v, want nil", first.Error)
	}
}

func TestPowerRequestRejectsReusedKeyWithDifferentAction(t *testing.T) {
	coordinator := NewPowerCoordinator(func() string { return "operation-1" }, time.Now)
	if _, err := coordinator.Request("server-1", "node-1", PowerStart, "power-key-0001"); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	_, err := coordinator.Request("server-1", "node-1", PowerStop, "power-key-0001")
	if !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("error = %v, want ErrIdempotencyKeyReused", err)
	}
}
