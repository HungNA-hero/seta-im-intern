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

func TestFolderDeletionRepository_PostgresIntegration(t *testing.T) {
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
	legacyFolderID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	childPath := rootPath + "." + strings.ReplaceAll(childID, "-", "")
	legacyPath := rootPath + "." + strings.ReplaceAll(legacyFolderID, "-", "")

	t.Cleanup(func() {
		_ = database.Exec("DELETE FROM folder_deletion_jobs WHERE org_id = ?", orgID).Error
		_ = database.Exec("DELETE FROM asset_lifecycle_units WHERE org_id = ?", orgID).Error
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
	if err := database.Exec("INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, NULL), (?, ?, ?::ltree, ?, ?, NULL), (?, ?, ?::ltree, ?, ?, NULL)",
		rootID, orgID, rootPath, "Delete root", userID,
		childID, orgID, childPath, "Delete child", userID,
		legacyFolderID, orgID, legacyPath, "Legacy tombstone", userID,
	).Error; err != nil {
		t.Fatalf("seed folder subtree: %v", err)
	}
	if err := database.Exec("INSERT INTO metadata_items (id, folder_id, title, created_by, deleted_at) VALUES (?, ?, ?, ?, NULL), (?, ?, ?, ?, NULL), (?, ?, ?, ?, NULL)",
		uuid.NewString(), rootID, "Active root metadata", userID,
		uuid.NewString(), childID, "Active child metadata", userID,
		uuid.NewString(), legacyFolderID, "Legacy metadata", userID,
	).Error; err != nil {
		t.Fatalf("seed metadata subtree: %v", err)
	}
	if err := database.Exec("UPDATE metadata_items SET deleted_at = NOW() WHERE folder_id = ?", legacyFolderID).Error; err != nil {
		t.Fatalf("soft-delete legacy metadata: %v", err)
	}
	if err := database.Exec("UPDATE folders SET deleted_at = NOW() WHERE id = ?", legacyFolderID).Error; err != nil {
		t.Fatalf("soft-delete legacy folder: %v", err)
	}

	deletions := repository.NewFolderDeletionRepository(database)
	assets := repository.NewAssetRepository(database)
	preview, err := deletions.PreviewFolderDeletion(ctx, orgID, userID, rootID)
	if err != nil {
		t.Fatalf("preview recursive deletion: %v", err)
	}
	if preview.ActiveFolderCount != 2 || preview.ActiveMetadataCount != 2 || preview.TombstoneFolderCount != 1 || preview.TombstoneMetadataCount != 1 {
		t.Fatalf("unexpected preview counts: %#v", preview)
	}

	queued, err := deletions.ConfirmFolderDeletion(ctx, orgID, userID, rootID, preview.ID, preview.ConfirmationToken)
	if err != nil {
		t.Fatalf("confirm recursive deletion: %v", err)
	}
	if queued.Status != domain.FolderDeletionQueued {
		t.Fatalf("expected queued job, got %s", queued.Status)
	}

	_, err = assets.CreateMetadataItem(ctx, orgID, userID, domain.CreateMetadataInput{FolderID: rootID, Title: "Blocked write"})
	if !errors.Is(err, domain.ErrFolderDeletionInProgress) {
		t.Fatalf("expected queued subtree write to be frozen, got %v", err)
	}

	cancelled, err := deletions.CancelFolderDeletionJob(ctx, orgID, userID, queued.ID, false)
	if err != nil || cancelled.Status != domain.FolderDeletionCancelled {
		t.Fatalf("cancel queued job: status=%s err=%v", cancelled.Status, err)
	}

	preview, err = deletions.PreviewFolderDeletion(ctx, orgID, userID, rootID)
	if err != nil {
		t.Fatalf("preview after cancellation: %v", err)
	}
	queued, err = deletions.ConfirmFolderDeletion(ctx, orgID, userID, rootID, preview.ID, preview.ConfirmationToken)
	if err != nil {
		t.Fatalf("confirm after cancellation: %v", err)
	}
	claimed, err := deletions.ClaimNextFolderDeletionJob(ctx, "integration-worker")
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim queued job: job=%#v err=%v", claimed, err)
	}
	if err := deletions.ProcessFolderDeletionJob(ctx, claimed.ID, "integration-worker"); err != nil {
		t.Fatalf("process recursive deletion: %v", err)
	}

	job, err := deletions.GetFolderDeletionJob(ctx, orgID, userID, queued.ID, false)
	if err != nil || job.Status != domain.FolderDeletionSucceeded {
		t.Fatalf("read completed job: status=%s err=%v", job.Status, err)
	}
	if job.DeletedFolderCount != 2 || job.DeletedMetadataCount != 2 {
		t.Fatalf("unexpected tombstone progress: %#v", job)
	}

	var lifecycleUnits []domain.LifecycleUnit
	if err := database.Where("org_id = ? AND root_resource_type = ? AND root_resource_id = ?", orgID, domain.LifecycleResourceFolder, rootID).
		Find(&lifecycleUnits).Error; err != nil {
		t.Fatalf("load recursive deletion lifecycle root: %v", err)
	}
	if len(lifecycleUnits) != 1 {
		t.Fatalf("expected exactly one lifecycle root for the deleted tree, got %#v", lifecycleUnits)
	}
	unit := lifecycleUnits[0]
	if unit.State != domain.LifecycleDeleted || unit.RootFolderPath != rootPath || unit.DeleteCompletedAt == nil || unit.RetentionUntil == nil {
		t.Fatalf("expected completed Recycle Bin root with retention, got %#v", unit)
	}
	var linkedRootCount int64
	if err := database.Raw("SELECT COUNT(*) FROM folders WHERE id = ? AND lifecycle_unit_id = ?", rootID, unit.ID).Scan(&linkedRootCount).Error; err != nil {
		t.Fatalf("count root lifecycle link: %v", err)
	}
	if linkedRootCount != 1 {
		t.Fatalf("expected the deletion root to link to its unit, got %d rows", linkedRootCount)
	}
	var linkedMemberCount int64
	if err := database.Raw(`
		SELECT
			(SELECT COUNT(*) FROM folders WHERE org_id = ? AND id != ? AND lifecycle_unit_id IS NOT NULL) +
			(SELECT COUNT(*) FROM metadata_items WHERE folder_id IN (?, ?, ?) AND lifecycle_unit_id IS NOT NULL)
	`, orgID, rootID, rootID, childID, legacyFolderID).Scan(&linkedMemberCount).Error; err != nil {
		t.Fatalf("count descendant lifecycle links: %v", err)
	}
	if linkedMemberCount != 0 {
		t.Fatalf("expected descendants to remain members rather than Recycle Bin roots, got %d links", linkedMemberCount)
	}
	entries, err := assets.ListRecycleBinEntries(ctx, orgID, domain.RecycleBinFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list Recycle Bin entries: %v", err)
	}
	if len(entries) != 1 || entries[0].LifecycleUnitID != unit.ID || entries[0].ResourceID != rootID {
		t.Fatalf("expected the tree to appear as exactly one Recycle Bin root, got %#v", entries)
	}

	var activeRows int64
	if err := database.Raw("SELECT (SELECT count(*) FROM folders WHERE org_id = ? AND deleted_at IS NULL) + (SELECT count(*) FROM metadata_items WHERE folder_id IN (?, ?, ?) AND deleted_at IS NULL)", orgID, rootID, childID, legacyFolderID).Scan(&activeRows).Error; err != nil {
		t.Fatalf("count active subtree rows: %v", err)
	}
	if activeRows != 0 {
		t.Fatalf("expected no active rows in tombstoned subtree, found %d", activeRows)
	}

	var storedRows int64
	if err := database.Raw("SELECT (SELECT count(*) FROM folders WHERE org_id = ?) + (SELECT count(*) FROM metadata_items WHERE folder_id IN (?, ?, ?))", orgID, rootID, childID, legacyFolderID).Scan(&storedRows).Error; err != nil {
		t.Fatalf("count stored subtree rows: %v", err)
	}
	if storedRows != 6 {
		t.Fatalf("expected all rows to remain restorable, found %d", storedRows)
	}
}
