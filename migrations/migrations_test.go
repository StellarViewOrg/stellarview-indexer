package migrations_test

import (
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/miguelnietoa/stellar-explorer/indexer/migrations"
)

// TestMigrationsFS_NoDuplicateVersions ensures that the embedded migrations
// directory initializes cleanly with golang-migrate's iofs driver.
// This test guards against duplicate migration version collisions (e.g. issue #69).
func TestMigrationsFS_NoDuplicateVersions(t *testing.T) {
	driver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("failed to initialize iofs driver with migrations.FS: %v", err)
	}

	first, err := driver.First()
	if err != nil {
		t.Fatalf("failed to retrieve first migration: %v", err)
	}
	if first != 1 {
		t.Errorf("expected first migration to be version 1, got %d", first)
	}

	current := first
	count := 1
	for {
		next, err := driver.Next(current)
		if err != nil {
			break
		}
		if next <= current {
			t.Errorf("migration sequence numbers must strictly increase: next (%d) <= current (%d)", next, current)
		}
		current = next
		count++
	}

	if count != 17 {
		t.Errorf("expected exactly 17 migrations up to version 000017, got %d", count)
	}
}
