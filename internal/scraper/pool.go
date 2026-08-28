package scraper

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Instance is one configured FlareSolverr/Byparr endpoint. Lower Priority
// numbers are preferred - an Acquire call always picks the highest-priority
// (lowest number) free instance first, matching the job_priority_* /
// PriorityKind convention used elsewhere in the app (see internal/monitor).
type Instance struct {
	URL      string `json:"url"`
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
}

// ParseInstances decodes the JSON array stored in the byparr_instances
// setting into a list of Instances, trimming trailing slashes from each URL
// the same way the old single flaresolverr_url setting was normalized.
// Tolerant of empty/invalid input (returns nil), matching the pattern used
// elsewhere in the app for JSON-encoded list settings (e.g.
// stash.parsePathRemaps) - validation of well-formedness belongs at the
// point the setting is saved (PUT /api/settings), not here.
func ParseInstances(raw string) []Instance {
	var instances []Instance
	if e := json.Unmarshal([]byte(raw), &instances); e != nil {
		return nil
	}
	for i := range instances {
		instances[i].URL = strings.TrimRight(strings.TrimSpace(instances[i].URL), "/")
	}
	return instances
}

// backgroundSolverPriority is the request priority assumed for any call that
// reaches documentOnce without an explicit priority set via
// WithSolverPriority on its context (e.g. a caller outside the monitor
// package's job system). It's deliberately low (numerically high) so a real
// job's requests are never starved behind an uncontextualized one.
const backgroundSolverPriority = 100

type solverPriorityKey struct{}

// WithSolverPriority attaches a solver-pool request priority to ctx. Lower
// numbers are served first when multiple callers are contending for a free
// pool instance (see SolverPool.Acquire). Passed down through every layer
// via ctx rather than as an explicit parameter, since ScrapeFiltered/
// Refresh/detail/document/documentOnce already all thread ctx through and
// adding a priority parameter to each would be far more invasive for the
// same effect.
func WithSolverPriority(ctx context.Context, priority int) context.Context {
	return context.WithValue(ctx, solverPriorityKey{}, priority)
}

func solverPriorityFromContext(ctx context.Context) int {
	if v, ok := ctx.Value(solverPriorityKey{}).(int); ok {
		return v
	}
	return backgroundSolverPriority
}

// poolSlot is one configured instance's live state.
type poolSlot struct {
	instance Instance
	busy     bool
}

// waiter is a pending Acquire call parked waiting for a free instance.
type waiter struct {
	priority int
	ch       chan *Lease
}

// Lease represents a checked-out solver instance. Call Release (typically
// via defer) once done with it so it becomes acquirable again - after its
// configured cooldown elapses, not immediately - so a busy pool naturally
// throttles requests to each instance exactly like the single-instance
// cooldown sleep used to.
type Lease struct {
	pool  *SolverPool
	index int
	url   string
}

// URL returns the solver endpoint this lease grants access to.
func (l *Lease) URL() string { return l.url }

// Release returns the leased instance to the pool. Safe to call more than
// once (only the first call has any effect) and safe to call on a nil
// *Lease.
func (l *Lease) Release() {
	if l == nil || l.pool == nil {
		return
	}
	p := l.pool
	idx := l.index
	l.pool = nil
	p.release(idx)
}

// SolverPool arbitrates concurrent access to a configurable set of
// FlareSolverr/Byparr instances. Acquire blocks until an enabled instance is
// free (not currently leased, and past its cooldown since its last
// release), preferring the highest-priority free instance; when several
// callers are waiting, the one with the lowest requestPriority is served
// first once an instance frees up, mirroring the existing job-priority
// convention (manual "Update details" jumping ahead of a background
// screenshot-backfill worker contending for the same pool).
//
// A pool configured with zero instances is a valid, common state (no
// FlareSolverr/Byparr configured at all) - callers should check
// EnabledCount() and skip the pool entirely (fetch direct) rather than call
// Acquire, which would otherwise block forever.
type SolverPool struct {
	mu       sync.Mutex
	slots    []*poolSlot
	cooldown time.Duration
	waiters  []*waiter
}

// NewSolverPool returns an empty, unconfigured pool (EnabledCount() == 0)
// ready to have Configure called on it.
func NewSolverPool() *SolverPool {
	return &SolverPool{}
}

