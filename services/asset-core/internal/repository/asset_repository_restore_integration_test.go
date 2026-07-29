package repository_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestAssetRepository_PostgresIntegration_RestoreIsParentFirstAndCollisionSafe(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}

	ctx := context.Background()
	orgID := uuid.NewString()
	userID := uuid.NewString()
	rootID := uuid.NewString()
	childID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	childPath := rootPath + "." + strings.ReplaceAll(childID, "-", "")
	originalTitleID := uuid.NewString()
	originalExternalID := uuid.NewString()

	t.Cleanup(func() {
		_ = database.Unscoped().Exec("DELETE FROM metadata_items USING folders WHERE metadata_items.folder_id = folders.id AND folders.org_id = ?", orgID).Error
		_ = database.Unscoped().Exec("DELETE FROM folders WHERE org_id = ?", orgID).Error
		_ = database.Exec("DELETE FROM user_ref WHERE user_id = ?", userID).Error
		_ = database.Exec("DELETE FROM organization_ref WHERE org_id = ?", orgID).Error
	})

	if err := database.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization reference: %v", err)
	}
	if err := database.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user reference: %v", err)
	}
	if err := database.Exec(`
		INSERT INTO folders (id, org_id, path, name, created_by)
		VALUES (?, ?, ?::ltree, ?, ?), (?, ?, ?::ltree, ?, ?)
	`, rootID, orgID, rootPath, "Project", userID, childID, orgID, childPath, "Design", userID).Error; err != nil {
		t.Fatalf("seed folders: %v", err)
	}
	if err := database.Exec(`
		INSERT INTO metadata_items (id, folder_id, title, external_source, external_id, created_by)
		VALUES (?, ?, ?, NULL, NULL, ?), (?, ?, ?, ?, ?, ?)
	`, originalTitleID, childID, "Item", userID, originalExternalID, childID, "Old external item", "open-images", "shared-1", userID).Error; err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	// First delete individual metadata while its folder is visible. New active
	// rows can then reuse its display title and (after V6) external identity.
	if err := database.Exec("UPDATE metadata_items SET deleted_at = NOW(), updated_by = ? WHERE id IN (?, ?)", userID, originalTitleID, originalExternalID).Error; err != nil {
		t.Fatalf("tombstone original metadata: %v", err)
	}
	if err := database.Exec(`
		INSERT INTO metadata_items (id, folder_id, title, external_source, external_id, created_by)
		VALUES (?, ?, ?, NULL, NULL, ?), (?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), childID, "Item", userID, uuid.NewString(), childID, "New external item", "open-images", "shared-1", userID).Error; err != nil {
		t.Fatalf("seed active metadata collisions: %v", err)
	}

	// A normal soft delete leaves the original child in storage. An active
	// sibling with the same display name can legitimately appear afterwards.
	if err := database.Exec("UPDATE folders SET deleted_at = NOW(), updated_by = ? WHERE id = ?", userID, childID).Error; err != nil {
		t.Fatalf("tombstone child folder: %v", err)
	}
	duplicateChildID := uuid.NewString()
	duplicateChildPath := rootPath + "." + strings.ReplaceAll(duplicateChildID, "-", "")
	if err := database.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)", duplicateChildID, orgID, duplicateChildPath, "Design", userID).Error; err != nil {
		t.Fatalf("seed active child collision: %v", err)
	}

	// This is the persisted state after a recursive worker has tombstoned the
	// root and descendants. The original rows remain physically present.
	if err := database.Exec("UPDATE folders SET deleted_at = NOW(), updated_by = ? WHERE id = ?", userID, rootID).Error; err != nil {
		t.Fatalf("tombstone root folder: %v", err)
	}
	duplicateRootID := uuid.NewString()
	duplicateRootPath := strings.ReplaceAll(duplicateRootID, "-", "")
	if err := database.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)", duplicateRootID, orgID, duplicateRootPath, "Project", userID).Error; err != nil {
		t.Fatalf("seed active root collision: %v", err)
	}

	assets := repository.NewAssetRepository(database)
	if _, err := assets.GetFolderByID(ctx, orgID, childID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected a child below a deleted root to be invisible, got %v", err)
	}
	if _, err := assets.RestoreFolder(ctx, orgID, userID, childID); !errors.Is(err, domain.ErrFolderParentDeleted) {
		t.Fatalf("expected child restore to require the parent first, got %v", err)
	}

	restoredRoot, err := assets.RestoreFolder(ctx, orgID, userID, rootID)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	if restoredRoot.Name != "Project (1)" {
		t.Fatalf("expected root collision rename, got %q", restoredRoot.Name)
	}

	restoredChild, err := assets.RestoreFolder(ctx, orgID, userID, childID)
	if err != nil {
		t.Fatalf("restore child after root: %v", err)
	}
	if restoredChild.Name != "Design (1)" {
		t.Fatalf("expected child collision rename, got %q", restoredChild.Name)
	}

	restoredItem, err := assets.RestoreMetadataItem(ctx, orgID, userID, originalTitleID)
	if err != nil {
		t.Fatalf("restore title-colliding metadata: %v", err)
	}
	if restoredItem.Title != "Item (1)" {
		t.Fatalf("expected metadata title collision rename, got %q", restoredItem.Title)
	}
	if _, err := assets.RestoreMetadataItem(ctx, orgID, userID, originalExternalID); !errors.Is(err, domain.ErrMetadataConflict) {
		t.Fatalf("expected active external identity to block restore, got %v", err)
	}

	var storedTombstones int64
	if err := database.Unscoped().Model(&domain.Folder{}).Where("org_id = ? AND deleted_at IS NOT NULL", orgID).Count(&storedTombstones).Error; err != nil {
		t.Fatalf("count tombstoned folders: %v", err)
	}
	if storedTombstones != 0 {
		t.Fatalf("expected folders chosen for restore to remain active, found %d tombstones", storedTombstones)
	}
	var remainingExternalTombstone int64
	if err := database.Unscoped().Model(&domain.MetadataItem{}).Where("id = ? AND deleted_at IS NOT NULL", originalExternalID).Count(&remainingExternalTombstone).Error; err != nil {
		t.Fatalf("count blocked external tombstone: %v", err)
	}
	if remainingExternalTombstone != 1 {
		t.Fatalf("expected external-conflicting row to remain a restoreable tombstone, found %d", remainingExternalTombstone)
	}
}
