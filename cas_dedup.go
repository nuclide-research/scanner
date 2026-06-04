package main

import (
	"hash/fnv"
	"sync/atomic"
	"time"
)

// CASDedup uses compare-and-swap for lock-free deduplication
// Each entry is a uint64 timestamp of when the (IP:port:version) was first claimed
type CASDedup struct {
	entries [2_000_000]uint64
}

// NewCASDedup creates a new CAS deduplication map
func NewCASDedup(size uint64) *CASDedup {
	return &CASDedup{}
}

// TryClaim attempts to claim a dedup key using CAS
// Returns true if this worker was first to claim it
// Returns false if another worker already claimed it
func (cd *CASDedup) TryClaim(key string) bool {
	// Hash the key to an index
	idx := cd.hashToIndex(key)

	// Current timestamp (monotonic)
	now := uint64(time.Now().UnixNano())

	// Load current value
	expected := atomic.LoadUint64(&cd.entries[idx])

	// If slot is empty (0), we try to claim it
	// If slot is occupied, we fail (another worker got it)
	if expected == 0 {
		// Try CAS: if still 0, set to now
		success := atomic.CompareAndSwapUint64(
			&cd.entries[idx],
			0,       // expected
			now,     // new value
		)
		return success
	}

	// Slot was already occupied
	// However, handle hash collisions: if collision, check if timestamp is "old"
	// If >1 hour old, allow re-claim (IP might be reused)
	if now-expected > 3600*1e9 { // 1 hour in nanoseconds
		success := atomic.CompareAndSwapUint64(
			&cd.entries[idx],
			expected,
			now,
		)
		return success
	}

	return false
}

// hashToIndex converts a dedup key to an index in the entries array
func (cd *CASDedup) hashToIndex(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32() % uint32(len(cd.entries))
}

// Stats returns the number of claimed entries (approximate)
func (cd *CASDedup) Stats() int {
	count := 0
	for i := 0; i < len(cd.entries); i++ {
		if atomic.LoadUint64(&cd.entries[i]) != 0 {
			count++
		}
	}
	return count
}
