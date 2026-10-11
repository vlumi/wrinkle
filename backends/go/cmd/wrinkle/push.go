package main

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vlumi/wrinkle/backends/go/presence"
	"github.com/vlumi/wrinkle/backends/go/push"
)

// newNotifier sends push notifications through APNs with the configured
// key; the key file may start with ~.
func newNotifier(cfg push.APNsConfig, userDB *sql.DB, pres *presence.Client, log *slog.Logger) (*push.Notifier, error) {
	cfg.KeyFile = expandHome(cfg.KeyFile)
	apns, err := push.NewAPNs(cfg)
	if err != nil {
		return nil, err
	}
	return push.NewNotifier(apns, userDB, pres, log), nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
