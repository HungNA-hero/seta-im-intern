package repository_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestFolderRepository_PostgresIntegration(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && err != gorm.ErrInvalidTransaction {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})

	ctx := context.Background()
	orgID := uuid.NewString()
	otherOrgID := uuid.NewString()
	userID := uuid.NewString()

	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?), (?)", orgID, otherOrgID).Error; err != nil {
		t.Fatalf("seed organization_ref: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user_ref: %v", err)
	}

	repo := repository.NewAssetRepository(tx)

	// Create root folder
	rootID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		rootID, orgID, rootPath, "Root", userID).Error; err != nil {
		t.Fatalf("seed root folder: %v", err)
	}

	// Create child folder "A"
	childAID := uuid.NewString()
	childAPath := rootPath + "." + strings.ReplaceAll(childAID, "-", "")
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		childAID, orgID, childAPath, "A", userID).Error; err != nil {
		t.Fatalf("seed child A folder: %v", err)
	}

	// Create grandchild folder "B" under "A"
	childBID := uuid.NewString()
	childBPath := childAPath + "." + strings.ReplaceAll(childBID, "-", "")
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		childBID, orgID, childBPath, "B", userID).Error; err != nil {
		t.Fatalf("seed child B folder: %v", err)
	}

	// 1. Move root into grandchild B -> should fail with ErrFolderCycle
	_, err = repo.MoveFolder(ctx, orgID, userID, rootID, domain.MoveFolderInput{
		DestinationParentID: &childBID,
	})
	if err != domain.ErrCycleDetected {
		t.Errorf("expected ErrCycleDetected, got %v", err)
	}

	// 2. Move A to root (sibling conflict)
	// Create another child "A" under root using SavePoint since we'll cause a conflict
	tx.SavePoint("before_conflict_setup")
	// We'll create "NewParent" at root
	newParentID := uuid.NewString()
	newParentPath := rootPath + "." + strings.ReplaceAll(newParentID, "-", "")
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		newParentID, orgID, newParentPath, "NewParent", userID).Error; err != nil {
		t.Fatalf("seed NewParent folder: %v", err)
	}

	// Create "A" under "NewParent" to cause a conflict when we move the original "A" to "NewParent"
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		uuid.NewString(), orgID, newParentPath+"."+strings.ReplaceAll(uuid.NewString(), "-", ""), "A", userID).Error; err != nil {
		t.Fatalf("seed conflicting A folder: %v", err)
	}

	// Now try to move the original A to NewParent -> should conflict
	tx.SavePoint("before_conflict_move")
	_, err = repo.MoveFolder(ctx, orgID, userID, childAID, domain.MoveFolderInput{
		DestinationParentID: &newParentID,
	})
	if err != domain.ErrFolderConflict {
		t.Errorf("expected ErrFolderConflict, got %v", err)
	}
	tx.RollbackTo("before_conflict_move") // DB remains unchanged

	// 3. Move subtree A (which contains B) to NewParent
	// First delete the conflicting "A" so we can move it
	tx.RollbackTo("before_conflict_setup")
	// Re-create NewParent without the conflicting "A"
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		newParentID, orgID, newParentPath, "NewParent", userID).Error; err != nil {
		t.Fatalf("seed NewParent folder: %v", err)
	}

	// Force a unique constraint violation (mid-update failure) during the set-based subtree UPDATE. PostgreSQL must
	// roll back the complete statement and GORM must restore the enclosing transaction.
	if err := tx.Exec(`
		CREATE FUNCTION kan36_fail_descendant_path_update() RETURNS trigger AS $$
		BEGIN
			IF OLD.id::text = current_setting('kan36.fail_folder_id', true) THEN
				RAISE EXCEPTION 'forced unique constraint violation' USING ERRCODE = '23505';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql
	`).Error; err != nil {
		t.Fatalf("create failure trigger function: %v", err)
	}
	if err := tx.Exec(`
		CREATE TRIGGER kan36_fail_descendant_path_update
		BEFORE UPDATE OF path ON folders
		FOR EACH ROW EXECUTE FUNCTION kan36_fail_descendant_path_update()
	`).Error; err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if err := tx.Exec("SELECT set_config('kan36.fail_folder_id', ?, true)", childBID).Error; err != nil {
		t.Fatalf("configure failure trigger: %v", err)
	}

	_, err = repo.MoveFolder(ctx, orgID, userID, childAID, domain.MoveFolderInput{
		DestinationParentID: &newParentID,
	})
	if err != domain.ErrFolderConflict {
		t.Fatalf("expected ErrFolderConflict due to mid-update unique violation, got %v", err)
	}

	var unchangedAPath, unchangedBPath string
	if err := tx.Raw("SELECT path::text FROM folders WHERE id = ?", childAID).Scan(&unchangedAPath).Error; err != nil {
		t.Fatalf("query A after forced rollback: %v", err)
	}
	if err := tx.Raw("SELECT path::text FROM folders WHERE id = ?", childBID).Scan(&unchangedBPath).Error; err != nil {
		t.Fatalf("query B after forced rollback: %v", err)
	}
	if unchangedAPath != childAPath || unchangedBPath != childBPath {
		t.Fatalf("subtree changed after rollback: A=%s B=%s", unchangedAPath, unchangedBPath)
	}
	if err := tx.Exec("DROP TRIGGER kan36_fail_descendant_path_update ON folders").Error; err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := tx.Exec("DROP FUNCTION kan36_fail_descendant_path_update()").Error; err != nil {
		t.Fatalf("drop failure trigger function: %v", err)
	}

	movedA, err := repo.MoveFolder(ctx, orgID, userID, childAID, domain.MoveFolderInput{
		DestinationParentID: &newParentID,
	})
	if err != nil {
		t.Fatalf("expected success moving A, got %v", err)
	}
	expectedAPath := newParentPath + "." + strings.ReplaceAll(childAID, "-", "")
	if movedA.Path != expectedAPath {
		t.Errorf("expected A path to be %s, got %s", expectedAPath, movedA.Path)
	}
	// Verify B (descendant) was also updated
	var actualBPath string
	if err := tx.Raw("SELECT path::text FROM folders WHERE id = ?", childBID).Scan(&actualBPath).Error; err != nil {
		t.Fatalf("failed to query B path: %v", err)
	}
	expectedBPath := expectedAPath + "." + strings.ReplaceAll(childBID, "-", "")
	if actualBPath != expectedBPath {
		t.Errorf("expected B path to be updated to %s, got %s", expectedBPath, actualBPath)
	}
	if movedA.UpdatedBy == nil || *movedA.UpdatedBy != userID {
		t.Errorf("expected source updated_by to be %s, got %v", userID, movedA.UpdatedBy)
	}
	var actualBUpdatedBy string
	if err := tx.Raw("SELECT updated_by::text FROM folders WHERE id = ?", childBID).Scan(&actualBUpdatedBy).Error; err != nil {
		t.Fatalf("query B updated_by: %v", err)
	}
	if actualBUpdatedBy != userID {
		t.Errorf("expected descendant updated_by to be %s, got %s", userID, actualBUpdatedBy)
	}

	// 4. Delete folder containing metadata
	// Insert metadata into B
	if err := tx.Exec("INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES (?, ?, ?, ?)",
		uuid.NewString(), childBID, "Meta Title", userID).Error; err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	err = repo.DeleteFolder(ctx, orgID, userID, childBID)
	if err != domain.ErrFolderNotEmpty {
		t.Errorf("expected ErrFolderNotEmpty when deleting folder with metadata, got %v", err)
	}

	// 5. Cross-org / deleted destination
	// Cross-org destination
	crossOrgDest := uuid.NewString()
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		crossOrgDest, otherOrgID, strings.ReplaceAll(crossOrgDest, "-", ""), "CrossOrg", userID).Error; err != nil {
		t.Fatalf("seed cross org folder: %v", err)
	}
	_, err = repo.MoveFolder(ctx, orgID, userID, childAID, domain.MoveFolderInput{
		DestinationParentID: &crossOrgDest,
	})
	if err != domain.ErrFolderNotFound {
		t.Errorf("expected ErrFolderNotFound for cross-org dest, got %v", err)
	}

	// Deleted destination
	deletedDest := uuid.NewString()
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, NOW())",
		deletedDest, orgID, strings.ReplaceAll(deletedDest, "-", ""), "DeletedDest", userID).Error; err != nil {
		t.Fatalf("seed deleted folder: %v", err)
	}
	_, err = repo.MoveFolder(ctx, orgID, userID, childAID, domain.MoveFolderInput{
		DestinationParentID: &deletedDest,
	})
	if err != domain.ErrFolderNotFound {
		t.Errorf("expected ErrFolderNotFound for deleted dest, got %v", err)
	}

	// 6. Delete empty folder (NewParent) - actually NewParent has A, so let's delete a truly empty folder
	emptyFolderID := uuid.NewString()
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		emptyFolderID, orgID, strings.ReplaceAll(emptyFolderID, "-", ""), "Empty", userID).Error; err != nil {
		t.Fatalf("seed empty folder: %v", err)
	}
	err = repo.DeleteFolder(ctx, orgID, userID, emptyFolderID)
	if err != nil {
		t.Fatalf("expected success deleting empty folder, got %v", err)
	}
	var emptyFolderCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM folders WHERE id = ?", emptyFolderID).Scan(&emptyFolderCount).Error; err != nil {
		t.Fatalf("count hard-deleted folder: %v", err)
	}
	if emptyFolderCount != 0 {
		t.Errorf("expected hard-deleted folder to be absent, got %d row", emptyFolderCount)
	}

	// 7. Hard deletion purges legacy tombstones inside the target subtree only.
	legacyRootID := uuid.NewString()
	legacyRootPath := strings.ReplaceAll(legacyRootID, "-", "")
	legacyFolderID := uuid.NewString()
	legacyFolderPath := legacyRootPath + "." + strings.ReplaceAll(legacyFolderID, "-", "")
	legacyRootMetadataID := uuid.NewString()
	legacyChildMetadataID := uuid.NewString()
	if err := tx.Exec(`
		INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?);
		INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, NOW());
		INSERT INTO metadata_items (id, folder_id, title, created_by, deleted_at) VALUES (?, ?, ?, ?, NOW()), (?, ?, ?, ?, NOW());
	`, legacyRootID, orgID, legacyRootPath, "Legacy purge root", userID,
		legacyFolderID, orgID, legacyFolderPath, "Legacy tombstone child", userID,
		legacyRootMetadataID, legacyRootID, "Legacy root metadata", userID,
		legacyChildMetadataID, legacyFolderID, "Legacy child metadata", userID).Error; err != nil {
		t.Fatalf("seed legacy tombstone subtree: %v", err)
	}
	if err := repo.DeleteFolder(ctx, orgID, userID, legacyRootID); err != nil {
		t.Fatalf("hard-delete legacy tombstone subtree root: %v", err)
	}
	var remainingLegacyRows int64
	if err := tx.Raw(`
		SELECT
			(SELECT COUNT(*) FROM folders WHERE id IN (?, ?)) +
			(SELECT COUNT(*) FROM metadata_items WHERE id IN (?, ?))
	`, legacyRootID, legacyFolderID, legacyRootMetadataID, legacyChildMetadataID).Scan(&remainingLegacyRows).Error; err != nil {
		t.Fatalf("query purged legacy tombstones: %v", err)
	}
	if remainingLegacyRows != 0 {
		t.Errorf("expected legacy tombstones to be purged, got %d remaining row", remainingLegacyRows)
	}
}
