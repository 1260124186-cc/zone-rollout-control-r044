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

func reconcileLeaseDeadlines(state *domain.State) {
	deadlines := make(map[string]domain.LeaseDeadline)
	for _, lease := range state.Leases {
		current, exists := deadlines[lease.NodeID]
		if exists && !lease.ExpiresAt.Before(current.ExpiresAt) {
			continue
		}
		deadlines[lease.NodeID] = domain.LeaseDeadline{
			LeaseID:   lease.ID,
			NodeID:    lease.NodeID,
			ExpiresAt: lease.ExpiresAt,
		}
	}
	state.LeaseDeadlines = state.LeaseDeadlines[:0]
	for _, deadline := range deadlines {
		state.LeaseDeadlines = append(state.LeaseDeadlines, deadline)
	}
	sort.Slice(state.LeaseDeadlines, func(i, j int) bool {
		if state.LeaseDeadlines[i].NodeID != state.LeaseDeadlines[j].NodeID {
			return state.LeaseDeadlines[i].NodeID < state.LeaseDeadlines[j].NodeID
		}
		return state.LeaseDeadlines[i].LeaseID < state.LeaseDeadlines[j].LeaseID
	})
}
