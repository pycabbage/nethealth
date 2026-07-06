package main

import (
	"log"
	"net"
	"os"
	"strings"
	"time"
)

var targets = []target{
	{"8.8.8.8", "google", "ipv4", "primary"},
	{"8.8.4.4", "google", "ipv4", "secondary"},
	{"2001:4860:4860::8888", "google", "ipv6", "primary"},
	{"2001:4860:4860::8844", "google", "ipv6", "secondary"},
	{"1.1.1.1", "cloudflare", "ipv4", "primary"},
	{"1.0.0.1", "cloudflare", "ipv4", "secondary"},
	{"2606:4700:4700::1111", "cloudflare", "ipv6", "primary"},
	{"2606:4700:4700::1001", "cloudflare", "ipv6", "secondary"},
}

func serviceName() string {
	if s := os.Getenv("OTEL_SERVICE_NAME"); s != "" {
		return s
	}
	return "nethealth-pinger"
}

// otlpEndpoint returns the Alloy OTLP target as a bare host:port from
// OTEL_EXPORTER_OTLP_ENDPOINT, defaulting to alloy:4317.
func otlpEndpoint() string {
	ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if ep == "" {
		ep = "alloy:4317"
	}
	ep = strings.TrimPrefix(ep, "http://")
	ep = strings.TrimPrefix(ep, "https://")
	return strings.TrimSuffix(ep, "/")
}

// waitForEndpoint blocks until a TCP connection to hostport succeeds.
func waitForEndpoint(hostport string) {
	for {
		conn, err := net.DialTimeout("tcp", hostport, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			log.Printf("otlp endpoint reachable (%s)", hostport)
			return
		}
		time.Sleep(2 * time.Second)
	}
}
