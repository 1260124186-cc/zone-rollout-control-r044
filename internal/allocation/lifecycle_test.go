package allocation_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"solo-0014-zone-rollout-control/internal/allocation"
	"solo-0014-zone-rollout-control/internal/domain"
	"solo-0014-zone-rollout-control/internal/platform"
	"solo-0014-zone-rollout-control/internal/probe"
	"solo-0014-zone-rollout-control/internal/store"
)

type fixture struct {
	t      *testing.T
	path   string
	clock  *platform.ManualClock
	store  *store.Store
	alloc  *allocation.Service
	probes *probe.Service
	planID string
	nodeID string
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := platform.NewManualClock(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	now := clock.Now()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ids := platform.CryptoIDGenerator{}
	nodeID := ids.New("nod")
	planID := ids.New("pln")
	err = st.Write(context.Background(), func(state *domain.State) error {
		state.Nodes[nodeID] = domain.ResolverNode{
			ID: nodeID, NodeName: "resolver-east-01", Region: "east",
			State: domain.NodeReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		state.Plans[planID] = domain.RolloutPlan{
			ID: planID, Zone: "lease.example", State: domain.PlanActive,
			MinimumSuccessRatio: 1, MaximumLatencyMS: 100, ExpectedNodes: 1,
			Version: 2, CreatedAt: now,
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}
	alloc := allocation.New(st, clock, ids)
	probes := probe.New(st, clock, ids, alloc)
	return &fixture{t: t, path: path, clock: clock, store: st, alloc: alloc, probes: probes,
		planID: planID, nodeID: nodeID, now: now}
}

func (f *fixture) reopen() {
	f.t.Helper()
	st, err := store.Open(f.path)
	if err != nil {
		f.t.Fatalf("reopen store: %v", err)
	}
	ids := platform.CryptoIDGenerator{}
	f.store = st
	f.alloc = allocation.New(st, f.clock, ids)
	f.probes = probe.New(st, f.clock, ids, f.alloc)
}

func (f *fixture) claim(key string, seconds int) domain.NodeLease {
	f.t.Helper()
	group, err := f.alloc.Allocate(context.Background(), f.planID, domain.LeaseInput{
		RequestKey: key, Region: "east", LeaseSeconds: seconds, MaxNodes: 1,
	})
	if err != nil {
		f.t.Fatalf("allocate %s: %v", key, err)
	}
	if len(group.LeaseIDs) != 1 {
		f.t.Fatalf("allocate %s: expected one lease, got %d", key, len(group.LeaseIDs))
	}
	lease := f.mustLease(group.LeaseIDs[0])
	if lease.State != domain.LeaseClaimed {
		f.t.Fatalf("allocate %s: lease state %s", key, lease.State)
	}
	if f.nodeState() != domain.NodeLeased {
		f.t.Fatalf("allocate %s: node state %s", key, f.nodeState())
	}
	return lease
}

func (f *fixture) mustLease(id string) domain.NodeLease {
	f.t.Helper()
	var lease domain.NodeLease
	if err := f.store.Read(context.Background(), func(state *domain.State) error {
		var ok bool
		lease, ok = state.Leases[id]
		if !ok {
			return domain.NotFound("lease", id)
		}
		return nil
	}); err != nil {
		f.t.Fatalf("read lease %s: %v", id, err)
	}
	return lease
}

func (f *fixture) nodeState() domain.NodeState {
	f.t.Helper()
	var state domain.NodeState
	if err := f.store.Read(context.Background(), func(s *domain.State) error {
		state = s.Nodes[f.nodeID].State
		return nil
	}); err != nil {
		f.t.Fatalf("read node: %v", err)
	}
	return state
}

func (f *fixture) deadlineCount() int {
	f.t.Helper()
	var n int
	if err := f.store.Read(context.Background(), func(s *domain.State) error {
		n = len(s.LeaseDeadlines)
		return nil
	}); err != nil {
		f.t.Fatalf("read deadlines: %v", err)
	}
	return n
}

// Baseline: first claim then natural expiry frees the node.
func TestBaselineFirstClaimExpiresOnTime(t *testing.T) {
	f := newFixture(t)
	lease := f.claim("req-key-000001", 600)
	f.clock.Advance(601 * time.Second)
	if n, err := f.alloc.ExpireDue(context.Background()); err != nil || n != 1 {
		f.t.Fatalf("expire: n=%d err=%v", n, err)
	}
	if got := f.mustLease(lease.ID).State; got != domain.LeaseExpired {
		f.t.Fatalf("lease state %s", got)
	}
	if f.nodeState() != domain.NodeReady {
		f.t.Fatalf("node state %s", f.nodeState())
	}
	if f.deadlineCount() != 0 {
		f.t.Fatalf("deadlines left: %d", f.deadlineCount())
	}
}

// Early release followed by a new claim: old deadline must not kill new lease.
func TestEarlyReleaseThenNewClaimNotKilled(t *testing.T) {
	f := newFixture(t)
	first := f.claim("req-key-000002", 100)
	if _, err := f.alloc.Release(context.Background(), first.ID); err != nil {
		f.t.Fatalf("release: %v", err)
	}
	if f.nodeState() != domain.NodeReady {
		f.t.Fatalf("node not ready after release")
	}
	if f.deadlineCount() != 0 {
		f.t.Fatalf("stale deadline after release: %d", f.deadlineCount())
	}
	second := f.claim("req-key-000003", 600)
	// Old deadline was t+100; passing it must not affect the second lease.
	f.clock.Advance(101 * time.Second)
	if n, err := f.alloc.ExpireDue(context.Background()); err != nil || n != 0 {
		f.t.Fatalf("expire after old deadline: n=%d err=%v", n, err)
	}
	if got := f.mustLease(second.ID).State; got != domain.LeaseClaimed {
		f.t.Fatalf("second lease state %s after old deadline", got)
	}
	if f.nodeState() != domain.NodeLeased {
		f.t.Fatalf("node state %s: new lease was killed by old deadline", f.nodeState())
	}
	// New lease's own deadline still fires on time.
	f.clock.Advance(500 * time.Second)
	if n, err := f.alloc.ExpireDue(context.Background()); err != nil || n != 1 {
		f.t.Fatalf("expire new lease: n=%d err=%v", n, err)
	}
	if f.nodeState() != domain.NodeReady || f.mustLease(second.ID).State != domain.LeaseExpired {
		f.t.Fatalf("new lease did not expire correctly")
	}
}

// Report completion followed by a new claim: old probe-settled deadline must
// not kill the new lease.
func TestReportThenNewClaimNotKilled(t *testing.T) {
	f := newFixture(t)
	first := f.claim("req-key-000004", 100)
	_, err := f.probes.Submit(context.Background(), first.ID, domain.ProbeInput{
		RequestKey: "probe-key-001", Success: true, ObservedSerial: 1001,
		LatencyMS: 10, ObservedAt: f.clock.Now(),
	})
	if err != nil {
		f.t.Fatalf("submit probe: %v", err)
	}
	if f.nodeState() != domain.NodeReady {
		f.t.Fatalf("node not ready after report")
	}
	if f.deadlineCount() != 0 {
		f.t.Fatalf("stale deadline after report: %d", f.deadlineCount())
	}
	second := f.claim("req-key-000005", 600)
	f.clock.Advance(101 * time.Second)
	if n, _ := f.alloc.ExpireDue(context.Background()); n != 0 {
		f.t.Fatalf("expire swept %d at old deadline", n)
	}
	if got := f.mustLease(second.ID).State; got != domain.LeaseClaimed {
		f.t.Fatalf("second lease state %s", got)
	}
	if f.nodeState() != domain.NodeLeased {
		f.t.Fatalf("node state %s", f.nodeState())
	}
	// Probe against the still-valid new lease must be accepted.
	_, err = f.probes.Submit(context.Background(), second.ID, domain.ProbeInput{
		RequestKey: "probe-key-002", Success: true, ObservedSerial: 1001,
		LatencyMS: 10, ObservedAt: f.clock.Now(),
	})
	if err != nil {
		f.t.Fatalf("probe on new lease rejected: %v", err)
	}
}

// Repeated release of an already-finished old lease must not drop the deadline
// of a later lease, which otherwise leaks forever.
func TestDuplicateReleaseDoesNotLeakNewLease(t *testing.T) {
	f := newFixture(t)
	first := f.claim("req-key-000006", 100)
	if _, err := f.alloc.Release(context.Background(), first.ID); err != nil {
		f.t.Fatalf("release: %v", err)
	}
	second := f.claim("req-key-000007", 600)
	// Retry of the old release must be idempotent and must not touch the new lease.
	if _, err := f.alloc.Release(context.Background(), first.ID); err != nil {
		f.t.Fatalf("duplicate release: %v", err)
	}
	if f.deadlineCount() != 1 {
		f.t.Fatalf("expected one active deadline, got %d", f.deadlineCount())
	}
	f.clock.Advance(601 * time.Second)
	if n, _ := f.alloc.ExpireDue(context.Background()); n != 1 {
		f.t.Fatalf("new lease not swept past its own deadline, n=%d", n)
	}
	if f.mustLease(second.ID).State != domain.LeaseExpired || f.nodeState() != domain.NodeReady {
		f.t.Fatalf("new lease leaked: state=%s node=%s", f.mustLease(second.ID).State, f.nodeState())
	}
}

// Restart between two claims: reconcile must rebuild deadlines only from
// claimed leases and must not resurrect finished-lease deadlines.
func TestRestartBetweenClaimsConsistent(t *testing.T) {
	f := newFixture(t)
	first := f.claim("req-key-000008", 100)
	if _, err := f.alloc.Release(context.Background(), first.ID); err != nil {
		f.t.Fatalf("release: %v", err)
	}
	f.reopen()
	if f.nodeState() != domain.NodeReady {
		f.t.Fatalf("node not ready after restart")
	}
	if f.deadlineCount() != 0 {
		f.t.Fatalf("reconcile resurrected a finished deadline: %d", f.deadlineCount())
	}
	second := f.claim("req-key-000009", 600)
	f.clock.Advance(101 * time.Second)
	if n, _ := f.alloc.ExpireDue(context.Background()); n != 0 {
		f.t.Fatalf("swept %d at old deadline after restart", n)
	}
	if f.mustLease(second.ID).State != domain.LeaseClaimed || f.nodeState() != domain.NodeLeased {
		f.t.Fatalf("new lease killed after restart: lease=%s node=%s",
			f.mustLease(second.ID).State, f.nodeState())
	}
	// Restart while the second lease is still claimed: deadline survives and fires.
	f.reopen()
	if f.nodeState() != domain.NodeLeased || f.deadlineCount() != 1 {
		f.t.Fatalf("restart lost active lease: node=%s deadlines=%d", f.nodeState(), f.deadlineCount())
	}
	f.clock.Advance(500 * time.Second)
	if n, _ := f.alloc.ExpireDue(context.Background()); n != 1 {
		f.t.Fatalf("active lease did not expire after restart")
	}
	if f.nodeState() != domain.NodeReady {
		f.t.Fatalf("node not ready after expiry post-restart")
	}
}

// Restart with a snapshot corrupted by the old bug: a claimed lease survives
// while the node was freed early and its deadline was dropped (stuck node).
// Reconcile must rebuild occupancy and the deadline solely from claimed
// leases, so the lease can then expire normally.
func TestRestartRepairsPrematurelyFreedNode(t *testing.T) {
	f := newFixture(t)
	first := f.claim("req-key-000010", 100)
	if _, err := f.alloc.Release(context.Background(), first.ID); err != nil {
		f.t.Fatalf("release: %v", err)
	}
	second := f.claim("req-key-000011", 600)
	if err := f.store.Write(context.Background(), func(state *domain.State) error {
		// Old-bug artifacts: the valid new lease is still claimed, but the
		// node was set ready early and its deadline was removed.
		node := state.Nodes[f.nodeID]
		node.State = domain.NodeReady
		state.Nodes[f.nodeID] = node
		state.LeaseDeadlines = state.LeaseDeadlines[:0]
		return nil
	}); err != nil {
		f.t.Fatalf("corrupt state: %v", err)
	}
	f.reopen()
	if f.nodeState() != domain.NodeLeased {
		f.t.Fatalf("node with claimed lease not repaired to leased")
	}
	if f.deadlineCount() != 1 {
		f.t.Fatalf("expected rebuilt claimed lease deadline, got %d", f.deadlineCount())
	}
	var deadlineLease string
	_ = f.store.Read(context.Background(), func(s *domain.State) error {
		deadlineLease = s.LeaseDeadlines[0].LeaseID
		return nil
	})
	if deadlineLease != second.ID {
		f.t.Fatalf("deadline points at %s, want %s", deadlineLease, second.ID)
	}
	// Node must not be allocatable while the claimed lease is alive.
	_, err := f.alloc.Allocate(context.Background(), f.planID, domain.LeaseInput{
		RequestKey: "req-key-000012", Region: "east", LeaseSeconds: 10, MaxNodes: 1,
	})
	if err == nil {
		f.t.Fatalf("allocation succeeded while repaired node was still leased")
	}
	// Rebuilt deadline fires on time and frees the node.
	f.clock.Advance(601 * time.Second)
	if n, _ := f.alloc.ExpireDue(context.Background()); n != 1 {
		f.t.Fatalf("rebuilt deadline did not expire the claimed lease")
	}
	if f.nodeState() != domain.NodeReady || f.mustLease(second.ID).State != domain.LeaseExpired {
		f.t.Fatalf("claimed lease did not expire after repair: lease=%s node=%s",
			f.mustLease(second.ID).State, f.nodeState())
	}
}
