#!/bin/bash

# Build the scanner
echo "Building shodan-clone..."
go build -o shodan-clone *.go

if [ $? -ne 0 ]; then
    echo "Build failed"
    exit 1
fi

echo "✓ Build successful"
echo ""

# Example 1: Scan a small test range
echo "=== Example 1: Quick test scan ==="
echo "Scanning 192.168.1.0/24 (256 IPs × 10 ports = 2560 checks)"
echo ""

./shodan-clone \
    --start=192.168.1.0 \
    --end=192.168.1.255 \
    --workers=50 \
    --output=test_results.jsonl

echo ""
echo "Results saved to: test_results.jsonl"
echo "Total lines: $(wc -l test_results.jsonl)"
echo ""

# Example 2: Analyze results
echo "=== Example 2: Analyze results ==="
echo ""
echo "Services found by protocol:"
jq -r '.protocol' test_results.jsonl | sort | uniq -c | sort -rn
echo ""
echo "Services found by port:"
jq -r '.port' test_results.jsonl | sort -n | uniq -c | sort -rn
echo ""
echo "Top 5 servers by version:"
jq -r '.version' test_results.jsonl | sort | uniq -c | sort -rn | head -5
echo ""

# Example 3: Export specific findings
echo "=== Example 3: Find Apache servers on port 80 ==="
jq 'select(.port == 80 and .version | contains("Apache"))' test_results.jsonl | jq -c '{ip, port, version}'
echo ""

# Example 4: Performance benchmarks
echo "=== Example 4: Performance metrics ==="
echo ""

# Count unique IPs scanned
unique_ips=$(jq -r '.ip' test_results.jsonl | sort -u | wc -l)
total_results=$(wc -l < test_results.jsonl)

echo "Total results indexed: $total_results"
echo "Unique IPs: $unique_ips"
echo "Average results per IP: $((total_results / (unique_ips > 0 ? unique_ips : 1)))"
echo ""

# Estimate deduplication effectiveness
echo "Memory efficiency:"
echo "  Bloom filter size: 16 MB"
echo "  CAS table size: 32 MB"
echo "  Total overhead: 48 MB"
echo "  Results indexed: $total_results"
echo "  Bytes per result: $((48 * 1024 * 1024 / (total_results > 0 ? total_results : 1))) bytes"
echo ""

# Cleanup
echo "=== Cleanup ==="
echo "Removing test results..."
rm -f test_results.jsonl

echo "Done!"
