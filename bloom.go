package main

import (
	"crypto/md5"
	"crypto/sha256"
	"hash"
	"hash/fnv"
	"math"
)

// BloomFilter is a thread-safe probabilistic set membership tester
type BloomFilter struct {
	bits   []byte
	size   uint64
	k      uint32 // number of hash functions
}

// NewBloomFilter creates a Bloom filter with estimated capacity and false positive rate
// capacity: expected number of elements
// fpRate: desired false positive rate (e.g., 0.02 = 2%)
func NewBloomFilter(capacity uint64, fpRate float64) *BloomFilter {
	// Calculate optimal bit array size
	size := optimalSize(capacity, fpRate)
	k := optimalK(size, capacity)

	return &BloomFilter{
		bits:   make([]byte, (size+7)/8),
		size:   size,
		k:      k,
	}
}

// Add adds an element to the filter
func (bf *BloomFilter) Add(data []byte) {
	for i := uint32(0); i < bf.k; i++ {
		idx := bf.hash(data, i)
		byteIdx := idx / 8
		bitIdx := uint8(idx % 8)
		bf.bits[byteIdx] |= 1 << bitIdx
	}
}

// Contains checks if an element is in the filter
// Returns false: element definitely not in set
// Returns true: element probably in set (with fpRate probability of false positive)
func (bf *BloomFilter) Contains(data []byte) bool {
	for i := uint32(0); i < bf.k; i++ {
		idx := bf.hash(data, i)
		byteIdx := idx / 8
		bitIdx := uint8(idx % 8)
		if bf.bits[byteIdx]&(1<<bitIdx) == 0 {
			return false // Definitely not present
		}
	}
	return true // Probably present
}

// hash generates the i-th hash value for data
func (bf *BloomFilter) hash(data []byte, i uint32) uint64 {
	var hashFn hash.Hash

	switch i % 3 {
	case 0:
		hashFn = fnv.New64a()
	case 1:
		hashFn = sha256.New()
	default:
		hashFn = md5.New()
	}

	hashFn.Write(data)
	hashBytes := hashFn.Sum(nil)

	// Convert bytes to uint64
	result := uint64(0)
	for j := 0; j < 8 && j < len(hashBytes); j++ {
		result = result*256 + uint64(hashBytes[j])
	}

	return result % bf.size
}

// optimalSize calculates the optimal bit array size
// m = -n*ln(p) / (ln(2)^2)
func optimalSize(n uint64, p float64) uint64 {
	return uint64(-float64(n) * math.Log(p) / (math.Log(2) * math.Log(2)))
}

// optimalK calculates the optimal number of hash functions
// k = (m/n) * ln(2)
func optimalK(m, n uint64) uint32 {
	if n == 0 {
		return 1
	}
	k := float64(m) / float64(n) * math.Log(2)
	if k < 1 {
		return 1
	}
	if k > 64 {
		return 64
	}
	return uint32(k)
}
