package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// healthcheck is "discern healthcheck", the image's HEALTHCHECK command. The
// image has no curl, so the binary checks itself: it asks the running
// server's /healthz and exits 0 if the answer is 200.
func healthcheck() int {
	url, err := healthURL(os.Getenv("DISCERN_LISTEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: HTTP", resp.StatusCode)
		return 1
	}
	return 0
}

// healthURL turns the listen address into a URL on this machine: ":8790" and
// "0.0.0.0:8790" listen everywhere, so ask 127.0.0.1.
func healthURL(listen string) (string, error) {
	listen = strings.TrimSpace(listen) // as internal/config reads it
	if listen == "" {
		listen = ":8790"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("DISCERN_LISTEN %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
