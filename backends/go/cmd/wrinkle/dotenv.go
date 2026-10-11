package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
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
		sc := bufio.NewScanner(f)
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
		f.Close()
		return path
	}
	return ""
}

// expandHome turns a leading ~ into the home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

var keyIDInName = regexp.MustCompile(`AuthKey_([A-Z0-9]{10})\.p8$`)

// keyIDFromFile reads the key id from the name Apple gives the download,
// AuthKey_<id>.p8.
func keyIDFromFile(path string) string {
	if m := keyIDInName.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}
