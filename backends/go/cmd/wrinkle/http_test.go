package main

import "testing"

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":   true,
		"[::1]:5000":       true,
		"127.0.0.2:80":     true,
		"10.0.0.1:5000":    false,
		"[2001:db8::1]:80": false,
		"garbage":          false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
