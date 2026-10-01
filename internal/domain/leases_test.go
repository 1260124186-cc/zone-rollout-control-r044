package domain

import (
	"testing"
	"time"
)

func TestReconcileLeaseDeadlinesUsesOnlyCurrentClaimedLeases(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	state := State{
		Nodes: map[string]ResolverNode{
			"node-1": {ID: "node-1", State: NodeLeased},
		},
		Leases: map[string]NodeLease{
			"lease-released": {
				ID:        "lease-released",
				NodeID:    "node-1",
				State:     LeaseReleased,
				ClaimedAt: now,
				ExpiresAt: now.Add(time.Minute),
			},
			"lease-reported": {
				ID:        "lease-reported",
				NodeID:    "node-2",
				State:     LeaseReported,
				ClaimedAt: now,
				ExpiresAt: now.Add(time.Minute),
			},
			"lease-expired": {
				ID:        "lease-expired",
				NodeID:    "node-3",
				State:     LeaseExpired,
				ClaimedAt: now,
				ExpiresAt: now.Add(time.Minute),
			},
			"lease-claimed": {
				ID:        "lease-claimed",
				NodeID:    "node-4",
				State:     LeaseClaimed,
				ClaimedAt: now,
				ExpiresAt: now.Add(2 * time.Minute),
			},
		},
	}

	ReconcileLeaseDeadlines(&state)

	if len(state.LeaseDeadlines) != 1 {
		t.Fatalf("deadline count = %d, want 1", len(state.LeaseDeadlines))
	}
	deadline := state.LeaseDeadlines[0]
	if deadline.LeaseID != "lease-claimed" || deadline.NodeID != "node-4" {
		t.Fatalf("deadline = %#v, want the claimed lease", deadline)
	}
}

func TestHasLeaseDeadlineRequiresLeaseAndNodeIdentity(t *testing.T) {
	state := State{LeaseDeadlines: []LeaseDeadline{
		{LeaseID: "lease-1", NodeID: "node-1"},
	}}

	if !HasLeaseDeadline(&state, "lease-1", "node-1") {
		t.Fatal("expected matching lease and node identity")
	}
	if HasLeaseDeadline(&state, "lease-1", "node-2") {
		t.Fatal("lease matched a deadline belonging to another node")
	}
	if HasLeaseDeadline(&state, "lease-2", "node-1") {
		t.Fatal("another lease matched the current lease deadline")
	}
}

func TestRemoveLeaseDeadlineScopedByLeaseAndNode(t *testing.T) {
	deadlines := []LeaseDeadline{
		{LeaseID: "old-lease", NodeID: "node-1", ExpiresAt: time.Now()},
		{LeaseID: "new-lease", NodeID: "node-1", ExpiresAt: time.Now().Add(time.Minute)},
	}
	state := State{LeaseDeadlines: deadlines}

	if !RemoveLeaseDeadline(&state, "old-lease", "node-1") {
		t.Fatal("expected the matching deadline to be removed")
	}
	if len(state.LeaseDeadlines) != 1 || state.LeaseDeadlines[0].LeaseID != "new-lease" {
		t.Fatalf("deadlines = %#v, want only new-lease", state.LeaseDeadlines)
	}
	if RemoveLeaseDeadline(&state, "old-lease", "other-node") {
		t.Fatal("unexpected removal when lease identity does not match")
	}
}
