package logging

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

type Entry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
	// Seq is a monotonically increasing sequence number assigned in append
	// order (1, 2, 3, ...), independent of the entry's position in the ring
	// (which shifts as older entries are evicted). It is the cursor Phase
	// 13's incremental log pagination (EntriesBefore/EntriesAfter) pages
	// against, since wall-clock Time is not guaranteed unique across
	// entries logged in the same instant.
	Seq int64 `json:"seq"`
}
type RingHandler struct {
	next    slog.Handler
	limit   int
	mu      sync.RWMutex
	entries []Entry
	nextSeq int64
}

// DefaultCapacity is how many log entries the ring retains in memory when a
// caller does not specify one. Phase 13 raised this from a previous fixed
// 500 - too small to make "load older entries as you scroll" meaningful for
// more than a couple of pages - to a much larger but still bounded figure,
// balancing "don't impose a small fixed total-entry limit" against
// "preserve reasonable ... memory usage" (each entry is small; retaining
// this many costs low-single-digit megabytes).
const DefaultCapacity = 5000

func NewRing(next slog.Handler, limit int) *RingHandler {
	if limit < 1 {
		limit = DefaultCapacity
	}
	return &RingHandler{next: next, limit: limit}
}
func (h *RingHandler) Enabled(c context.Context, l slog.Level) bool { return h.next.Enabled(c, l) }
func (h *RingHandler) Handle(c context.Context, r slog.Record) error {
	e := Entry{Time: r.Time, Level: r.Level.String(), Message: r.Message, Fields: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		if err, ok := a.Value.Any().(error); ok {
			e.Fields[a.Key] = err.Error()
		} else {
			e.Fields[a.Key] = fmt.Sprint(a.Value.Any())
		}
		return true
	})
	h.mu.Lock()
	h.nextSeq++
	e.Seq = h.nextSeq
	h.entries = append(h.entries, e)
	if len(h.entries) > h.limit {
		h.entries = append([]Entry(nil), h.entries[len(h.entries)-h.limit:]...)
	}
	h.mu.Unlock()
	return h.next.Handle(c, r)
}
func (h *RingHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &child{root: h, next: h.next.WithAttrs(a)}
}
func (h *RingHandler) WithGroup(n string) slog.Handler {
	return &child{root: h, next: h.next.WithGroup(n)}
}
func (h *RingHandler) Entries(limit int) []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if limit <= 0 || limit > len(h.entries) {
		limit = len(h.entries)
	}
	return append([]Entry(nil), h.entries[len(h.entries)-limit:]...)
}

// EntriesBefore returns up to limit entries strictly older (lower Seq) than
// cursor, oldest-first, for infinite-scroll "load older entries" paging
// (Phase 13). cursor <= 0 always returns nothing - callers wanting the
// newest page should use Entries instead - and a cursor whose older
// entries have already been evicted by the ring's capacity limit likewise
// returns an empty page rather than an error, so the caller just learns
// there is nothing further back to load.
func (h *RingHandler) EntriesBefore(cursor int64, limit int) []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if cursor <= 0 {
		return nil
	}
	if limit <= 0 {
		limit = len(h.entries)
	}
	out := make([]Entry, 0, min(limit, len(h.entries)))
	for i := len(h.entries) - 1; i >= 0 && len(out) < limit; i-- {
		if h.entries[i].Seq < cursor {
			out = append(out, h.entries[i])
		}
	}
	slices.Reverse(out)
	return out
}

// EntriesAfter returns up to limit entries strictly newer (higher Seq) than
// cursor, ascending, for an efficient tail-poll page (Phase 13): a live
// client only fetches lines it does not already have instead of re-fetching
// its whole visible window on every poll tick.
func (h *RingHandler) EntriesAfter(cursor int64, limit int) []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []Entry{}
	for _, e := range h.entries {
		if e.Seq > cursor {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

type child struct {
	root *RingHandler
	next slog.Handler
}

func (h *child) Enabled(c context.Context, l slog.Level) bool  { return h.next.Enabled(c, l) }
func (h *child) Handle(c context.Context, r slog.Record) error { return h.root.Handle(c, r) }
func (h *child) WithAttrs(a []slog.Attr) slog.Handler {
	return &child{root: h.root, next: h.next.WithAttrs(a)}
}
func (h *child) WithGroup(n string) slog.Handler {
	return &child{root: h.root, next: h.next.WithGroup(n)}
}
