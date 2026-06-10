package pgstore_test

import (
	"context"
	"os"
	"testing"

	"m31labs.dev/tiller/internal/scratch/pgstore"
)

// TestMigrateIdempotent verifies that Migrate can be called twice on a live
// PostgreSQL rig without error and that the schema version row is present.
//
// Skipped unless TILLER_TEST_PG_DSN is set. The rig is disposable; all writes
// go to the public schema and are safe to re-run (idempotent).
func TestMigrateIdempotent(t *testing.T) {
	dsn := os.Getenv("TILLER_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TILLER_TEST_PG_DSN not set — skipping postgres integration test")
	}

	ctx := context.Background()

	// First run.
	v1, err := pgstore.Migrate(ctx, dsn)
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if v1 != pgstore.SchemaVersion {
		t.Errorf("first Migrate: version = %d, want %d", v1, pgstore.SchemaVersion)
	}

	// Second run — must be idempotent.
	v2, err := pgstore.Migrate(ctx, dsn)
	if err != nil {
		t.Fatalf("second Migrate (idempotency): %v", err)
	}
	if v2 != pgstore.SchemaVersion {
		t.Errorf("second Migrate: version = %d, want %d", v2, pgstore.SchemaVersion)
	}

	// QueryStatus should report the version and non-zero schema_version count.
	st, err := pgstore.QueryStatus(ctx, dsn)
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if st.Version != pgstore.SchemaVersion {
		t.Errorf("QueryStatus: version = %d, want %d", st.Version, pgstore.SchemaVersion)
	}
	if st.RowCounts["schema_version"] < 1 {
		t.Errorf("QueryStatus: schema_version count = %d, want ≥1", st.RowCounts["schema_version"])
	}
}
