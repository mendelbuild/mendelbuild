package schema_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMigrationsMatchFullSchema verifies that applying all migrations produces
// the same schema as running full.sql directly.
//
// This test requires a PostgreSQL server. Set MENDEL_TEST_DB_URL to run it.
// The test will create the database if it doesn't exist.
// Example: MENDEL_TEST_DB_URL="postgres://localhost/mendel_test?sslmode=disable" go test ./schema
func TestMigrationsMatchFullSchema(t *testing.T) {
	connString := os.Getenv("MENDEL_TEST_DB_URL")
	if connString == "" {
		t.Skip("MENDEL_TEST_DB_URL not set; skipping schema test")
	}

	ctx := context.Background()

	// Ensure the test database exists
	if err := ensureDatabase(ctx, connString); err != nil {
		t.Skipf("could not ensure database exists (skipping): %v", err)
	}

	// Connect to the database
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Skipf("could not connect to database (skipping): %v", err)
	}
	defer pool.Close()

	// Verify we can actually ping the database
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("could not ping database (skipping): %v", err)
	}

	// Create two separate schemas for comparison
	migrationSchema := "test_migrations_" + randomSuffix()
	fullSchema := "test_full_" + randomSuffix()

	// Clean up schemas on exit
	defer func() {
		pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", migrationSchema))
		pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", fullSchema))
	}()

	// Create schemas
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", migrationSchema)); err != nil {
		t.Fatalf("create migration schema: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", fullSchema)); err != nil {
		t.Fatalf("create full schema: %v", err)
	}

	// Apply migrations to migrationSchema
	if err := applyMigrations(ctx, pool, migrationSchema); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	// Apply full.sql to fullSchema
	if err := applyFullSQL(ctx, pool, fullSchema); err != nil {
		t.Fatalf("apply full.sql: %v", err)
	}

	// Compare schemas
	differences := compareSchemas(ctx, pool, migrationSchema, fullSchema)
	if len(differences) > 0 {
		t.Errorf("schema differences found:\n%s", strings.Join(differences, "\n"))
	}
}

func randomSuffix() string {
	return fmt.Sprintf("%d", os.Getpid())
}

