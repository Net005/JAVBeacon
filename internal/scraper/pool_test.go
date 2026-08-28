package scraper

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSolverPoolAcquirePrefersHighestPriorityFreeInstance covers the "prefer
// a preferred instance" half of the multi-Byparr feature: with several
// instances free at once, Acquire must always pick the lowest Priority
// number first.
func TestSolverPoolAcquirePrefersHighestPriorityFreeInstance(t *testing.T) {
	p := NewSolverPool()
	p.Configure([]Instance{
		{URL: "http://mid", Priority: 5, Enabled: true},
		{URL: "http://top", Priority: 1, Enabled: true},
		{URL: "http://low", Priority: 10, Enabled: true},
	}, 0)

	lease, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if lease.URL() != "http://top" {
		t.Fatalf("acquired %q, want the priority-1 instance", lease.URL())
	}
	lease.Release()
}

// TestSolverPoolAcquireFallsThroughToNextFreeInstance covers the "evenly
// spread once the preferred one is busy" half: once the top-priority
// instance is held, a second concurrent Acquire must fall through to the
// next-highest-priority free one rather than blocking behind the first.
func TestSolverPoolAcquireFallsThroughToNextFreeInstance(t *testing.T) {
	p := NewSolverPool()
	p.Configure([]Instance{
		{URL: "http://top", Priority: 1, Enabled: true},
		{URL: "http://second", Priority: 2, Enabled: true},
	}, 0)

	first, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.URL() != "http://top" {
		t.Fatalf("first acquired %q, want http://top", first.URL())
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := p.Acquire(ctx, 0)
	if err != nil {
		t.Fatalf("second Acquire should not block on the busy top instance: %v", err)
	}
	if second.URL() != "http://second" {
		t.Fatalf("second acquired %q, want http://second", second.URL())
	}
	first.Release()
	second.Release()
}

// TestSolverPoolWaitersServedInPriorityOrder covers contention: with a
// single instance held, two callers waiting for it must be served in
// requestPriority order (lower first) regardless of which called Acquire
// first - this is what lets a manual "Update details" request jump ahead of
// a background screenshot-backfill worker contending for the same pool.
func TestSolverPoolWaitersServedInPriorityOrder(t *testing.T) {
	p := NewSolverPool()
	p.Configure([]Instance{{URL: "http://only", Priority: 1, Enabled: true}}, 0)

	held, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	record := func(priority int) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		lease, err := p.Acquire(ctx, priority)
		if err != nil {
			t.Errorf("Acquire(priority=%d): %v", priority, err)
			return
		}
		mu.Lock()
		order = append(order, priority)
		mu.Unlock()
		lease.Release()
	}

	// Low-priority (background) waiter registers first...
	wg.Add(1)
	go func() { defer wg.Done(); record(100) }()
	time.Sleep(30 * time.Millisecond)
	// ...then a high-priority (manual) waiter registers second, and must
	// still be served first once the held instance is released below.
	wg.Add(1)
	go func() { defer wg.Done(); record(1) }()
	time.Sleep(30 * time.Millisecond)

	held.Release()
	wg.Wait()

	if len(order) != 2 || order[0] != 1 || order[1] != 100 {
		t.Fatalf("service order=%v, want [1 100] (priority 1 served before 100)", order)
	}
}

// TestSolverPoolCooldownDelaysReacquisition covers the per-instance
// cooldown: a released instance must not be handed out again until the
// configured cooldown elapses, and must be handed out once it does.
func TestSolverPoolCooldownDelaysReacquisition(t *testing.T) {
	p := NewSolverPool()
	p.Configure([]Instance{{URL: "http://only", Priority: 1, Enabled: true}}, 80*time.Millisecond)

	lease, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()

	tooSoon, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(tooSoon, 0); err == nil {
		t.Fatal("Acquire succeeded before cooldown elapsed, want it to still be blocked")
	}

	afterCooldown, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	relet, err := p.Acquire(afterCooldown, 0)
	if err != nil {
		t.Fatalf("Acquire after cooldown elapsed: %v", err)
	}
	relet.Release()
}

// TestSolverPoolZeroInstancesBlocksUntilContextDone covers the "no solver
// configured" state: Acquire on an empty pool must never return a lease -
// callers are expected to check EnabledCount() first and fetch directly
// instead (see JavLibrary.documentOnce) - so here it should simply respect
// context cancellation rather than hang forever or panic.
func TestSolverPoolZeroInstancesBlocksUntilContextDone(t *testing.T) {
	p := NewSolverPool()
	if n := p.EnabledCount(); n != 0 {
		t.Fatalf("EnabledCount=%d, want 0 for an unconfigured pool", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx, 0); err == nil {
		t.Fatal("Acquire on an empty pool returned a lease, want it to block until ctx is done")
	}
}
