// File: internal/database/pool.go
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// ConnectionPool manages database connection pooling
type ConnectionPool struct {
	db *sql.DB
}

// PoolConfig holds connection pool configuration
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPoolConfig returns default pool configuration
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    25,
		MaxIdleConns:   5,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 10 * time.Minute,
	}
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(dsn string, config PoolConfig) (*ConnectionPool, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime)

	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &ConnectionPool{db: db}, nil
}

// GetDB returns the underlying database connection
func (p *ConnectionPool) GetDB() *sql.DB {
	return p.db
}

// Stats returns connection pool statistics
func (p *ConnectionPool) Stats() sql.DBStats {
	return p.db.Stats()
}

// Close closes the connection pool
func (p *ConnectionPool) Close() error {
	return p.db.Close()
}

// HealthCheck performs a health check on the connection pool
func (p *ConnectionPool) HealthCheck(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

