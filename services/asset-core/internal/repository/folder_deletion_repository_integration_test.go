package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestFolderDeletionRepository_PostgresIntegration(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
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
		_ = database.Exec("DELETE FROM asset_lifecycle_jobs WHERE org_id = ?", orgID).Error
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
	var immediatelyHiddenRootCount int64
	if err := database.Raw(`
		SELECT COUNT(*)
		FROM folders
		WHERE id = ? AND org_id = ? AND deleted_at IS NOT NULL AND lifecycle_unit_id IS NOT NULL
	`, rootID, orgID).Scan(&immediatelyHiddenRootCount).Error; err != nil {
		t.Fatalf("count immediately hidden deletion root: %v", err)
	}
	if immediatelyHiddenRootCount != 1 {
		t.Fatalf("expected confirm to tombstone and link the root before a worker runs, got %d", immediatelyHiddenRootCount)
	}
	var queuedLifecycleJob domain.LifecycleJob
	if err := database.Where("legacy_folder_deletion_job_id = ?", queued.ID).First(&queuedLifecycleJob).Error; err != nil {
		t.Fatalf("load V8 lifecycle job after confirm: %v", err)
	}
	if queuedLifecycleJob.Status != domain.LifecycleJobQueued || queuedLifecycleJob.UnitID == nil {
		t.Fatalf("expected confirm to create a queued V8 lifecycle job with a unit, got %#v", queuedLifecycleJob)
	}
	var queuedCheckpoint folderDeletionCheckpointView
	if err := json.Unmarshal(queuedLifecycleJob.Checkpoint, &queuedCheckpoint); err != nil {
		t.Fatalf("decode queued lifecycle checkpoint: %v", err)
	}
	if !queuedCheckpoint.RootVisibilityClosed || queuedCheckpoint.FolderRows != 1 {
		t.Fatalf("expected root gate checkpoint, got %#v", queuedCheckpoint)
	}
	visibleBeforeWorker, err := assets.GetFolderTree(ctx, orgID, rootPath)
	if err != nil {
		t.Fatalf("read tree after confirm: %v", err)
	}
	if len(visibleBeforeWorker) != 0 {
		t.Fatalf("expected the visibility gate to hide the full subtree before a worker runs, got %#v", visibleBeforeWorker)
	}

	_, err = assets.CreateMetadataItem(ctx, orgID, userID, domain.CreateMetadataInput{FolderID: rootID, Title: "Blocked write"})
	if !errors.Is(err, domain.ErrFolderNotFound) {
		t.Fatalf("expected a gated subtree write to return safe not-found, got %v", err)
	}

	cancelled, err := deletions.CancelFolderDeletionJob(ctx, orgID, userID, queued.ID, false)
	if err != nil || cancelled.Status != domain.FolderDeletionCancelled {
		t.Fatalf("cancel queued job: status=%s err=%v", cancelled.Status, err)
	}
	visibleAfterCancel, err := assets.GetFolderTree(ctx, orgID, rootPath)
	if err != nil {
		t.Fatalf("read tree after queued cancellation: %v", err)
	}
	if len(visibleAfterCancel) != 2 {
		t.Fatalf("expected queued cancellation to reopen the root and child, got %#v", visibleAfterCancel)
	}
	var restoredUnitCount int64
	if err := database.Model(&domain.LifecycleUnit{}).
		Where("org_id = ? AND root_resource_type = ? AND root_resource_id = ? AND state = ?", orgID, domain.LifecycleResourceFolder, rootID, domain.LifecycleRestored).
		Count(&restoredUnitCount).Error; err != nil {
		t.Fatalf("count cancelled lifecycle unit: %v", err)
	}
	if restoredUnitCount != 1 {
		t.Fatalf("expected queued cancellation to retain one restored lifecycle unit, got %d", restoredUnitCount)
	}
	if err := database.Where("id = ?", queuedLifecycleJob.ID).First(&queuedLifecycleJob).Error; err != nil {
		t.Fatalf("reload cancelled V8 lifecycle job: %v", err)
	}
	if queuedLifecycleJob.Status != domain.LifecycleJobSuppressed {
		t.Fatalf("expected queued cancellation to suppress its V8 lifecycle job, got %s", queuedLifecycleJob.Status)
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
	secondClaim, err := deletions.ClaimNextFolderDeletionJob(ctx, "second-integration-worker")
	if err != nil {
		t.Fatalf("claim while the first worker owns the V8 lease: %v", err)
	}
	if secondClaim != nil {
		t.Fatalf("expected only one worker to claim the V8 lifecycle job, got %#v", secondClaim)
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
	if err := database.Where("org_id = ? AND root_resource_type = ? AND root_resource_id = ? AND state = ?", orgID, domain.LifecycleResourceFolder, rootID, domain.LifecycleDeleted).
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
	if linkedMemberCount != 3 {
		t.Fatalf("expected the active descendants to retain the root unit for later restore, got %d links", linkedMemberCount)
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

	var completedLifecycleJob domain.LifecycleJob
	if err := database.Where("legacy_folder_deletion_job_id = ?", queued.ID).First(&completedLifecycleJob).Error; err != nil {
		t.Fatalf("load completed V8 lifecycle job: %v", err)
	}
	if completedLifecycleJob.Status != domain.LifecycleJobSucceeded || completedLifecycleJob.Attempts != 1 {
		t.Fatalf("expected V8 worker ownership and successful completion, got %#v", completedLifecycleJob)
	}
	var completedCheckpoint folderDeletionCheckpointView
	if err := json.Unmarshal(completedLifecycleJob.Checkpoint, &completedCheckpoint); err != nil {
		t.Fatalf("decode completed lifecycle checkpoint: %v", err)
	}
	if completedCheckpoint.MetadataBatches != 1 || completedCheckpoint.MetadataRows != 2 || completedCheckpoint.FolderBatches != 1 || completedCheckpoint.FolderRows != 2 {
		t.Fatalf("unexpected durable V8 delete checkpoint: %#v", completedCheckpoint)
	}
}

type folderDeletionCheckpointView struct {
	RootVisibilityClosed bool  `json:"root_visibility_closed"`
	MetadataBatches      int64 `json:"metadata_batches"`
	MetadataRows         int64 `json:"metadata_rows"`
	FolderBatches        int64 `json:"folder_batches"`
	FolderRows           int64 `json:"folder_rows"`
}

func TestFolderDeletionRepositoryAdoptsLegacyQueuedJob_PostgresIntegration(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}

	ctx := context.Background()
	orgID := uuid.NewString()
	userID := uuid.NewString()
	rootID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	t.Cleanup(func() {
		_ = database.Exec("DELETE FROM asset_lifecycle_jobs WHERE org_id = ?", orgID).Error
		_ = database.Exec("DELETE FROM folder_deletion_jobs WHERE org_id = ?", orgID).Error
		_ = database.Exec("DELETE FROM asset_lifecycle_units WHERE org_id = ?", orgID).Error
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
	if err := database.Exec("INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, NULL)", rootID, orgID, rootPath, "Legacy queued root", userID).Error; err != nil {
		t.Fatalf("seed legacy root: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	legacy := domain.FolderDeletionJob{
		ID:                uuid.NewString(),
		OrgID:             orgID,
		RootFolderID:      rootID,
		RootPath:          rootPath,
		RequestedBy:       userID,
		Status:            domain.FolderDeletionQueued,
		ActiveFolderCount: 1,
		Attempts:          2,
		NextRunAt:         &now,
		QueuedAt:          &now,
	}
	if err := database.Create(&legacy).Error; err != nil {
		t.Fatalf("seed V5 queued job without a V8 mapping: %v", err)
	}

	repository := repository.NewFolderDeletionRepository(database)
	claimed, err := repository.ClaimNextFolderDeletionJob(ctx, "legacy-adoption-worker")
	if err != nil || claimed == nil || claimed.ID != legacy.ID {
		t.Fatalf("adopt and claim queued V5 job: job=%#v err=%v", claimed, err)
	}
	var lifecycleJob domain.LifecycleJob
	if err := database.Where("legacy_folder_deletion_job_id = ?", legacy.ID).First(&lifecycleJob).Error; err != nil {
		t.Fatalf("load adopted V8 job: %v", err)
	}
	if lifecycleJob.Status != domain.LifecycleJobRunning || lifecycleJob.Attempts != 3 || lifecycleJob.LeaseOwner == nil || *lifecycleJob.LeaseOwner != "legacy-adoption-worker" {
		t.Fatalf("expected V8 job to own adopted legacy work, got %#v", lifecycleJob)
	}
	var hiddenRootCount int64
	if err := database.Raw("SELECT COUNT(*) FROM folders WHERE id = ? AND deleted_at IS NOT NULL AND lifecycle_unit_id IS NOT NULL", rootID).Scan(&hiddenRootCount).Error; err != nil {
		t.Fatalf("count adopted root visibility gate: %v", err)
	}
	if hiddenRootCount != 1 {
		t.Fatalf("expected adoption to hide and link the legacy root, got %d", hiddenRootCount)
	}
	if err := repository.ProcessFolderDeletionJob(ctx, legacy.ID, "legacy-adoption-worker"); err != nil {
		t.Fatalf("complete adopted V5 job through V8 worker ownership: %v", err)
	}
	if err := database.Where("id = ?", lifecycleJob.ID).First(&lifecycleJob).Error; err != nil {
		t.Fatalf("reload adopted V8 job: %v", err)
	}
	if lifecycleJob.Status != domain.LifecycleJobSucceeded {
		t.Fatalf("expected adopted V8 job completion, got %s", lifecycleJob.Status)
	}
}
