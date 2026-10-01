package allocation_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"solo-0014-zone-rollout-control/internal/allocation"
	"solo-0014-zone-rollout-control/internal/domain"
	"solo-0014-zone-rollout-control/internal/platform"
	"solo-0014-zone-rollout-control/internal/probe"
	"solo-0014-zone-rollout-control/internal/store"
)

const testPlanID = "plan-test"
const testNodeID = "node-test"

type sequenceIDs struct {
	values map[string]int
}

func newSequenceIDs() *sequenceIDs {
	return &sequenceIDs{values: make(map[string]int)}
}

func (ids *sequenceIDs) New(prefix string) string {
	ids.values[prefix]++
	return prefix + "-" + strconv.Itoa(ids.values[prefix])
}

type leaseFixture struct {
	store       *store.Store
	clock       *platform.ManualClock
	ids         *sequenceIDs
	allocations *allocation.Service
	probes      *probe.Service
}

func newLeaseFixture(t *testing.T) *leaseFixture {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "state.json")
	stateStore, err := store.Open(statePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	clock := platform.NewManualClock(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	ids := newSequenceIDs()
	allocations := allocation.New(stateStore, clock, ids)
	probes := probe.New(stateStore, clock, ids, allocations)

	if err := stateStore.Write(context.Background(), func(state *domain.State) error {
		state.Plans[testPlanID] = domain.RolloutPlan{
			ID:    testPlanID,
			Zone:  "lease.example",
			State: domain.PlanActive,
		}
		state.Nodes[testNodeID] = domain.ResolverNode{
			ID:        testNodeID,
			NodeName:  "resolver-test",
			Region:    "east",
			State:     domain.NodeReady,
			CreatedAt: clock.Now(),
			UpdatedAt: clock.Now(),
		}
		return nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	return &leaseFixture{
		store:       stateStore,
		clock:       clock,
		ids:         ids,
		allocations: allocations,
		probes:      probes,
	}
}

func (f *leaseFixture) reopen(t *testing.T) {
	t.Helper()
	stateStore, err := store.Open(f.store.Path())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	f.store = stateStore
	f.allocations = allocation.New(stateStore, f.clock, f.ids)
	f.probes = probe.New(stateStore, f.clock, f.ids, f.allocations)
}

func (f *leaseFixture) allocate(t *testing.T, requestKey string) domain.LeaseGroup {
	t.Helper()
	group, err := f.allocations.Allocate(context.Background(), testPlanID, domain.LeaseInput{
		RequestKey:   requestKey,
		Region:       "east",
		LeaseSeconds: 60,
		MaxNodes:     1,
	})
	if err != nil {
		t.Fatalf("allocate %s: %v", requestKey, err)
	}
	return group
}

func (f *leaseFixture) release(t *testing.T, leaseID string) domain.NodeLease {
	t.Helper()
	lease, err := f.allocations.Release(context.Background(), leaseID)
	if err != nil {
		t.Fatalf("release %s: %v", leaseID, err)
	}
	return lease
}

func (f *leaseFixture) submit(t *testing.T, leaseID, requestKey string) domain.Probe {
	t.Helper()
	result, err := f.probes.Submit(context.Background(), leaseID, domain.ProbeInput{
		RequestKey:     requestKey,
		Success:        true,
		ObservedSerial: 1001,
		LatencyMS:      10,
		ObservedAt:     f.clock.Now(),
	})
	if err != nil {
		t.Fatalf("submit probe for %s: %v", leaseID, err)
	}
	return result
}

func (f *leaseFixture) expireDue(t *testing.T) int {
	t.Helper()
	count, err := f.allocations.ExpireDue(context.Background())
	if err != nil {
		t.Fatalf("expire due leases: %v", err)
	}
	return count
}

func (f *leaseFixture) snapshot(t *testing.T) domain.State {
	t.Helper()
	state, err := f.store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot state: %v", err)
	}
	return state
}

func assertCurrentLease(t *testing.T, state domain.State, leaseID string, leaseState domain.LeaseState, nodeState domain.NodeState) {
	t.Helper()
	lease, ok := state.Leases[leaseID]
	if !ok {
		t.Fatalf("lease %s not found", leaseID)
	}
	if lease.State != leaseState {
		t.Fatalf("lease %s state = %s, want %s", leaseID, lease.State, leaseState)
	}
	node := state.Nodes[testNodeID]
	if node.State != nodeState {
		t.Fatalf("node state = %s, want %s", node.State, nodeState)
	}
}

func TestReleasedLeaseCannotExpireOrReleaseReplacement(t *testing.T) {
	fixture := newLeaseFixture(t)
	first := fixture.allocate(t, "lease-request-0001")
	fixture.release(t, first.LeaseIDs[0])

	fixture.clock.Advance(30 * time.Second)
	second := fixture.allocate(t, "lease-request-0002")

	// The first lease's original deadline has passed, but it is already released.
	fixture.clock.Advance(31 * time.Second)
	if count := fixture.expireDue(t); count != 0 {
		t.Fatalf("expired count = %d, want 0", count)
	}
	state := fixture.snapshot(t)
	assertCurrentLease(t, state, second.LeaseIDs[0], domain.LeaseClaimed, domain.NodeLeased)
	if len(state.LeaseDeadlines) != 1 || state.LeaseDeadlines[0].LeaseID != second.LeaseIDs[0] {
		t.Fatalf("deadlines = %#v, want only the replacement lease", state.LeaseDeadlines)
	}

	// A retried release of the finished first lease must remain idempotent.
	fixture.release(t, first.LeaseIDs[0])
	state = fixture.snapshot(t)
	assertCurrentLease(t, state, second.LeaseIDs[0], domain.LeaseClaimed, domain.NodeLeased)
	if len(state.LeaseDeadlines) != 1 || state.LeaseDeadlines[0].LeaseID != second.LeaseIDs[0] {
		t.Fatalf("deadlines after duplicate release = %#v, want only the replacement lease", state.LeaseDeadlines)
	}

	// The replacement still expires according to its own deadline.
	fixture.clock.Advance(1 * time.Second)
	if count := fixture.expireDue(t); count != 1 {
		t.Fatalf("expired count = %d, want 1", count)
	}
	state = fixture.snapshot(t)
	assertCurrentLease(t, state, second.LeaseIDs[0], domain.LeaseExpired, domain.NodeReady)
}

func TestReportedLeaseCannotExpireReplacement(t *testing.T) {
	fixture := newLeaseFixture(t)
	first := fixture.allocate(t, "lease-request-0001")
	fixture.submit(t, first.LeaseIDs[0], "probe-request-0001")

	fixture.clock.Advance(30 * time.Second)
	second := fixture.allocate(t, "lease-request-0002")

	fixture.clock.Advance(31 * time.Second)
	if count := fixture.expireDue(t); count != 0 {
		t.Fatalf("expired count = %d, want 0", count)
	}
	state := fixture.snapshot(t)
	assertCurrentLease(t, state, second.LeaseIDs[0], domain.LeaseClaimed, domain.NodeLeased)
	if len(state.LeaseDeadlines) != 1 || state.LeaseDeadlines[0].LeaseID != second.LeaseIDs[0] {
		t.Fatalf("deadlines = %#v, want only the replacement lease", state.LeaseDeadlines)
	}
}

func TestDuplicateReleaseAfterExpiryCannotReleaseReplacement(t *testing.T) {
	fixture := newLeaseFixture(t)
	first := fixture.allocate(t, "lease-request-0001")

	fixture.clock.Advance(60 * time.Second)
	if count := fixture.expireDue(t); count != 1 {
		t.Fatalf("expired count = %d, want 1", count)
	}

	fixture.clock.Advance(30 * time.Second)
	second := fixture.allocate(t, "lease-request-0002")
	fixture.release(t, first.LeaseIDs[0])

	state := fixture.snapshot(t)
	assertCurrentLease(t, state, second.LeaseIDs[0], domain.LeaseClaimed, domain.NodeLeased)
	if len(state.LeaseDeadlines) != 1 || state.LeaseDeadlines[0].LeaseID != second.LeaseIDs[0] {
		t.Fatalf("deadlines = %#v, want only the replacement lease", state.LeaseDeadlines)
	}
}

func TestRestartRecoveryKeepsFinishedLeaseAwayFromReplacement(t *testing.T) {
	for name, finish := range map[string]func(*testing.T, *leaseFixture, string){
		"release": func(t *testing.T, fixture *leaseFixture, leaseID string) {
			fixture.release(t, leaseID)
		},
		"report": func(t *testing.T, fixture *leaseFixture, leaseID string) {
			fixture.submit(t, leaseID, "probe-request-0001")
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newLeaseFixture(t)
			first := fixture.allocate(t, "lease-request-0001")
			finish(t, fixture, first.LeaseIDs[0])

			fixture.reopen(t)
			fixture.clock.Advance(30 * time.Second)
			second := fixture.allocate(t, "lease-request-0002")
			if name == "release" {
				fixture.release(t, first.LeaseIDs[0])
			}

			fixture.clock.Advance(31 * time.Second)
			if count := fixture.expireDue(t); count != 0 {
				t.Fatalf("expired count = %d, want 0", count)
			}
			state := fixture.snapshot(t)
			assertCurrentLease(t, state, second.LeaseIDs[0], domain.LeaseClaimed, domain.NodeLeased)
			if len(state.LeaseDeadlines) != 1 || state.LeaseDeadlines[0].LeaseID != second.LeaseIDs[0] {
				t.Fatalf("deadlines = %#v, want only the replacement lease", state.LeaseDeadlines)
			}
		})
	}
}

func TestFirstLeaseStillExpiresOnTime(t *testing.T) {
	fixture := newLeaseFixture(t)
	first := fixture.allocate(t, "lease-request-0001")

	fixture.clock.Advance(59 * time.Second)
	if count := fixture.expireDue(t); count != 0 {
		t.Fatalf("expired count before deadline = %d, want 0", count)
	}
	state := fixture.snapshot(t)
	assertCurrentLease(t, state, first.LeaseIDs[0], domain.LeaseClaimed, domain.NodeLeased)

	fixture.clock.Advance(1 * time.Second)
	if count := fixture.expireDue(t); count != 1 {
		t.Fatalf("expired count at deadline = %d, want 1", count)
	}
	state = fixture.snapshot(t)
	assertCurrentLease(t, state, first.LeaseIDs[0], domain.LeaseExpired, domain.NodeReady)
}
