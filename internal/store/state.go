package store

import (
	"sort"

	"solo-0014-zone-rollout-control/internal/domain"
)

const schemaVersion = 1

func blankState() domain.State {
	return normalize(domain.State{})
}

func normalize(state domain.State) domain.State {
	if state.SchemaVersion == 0 {
		state.SchemaVersion = schemaVersion
	}
	if state.Plans == nil {
		state.Plans = make(map[string]domain.RolloutPlan)
	}
	if state.Revisions == nil {
		state.Revisions = make(map[string]domain.ZoneRevision)
	}
	if state.Nodes == nil {
		state.Nodes = make(map[string]domain.ResolverNode)
	}
	if state.Leases == nil {
		state.Leases = make(map[string]domain.NodeLease)
	}
	if state.LeaseDeadlines == nil {
		state.LeaseDeadlines = []domain.LeaseDeadline{}
	}
	if state.LeaseGroups == nil {
		state.LeaseGroups = make(map[string]domain.LeaseGroup)
	}
	if state.Probes == nil {
		state.Probes = make(map[string]domain.Probe)
	}
	if state.Assessments == nil {
		state.Assessments = make(map[string]domain.RolloutAssessment)
	}
	if state.LeaseRequestKeys == nil {
		state.LeaseRequestKeys = make(map[string]string)
	}
	if state.AssessmentRequestKeys == nil {
		state.AssessmentRequestKeys = make(map[string]string)
	}
	if state.AuditEvents == nil {
		state.AuditEvents = []domain.AuditEvent{}
	}
	return state
}

// RemoveLeaseDeadline drops only the deadline belonging to the given lease.
// It never touches deadlines of other leases on the same node, so a finished
// lease cannot affect a later lease that may have replaced it.
func RemoveLeaseDeadline(state *domain.State, leaseID string) {
	active := state.LeaseDeadlines[:0]
	for _, deadline := range state.LeaseDeadlines {
		if deadline.LeaseID != leaseID {
			active = append(active, deadline)
		}
	}
	state.LeaseDeadlines = active
}

// HasOtherClaimedLease reports whether the node currently holds a claimed
// lease other than leaseID. Finishing transitions use it so a finished lease
// can never free a node already occupied by a newer lease.
func HasOtherClaimedLease(state *domain.State, nodeID, leaseID string) bool {
	for _, other := range state.Leases {
		if other.NodeID == nodeID && other.ID != leaseID && other.State == domain.LeaseClaimed {
			return true
		}
	}
	return false
}

func reconcileLeaseDeadlines(state *domain.State) {
	claimedByNode := make(map[string]domain.NodeLease)
	for _, lease := range state.Leases {
		// Only a lease that is still claimed is eligible for deadline
		// cleanup; finished leases must never generate sweep entries that
		// could affect later leases on the same node.
		if lease.State != domain.LeaseClaimed {
			continue
		}
		current, exists := claimedByNode[lease.NodeID]
		if exists && !lease.ExpiresAt.Before(current.ExpiresAt) {
			continue
		}
		claimedByNode[lease.NodeID] = lease
	}
	// Repair node occupancy from the claimed leases: a node currently
	// holding a claimed lease must be leased, otherwise it is ready (unless
	// quarantined). This restores a consistent view after a restart with a
	// snapshot left behind by an earlier lifecycle bug.
	claimedNodes := make(map[string]bool, len(claimedByNode))
	for nodeID := range claimedByNode {
		claimedNodes[nodeID] = true
	}
	for nodeID, node := range state.Nodes {
		target := domain.NodeReady
		if node.State == domain.NodeQuarantined {
			target = domain.NodeQuarantined
		}
		if claimedNodes[nodeID] {
			target = domain.NodeLeased
		}
		if node.State != target {
			node.State = target
			node.Version++
			state.Nodes[nodeID] = node
		}
	}
	state.LeaseDeadlines = state.LeaseDeadlines[:0]
	for nodeID, lease := range claimedByNode {
		state.LeaseDeadlines = append(state.LeaseDeadlines, domain.LeaseDeadline{
			LeaseID:   lease.ID,
			NodeID:    nodeID,
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
