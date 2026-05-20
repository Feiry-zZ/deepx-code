package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFlattenPlans_Basic(t *testing.T) {
	plans := []PlanItem{
		{ID: "p1", Title: "First", Model: "flash"},
		{ID: "p2", Title: "Second", Model: "pro", DependsOn: []string{"p1"}},
	}
	nodes := flattenPlans(plans)
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].ID != "p1" || nodes[1].ID != "p2" {
		t.Errorf("unexpected ids: %v", nodes)
	}
	if len(nodes[1].DependsOn) != 1 || nodes[1].DependsOn[0] != "p1" {
		t.Errorf("p2 DependsOn wrong: %v", nodes[1].DependsOn)
	}
}

func TestRunDAG_SequentialOrdering(t *testing.T) {
	// p1 → p2 → p3, 必须按 1→2→3 顺序跑
	nodes := flattenPlans([]PlanItem{
		{ID: "p1", Model: "flash"},
		{ID: "p2", Model: "flash", DependsOn: []string{"p1"}},
		{ID: "p3", Model: "flash", DependsOn: []string{"p2"}},
	})

	var (
		mu    sync.Mutex
		order []string
	)
	exec := func(n *schedulerNode, preds map[string]string) (string, error) {
		mu.Lock()
		order = append(order, n.ID)
		mu.Unlock()
		return "ok-" + n.ID, nil
	}

	runDAG(context.Background(), nodes, exec, nil)

	if len(order) != 3 || order[0] != "p1" || order[1] != "p2" || order[2] != "p3" {
		t.Fatalf("expected sequential [p1 p2 p3], got %v", order)
	}
}

func TestRunDAG_ParallelInitialReady(t *testing.T) {
	nodes := flattenPlans([]PlanItem{
		{ID: "p1", Model: "flash"},
		{ID: "p2", Model: "flash"},
	})

	started := make(chan string, 2)
	release := make(chan struct{})
	exec := func(n *schedulerNode, preds map[string]string) (string, error) {
		started <- n.ID
		<-release
		return "ok", nil
	}

	finished := make(chan []schedulerNode, 1)
	go func() {
		finished <- runDAG(context.Background(), nodes, exec, nil)
	}()

	deadline := time.After(500 * time.Millisecond)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-deadline:
			t.Fatalf("only saw %d/2 started before deadline: %v", len(seen), seen)
		}
	}
	close(release)
	<-finished
}

func TestRunDAG_FailurePropagatesBlock(t *testing.T) {
	nodes := flattenPlans([]PlanItem{
		{ID: "p1", Model: "flash"},
		{ID: "p2", Model: "flash", DependsOn: []string{"p1"}},
		{ID: "p3", Model: "flash"},
	})

	exec := func(n *schedulerNode, preds map[string]string) (string, error) {
		if n.ID == "p1" {
			return "", errors.New("boom")
		}
		return "ok-" + n.ID, nil
	}
	final := runDAG(context.Background(), nodes, exec, nil)

	st := map[string]PlanStatus{}
	for _, n := range final {
		st[n.ID] = n.Status
	}
	if st["p1"] != PlanStatusFailed {
		t.Errorf("p1 want failed, got %s", st["p1"])
	}
	if st["p2"] != PlanStatusBlocked {
		t.Errorf("p2 want blocked, got %s", st["p2"])
	}
	if st["p3"] != PlanStatusDone {
		t.Errorf("p3 want done, got %s", st["p3"])
	}
}

func TestRunDAG_CycleDeadlockMarkedBlocked(t *testing.T) {
	nodes := flattenPlans([]PlanItem{
		{ID: "p1", Model: "flash", DependsOn: []string{"p2"}},
		{ID: "p2", Model: "flash", DependsOn: []string{"p1"}},
	})

	var calls int32
	exec := func(n *schedulerNode, preds map[string]string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", nil
	}

	final := runDAG(context.Background(), nodes, exec, nil)

	if c := atomic.LoadInt32(&calls); c != 0 {
		t.Errorf("exec should never be called for cyclic deps, got %d calls", c)
	}
	for _, n := range final {
		if n.Status != PlanStatusBlocked {
			t.Errorf("%s want blocked, got %s", n.ID, n.Status)
		}
	}
}

func TestRunDAG_PredecessorSummariesPassed(t *testing.T) {
	nodes := flattenPlans([]PlanItem{
		{ID: "p1", Model: "flash"},
		{ID: "p2", Model: "flash", DependsOn: []string{"p1"}},
	})

	var seenPredForP2 string
	exec := func(n *schedulerNode, preds map[string]string) (string, error) {
		if n.ID == "p2" {
			seenPredForP2 = preds["p1"]
		}
		return "summary-of-" + n.ID, nil
	}
	runDAG(context.Background(), nodes, exec, nil)

	if seenPredForP2 != "summary-of-p1" {
		t.Errorf("p2 should see p1's summary, got %q", seenPredForP2)
	}
}
