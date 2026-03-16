// Package database provides a PostgreSQL connection pool abstraction used by
// components that need direct *sql.DB access rather than the full Storage
// interface.
//
// This file (pool.go) wraps the standard library's database/sql.DB with a
// named struct and a typed configuration, making pool settings explicit and
// self-documenting rather than scattered across main.go.
//
// Why a connection pool at all?
//   Opening a new TCP connection + TLS handshake + PostgreSQL authentication
//   round-trip takes ~5–20 ms. For an API that handles hundreds of requests
//   per second, creating a fresh connection on every request would be the
//   dominant cost. A pool maintains a set of pre-authenticated connections
//   that goroutines borrow and return, cutting connection overhead to ~0.
//
// It connects to:
//   - PostgreSQL via the lib/pq driver (blank import below)
//
// Called by:
//   - cmd/api/main.go — uses DefaultPoolConfig() then NewConnectionPool()
//   - internal/storage/storage.go — configures pool settings on the sqlx.DB
//     it manages internally (uses the same values but via sqlx.Connect).
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// lib/pq registers itself as the "postgres" driver with database/sql.
	// The blank import is required; nothing in this file calls pq directly.
	_ "github.com/lib/pq"
)

// ConnectionPool wraps *sql.DB to provide a named, closeable pool with helpers.
//
// Fields explained:
//
//	db — the standard library connection pool. database/sql manages a set of
//	     *sql.Conn internally; goroutines call db.QueryContext() etc. and the
//	     pool transparently assigns them an available connection.
type ConnectionPool struct {
	db *sql.DB
}

// PoolConfig holds all tunable parameters for the connection pool.
// Externalising these into a struct (rather than passing four arguments)
// makes it easy to load config from environment variables in main.go and
// pass a single value to NewConnectionPool.
//
// Fields explained:
//
//	MaxOpenConns    — the hard ceiling on total connections (both idle and in-use).
//	                  If all MaxOpenConns connections are busy, the next call blocks
//	                  until one is returned. Setting this too high (> postgres max_connections)
//	                  causes connection-refused errors on the DB server. Setting it
//	                  too low causes request queuing on the Go side.
//	                  Rule of thumb: start at 25 and tune under load.
//
//	MaxIdleConns    — how many connections are kept open but not in use.
//	                  Idle connections are "warm" — they avoid the TCP/auth overhead
//	                  of a fresh connection when the next request arrives.
//	                  Setting this too low means busy-then-quiet bursts cause
//	                  repeated connection teardown/setup. Setting it too high
//	                  wastes memory and PostgreSQL backend processes.
//	                  Rule of thumb: 20–40% of MaxOpenConns (e.g. 5 out of 25).
//
//	ConnMaxLifetime — a connection is closed and replaced after this duration,
//	                  regardless of activity. Without a lifetime cap, a connection
//	                  can live for days and accumulate per-connection server state
//	                  (temp tables, session settings) or drift into a bad state
//	                  after a firewall mid-session TCP reset. 5 minutes forces
//	                  periodic reconnection, keeping connections clean.
//
//	ConnMaxIdleTime — an idle connection that has not been used for this long is
//	                  closed. Unlike ConnMaxLifetime (which affects all connections),
//	                  this only recycles connections that nobody is borrowing.
//	                  Useful for scaling down gracefully after traffic spikes.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPoolConfig returns a conservative, production-ready configuration.
//
//	MaxOpenConns 25    — leaves 75 connections headroom for other clients on a
//	                     default postgres max_connections=100 server.
//	MaxIdleConns  5    — 5 warm connections at all times; low enough to not waste
//	                     postgres backend processes during quiet periods.
//	ConnMaxLifetime 5m — rotates connections every 5 minutes; balances freshness
//	                     vs connection-setup overhead.
//	ConnMaxIdleTime 10m — idles unused connections for up to 10 minutes before
//	                      closing them; tolerates brief traffic lulls.
//
// Called by: cmd/api/main.go when no custom pool config is provided.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 10 * time.Minute,
	}
}

// NewConnectionPool opens a PostgreSQL connection pool, applies the given config,
// and verifies the server is reachable before returning.
//
// Why sqlx is NOT used here (unlike storage.go):
//   This package provides a raw *sql.DB pool for components that do not need
//   sqlx's struct-scanning helpers (e.g. health checkers, migration tools).
//   storage.go uses sqlx.Connect separately because it needs SelectContext /
//   GetContext for clean struct mapping.
//
// Parameters:
//   dsn    — PostgreSQL DSN e.g. "postgres://user:pass@host:5432/db?sslmode=disable".
//   config — pool sizing parameters; use DefaultPoolConfig() for sensible defaults.
//
// Returns:
//   *ConnectionPool — ready-to-use pool.
//   error           — if the driver cannot be opened or the initial Ping fails.
//
// Called by: cmd/api/main.go, cmd/grpc-server/main.go.
func NewConnectionPool(dsn string, config PoolConfig) (*ConnectionPool, error) {
	// sql.Open does NOT open a connection — it only validates the driver name and
	// registers the DSN. The actual connection is lazy; we verify it below with Ping.
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Apply connection pool settings before the first use.
	db.SetMaxOpenConns(config.MaxOpenConns)    // hard cap on total connections
	db.SetMaxIdleConns(config.MaxIdleConns)    // idle connection keep-alive count
	db.SetConnMaxLifetime(config.ConnMaxLifetime) // maximum age of any connection
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime) // maximum idle time before closing

	// Verify the connection is actually reachable with a 5-second timeout.
	// Without this Ping, the error would surface at the first query (potentially
	// seconds later, and buried inside a handler rather than at startup).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel() // always release the timer goroutine

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &ConnectionPool{db: db}, nil
}

// GetDB returns the underlying *sql.DB.
// Use this to pass the pool to code that expects the standard library interface
// (e.g. Prometheus sql_db_stats collector, migration libraries).
//
// Called by: health checks, Prometheus stats collectors, migration runners.
func (p *ConnectionPool) GetDB() *sql.DB {
	return p.db
}

// Stats returns a snapshot of pool counters from the standard library.
// Useful for Prometheus metrics — track OpenConnections, InUse, Idle,
// WaitCount and WaitDuration to detect pool exhaustion under load.
//
// Called by: monitoring/metrics.go for the db_pool_stats gauge family.
func (p *ConnectionPool) Stats() sql.DBStats {
	return p.db.Stats()
}

// Close releases all idle connections and waits for in-use connections to be
// returned before closing them. After Close returns the pool is unusable.
//
// Must be called during graceful shutdown to avoid leaving orphaned PostgreSQL
// backend processes (visible as idle connections in pg_stat_activity).
//
// Called by: cmd/api/main.go and cmd/grpc-server/main.go shutdown sequences.
func (p *ConnectionPool) Close() error {
	return p.db.Close()
}

// HealthCheck sends a lightweight PING to PostgreSQL and returns an error if
// the server is not reachable or the connection pool is exhausted.
//
// Used by the health-check registry in reliability/health.go to report
// PostgreSQL status on the /health endpoint — Kubernetes readiness probes
// use this to decide whether to route traffic to this pod.
//
// Parameters:
//   ctx — allows the health check to be cancelled or time-boxed by the caller.
//         The health check endpoint typically passes a 3-second timeout context.
//
// Returns:
//   nil   — PostgreSQL responded to the ping within the context deadline.
//   error — if the ping times out or the pool is at max connections.
//
// Called by: reliability/health.go PostgreSQL health-check function.
func (p *ConnectionPool) HealthCheck(ctx context.Context) error {
	return p.db.PingContext(ctx)
}
