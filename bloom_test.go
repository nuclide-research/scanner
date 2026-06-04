package main

import (
	"fmt"
	"sync"
	"testing"
)

// A Bloom filter must have no false negatives: everything added reads back present.
func TestBloomNoFalseNegatives(t *testing.T) {
	bf := NewBloomFilter(10000, 0.01)
	keys := make([]string, 5000)
	for i := range keys {
		keys[i] = fmt.Sprintf("1.2.3.%d:8123:ch", i)
		bf.Add([]byte(keys[i]))
	}
	for _, k := range keys {
		if !bf.Contains([]byte(k)) {
			t.Fatalf("false negative: %q was added but reads absent", k)
		}
	}
}

// The old bug: the filter set only 3 distinct bits regardless of k (the hash
// switched on i%3 over the same data). A correct double-hashed filter sets k
// distinct positions, so adding one element sets close to k bits, not 3.
func TestBloomSetsKDistinctBits(t *testing.T) {
	bf := NewBloomFilter(1000, 0.0001) // tiny capacity + low FPR -> k is large (>3)
	if bf.k <= 3 {
		t.Skipf("k=%d not >3; cannot exercise the k-collapse regression", bf.k)
	}
	bf.Add([]byte("single-element"))
	set := bf.popcount()
	if set <= 3 {
		t.Fatalf("adding one element set only %d bits with k=%d; the filter collapsed to <=3 hashes", set, bf.k)
	}
}

// Concurrent Add must not lose bit writes (run with -race). After concurrent
// adds, every key must still read present (a lost OR would cause a false negative).
func TestBloomConcurrentAddNoLostWrites(t *testing.T) {
	bf := NewBloomFilter(100000, 0.01)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				bf.Add([]byte(fmt.Sprintf("w%d-%d", w, i)))
			}
		}(w)
	}
	wg.Wait()
	for w := 0; w < 16; w++ {
		for i := 0; i < 2000; i++ {
			if !bf.Contains([]byte(fmt.Sprintf("w%d-%d", w, i))) {
				t.Fatalf("lost write: w%d-%d added concurrently but reads absent", w, i)
			}
		}
	}
}
