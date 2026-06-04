package main

import (
	"fmt"
	"sync"
	"testing"
)

// The fatal bug: a fixed table with no collision handling silently drops
// DISTINCT keys that hash to the same slot. With open addressing, every distinct
// key must claim exactly once and never be dropped as a phantom duplicate.
func TestDistinctKeysNeverDropped(t *testing.T) {
	cd := NewCASDedup(65536)
	const n = 20000 // load factor ~0.3 in a 65536 table — collisions are guaranteed
	claimed := 0
	for i := 0; i < n; i++ {
		if cd.TryClaim(fmt.Sprintf("10.0.%d.%d:8123:clickhouse", i/256, i%256)) {
			claimed++
		}
	}
	if claimed != n {
		t.Fatalf("claimed %d of %d distinct keys; %d were dropped as phantom duplicates", claimed, n, n-claimed)
	}
}

// The same key must dedup: first claim wins, every repeat loses.
func TestSameKeyDedups(t *testing.T) {
	cd := NewCASDedup(1024)
	if !cd.TryClaim("1.1.1.1:9200:elastic") {
		t.Fatal("first claim of a fresh key should succeed")
	}
	for i := 0; i < 5; i++ {
		if cd.TryClaim("1.1.1.1:9200:elastic") {
			t.Fatal("repeat claim of the same key should fail (it is a duplicate)")
		}
	}
}

// NewCASDedup must honor the size argument (the old version ignored it).
func TestNewCASDedupHonorsSize(t *testing.T) {
	small := NewCASDedup(8)
	// 8 distinct keys fill it exactly; all must still claim (fail-open on full,
	// never drop a distinct key).
	for i := 0; i < 8; i++ {
		if !small.TryClaim(fmt.Sprintf("k%d", i)) {
			t.Fatalf("key %d dropped in a size-8 table that is not yet full", i)
		}
	}
}

// Concurrent claims of distinct keys (run with -race): no lost updates, every
// distinct key claimed exactly once across all goroutines.
func TestConcurrentDistinctClaims(t *testing.T) {
	cd := NewCASDedup(65536)
	const workers, perWorker = 16, 1000
	var claimed int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			local := 0
			for i := 0; i < perWorker; i++ {
				if cd.TryClaim(fmt.Sprintf("w%d-k%d", w, i)) {
					local++
				}
			}
			mu.Lock()
			claimed += int64(local)
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	if claimed != workers*perWorker {
		t.Fatalf("claimed %d, want %d (distinct keys dropped under concurrency)", claimed, workers*perWorker)
	}
}
