package domain

// BackupStatusTransitionAllowed defines the only state transitions that may
// be applied to a backup record. A failed restore or deletion returns the
// recovery point to ready so the last known-good backup remains usable.
func BackupStatusTransitionAllowed(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case "creating":
		return to == "ready" || to == "failed"
	case "failed":
		return to == "creating" || to == "deleting"
	case "ready":
		return to == "restoring" || to == "deleting"
	case "restoring":
		return to == "ready"
	case "deleting":
		return to == "ready" || to == "failed" || to == "deleted"
	case "deleted":
		return false
	default:
		return false
	}
}
