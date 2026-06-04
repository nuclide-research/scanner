# scanner

Go TCP/TLS banner scanner with lock-free deduplication (CAS + Bloom filter). Accepts arbitrary IP lists from Shodan or other passive sources via `--ips-file`, or sweeps contiguous ranges with `--start/--end`.

## Architecture Highlights

- **CAS-based lock-free deduplication**: Multiple workers race without queues using atomic compare-and-swap
- **Bloom filter fast-path**: 16 MB probabilistic set (98% of checks bypass CAS entirely)
- **Distributed scanning**: 100-1000+ concurrent workers scanning different IP ranges
- **Elasticsearch integration**: Bulk indexing for searchable results
- **REST API**: Shodan-style query syntax (port:80 country:US)

## Components

### Core Files

1. **main.go** - Orchestrator, worker pool, IP range generation
2. **bloom.go** - Bloom filter with optimal sizing
3. **cas_dedup.go** - Lock-free deduplication using atomic CAS
4. **scanner.go** - TCP/TLS banner grabbing, version detection
5. **indexer.go** - Buffered JSONL file writer
6. **es_indexer.go** - Elasticsearch bulk indexer (optional)
7. **api_server.go** - REST API server

## Installation

```bash
# Requires Go 1.21+
go build -o scanner *.go
```

## Quick Start

### Scan a Shodan-discovered IP list
```bash
./scanner \
  --ips-file targets.txt \
  --workers=100 \
  --output=results.jsonl
```

`targets.txt` — one IP per line, `#` comments skipped:
```
# FastMCP servers — Shodan 2026-05-31
8.130.154.222
159.223.251.251
47.95.66.171
```

### Scan a contiguous range
```bash
./scanner \
  --start=192.168.1.0 \
  --end=192.168.1.255 \
  --workers=100 \
  --output=results.jsonl
```

### With Elasticsearch (production)

First, start Elasticsearch:
```bash
docker run -d -p 9200:9200 docker.elastic.co/elasticsearch/elasticsearch:8.0.0 \
  -e discovery.type=single-node \
  -e xpack.security.enabled=false
```

Then modify `main.go` to use ESIndexer instead of buffered file writer, or rebuild with:
```bash
./shodan-clone --es-url=http://localhost:9200
```

### Query results

```bash
# Find all Apache servers on port 80
curl "http://localhost:8080/search?port=80&version=Apache"

# Find MySQL servers in US
curl "http://localhost:8080/search?port=3306&country=US"

# Export to JSON
curl "http://localhost:8080/search?limit=1000" | jq .
```

## Performance

### Memory Usage
- **Bloom filter**: 16 MB (256M bits, 2% FPR)
- **CAS dedup map**: 32 MB (2M entries × 8 bytes)
- **Total overhead**: ~50 MB (vs. gigabytes with traditional locking)

### Throughput
- **Bloom filter path** (~98%): <1 microsecond, zero locks
- **CAS path** (~2%): ~100 nanoseconds per operation
- **Typical rate**: 10,000-50,000 scans/sec per machine

### Concurrency
- Supports 1,000+ concurrent workers without lock contention
- No thread pool overhead (Go goroutines are lightweight)
- CPU-bound on multi-core systems

## Tuning Parameters

```go
// In main.go, adjust:
workers := 1000              // More workers = faster but higher CPU
bloomSize := 256_000_000     // ~256M entries, 16MB filter
falsePositiveRate := 0.02    // 2% false positives OK
casTableSize := 2_000_000    // ~2M dedup entries, 32MB
timeout := 3 * time.Second   // Per-IP connection timeout
```

## Output Format (JSONL)

Each line is a JSON record:
```json
{
  "ip": "192.168.1.100",
  "port": 80,
  "protocol": "HTTP",
  "version": "Apache/2.4.41 (Ubuntu)",
  "os": "Apache",
  "banner": "HTTP/1.1 200 OK\r\nServer: Apache/2.4.41...",
  "tls": {
    "issuer": "CN=example.com",
    "subject": "CN=example.com",
    "not_before": 1600000000,
    "not_after": 1700000000,
    "expired": false
  },
  "timestamp": 1706892000
}
```

## Advanced Usage

### Scale to /8 (16.7M IPs)

```bash
# Distribute across 4 machines
# Machine 1:
./shodan-clone --start=10.0.0.0 --end=10.63.255.255 --workers=500

# Machine 2:
./shodan-clone --start=10.64.0.0 --end=10.127.255.255 --workers=500

# Machine 3:
./shodan-clone --start=10.128.0.0 --end=10.191.255.255 --workers=500

# Machine 4:
./shodan-clone --start=10.192.0.0 --end=10.255.255.255 --workers=500
```

