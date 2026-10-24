package db

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// localHosts are the database hosts that mean "on this machine": the loopback
// names, plus the "db" service of the docker compose file. A unix socket
// (a path) counts too.
var localHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
	"db":        true,
}

func isLocalHost(h string) bool {
	return localHosts[strings.ToLower(h)] || strings.HasPrefix(h, "/")
}

// Describe names the database a DSN points at, without credentials, for a
// "you are about to touch X" line.
func Describe(dsn string) (string, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("invalid postgres DSN: %w", err)
	}
	return fmt.Sprintf("%s on %s:%d", cfg.ConnConfig.Database, cfg.ConnConfig.Host, cfg.ConnConfig.Port), nil
}

// IsLocal reports whether every host in the DSN (the first and any fallbacks)
// is on this machine.
func IsLocal(dsn string) (bool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return false, fmt.Errorf("invalid postgres DSN: %w", err)
	}
	if !isLocalHost(cfg.ConnConfig.Host) {
		return false, nil
	}
	for _, fb := range cfg.ConnConfig.Fallbacks {
		if !isLocalHost(fb.Host) {
			return false, nil
		}
	}
	return true, nil
}

// RefuseRemote stops a destructive command from running against a database
// that is not on this machine, unless the caller said so explicitly (allow).
// A DB_DSN left in the shell from deploying would otherwise turn `make fresh`
// into wiping the live database.
func RefuseRemote(dsn string, allow bool, what string) error {
	local, err := IsLocal(dsn)
	if err != nil {
		return err
	}
	if local || allow {
		return nil
	}
	where, _ := Describe(dsn)
	return fmt.Errorf("refusing to %s: %s is not on this machine (pass -allow-remote only if you mean it)", what, where)
}
