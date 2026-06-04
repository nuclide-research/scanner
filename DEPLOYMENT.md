# Production Deployment Guide

## Infrastructure Setup

### Compute Requirements

For scanning a /16 (65,536 IPs × 10 ports) in ~30 minutes:

```
Cores:    8-16
RAM:      4-8 GB
Disk:     1 TB (for 500k results at ~2KB each)
Network:  10 Mbps uplink
```

### Docker Deployment

```dockerfile
FROM golang:1.21-alpine as builder
WORKDIR /app
COPY *.go go.* ./
RUN CGO_ENABLED=0 GOARCH=amd64 go build -o shodan-clone .

FROM alpine:3.18
COPY --from=builder /app/shodan-clone /usr/local/bin/
ENTRYPOINT ["shodan-clone"]
```

Build and run:
```bash
docker build -t shodan-clone .
docker run -v /output:/data shodan-clone \
  --start=10.0.0.0 \
  --end=10.0.255.255 \
  --workers=500 \
  --output=/data/results.jsonl
```

### Kubernetes Job

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: shodan-scan-10-0
spec:
  parallelism: 1
  template:
    spec:
      containers:
      - name: scanner
        image: shodan-clone:latest
        args:
        - --start=10.0.0.0
        - --end=10.0.255.255
        - --workers=500
        - --output=/data/results.jsonl
        resources:
          requests:
            cpu: "8"
            memory: "8Gi"
          limits:
            cpu: "16"
            memory: "16Gi"
        volumeMounts:
        - name: results
          mountPath: /data
      volumes:
      - name: results
        persistentVolumeClaim:
          claimName: scan-results
      restartPolicy: Never
```

## Elasticsearch Setup

### Single-node (testing)
```bash
docker run -d \
  -e discovery.type=single-node \
  -e xpack.security.enabled=false \
  -p 9200:9200 \
  docker.elastic.co/elasticsearch/elasticsearch:8.0.0
```

### Production cluster (3 nodes)
```yaml
apiVersion: elasticsearch.k8s.elastic.co/v1
kind: Elasticsearch
metadata:
  name: shodan-cluster
spec:
  version: 8.0.0
  nodeSets:
  - name: default
    count: 3
    config:
      node.roles: ["master", "data"]
      indices.memory.index_buffer_size: "40%"
    volumeClaimTemplates:
    - metadata:
        name: elasticsearch-data
      spec:
        accessModes:
        - ReadWriteOnce
        resources:
          requests:
            storage: 500Gi
```

### Index settings
```bash
PUT /scan-2024
{
  "settings": {
    "number_of_shards": 20,
    "number_of_replicas": 2,
    "index.codec": "best_compression"
  },
  "mappings": {
    "properties": {
      "ip": { "type": "ip" },
      "port": { "type": "integer" },
      "protocol": { "type": "keyword" },
      "version": { "type": "text", "analyzer": "standard" },
      "os": { "type": "keyword" },
      "timestamp": { "type": "date" }
    }
  }
}
```

## Monitoring & Observability

### Prometheus metrics (add to scanner.go)
```go
import "github.com/prometheus/client_golang/prometheus"

var (
    scanCounter = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "scans_total"},
        []string{"port", "protocol"},
    )
    dedupCounter = prometheus.NewCounter(
        prometheus.CounterOpts{Name: "dedup_hits_total"},
    )
    indexLatency = prometheus.NewHistogram(
        prometheus.HistogramOpts{Name: "index_latency_seconds"},
    )
)
```

### Grafana dashboard
```json
{
  "panels": [
    {
      "title": "Scans/sec",
      "targets": [{"expr": "rate(scans_total[1m])"}]
    },
    {
      "title": "Dedup hit rate",
      "targets": [{"expr": "rate(dedup_hits_total[1m]) / rate(scans_total[1m])"}]
    },
    {
      "title": "Indexing latency (p99)",
      "targets": [{"expr": "histogram_quantile(0.99, index_latency_seconds)"}]
    }
  ]
}
```

## Scaling to /8 (4.3B IPs)

### Strategy: Geographic partitioning

```
Regions:
├─ North America (8.0.0.0 - 8.127.255.255)
│   └─ 8 machines × 500 workers = 4000 concurrent
├─ Europe (8.128.0.0 - 8.191.255.255)
│   └─ 8 machines × 500 workers = 4000 concurrent
├─ Asia (8.192.0.0 - 8.255.255.255)
│   └─ 8 machines × 500 workers = 4000 concurrent
└─ [Repeat for other RIRs]

Total: 32 machines, 16,000 workers
Time: 4.3B IPs ÷ (16,000 concurrent × 10 ports) ÷ (10k scans/sec) ≈ 5-7 days
```

### Cost estimation

**AWS EC2 c5.2xlarge (8vCPU, 16GB RAM):**
- Hourly: $0.34
- 7 days @ 24/7: $56.6 per machine
- 32 machines: ~$1,811 total
- Storage (EBS 1TB): ~$100/month

**Result: ~$2,000 to scan entire IPv4 space**

## Maintenance

### Log rotation
```bash
# Logrotate config
/var/log/shodan-clone.log {
    daily
    rotate 7
    compress
    delaycompress
    notifempty
    create 0640 shodan shodan
}
```

### Backup Elasticsearch data
```bash
# Daily snapshot
curl -X PUT "localhost:9200/_snapshot/backup/scan-2024-01-01?wait_for_completion=true" \
  -H 'Content-Type: application/json' \
  -d'{
    "type": "fs",
    "settings": {
      "location": "/backup/elasticsearch",
      "compress": true
    }
  }'
```

### Incremental scanning
```bash
# Only rescan if last seen > 30 days ago
./shodan-clone \
  --start=10.0.0.0 \
  --end=10.255.255.255 \
  --workers=1000 \
  --last-seen-before=30d \  # Add this flag
  --output=/data/incremental.jsonl
```

## Troubleshooting

### High memory usage
- Reduce workers: `--workers=50` instead of 500
- Reduce Bloom filter size (trade accuracy for memory)
- Monitor with `top -p $(pidof shodan-clone)`

### Slow indexing
- Increase batch size in es_indexer.go (default 100)
- Add more Elasticsearch data nodes
- Use `index.refresh_interval: 30s` to batch slower

### Network timeout
- Increase `timeout` in scanner.go (default 3s)
- Check firewall rules: ensure outbound to ports 80, 443, 22, etc.
- Use `tcpdump` to verify packets are leaving

### High dedup rate (>20%)
- Your IP range has overlap with previous scan
- Consider full re-index with fresh Bloom filter
- Increase `bloom.size` for longer-term dedup across days

## Security Hardening

1. **Network isolation**: Scanner should not access internal services
2. **Rate limiting**: OS-level rate limits to avoid ISP throttling
   ```bash
   tc qdisc add dev eth0 root tbf rate 100mbit burst 1m latency 50ms
   ```

3. **Authentication**: Basic auth on API server
   ```go
   if r.Header.Get("Authorization") != "Bearer "+apiKey {
       w.WriteHeader(http.StatusForbidden)
       return
   }
   ```

4. **Logging**: All scans logged for audit
   ```bash
   ./shodan-clone ... 2>&1 | tee scan-$(date +%s).log
   ```
