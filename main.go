package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	startIP := flag.String("start", "", "Starting IP (range mode)")
	endIP := flag.String("end", "", "Ending IP (range mode)")
	ipsFile := flag.String("ips-file", "", "File containing IPs to scan, one per line (skips # comments)")
	workers := flag.Int("workers", 100, "Number of concurrent workers")
	outputFile := flag.String("output", "scan_results.jsonl", "Output file for results")
	portsFlag := flag.String("ports", "80,443,22,3306,5432,3389,3567,8000,8080,8081,8123,9000,9042,9200,10000,27017,5984",
		"comma-separated ports to scan (defaults cover web + common data layers)")
	flag.Parse()

	ports := parsePorts(*portsFlag)
	if len(ports) == 0 {
		fmt.Fprintln(os.Stderr, "no valid ports in --ports")
		os.Exit(1)
	}

	// Initialize components
	bloomFilter := NewBloomFilter(256_000_000, 0.02)
	casDedup := NewCASDedup(2_000_000)
	indexer := NewIndexer(*outputFile)
	defer indexer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// IP source: file or range
	var ipChan chan string
	if *ipsFile != "" {
		ipChan = loadIPsFromFile(*ipsFile)
	} else if *startIP != "" && *endIP != "" {
		ipChan = generateIPRange(*startIP, *endIP)
	} else {
		fmt.Fprintln(os.Stderr, "usage: --ips-file <file>  OR  --start <ip> --end <ip>")
		os.Exit(1)
	}

	// Worker pool
	var wg sync.WaitGroup
	var statsLock sync.Mutex
	stats := &ScanStats{}

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(ctx, &wg, i, ipChan, bloomFilter, casDedup, indexer, stats, &statsLock, ports)
	}

	// Print stats every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			statsLock.Lock()
			fmt.Printf("[%s] Scanned: %d | Indexed: %d | Deduped: %d\n",
				time.Now().Format("15:04:05"),
				atomic.LoadUint64(&stats.TotalScanned),
				atomic.LoadUint64(&stats.Indexed),
				atomic.LoadUint64(&stats.Deduped))
			statsLock.Unlock()
		}
	}()

	wg.Wait()
	ticker.Stop()

	statsLock.Lock()
	fmt.Printf("\n=== Final Stats ===\n")
	fmt.Printf("Total scanned: %d\n", atomic.LoadUint64(&stats.TotalScanned))
	fmt.Printf("Indexed: %d\n", atomic.LoadUint64(&stats.Indexed))
	fmt.Printf("Deduped: %d\n", atomic.LoadUint64(&stats.Deduped))
	fmt.Printf("Results saved to: %s\n", *outputFile)
	statsLock.Unlock()
}

type ScanStats struct {
	TotalScanned uint64
	Indexed      uint64
	Deduped      uint64
}

func worker(ctx context.Context, wg *sync.WaitGroup, id int, ipChan chan string, bloom *BloomFilter, dedup *CASDedup, indexer *Indexer, stats *ScanStats, lock *sync.Mutex, ports []int) {
	defer wg.Done()

	timeout := 3 * time.Second

	for ip := range ipChan {
		for _, port := range ports {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Scan the service
			result := scanService(ip, port, timeout)
			atomic.AddUint64(&stats.TotalScanned, 1)

			if result == nil {
				continue
			}

			// Generate dedup key
			key := fmt.Sprintf("%s:%d:%s", ip, port, result.Version)

			// Bloom filter fast path (read-only)
			if bloom.Contains([]byte(key)) {
				// Might be duplicate, check CAS
				if dedup.TryClaim(key) {
					// CAS succeeded, index it
					indexer.Send(result)
					bloom.Add([]byte(key))
					atomic.AddUint64(&stats.Indexed, 1)
				} else {
					// CAS failed, another worker got it
					atomic.AddUint64(&stats.Deduped, 1)
				}
			} else {
				// Definitely new
				if dedup.TryClaim(key) {
					indexer.Send(result)
					bloom.Add([]byte(key))
					atomic.AddUint64(&stats.Indexed, 1)
				}
			}
		}
	}
}

// parsePorts turns a comma-separated port list into ints, trimming spaces and
// skipping empty or non-numeric entries.
func parsePorts(s string) []int {
	var ports []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil {
			ports = append(ports, n)
		}
	}
	return ports
}

func generateIPRange(startIP, endIP string) chan string {
	ch := make(chan string, 1000)
	go func() {
		defer close(ch)
		start := net.ParseIP(startIP).To4()
		end := net.ParseIP(endIP).To4()

		for i := ip2int(start); i <= ip2int(end); i++ {
			ch <- int2ip(i).String()
		}
	}()
	return ch
}

func ip2int(ip net.IP) uint32 {
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func int2ip(i uint32) net.IP {
	return net.IPv4(byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
}

func loadIPsFromFile(path string) chan string {
	ch := make(chan string, 1000)
	go func() {
		defer close(ch)
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot open %s: %v\n", path, err)
			return
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if net.ParseIP(line) != nil {
				ch <- line
			}
		}
	}()
	return ch
}