// ensureDatabase creates the database if it doesn't exist.
// It connects to the 'postgres' database to do this.
func ensureDatabase(ctx context.Context, connString string) error {
	// Parse the connection string to extract database name
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return fmt.Errorf("parse connection string: %w", err)
	}

	dbName := config.ConnConfig.Database
	if dbName == "" {
		return fmt.Errorf("no database name in connection string")
	}

	// Connect to 'postgres' database to create the target database
	config.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect to postgres database: %w", err)
	}
	defer adminPool.Close()

	// Check if database exists
	var exists bool
	err = adminPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check database exists: %w", err)
	}

	if !exists {
		// Create the database (can't use parameterized query for CREATE DATABASE)
		_, err = adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", dbName))
		if err != nil {
			return fmt.Errorf("create database: %w", err)
		}
	}

	return nil
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	migrationsDir := "migrations"

	// Get all up migrations
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

	// Set search path and apply each migration
	for _, name := range upMigrations {
		content, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		// Wrap in transaction with search_path
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, fmt.Sprintf("SET search_path TO %s", schema)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("set search_path for %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, string(content)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}

func applyFullSQL(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	content, err := os.ReadFile("full.sql")
	if err != nil {
		return fmt.Errorf("read full.sql: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET search_path TO %s", schema)); err != nil {
		tx.Rollback(ctx)
		return fmt.Errorf("set search_path: %w", err)
	}

	if _, err := tx.Exec(ctx, string(content)); err != nil {
		tx.Rollback(ctx)
		return fmt.Errorf("apply full.sql: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit full.sql: %w", err)
	}

	return nil
}

func compareSchemas(ctx context.Context, pool *pgxpool.Pool, schema1, schema2 string) []string {
	var differences []string

	// Compare tables
	tables1 := getTables(ctx, pool, schema1)
	tables2 := getTables(ctx, pool, schema2)

	if diff := compareStringSlices("tables", tables1, tables2); diff != "" {
		differences = append(differences, diff)
	}

	// For each common table, compare columns
	commonTables := intersection(tables1, tables2)
	for _, table := range commonTables {
		cols1 := getColumns(ctx, pool, schema1, table)
		cols2 := getColumns(ctx, pool, schema2, table)

		if diff := compareStringSlices(fmt.Sprintf("columns in %s", table), cols1, cols2); diff != "" {
			differences = append(differences, diff)
		}
	}

	// Compare indexes
	indexes1 := getIndexes(ctx, pool, schema1)
	indexes2 := getIndexes(ctx, pool, schema2)

	if diff := compareStringSlices("indexes", indexes1, indexes2); diff != "" {
		differences = append(differences, diff)
	}

	// Compare constraints
	constraints1 := getConstraints(ctx, pool, schema1)
	constraints2 := getConstraints(ctx, pool, schema2)

	if diff := compareStringSlices("constraints", constraints1, constraints2); diff != "" {
		differences = append(differences, diff)
	}

	return differences
}

func getTables(ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	rows, err := pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = $1 AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		// Skip migrations tracking table
		if name != "_migrations" {
			tables = append(tables, name)
		}
	}
	return tables
}

func getColumns(ctx context.Context, pool *pgxpool.Pool, schema, table string) []string {
	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position
	`, schema, table)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name, dataType, nullable string
		var defaultVal *string
		rows.Scan(&name, &dataType, &nullable, &defaultVal)

		// Normalize data types for comparison
		dataType = normalizeDataType(dataType)

		col := fmt.Sprintf("%s %s %s", name, dataType, nullable)
		if defaultVal != nil {
			col += fmt.Sprintf(" DEFAULT %s", normalizeDefault(*defaultVal))
		}
		cols = append(cols, col)
	}
	return cols
}

func getIndexes(ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	rows, err := pool.Query(ctx, `
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE schemaname = $1
		ORDER BY indexname
	`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var indexes []string
	for rows.Next() {
		var name, def string
		rows.Scan(&name, &def)
		// Normalize the schema name out of the definition
		def = strings.ReplaceAll(def, schema+".", "")
		indexes = append(indexes, fmt.Sprintf("%s: %s", name, def))
	}
	return indexes
}

func getConstraints(ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	rows, err := pool.Query(ctx, `
		SELECT c.conname, c.contype, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE n.nspname = $1
		ORDER BY c.conname
	`, schema)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var constraints []string
	for rows.Next() {
		var name, contype, def string
		rows.Scan(&name, &contype, &def)
		// Normalize the schema name out of the definition
		def = strings.ReplaceAll(def, schema+".", "")
		constraints = append(constraints, fmt.Sprintf("%s (%s): %s", name, contype, def))
	}
	return constraints
}

func normalizeDataType(dt string) string {
	// PostgreSQL reports some types differently
	dt = strings.ToLower(dt)
	switch dt {
	case "character varying":
		return "varchar"
	case "timestamp without time zone":
		return "timestamp"
	case "double precision":
		return "float8"
	}
	return dt
}

func normalizeDefault(d string) string {
	// Normalize common default expressions
	d = strings.ReplaceAll(d, "now()", "NOW()")
	return d
}

func compareStringSlices(name string, s1, s2 []string) string {
	set1 := make(map[string]bool)
	set2 := make(map[string]bool)

	for _, s := range s1 {
		set1[s] = true
	}
	for _, s := range s2 {
		set2[s] = true
	}

	var onlyIn1, onlyIn2 []string

	for s := range set1 {
		if !set2[s] {
			onlyIn1 = append(onlyIn1, s)
		}
	}
	for s := range set2 {
		if !set1[s] {
			onlyIn2 = append(onlyIn2, s)
		}
	}

	if len(onlyIn1) == 0 && len(onlyIn2) == 0 {
		return ""
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("%s differ:\n", name))
	if len(onlyIn1) > 0 {
		msg.WriteString("  Only in migrations:\n")
		for _, s := range onlyIn1 {
			msg.WriteString(fmt.Sprintf("    - %s\n", s))
		}
	}
	if len(onlyIn2) > 0 {
		msg.WriteString("  Only in full.sql:\n")
		for _, s := range onlyIn2 {
			msg.WriteString(fmt.Sprintf("    - %s\n", s))
		}
	}
	return msg.String()
}

func intersection(s1, s2 []string) []string {
	set := make(map[string]bool)
	for _, s := range s1 {
		set[s] = true
	}

	var result []string
	for _, s := range s2 {
		if set[s] {
			result = append(result, s)
		}
	}
	return result
}
