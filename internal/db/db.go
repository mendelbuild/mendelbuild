package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool with migration support.
type DB struct {
	Pool *pgxpool.Pool
}

// Connect establishes a connection pool to Postgres.
func Connect(ctx context.Context, connString string) (*DB, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close closes the connection pool.
func (db *DB) Close() {
	db.Pool.Close()
}

// findMigrationsDir locates the schema/migrations directory.
// It walks up from the current working directory looking for it.
func findMigrationsDir() (string, error) {
	// Try relative to working directory first
	candidates := []string{
		"schema/migrations",
		"../schema/migrations",
		"../../schema/migrations",
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
	}

	// Try using executable location
	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		candidate := filepath.Join(exeDir, "schema/migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("could not find schema/migrations directory")
}

// Migrate runs all pending up migrations.
func (db *DB) Migrate(ctx context.Context) error {
	migrationsDir, err := findMigrationsDir()
	if err != nil {
		return err
	}

	// Create migrations tracking table if not exists
	_, err = db.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS _migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	// Get list of applied migrations
	rows, err := db.Pool.Query(ctx, "SELECT name FROM _migrations ORDER BY name")
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan migration name: %w", err)
		}
		applied[name] = true
	}

	// Get all up migrations from filesystem
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var upMigrations []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			upMigrations = append(upMigrations, e.Name())
		}
	}
	sort.Strings(upMigrations)

	// Apply pending migrations
	for _, name := range upMigrations {
		baseName := strings.TrimSuffix(name, ".up.sql")
		if applied[baseName] {
			continue
		}

		content, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction for %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("execute migration %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, "INSERT INTO _migrations (name) VALUES ($1)", baseName); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}

		fmt.Printf("Applied migration: %s\n", baseName)
	}

	return nil
}

// MigrateDown reverts the last N migrations (default 1).
func (db *DB) MigrateDown(ctx context.Context, count int) error {
	migrationsDir, err := findMigrationsDir()
	if err != nil {
		return err
	}

	if count <= 0 {
		count = 1
	}

	// Get applied migrations in reverse order
	rows, err := db.Pool.Query(ctx, "SELECT name FROM _migrations ORDER BY name DESC LIMIT $1", count)
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	var toRevert []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan migration name: %w", err)
		}
		toRevert = append(toRevert, name)
	}

	for _, baseName := range toRevert {
		downFile := baseName + ".down.sql"
		content, err := os.ReadFile(filepath.Join(migrationsDir, downFile))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", downFile, err)
		}

		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction for %s: %w", downFile, err)
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("execute migration %s: %w", downFile, err)
		}

		if _, err := tx.Exec(ctx, "DELETE FROM _migrations WHERE name = $1", baseName); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("remove migration record %s: %w", baseName, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", downFile, err)
		}

		fmt.Printf("Reverted migration: %s\n", baseName)
	}

	return nil
}
