package logging

import (
	"io"
	"log/slog"
	"testing"
)

func newTestRing(t *testing.T, capacity int) (*RingHandler, *slog.Logger) {
	t.Helper()
	ring := NewRing(slog.NewTextHandler(io.Discard, nil), capacity)
	return ring, slog.New(ring)
}

// TestRingAssignsMonotonicSeqAndEntriesReturnsNewest covers the Phase 13
// addition of a monotonic Seq to each Entry (the cursor incremental
// pagination is built on), and that Entries(limit) keeps its pre-existing
// "most recent N, ascending order" behavior.
func TestRingAssignsMonotonicSeqAndEntriesReturnsNewest(t *testing.T) {
	ring, log := newTestRing(t, 100)
	log.Info("first")
	log.Info("second")
	log.Info("third")

	all := ring.Entries(0)
	if len(all) != 3 {
		t.Fatalf("len=%d, want 3", len(all))
	}
	for i, want := range []string{"first", "second", "third"} {
		if all[i].Message != want || all[i].Seq != int64(i+1) {
			t.Fatalf("entry[%d]=%+v, want message=%q seq=%d", i, all[i], want, i+1)
		}
	}

	newest := ring.Entries(2)
	if len(newest) != 2 || newest[0].Message != "second" || newest[1].Message != "third" {
		t.Fatalf("newest=%+v, want [second third]", newest)
	}
}

// TestRingEntriesBeforeAndAfterPageIncrementally covers Phase 13's
// infinite-scroll ("before" = older, ascending) and tail-poll ("after" =
// only strictly newer, ascending) cursor pagination, so a client never has
// to re-fetch the entire visible window to get either older history or new
// lines that arrived since its last poll.
func TestRingEntriesBeforeAndAfterPageIncrementally(t *testing.T) {
	ring, log := newTestRing(t, 100)
	for _, msg := range []string{"one", "two", "three", "four", "five"} {
		log.Info(msg)
	}

	newest := ring.Entries(2)
	if len(newest) != 2 || newest[0].Message != "four" || newest[1].Message != "five" {
		t.Fatalf("newest page=%+v, want [four five]", newest)
	}
	cursor := newest[0].Seq // seq of "four"

	older := ring.EntriesBefore(cursor, 10)
	if len(older) != 3 || older[0].Message != "one" || older[1].Message != "two" || older[2].Message != "three" {
		t.Fatalf("older page=%+v, want [one two three]", older)
	}

	// A limited page takes the entries closest to the cursor (not the
	// oldest overall), still returned oldest-first.
	limited := ring.EntriesBefore(cursor, 1)
	if len(limited) != 1 || limited[0].Message != "three" {
		t.Fatalf("limited older page=%+v, want [three]", limited)
	}

	if none := ring.EntriesAfter(newest[1].Seq, 10); len(none) != 0 {
		t.Fatalf("after latest seq=%+v, want none", none)
	}

	log.Info("six")
	tail := ring.EntriesAfter(newest[1].Seq, 10)
	if len(tail) != 1 || tail[0].Message != "six" {
		t.Fatalf("tail page=%+v, want [six]", tail)
	}

	// A zero/negative cursor is the "no page beyond this" sentinel for
	// EntriesBefore -- callers requesting the initial/newest page should
	// use Entries instead, so this must not be treated as "everything".
	if empty := ring.EntriesBefore(0, 10); len(empty) != 0 {
		t.Fatalf("EntriesBefore(0, ...)=%+v, want empty", empty)
	}
}

// TestRingEvictsOldestBeyondCapacity covers the bounded-memory side of Phase
// 13: the ring still caps retained entries at its configured capacity (a
// much larger default than before, but not literally unlimited), and
// EntriesBefore a cursor whose older entries have already been evicted
// returns an empty page rather than erroring, so infinite-scroll simply
// stops offering more instead of failing.
func TestRingEvictsOldestBeyondCapacity(t *testing.T) {
	ring, log := newTestRing(t, 3)
	for _, msg := range []string{"one", "two", "three", "four", "five"} {
		log.Info(msg)
	}

	all := ring.Entries(0)
	if len(all) != 3 {
		t.Fatalf("len=%d, want 3 (capacity-bounded)", len(all))
	}
	for i, want := range []string{"three", "four", "five"} {
		if all[i].Message != want {
			t.Fatalf("entry[%d]=%+v, want message=%q", i, all[i], want)
		}
	}
	oldestRetainedSeq := all[0].Seq // seq of "three"
	if evicted := ring.EntriesBefore(oldestRetainedSeq, 10); len(evicted) != 0 {
		t.Fatalf("EntriesBefore(oldest retained seq, ...)=%+v, want empty (older entries were evicted)", evicted)
	}
}
