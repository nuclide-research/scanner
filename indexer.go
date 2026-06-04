package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Indexer writes scan results to a JSONL file
type Indexer struct {
	file   *os.File
	writer *bufio.Writer
	lock   sync.Mutex
}

// NewIndexer creates a new indexer
func NewIndexer(filename string) *Indexer {
	file, err := os.Create(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output file: %v\n", err)
		os.Exit(1)
	}

	return &Indexer{
		file:   file,
		writer: bufio.NewWriterSize(file, 64*1024), // 64KB buffer
	}
}

// Send sends a result to the indexer
func (idx *Indexer) Send(result *ScanResult) {
	idx.lock.Lock()
	defer idx.lock.Unlock()

	data, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON marshal error: %v\n", err)
		return
	}

	idx.writer.Write(data)
	idx.writer.WriteByte('\n')
}

// Close flushes and closes the indexer
func (idx *Indexer) Close() error {
	idx.lock.Lock()
	defer idx.lock.Unlock()

	if err := idx.writer.Flush(); err != nil {
		return err
	}
	return idx.file.Close()
}
