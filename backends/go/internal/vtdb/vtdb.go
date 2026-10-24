// Package vtdb opens connections to a Vitess keyspace through vtgate, and
// holds what the services share for working with it.
package vtdb

import (
	"context"
	"database/sql"
	"errors"
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
	// vtgate takes any user; plain MySQL, on the mysql profile, root.
	cfg.User = "root"
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

// Now is the current time as the database keeps it: UTC, to the
// microsecond, so a value returned to a device equals the one read back.
func Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// MySQL error for a unique key violation.
const duplicateKey = 1062

// IsDuplicate reports whether an insert failed on a unique key.
func IsDuplicate(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == duplicateKey
}

// RowQuerier and Querier are what a pool and a transaction have in common,
// for queries that run in either.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// NullBytes stores empty bytes as NULL.
func NullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// Collect scans every row and closes the rows, so the connection is free
// for the next statement.
func Collect[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var list []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}
