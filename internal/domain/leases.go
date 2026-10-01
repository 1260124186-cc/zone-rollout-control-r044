package domain

import "sort"

// ReconcileLeaseDeadlines rebuilds the expiry index only from leases that are
// currently claimed. Finished leases must never contribute a deadline for a
// later lease on the same node.
func ReconcileLeaseDeadlines(state *State) {
	currentLeases := make(map[string]NodeLease)
	for _, lease := range state.Leases {
		if lease.State != LeaseClaimed {
			continue
		}
		existing, exists := currentLeases[lease.NodeID]
		if !exists || isNewerLease(lease, existing) {
			currentLeases[lease.NodeID] = lease
		}
	}

	state.LeaseDeadlines = make([]LeaseDeadline, 0, len(currentLeases))
	for _, lease := range currentLeases {
		state.LeaseDeadlines = append(state.LeaseDeadlines, LeaseDeadline{
			LeaseID:   lease.ID,
			NodeID:    lease.NodeID,
			ExpiresAt: lease.ExpiresAt,
		})
	}
	sort.Slice(state.LeaseDeadlines, func(i, j int) bool {
		if state.LeaseDeadlines[i].NodeID != state.LeaseDeadlines[j].NodeID {
			return state.LeaseDeadlines[i].NodeID < state.LeaseDeadlines[j].NodeID
		}
		return state.LeaseDeadlines[i].LeaseID < state.LeaseDeadlines[j].LeaseID
	})
}

func isNewerLease(candidate, existing NodeLease) bool {
	if !candidate.ClaimedAt.Equal(existing.ClaimedAt) {
		return candidate.ClaimedAt.After(existing.ClaimedAt)
	}
	if !candidate.ExpiresAt.Equal(existing.ExpiresAt) {
		return candidate.ExpiresAt.After(existing.ExpiresAt)
	}
	return candidate.ID > existing.ID
}

func HasLeaseDeadline(state *State, leaseID, nodeID string) bool {
	for _, deadline := range state.LeaseDeadlines {
		if deadline.LeaseID == leaseID && deadline.NodeID == nodeID {
			return true
		}
	}
	return false
}

// RemoveLeaseDeadline removes only the deadline that belongs to the supplied
// lease on the supplied node.
func RemoveLeaseDeadline(state *State, leaseID, nodeID string) bool {
	active := make([]LeaseDeadline, 0, len(state.LeaseDeadlines))
	removed := false
	for _, deadline := range state.LeaseDeadlines {
		if deadline.LeaseID == leaseID && deadline.NodeID == nodeID {
			removed = true
			continue
		}
		active = append(active, deadline)
	}
	state.LeaseDeadlines = active
	return removed
}
