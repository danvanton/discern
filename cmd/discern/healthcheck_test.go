package main

import "testing"

func TestHealthURL(t *testing.T) {
	for listen, want := range map[string]string{
		"":                "http://127.0.0.1:8790/healthz",
		":8790":           "http://127.0.0.1:8790/healthz",
		" :8790 ":         "http://127.0.0.1:8790/healthz", // config trims it too
		"0.0.0.0:9000":    "http://127.0.0.1:9000/healthz",
		"[::]:9000":       "http://127.0.0.1:9000/healthz",
		"127.0.0.1:18790": "http://127.0.0.1:18790/healthz",
		"[::1]:8790":      "http://[::1]:8790/healthz",
	} {
		got, err := healthURL(listen)
		if err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", listen, got, err, want)
		}
	}
	if _, err := healthURL("no-port"); err == nil {
		t.Error("healthURL accepted an address without a port")
	}
}
