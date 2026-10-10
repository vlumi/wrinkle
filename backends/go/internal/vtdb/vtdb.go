// Package vtdb opens connections to a Vitess keyspace through vtgate.
package vtdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Open returns a pool for one keyspace. Parameters are interpolated on the
// client, which keeps vtgate from having to prepare statements.
func Open(ctx context.Context, addr, keyspace string, maxConns int) (*sql.DB, error) {
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = addr
	cfg.DBName = keyspace
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.InterpolateParams = true
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("keyspace %s at %s: %w", keyspace, addr, err)
	}
	return db, nil
}
