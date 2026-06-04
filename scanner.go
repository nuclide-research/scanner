package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// ScanResult contains the results of scanning a single service
type ScanResult struct {
	IP           string `json:"ip"`
	Port         int    `json:"port"`
	Protocol     string `json:"protocol"`
	Version      string `json:"version"`
	OS           string `json:"os,omitempty"`
	Banner       string `json:"banner,omitempty"`
	TLSInfo      *TLSData `json:"tls,omitempty"`
	Timestamp    int64  `json:"timestamp"`
}

// TLSData contains TLS certificate information
type TLSData struct {
	Issuer      string `json:"issuer,omitempty"`
	Subject     string `json:"subject,omitempty"`
	NotBefore   int64  `json:"not_before,omitempty"`
	NotAfter    int64  `json:"not_after,omitempty"`
	Expired     bool   `json:"expired"`
}

// scanService attempts to connect to a service and grab its banner
func scanService(ip string, port int, timeout time.Duration) *ScanResult {
	addr := fmt.Sprintf("%s:%d", ip, port)

	// Try TLS first (for HTTPS)
	if port == 443 || port == 8443 {
		if result := scanTLS(addr, timeout); result != nil {
			result.IP = ip
			result.Port = port
			result.Timestamp = time.Now().Unix()
			return result
		}
	}

	// Fall back to plain TCP
	result := scanTCP(addr, timeout)
	if result != nil {
		result.IP = ip
		result.Port = port
		result.Timestamp = time.Now().Unix()
	}
	return result
}

// scanTCP connects via TCP and grabs the banner
func scanTCP(addr string, timeout time.Duration) *ScanResult {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()

	// Set read timeout
	conn.SetReadDeadline(time.Now().Add(timeout))

	// Send an HTTP probe on every port that is NOT a known binary-first-speaker.
	// SSH/SMTP/MySQL/Postgres/Mongo/Redis/RDP speak first or need a binary
	// handshake, so an HTTP GET there is pointless; everything else (8123
	// ClickHouse, 9200 Elasticsearch, 8030/8040 StarRocks, 8123, 9000, ...) is an
	// HTTP API that only answers once probed. The old gate hit only 5 ports and
	// so silently missed every HTTP data layer.
	host := strings.Split(addr, ":")[0]
	port := strings.Split(addr, ":")[1]
	if !binaryFirstSpeaker(port) {
		fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: %s\r\n\r\n", host)
	}

	// Read response
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)

	if n == 0 {
		return nil
	}

	banner := string(buf[:n])
	result := parseBanner(banner)
	result.Banner = banner[:min(len(banner), 500)] // Truncate for storage

	return result
}

// scanTLS connects via TLS and extracts certificate info
func scanTLS(addr string, timeout time.Duration) *ScanResult {
	config := &tls.Config{
		InsecureSkipVerify: true,
	}

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr, config)
	if err != nil {
		return nil
	}
	defer conn.Close()

	// Get certificate
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}

	cert := certs[0]
	return &ScanResult{
		Protocol: "HTTPS",
		Version:  cert.Subject.String(),
		TLSInfo: &TLSData{
			Issuer:    cert.Issuer.String(),
			Subject:   cert.Subject.String(),
			NotBefore: cert.NotBefore.Unix(),
			NotAfter:  cert.NotAfter.Unix(),
			Expired:   time.Now().After(cert.NotAfter),
		},
	}
}

// binaryFirstSpeaker reports whether a port runs a protocol that speaks first or
// needs a binary handshake, so an HTTP GET would be useless or harmful.
func binaryFirstSpeaker(port string) bool {
	switch port {
	case "22", "25", "465", "587", "3306", "5432", "27017", "6379", "3389", "9042", "11211":
		return true
	}
	return false
}

// parseBanner extracts version and OS info from banner text
func parseBanner(banner string) *ScanResult {
	result := &ScanResult{
		Protocol: "TCP",
		Version:  "unknown",
		OS:       "unknown",
	}

	lines := strings.Split(banner, "\n")
	if len(lines) == 0 {
		return result
	}

	firstLine := lines[0]

	// HTTP detection
	if strings.Contains(firstLine, "HTTP") {
		result.Protocol = "HTTP"
		for _, line := range lines {
			if strings.HasPrefix(line, "Server:") {
				result.Version = strings.TrimSpace(strings.TrimPrefix(line, "Server:"))
				parseServerString(result)
				return result
			}
		}
	}

	// SSH detection
	if strings.Contains(firstLine, "SSH") {
		result.Protocol = "SSH"
		result.Version = strings.TrimSpace(firstLine)
		if strings.Contains(firstLine, "OpenSSH") {
			result.OS = "Linux/Unix"
		}
		return result
	}

	// SMTP detection
	if strings.HasPrefix(firstLine, "220") && strings.Contains(firstLine, "ESMTP") {
		result.Protocol = "SMTP"
		result.Version = strings.TrimSpace(firstLine)
		return result
	}

	// MySQL detection
	if strings.Contains(banner, "MySQL") {
		result.Protocol = "MySQL"
		result.Version = extractMySQLVersion(banner)
		return result
	}

	// PostgreSQL detection
	if strings.Contains(banner, "PostgreSQL") {
		result.Protocol = "PostgreSQL"
		result.Version = extractPostgresVersion(banner)
		return result
	}

	// Generic fallback
	result.Version = truncate(firstLine, 100)
	return result
}

// parseServerString parses Apache/Nginx/IIS version strings
func parseServerString(result *ScanResult) {
	lower := strings.ToLower(result.Version)

	if strings.Contains(lower, "apache") {
		result.OS = "Apache"
	} else if strings.Contains(lower, "nginx") {
		result.OS = "Nginx"
	} else if strings.Contains(lower, "microsoft") {
		result.OS = "Windows/IIS"
	} else if strings.Contains(lower, "ubuntu") {
		result.OS = "Ubuntu"
	} else if strings.Contains(lower, "centos") {
		result.OS = "CentOS"
	} else if strings.Contains(lower, "debian") {
		result.OS = "Debian"
	}
}

// extractMySQLVersion extracts MySQL version from protocol handshake
func extractMySQLVersion(banner string) string {
	// MySQL sends version in initial handshake
	// Format: version\x00
	if idx := bytes.IndexByte([]byte(banner), 0); idx > 0 {
		return banner[:idx]
	}
	return "MySQL"
}

// extractPostgresVersion extracts PostgreSQL version
func extractPostgresVersion(banner string) string {
	if strings.Contains(banner, "PostgreSQL") {
		parts := strings.Split(banner, " ")
		for i, part := range parts {
			if part == "PostgreSQL" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return "PostgreSQL"
}

// Helper functions
func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}
