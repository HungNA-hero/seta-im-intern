package repository_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaMigrationRequiresSourceDimensionsToBeBothNullOrBothPresent(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "infra", "db", "asset", "migrations", "V9__media_upload_processing.sql")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read media migration: %v", err)
	}
	constraint := string(contents)
	want := "(source_width IS NOT NULL AND source_height IS NOT NULL AND source_width > 0 AND source_height > 0)"
	if !strings.Contains(constraint, want) {
		t.Fatalf("dimension constraint does not enforce null parity; missing %q", want)
	}
}
