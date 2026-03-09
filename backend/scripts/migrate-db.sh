#!/bin/bash
# Database migration script

set -e

MIGRATIONS_DIR="migrations"
POSTGRES_DSN="${POSTGRES_DSN:-postgres://taskqueue_user:password@localhost:5432/taskqueue?sslmode=disable}"

echo "Running database migrations..."

if [ ! -d "$MIGRATIONS_DIR" ]; then
    echo "Migrations directory not found"
    exit 1
fi

# Run migrations in order
for migration in $(ls $MIGRATIONS_DIR/*.up.sql | sort); do
    echo "Running migration: $migration"
    psql "$POSTGRES_DSN" -f "$migration"
done

echo "✓ Migrations completed"

