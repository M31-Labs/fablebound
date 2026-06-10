package pgstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"m31labs.dev/tiller/internal/scratch"
	"m31labs.dev/tiller/internal/scratch/pgstore"
	"m31labs.dev/tiller/internal/scratch/storetest"
)

// TestConformance runs the full Store conformance suite against pgstore,
// with each sub-test getting its own isolated PostgreSQL schema.
//
// Skipped unless TILLER_TEST_PG_DSN is set.
func TestConformance(t *testing.T) {
	dsn := os.Getenv("TILLER_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TILLER_TEST_PG_DSN not set — skipping pgstore conformance tests")
	}

	// Open once to apply baseline migrations to the public schema.
	ctx := context.Background()
	_, err := pgstore.Migrate(ctx, dsn)
	if err != nil {
		t.Fatalf("baseline migrate: %v", err)
	}

	// Factory function that creates a fresh schema per test.
	openTestStore := func(t *testing.T) scratch.Store {
		t.Helper()
		return openStoreWithFreshSchema(t, dsn)
	}

	storetest.Run(t, openTestStore)
}

// openStoreWithFreshSchema creates a fresh PostgreSQL schema for the test
// and returns a Store connected to it via search_path in the connection string.
func openStoreWithFreshSchema(t *testing.T, baseDSN string) *pgstore.Store {
	t.Helper()
	ctx := context.Background()

	// Generate a unique schema name for this test.
	schemaName := fmt.Sprintf("test_%d", time.Now().UnixNano())

	// Create the schema using a raw connection.
	sqldb, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open raw sql.DB: %v", err)
	}
	_, err = sqldb.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", schemaName))
	if err != nil {
		sqldb.Close()
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	sqldb.Close()

	// Build a modified DSN that includes search_path in the connection options.
	testDSN := addSearchPathToDSN(baseDSN, schemaName)

	// Open a pgstore DB with search_path set.
	db, err := pgstore.Open(testDSN)
	if err != nil {
		t.Fatalf("open pgstore db with search_path: %v", err)
	}

	// Migrate the schema.
	_, err = db.Migrate(ctx)
	if err != nil {
		db.Close()
		t.Fatalf("migrate schema %s: %v", schemaName, err)
	}

	store := pgstore.NewStore(db)

	// Register cleanup: close the store.
	t.Cleanup(func() {
		store.Close()
	})

	return store
}

// addSearchPathToDSN modifies a PostgreSQL DSN to include search_path in the connection options.
// This ensures all queries in the connection use the specified schema.
func addSearchPathToDSN(baseDSN, schema string) string {
	// Parse the DSN to check if it's a URL-style or keyword-style DSN.
	if strings.HasPrefix(baseDSN, "postgres://") || strings.HasPrefix(baseDSN, "postgresql://") {
		// URL-style DSN: add search_path to the query parameters.
		u, err := url.Parse(baseDSN)
		if err != nil {
			// If parsing fails, fall back to appending to the string.
			if strings.Contains(baseDSN, "?") {
				return baseDSN + "&options=" + url.QueryEscape("-c search_path="+schema)
			}
			return baseDSN + "?options=" + url.QueryEscape("-c search_path="+schema)
		}
		q := u.Query()
		// Set or append to options.
		if existing := q.Get("options"); existing != "" {
			q.Set("options", existing+" -c search_path="+schema)
		} else {
			q.Set("options", "-c search_path="+schema)
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	// For keyword-style DSNs, append the options parameter.
	if strings.Contains(baseDSN, " ") {
		return baseDSN + " options='-c search_path=" + schema + "'"
	}
	return baseDSN
}
