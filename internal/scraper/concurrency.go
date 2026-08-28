package scraper

import "sync"

// ScrapeConcurrency configures how many release detail pages a listing scan
// (ScrapeFiltered/ScrapeFilteredThroughEnd) fetches at once, and an optional
// checkpoint hook invoked once before each batch of concurrent fetches
// starts. Before Byparr pooling, every detail fetch happened one at a time,
// and the caller's per-item Progress callback doubled as its own yield
// point for job preemption; now that several fetches can be in flight at
// once, Checkpoint is the equivalent yield point at batch granularity - the
// caller (internal/monitor) can still let a higher-priority job preempt
// promptly, just no longer between every single fetch.
//
// The zero value (Max <= 1) preserves the pre-pooling behavior exactly: one
// detail fetch at a time, Checkpoint (if set) called before each one.
type ScrapeConcurrency struct {
	Max        int
	Checkpoint func()
}

// fetchDetailsConcurrently calls fetch(i) for every i in [0,n), running up
// to concurrency.Max of them at once in fixed-size batches - the next batch
// only starts once every fetch in the current one has returned. This is a
// deliberately simple bounded-batch model rather than a continuous-flow
// worker pool: given the workload here is dominated by solver round trips
// and per-instance cooldowns (see SolverPool), the small efficiency loss
// from a fast fetch waiting out a slow batch-mate is a fair trade for much
// simpler, easier-to-verify concurrency than a work-stealing pool would
// need. concurrency.Checkpoint, if set, is called once before each batch is
// dispatched (including the very first), so a preempting job is only ever
// blocked behind one in-flight batch, not the whole remaining candidate
// list.
func fetchDetailsConcurrently(n int, concurrency ScrapeConcurrency, fetch func(i int)) {
	batch := concurrency.Max
	if batch < 1 {
		batch = 1
	}
	for start := 0; start < n; start += batch {
		if concurrency.Checkpoint != nil {
			concurrency.Checkpoint()
		}
		end := min(start+batch, n)
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				fetch(i)
			}(i)
		}
		wg.Wait()
	}
}
