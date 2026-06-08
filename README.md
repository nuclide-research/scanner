<h1 align="center">scanner</h1>

<h4 align="center">Fast active-banner stage between passive discovery and deep enumeration.</h4>

<p align="center">
  <a href="https://github.com/nuclide-research/scanner/releases"><img src="https://img.shields.io/github/v/release/nuclide-research/scanner?style=flat-square" alt="release"></a>
  <a href="https://github.com/nuclide-research/scanner/blob/main/LICENSE"><img src="https://img.shields.io/github/license/nuclide-research/scanner?style=flat-square" alt="license"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.21%2B-00ADD8?style=flat-square&logo=go" alt="go"></a>
  <a href="https://nuclide-research.com"><img src="https://img.shields.io/badge/by-NuClide-blue?style=flat-square" alt="NuClide"></a>
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#installation">Installation</a> •
  <a href="#usage">Usage</a> •
  <a href="#field-results">Field Results</a> •
  <a href="#output">Output</a> •
  <a href="#how-it-works">How It Works</a> •
  <a href="#scope">Scope</a>
</p>

---

scanner takes a list of IPs from a passive discovery engine (Shodan, Censys, FOFA) and does a full TCP plus TLS handshake on each one. It reads the banner, parses the version, deduplicates without locks, and writes JSONL. The output goes to a deep-enumeration tool such as [aimap](https://github.com/nuclide-research/aimap) or to Elasticsearch.

Three load-bearing jobs in one pass:

1. **Liveness.** About 71% of Shodan-cached IPs are dead, stale, or moved. The expensive deep-enum stage only needs the live ones.
2. **Fresh version.** The banner read returns the version string from right now, not whatever Shodan saw last quarter. That is what CVE scoping needs.
3. **Dork false-positive strip.** A Shodan dork over an HTTP product matches nginx and Cloudflare pages too. The banner stage drops them.

masscan does millions of SYN per second and tells you "port open." scanner does the opposite. It does the full handshake, reads the banner, and parses the version. The output is `Qdrant 1.17.0, here is the JSON`, not `port 6333 open`.

# Features

- Single Go binary, zero dependencies, Linux amd64 and arm64
- Lock-free deduplication: CAS atomic plus 16 MB Bloom filter, no mutex contention at 1000+ workers
- About 176 full-handshake probes per second from a single congestion-controlled VPN box
- Accepts `--ips-file` (Shodan or Censys export) or `--start` / `--end` (contiguous range)
- 28-port default sweep covering AI-infra service classes plus optional Elasticsearch and REST API
- JSONL output: `ip, port, protocol, version, banner, tls{issuer, subject, not_before, not_after, expired}, timestamp`
- TLS handshake recorded per host (issuer, subject, expiry, expired flag)
- Shadow-port discovery: surfaces services running off the dork (Attu, MinIO, etcd, Prometheus, Docker registry on Qdrant hosts)

# Installation

```bash
go install -v github.com/nuclide-research/scanner@latest
```

Or build from source:

```bash
git clone https://github.com/nuclide-research/scanner
cd scanner
go build -o scanner .
```

Requires Go 1.21 or later.

# Usage

```console
# scan a passive-discovery IP list
scanner --ips-file targets.txt --workers 100 --output results.jsonl

# scan a contiguous range
scanner --start 192.168.1.0 --end 192.168.1.255 --workers 100 --output results.jsonl

# stream into Elasticsearch
scanner --ips-file targets.txt --es-url http://localhost:9200
```

`targets.txt` is one IP per line. `#` comments are skipped:

```
# Qdrant hosts, Shodan 2026-06-05
8.130.154.222
159.223.251.251
47.95.66.171
```

<details>
  <summary>Tuning</summary>

```go
workers := 1000              // more workers = faster, higher CPU
bloomSize := 256_000_000     // 256M bits, 16 MB filter
falsePositiveRate := 0.02    // 2% false positives
casTableSize := 2_000_000    // 2M dedup slots, 32 MB
timeout := 3 * time.Second   // per-IP connection timeout
```

</details>

# Field results

Run as Step 0c of the NuClide assessment chain on a 3,362-host Cat-02 vector-database survey (2026-06-05):

| Metric | Result |
|--------|--------|
| IPs scanned (from Shodan) | 3,362 |
| Wall-clock | ~9 minutes (533s) |
| Probes | 94,136 full TCP+TLS handshakes (28 ports per host) |
| Banners captured | 2,580 |
| **Live-host rate** | **29%**. 71% of Shodan's cached IPs were dead or stale |
| Dork false-positives stripped | 122 (nginx and Cloudflare matched the dork, were not the service) |
| Shadow-port exposures surfaced | ~550 (Attu, MinIO, etcd, Prometheus, Docker registry) |
| Versions captured | Live, per-host (Qdrant 1.13.4 through 1.17.1 for CVE scoping) |

Full writeup and raw evidence: [`results/poc-cat02-vectordb-2026-06-05.md`](results/poc-cat02-vectordb-2026-06-05.md).

That throughput is why it belongs *before* a heavy deep-enumeration tool. Cheap enough to run on the entire raw passive-discovery list, so the expensive stage only ever touches the ~29% that are actually live, already false-positive-stripped, with versions and shadow ports identified.

# Output

JSONL, one record per line:

```json
{
  "ip": "192.168.1.100",
  "port": 6333,
  "protocol": "HTTP",
  "version": "Qdrant 1.17.0",
  "banner": "HTTP/1.1 200 OK\r\nServer: Qdrant 1.17.0...",
  "tls": {
    "issuer": "CN=example.com",
    "subject": "CN=example.com",
    "not_before": 1700000000,
    "not_after": 1800000000,
    "expired": false
  },
  "timestamp": 1733600000
}
```

# REST API

`scanner` ships an optional REST server with Shodan-style query syntax:

```bash
curl "http://localhost:8080/search?port=80&version=Apache"
curl "http://localhost:8080/search?port=3306&country=US"
curl "http://localhost:8080/search?limit=1000" | jq .
```

# How it works

```
main()
  |
  +- generateIPRange() / loadIPsFromFile() -> chan string (buf 1000)
  |                                                  |
  |    fan-out to N workers                          |
  |    v                                             v
  |  w-0  w-1  w-2  ...  w-99  (goroutines)
  |    |
  |    |  for each IP across 28 ports:
  |    +-> net.DialTimeout()        TCP connect (3s timeout)
  |    +-> banner read + parseBanner()
  |    |  dedup:
  |    +-> BloomFilter.Contains()   read-only, no lock
  |    +-> CASDedup.TryClaim()      one atomic CAS
  |    +-> Indexer.Send()           buffered write
  |
  +- stats ticker (every 5s)
```

**Goroutine fan-out.** Every worker blocks independently on `net.DialTimeout`. Blocking in Go parks the goroutine and yields the OS thread. 1000 workers blocked on network I/O consume essentially zero CPU while waiting.

**Lock-free dedup.** A mutex approach has every worker queue to write "I have seen this key." At 1000 workers, 99% of time is spent waiting on the lock. Two structures avoid the mutex:

- `BloomFilter.Contains()`: 3 array reads, returns true or false. No write, no lock, unlimited concurrent readers.
- `CASDedup.TryClaim()`: one atomic compare-and-swap. Winner gets the slot. Loser returns false immediately. CPU handles contention in hardware, no scheduler involvement.

**Real throughput against the internet.** Closed ports respond in under 5ms (RST). Filtered ports burn the full 3s timeout. 100 workers at ~200ms average per probe yields ~500 effective checks per second. The scanner is fast against hosts that respond. Against filtered hosts where every connection burns the full timeout, throughput collapses. That is why passive discovery (Shodan or Censys) builds the target list first.

# Components

- `main.go`: orchestrator, worker pool, IP range generation
- `scanner.go`: TCP and TLS banner grabbing, version detection
- `bloom.go`: Bloom filter with optimal sizing
- `cas_dedup.go`: lock-free deduplication, atomic CAS
- `indexer.go`: buffered JSONL writer
- `es_indexer.go`: Elasticsearch bulk indexer
- `api_server.go`: REST API

# Scope

scanner makes real TCP connections and reads banners. It does not authenticate, POST data, execute exploits, or modify anything on the target. Scanning networks without authorization is illegal in most jurisdictions. Only scan systems you own or have explicit written authorization to test.

# Our other projects

- [aimap](https://github.com/nuclide-research/aimap) — AI/ML infrastructure fingerprint scanner, the deep-enum stage after this one
- [tiptoe](https://github.com/nuclide-research/tiptoe) — quiet, congestion-controlled counterpart for sensitive targets
- [VisorLog](https://github.com/nuclide-research/visorlog) — finding ledger and ingest pipeline
- [VisorCAS](https://github.com/nuclide-research/VisorCAS) — content-addressed false-positive ledger
- [BARE](https://github.com/nuclide-research/BARE) — semantic exploit-module ranking over scanner findings

# License

MIT. Part of the NuClide toolchain. Contact: [nuclide-research.com](https://nuclide-research.com)