// Configure hot-swaps the pool's configured instances and shared
// per-instance cooldown. Safe to call while leases are in flight - existing
// leases are unaffected by their slot disappearing from a later Configure
// call (Release on a since-removed slot index is a harmless no-op via the
// bounds check in release); any callers waiting in Acquire are re-evaluated
// against the new instance list immediately.
func (p *SolverPool) Configure(instances []Instance, cooldown time.Duration) {
	p.mu.Lock()
	slots := make([]*poolSlot, 0, len(instances))
	for _, inst := range instances {
		slots = append(slots, &poolSlot{instance: inst})
	}
	p.slots = slots
	p.cooldown = cooldown
	p.mu.Unlock()
	p.wakeWaiters()
}

// EnabledCount returns how many configured instances are currently enabled,
// regardless of whether they're free right now. Callers use this to size
// concurrent worker pools ("use up to N at once") and to decide whether any
// solver is configured at all.
func (p *SolverPool) EnabledCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, s := range p.slots {
		if s.instance.Enabled {
			n++
		}
	}
	return n
}

// Acquire blocks until a free, enabled instance is available or ctx is
// done, then returns a Lease for it. requestPriority controls ordering
// among concurrent callers contending for the same pool - see the SolverPool
// doc comment.
func (p *SolverPool) Acquire(ctx context.Context, requestPriority int) (*Lease, error) {
	p.mu.Lock()
	if lease := p.tryAcquireLocked(); lease != nil {
		p.mu.Unlock()
		return lease, nil
	}
	w := &waiter{priority: requestPriority, ch: make(chan *Lease, 1)}
	p.insertWaiterLocked(w)
	p.mu.Unlock()

	select {
	case lease := <-w.ch:
		if lease != nil {
			return lease, nil
		}
		return nil, ctx.Err()
	case <-ctx.Done():
		p.mu.Lock()
		p.removeWaiterLocked(w)
		p.mu.Unlock()
		// A lease may have been handed to this waiter concurrently, right as
		// ctx fired - drain non-blockingly and give it back rather than
		// leaking a checked-out instance nobody will ever release.
		select {
		case lease := <-w.ch:
			lease.Release()
		default:
		}
		return nil, ctx.Err()
	}
}

// tryAcquireLocked picks the highest-priority (lowest Instance.Priority)
// free, enabled slot, marks it busy, and returns a Lease for it - or nil if
// none is currently available. Caller must hold p.mu.
func (p *SolverPool) tryAcquireLocked() *Lease {
	best := -1
	for i, s := range p.slots {
		if !s.instance.Enabled || s.busy {
			continue
		}
		if best == -1 || p.slots[i].instance.Priority < p.slots[best].instance.Priority {
			best = i
		}
	}
	if best == -1 {
		return nil
	}
	p.slots[best].busy = true
	return &Lease{pool: p, index: best, url: p.slots[best].instance.URL}
}

func (p *SolverPool) insertWaiterLocked(w *waiter) {
	index := len(p.waiters)
	for i, existing := range p.waiters {
		if w.priority < existing.priority {
			index = i
			break
		}
	}
	p.waiters = append(p.waiters, nil)
	copy(p.waiters[index+1:], p.waiters[index:])
	p.waiters[index] = w
}

func (p *SolverPool) removeWaiterLocked(w *waiter) {
	for i, existing := range p.waiters {
		if existing == w {
			p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
			return
		}
	}
}

// release marks the instance at index free again, after this pool's
// configured cooldown elapses (immediately, if no cooldown is set), then
// wakes any waiters. index may reference a slot that no longer exists (a
// Configure call replaced the slots while this lease was outstanding); the
// bounds check makes that a harmless no-op.
func (p *SolverPool) release(index int) {
	p.mu.Lock()
	cooldown := p.cooldown
	p.mu.Unlock()

	freeSlot := func() {
		p.mu.Lock()
		if index >= 0 && index < len(p.slots) {
			p.slots[index].busy = false
		}
		p.mu.Unlock()
		p.wakeWaiters()
	}
	if cooldown <= 0 {
		freeSlot()
		return
	}
	time.AfterFunc(cooldown, freeSlot)
}

// wakeWaiters hands out any newly-free instances to the highest-priority
// waiters, in priority order, until either no waiters remain or no
// instances are free.
func (p *SolverPool) wakeWaiters() {
	for {
		p.mu.Lock()
		if len(p.waiters) == 0 {
			p.mu.Unlock()
			return
		}
		lease := p.tryAcquireLocked()
		if lease == nil {
			p.mu.Unlock()
			return
		}
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		p.mu.Unlock()

		select {
		case w.ch <- lease:
		default:
			// Waiter's Acquire already gave up (ctx cancelled) and drained/
			// removed itself; hand the instance back instead of losing it.
			lease.Release()
		}
	}
}