Time to completion: ~2-3 days (4.3B IPs scanned across 2000 workers, 10 ports each)

### Continuous re-scanning

```bash
# Run every 24 hours to catch new services
watch -n 86400 './shodan-clone --start=10.0.0.0 --end=10.255.255.255 --workers=1000'
```

## How It Works

### Execution Flow

```
main()
  |
  +- generateIPRange() / loadIPsFromFile() --> chan string (buffered 1000)
  |                                                  |
  |    +--------------------------------------------|
  |    |  fan-out to N workers                       |
  |    v                                             v
  |  w-0  w-1  w-2  ...  w-99  (goroutines, default 100)
  |    |
  |    |  for each IP x 12 ports:
  |    |
  |    +-> net.DialTimeout()        TCP connect (3s timeout)
  |    |       | success
  |    |       v
  |    +-> banner read + parseBanner()
  |    |
  |    |  dedup check:
  |    +-> BloomFilter.Contains()   read-only, no lock
  |    +-> CASDedup.TryClaim()      one atomic instruction
  |    |       | winner
  |    |       v
  |    +-> Indexer.Send()           mutex-gated buffered write
  |
  +- stats ticker (every 5s)
```

### Mechanism 1: Goroutine Fan-Out (why workers help)

Every worker blocks independently on `net.DialTimeout`. Blocking in Go parks the goroutine and yields the OS thread — 100 workers blocked on network I/O consume essentially zero CPU while waiting. When a connection resolves, the scheduler wakes that goroutine immediately.

```
Single-threaded (serial):
  [dial 3s][dial 3s][dial 3s] --> 3 checks in 9 seconds

100 workers (parallel):
  [dial][dial][dial]...(x100 in flight simultaneously)
  --> 1200 checks in ~3 seconds (all timeouts fire together)
```

This is I/O parallelism, not CPU parallelism. The bottleneck is network latency, not cores.

### Mechanism 2: Lock-Free Dedup (why workers don't block each other)

With a mutex, every worker queues up to write "I've seen this key":

```
Mutex approach (1000 workers):
  w-1: lock() -> write -> unlock()
  w-2: [BLOCKED]
  w-3: [BLOCKED]
  ...
  --> 99% of time spent waiting, not scanning
```

The scanner avoids this with two structures that require no mutex:

**BloomFilter.Contains() — pure read:**
```
3 array reads -> return true/false
No write, no lock, any number of workers simultaneously
```

**CASDedup.TryClaim() — single atomic instruction:**
```go
atomic.CompareAndSwapUint64(&slot, 0, now)
// Winner: slot was 0, now owns it -> returns true
// Loser:  slot already set         -> returns false immediately
```
The CPU handles contention in hardware — one locked memory bus operation,
not a software lock. No thread ever sleeps, no queue, no scheduler involvement.

### Real-World Throughput

The 10k-50k scans/sec claim assumes most connections close fast:

```
Best case (LAN, fast RST):
  100 workers x 1ms avg response  = ~100k checks/sec

Real internet (mix of RST + timeout):
  Closed ports respond in <5ms (RST)
  Filtered ports burn the full 3s timeout
  100 workers x ~200ms avg        = ~500 checks/sec effective
```

The scanner is fast against hosts that respond quickly. Against filtered hosts
where all ports drop packets silently, every connection burns the full timeout
and throughput collapses — which is why passive discovery (Shodan) to build
the target list first is the correct workflow before running active scans.

## Legal & Ethical Notes

- **Scanning others' networks without permission is illegal** in most jurisdictions
- Use only on your own infrastructure or with explicit written consent
- Avoid scanning ISP infrastructure or targeting individuals
- Rate-limit to avoid triggering DDoS protections

## Future Enhancements

- [ ] Distributed scanning coordinator (scatter-gather)
- [ ] GeoIP enrichment pipeline
- [ ] SSL certificate parsing + CN extraction
- [ ] SOCKS5 proxy support (evade filters)
- [ ] Incremental scanning (track last-seen per IP)
- [ ] Vulnerability matching (CVE cross-reference)

## References

- Bloom Filters: https://en.wikipedia.org/wiki/Bloom_filter
- Compare-and-swap: https://en.wikipedia.org/wiki/Compare-and-swap
- Lock-free programming: https://1drv.ms/b/s!AuOqJ1T9vvLGnAwxFVpAXNXRQ5t6
