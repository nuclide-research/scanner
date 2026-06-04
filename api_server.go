package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// APIServer provides REST endpoints for search
type APIServer struct {
	indexer *ESIndexer
	port    int
}

// NewAPIServer creates a new API server
func NewAPIServer(esURL string, port int) *APIServer {
	return &APIServer{
		indexer: NewESIndexer(esURL, 100),
		port:    port,
	}
}

// Start starts the API server
func (srv *APIServer) Start() {
	http.HandleFunc("/search", srv.handleSearch)
	http.HandleFunc("/health", srv.handleHealth)

	addr := fmt.Sprintf(":%d", srv.port)
	log.Printf("API server listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// handleSearch processes search queries
// Query format: /search?port=80&country=US&version=Apache
func (srv *APIServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Parse query parameters
	port := r.URL.Query().Get("port")
	country := r.URL.Query().Get("country")
	version := r.URL.Query().Get("version")
	limit := r.URL.Query().Get("limit")

	if limit == "" {
		limit = "100"
	}

	limitInt, _ := strconv.Atoi(limit)
	if limitInt > 10000 {
		limitInt = 10000
	}

	// Build Elasticsearch bool query
	must := []map[string]interface{}{}

	if port != "" {
		must = append(must, map[string]interface{}{
			"term": map[string]interface{}{"port": port},
		})
	}

	if country != "" {
		must = append(must, map[string]interface{}{
			"term": map[string]interface{}{"country": country},
		})
	}

	if version != "" {
		must = append(must, map[string]interface{}{
			"match": map[string]interface{}{"version": version},
		})
	}

	query := map[string]interface{}{
		"bool": map[string]interface{}{
			"must": must,
		},
	}

	// Execute search
	results := srv.indexer.Search(query)

	// Return results
	response := map[string]interface{}{
		"count":   len(results),
		"results": results,
	}

	json.NewEncoder(w).Encode(response)
}

// handleHealth returns health status
func (srv *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// QueryDSL parses a Shodan-style query string
// Format: "port:80 country:US version:Apache"
func ParseQueryDSL(q string) map[string]interface{} {
	filters := map[string]interface{}{}

	// Split by space
	tokens := strings.Fields(q)

	for _, token := range tokens {
		if strings.Contains(token, ":") {
			parts := strings.SplitN(token, ":", 2)
			key := parts[0]
			value := parts[1]

			// Convert numeric values
			if key == "port" {
				if portInt, err := strconv.Atoi(value); err == nil {
					filters[key] = portInt
				}
			} else {
				filters[key] = value
			}
		}
	}

	return filters
}
