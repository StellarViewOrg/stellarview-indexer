package migrations

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestMigrationsDriverInitialization(t *testing.T) {
	// Verify that iofs driver initializes with FS without duplicate migration errors
	source, err := iofs.New(FS, ".")
	if err != nil {
		t.Fatalf("failed to initialize iofs driver with migrations.FS: %v", err)
	}
	defer source.Close()

	firstVersion, err := source.First()
	if err != nil {
		t.Fatalf("failed to get first migration version: %v", err)
	}
	if firstVersion != 1 {
		t.Errorf("expected first migration version to be 1, got %d", firstVersion)
	}
}

func TestNoDuplicateMigrationVersions(t *testing.T) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatalf("failed to read migrations directory: %v", err)
	}

	versionPattern := regexp.MustCompile(`^(\d{6})_.*\.(up|down)\.sql$`)
	seen := make(map[string]string) // key: version+direction, value: filename

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		matches := versionPattern.FindStringSubmatch(entry.Name())
		if len(matches) != 3 {
			t.Errorf("migration file %s does not follow naming convention NNNNNN_name.{up,down}.sql", entry.Name())
			continue
		}

		version := matches[1]
		direction := matches[2]
		key := fmt.Sprintf("%s_%s", version, direction)

		if existingFile, exists := seen[key]; exists {
			t.Errorf("duplicate migration version detected: %s conflicts with %s", entry.Name(), existingFile)
		} else {
			seen[key] = entry.Name()
		}
	}
}
