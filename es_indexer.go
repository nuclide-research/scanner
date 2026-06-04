package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ESIndexer indexes scan results into Elasticsearch
type ESIndexer struct {
	esURL      string
	httpClient *http.Client
	batchChan  chan *ScanResult
	batchSize  int
	lock       sync.Mutex
	batch      []*ScanResult
}

// NewESIndexer creates a new Elasticsearch indexer
func NewESIndexer(esURL string, batchSize int) *ESIndexer {
	idx := &ESIndexer{
		esURL:      esURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		batchChan:  make(chan *ScanResult, 1000),
		batchSize:  batchSize,
		batch:      make([]*ScanResult, 0, batchSize),
	}

	// Start batch processor
	go idx.processBatches()

	return idx
}

// Send queues a result for indexing
func (idx *ESIndexer) Send(result *ScanResult) {
	select {
	case idx.batchChan <- result:
	case <-time.After(100 * time.Millisecond):
		// Channel full, force batch flush
		idx.flush()
		idx.batchChan <- result
	}
}

// processBatches processes results in batches
func (idx *ESIndexer) processBatches() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case result := <-idx.batchChan:
			idx.lock.Lock()
			idx.batch = append(idx.batch, result)
			shouldFlush := len(idx.batch) >= idx.batchSize
			idx.lock.Unlock()

			if shouldFlush {
				idx.flush()
			}

		case <-ticker.C:
			idx.flush()
		}
	}
}

// flush sends a batch to Elasticsearch
func (idx *ESIndexer) flush() {
	idx.lock.Lock()
	if len(idx.batch) == 0 {
		idx.lock.Unlock()
		return
	}

	batch := idx.batch
	idx.batch = make([]*ScanResult, 0, idx.batchSize)
	idx.lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build bulk request
	var buf bytes.Buffer
	for _, result := range batch {
		// Elasticsearch bulk format: action metadata, document
		meta := map[string]interface{}{
			"index": map[string]string{
				"_index": fmt.Sprintf("scan-%d", time.Now().Year()),
				"_id":    fmt.Sprintf("%s:%d:%d", result.IP, result.Port, result.Timestamp),
			},
		}
		metaJSON, _ := json.Marshal(meta)
		buf.Write(metaJSON)
		buf.WriteByte('\n')

		docJSON, _ := json.Marshal(result)
		buf.Write(docJSON)
		buf.WriteByte('\n')
	}

	// Send bulk request
	req, _ := http.NewRequestWithContext(ctx, "POST", idx.esURL+"/_bulk", &buf)
	req.Header.Set("Content-Type", "application/json")

	resp, err := idx.httpClient.Do(req)
	if err != nil {
		fmt.Printf("ES bulk error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("ES bulk failed (%d): %s\n", resp.StatusCode, body)
	}
}

// Search queries Elasticsearch
func (idx *ESIndexer) Search(query map[string]interface{}) []map[string]interface{} {
	queryJSON, _ := json.Marshal(map[string]interface{}{"query": query})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", idx.esURL+"/_search", bytes.NewReader(queryJSON))
	req.Header.Set("Content-Type", "application/json")

	resp, err := idx.httpClient.Do(req)
	if err != nil {
		fmt.Printf("ES search error: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	// Extract hits
	if hits, ok := result["hits"].(map[string]interface{}); ok {
		if hitsArray, ok := hits["hits"].([]interface{}); ok {
			results := make([]map[string]interface{}, 0, len(hitsArray))
			for _, hit := range hitsArray {
				if hitMap, ok := hit.(map[string]interface{}); ok {
					if source, ok := hitMap["_source"].(map[string]interface{}); ok {
						results = append(results, source)
					}
				}
			}
			return results
		}
	}

	return nil
}

// Close closes the indexer
func (idx *ESIndexer) Close() error {
	close(idx.batchChan)
	// Wait for remaining batches to be processed
	time.Sleep(1 * time.Second)
	idx.flush()
	return nil
}
