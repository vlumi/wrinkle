package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// loadDotEnv sets variables from the repository's .env, which holds the
// local, uncommitted settings (the Apple team and APNs key). Variables
// already in the environment win. It looks in the working directory, then
// beside the binary's bin/ directory, so the run scripts find it from
// anywhere.
func loadDotEnv() string {
	candidates := []string{".env"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", ".env"))
	}
	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		setUnsetFrom(f)
		f.Close()
		return path
	}
	return ""
}

// setUnsetFrom sets the variables of KEY=value lines, skipping comments and
// variables that are already set.
func setUnsetFrom(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); !set {
			_ = os.Setenv(key, value)
		}
	}
}

// env returns the variable's value, or the fallback when it is unset or
// empty.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
