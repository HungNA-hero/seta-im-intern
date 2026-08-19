package repository_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/storage"
	"seta-im-intern/go-asset-core/internal/usecase"
)

func TestLifecyclePurgeRepository_PostgresIntegration_TeardownMetadataAfterManifestedObjects(t *testing.T) {
	fixture := newTeardownFixture(t)
	ctx := context.Background()

	var folder struct {
		ID   string
		Path string
	}
	if err := fixture.db.Raw(`SELECT folder.id, folder.path
		FROM folders AS folder JOIN metadata_items AS metadata ON metadata.folder_id = folder.id
		WHERE metadata.id = ?`, fixture.assetID).Scan(&folder).Error; err != nil {
		t.Fatalf("read metadata parent folder: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	unitID := uuid.NewString()
	unit := domain.LifecycleUnit{
		ID:                unitID,
		OrgID:             fixture.orgID,
		RootResourceType:  domain.LifecycleResourceMetadata,
		RootResourceID:    fixture.assetID,
		RootFolderPath:    folder.Path,
		OriginalFolderID:  &folder.ID,
		State:             domain.LifecyclePurgeQueued,
		RequestedBy:       fixture.userID,
		DeleteCompletedAt: &now,
	}
	if err := fixture.db.Create(&unit).Error; err != nil {
		t.Fatalf("seed purge lifecycle unit: %v", err)
	}
	if err := fixture.db.Exec(`UPDATE metadata_items
		SET deleted_at = ?, lifecycle_unit_id = ? WHERE id = ?`, now, unit.ID, fixture.assetID).Error; err != nil {
		t.Fatalf("tombstone purge metadata: %v", err)
	}
	jobID := uuid.NewString()
	job := domain.LifecycleJob{
		ID:               jobID,
		OrgID:            fixture.orgID,
		UnitID:           &unitID,
		RootResourceType: domain.LifecycleResourceMetadata,
		RootResourceID:   fixture.assetID,
		RootFolderID:     folder.ID,
		RootFolderPath:   folder.Path,
		InitiatedByType:  domain.LifecycleJobInitiatorSystem,
		Operation:        domain.LifecycleJobPurge,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       []byte(`{}`),
		NextRunAt:        &now,
		QueuedAt:         &now,
	}
	if err := fixture.db.Create(&job).Error; err != nil {
		t.Fatalf("seed purge lifecycle job: %v", err)
	}

	worker := repository.NewLifecycleJobWorkerRepository(fixture.db)
	claimed, err := worker.ClaimNextLifecycleJob(ctx, "purge-integration-worker")
	if err != nil || claimed == nil || claimed.ID != job.ID || claimed.Operation != domain.LifecycleJobPurge {
		t.Fatalf("claim purge job: job=%#v err=%v", claimed, err)
	}

	purge := repository.NewLifecyclePurgeRepository(fixture.db)
	work, err := purge.NextLifecyclePurgeAsset(ctx, job.ID, "purge-integration-worker")
	if err != nil || work == nil || work.AssetID != fixture.assetID {
		t.Fatalf("prepare asset purge: work=%#v err=%v", work, err)
	}
	if len(work.ObjectKeys) != 6 {
		t.Fatalf("manifested object keys = %d, want raw plus two outputs for two versions", len(work.ObjectKeys))
	}
	if err := purge.MarkLifecyclePurgeObjectsDeleted(ctx, job.ID, "purge-integration-worker", fixture.assetID, work.ObjectKeys); err != nil {
		t.Fatalf("mark storage objects deleted: %v", err)
	}
	if err := purge.FinalizeLifecyclePurgeAsset(ctx, job.ID, "purge-integration-worker", fixture.assetID); err != nil {
		t.Fatalf("teardown metadata after storage: %v", err)
	}
	done, err := purge.FinalizeLifecyclePurgeJob(ctx, job.ID, "purge-integration-worker")
	if err != nil || !done {
		t.Fatalf("finish metadata purge: done=%t err=%v", done, err)
	}

	for table, query := range map[string]string{
		"metadata_items":        "SELECT count(*) FROM metadata_items WHERE id = ?",
		"asset_media_versions":  "SELECT count(*) FROM asset_media_versions WHERE asset_id = ?",
		"media_upload_sessions": "SELECT count(*) FROM media_upload_sessions WHERE asset_id = ?",
		"media_processing_jobs": "SELECT count(*) FROM media_processing_jobs WHERE asset_id = ?",
	} {
		var count int64
		if err := fixture.db.Raw(query, fixture.assetID).Scan(&count).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s rows = %d, want zero", table, count)
		}
	}
	var persistedJob domain.LifecycleJob
	if err := fixture.db.Where("id = ?", job.ID).First(&persistedJob).Error; err != nil {
		t.Fatalf("read completed purge job: %v", err)
	}
	if persistedJob.Status != domain.LifecycleJobSucceeded || persistedJob.CompletedAt == nil {
		t.Errorf("purge job = %#v, want succeeded with completion time", persistedJob)
	}
	var persistedUnit domain.LifecycleUnit
	if err := fixture.db.Where("id = ?", unit.ID).First(&persistedUnit).Error; err != nil {
		t.Fatalf("read completed purge unit: %v", err)
	}
	if persistedUnit.State != domain.LifecyclePurged {
		t.Errorf("purge unit state = %q, want %q", persistedUnit.State, domain.LifecyclePurged)
	}
}

func TestLifecyclePurgeRepository_PostgresIntegration_PurgesFolderMembersAndLeaves(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	ctx := context.Background()
	orgID := uuid.NewString()
	userID := uuid.NewString()
	rootID := uuid.NewString()
	childID := uuid.NewString()
	metadataAID := uuid.NewString()
	metadataBID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	childPath := rootPath + "." + strings.ReplaceAll(childID, "-", "")
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	unitID := uuid.NewString()
	unit := domain.LifecycleUnit{
		ID:                unitID,
		OrgID:             orgID,
		RootResourceType:  domain.LifecycleResourceFolder,
		RootResourceID:    rootID,
		RootFolderPath:    rootPath,
		State:             domain.LifecyclePurgeQueued,
		RequestedBy:       userID,
		DeleteCompletedAt: &now,
	}
	if err := tx.Create(&unit).Error; err != nil {
		t.Fatalf("seed purge folder lifecycle unit: %v", err)
	}
	if err := tx.Exec(`
		INSERT INTO folders (id, org_id, path, name, created_by, deleted_at, lifecycle_unit_id)
		VALUES (?, ?, ?::ltree, ?, ?, NULL, NULL), (?, ?, ?::ltree, ?, ?, NULL, NULL)`,
		rootID, orgID, rootPath, "root", userID,
		childID, orgID, childPath, "child", userID,
	).Error; err != nil {
		t.Fatalf("seed tombstoned folder tree: %v", err)
	}
	if err := tx.Exec(`
		INSERT INTO metadata_items (id, folder_id, title, created_by, deleted_at, lifecycle_unit_id)
		VALUES (?, ?, ?, ?, NULL, NULL), (?, ?, ?, ?, NULL, NULL)`,
		metadataAID, rootID, "root asset", userID,
		metadataBID, childID, "child asset", userID,
	).Error; err != nil {
		t.Fatalf("seed tombstoned metadata members: %v", err)
	}
	// This is the same order as the live delete worker: members first, then
	// folders. The DB trigger intentionally refuses the reverse order.
	if err := tx.Exec(`UPDATE metadata_items SET deleted_at = ?, lifecycle_unit_id = ?
		WHERE id IN (?, ?)`, now, unitID, metadataAID, metadataBID).Error; err != nil {
		t.Fatalf("tombstone metadata members: %v", err)
	}
	if err := tx.Exec(`UPDATE folders SET deleted_at = ?, lifecycle_unit_id = ?
		WHERE id IN (?, ?)`, now, unitID, rootID, childID).Error; err != nil {
		t.Fatalf("tombstone folder members: %v", err)
	}
	jobID := uuid.NewString()
	job := domain.LifecycleJob{
		ID:               jobID,
		OrgID:            orgID,
		UnitID:           &unitID,
		RootResourceType: domain.LifecycleResourceFolder,
		RootResourceID:   rootID,
		RootFolderID:     rootID,
		RootFolderPath:   rootPath,
		InitiatedByType:  domain.LifecycleJobInitiatorSystem,
		Operation:        domain.LifecycleJobPurge,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       []byte(`{}`),
		NextRunAt:        &now,
		QueuedAt:         &now,
	}
	if err := tx.Create(&job).Error; err != nil {
		t.Fatalf("seed folder purge job: %v", err)
	}
	worker := repository.NewLifecycleJobWorkerRepository(tx)
	if claimed, err := worker.ClaimNextLifecycleJob(ctx, "folder-purge-worker"); err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim folder purge job: job=%#v err=%v", claimed, err)
	}
	purge := repository.NewLifecyclePurgeRepository(tx)
	for {
		work, err := purge.NextLifecyclePurgeAsset(ctx, job.ID, "folder-purge-worker")
		if err != nil {
			t.Fatalf("select next folder-member purge: %v", err)
		}
		if work != nil {
			if len(work.ObjectKeys) != 0 {
				t.Fatalf("empty-media folder member unexpectedly has %d objects", len(work.ObjectKeys))
			}
			if err := purge.FinalizeLifecyclePurgeAsset(ctx, job.ID, "folder-purge-worker", work.AssetID); err != nil {
				t.Fatalf("purge folder-member asset %s: %v", work.AssetID, err)
			}
			continue
		}
		done, err := purge.FinalizeLifecyclePurgeJob(ctx, job.ID, "folder-purge-worker")
		if err != nil {
			t.Fatalf("purge folder leaf / complete job: %v", err)
		}
		if done {
			break
		}
	}

	var metadataCount int64
	if err := tx.Raw("SELECT count(*) FROM metadata_items WHERE id IN (?, ?)", metadataAID, metadataBID).Scan(&metadataCount).Error; err != nil {
		t.Fatalf("count metadata rows: %v", err)
	}
	if metadataCount != 0 {
		t.Errorf("metadata_items rows = %d, want zero", metadataCount)
	}
	var folderCount int64
	if err := tx.Raw("SELECT count(*) FROM folders WHERE id IN (?, ?)", rootID, childID).Scan(&folderCount).Error; err != nil {
		t.Fatalf("count folder rows: %v", err)
	}
	if folderCount != 0 {
		t.Errorf("folders rows = %d, want zero", folderCount)
	}
}

func TestMediaJobStore_PostgresIntegration_RefusesADeletedAsset(t *testing.T) {
	fixture := newMediaJobFixture(t)
	if err := fixture.db.Exec("UPDATE metadata_items SET deleted_at = statement_timestamp() WHERE id = ?", fixture.assetID).Error; err != nil {
		t.Fatalf("tombstone media asset: %v", err)
	}
	if _, _, err := fixture.store().ClaimJob(context.Background(), fixture.jobID, "media-worker-after-delete"); err == nil {
		t.Fatal("a soft-deleted asset must not be claimed for new media work")
	}
}

func TestLifecyclePurgeJob_MaxAttemptsMarksUnitFailed_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	orgID := uuid.NewString()
	userID := uuid.NewString()
	rootID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	unit := domain.LifecycleUnit{
		OrgID:             orgID,
		RootResourceType:  domain.LifecycleResourceFolder,
		RootResourceID:    rootID,
		RootFolderPath:    rootPath,
		State:             domain.LifecyclePurgeQueued,
		RequestedBy:       userID,
		DeleteCompletedAt: &now,
	}
	if err := tx.Create(&unit).Error; err != nil {
		t.Fatalf("seed lifecycle unit: %v", err)
	}
	unitID := unit.ID
	job := domain.LifecycleJob{
		OrgID:            orgID,
		UnitID:           &unitID,
		RootResourceType: domain.LifecycleResourceFolder,
		RootResourceID:   rootID,
		RootFolderID:     rootID,
		RootFolderPath:   rootPath,
		InitiatedByType:  domain.LifecycleJobInitiatorSystem,
		Operation:        domain.LifecycleJobPurge,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       []byte(`{}`),
		Attempts:         domain.FolderDeletionMaxAttempts,
		NextRunAt:        &now,
		QueuedAt:         &now,
	}
	if err := tx.Create(&job).Error; err != nil {
		t.Fatalf("seed exhausted purge job: %v", err)
	}

	claimed, err := repository.NewLifecycleJobWorkerRepository(tx).ClaimNextLifecycleJob(context.Background(), "purge-worker")
	if err != nil {
		t.Fatalf("claim exhausted purge job: %v", err)
	}
	if claimed != nil {
		t.Fatalf("claimed exhausted purge job = %#v, want nil", claimed)
	}
	var persistedJob domain.LifecycleJob
	if err := tx.Where("id = ?", job.ID).First(&persistedJob).Error; err != nil {
		t.Fatalf("read failed purge job: %v", err)
	}
	if persistedJob.Status != domain.LifecycleJobFailed || persistedJob.CompletedAt == nil {
		t.Errorf("purge job = %#v, want FAILED with completion time", persistedJob)
	}
	assertLifecycleUnitState(t, tx, unit.ID, domain.LifecycleFailed)
}

func TestLifecyclePurgeJob_FailureRequeuesUnit_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	orgID := uuid.NewString()
	userID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	unit := createRetentionCleanupUnit(t, tx, orgID, userID, "purge_retry", now.Add(-time.Minute))
	if err := tx.Model(&domain.LifecycleUnit{}).Where("id = ?", unit.ID).Update("state", domain.LifecyclePurgeQueued).Error; err != nil {
		t.Fatalf("queue lifecycle unit for purge: %v", err)
	}
	unitID := unit.ID
	job := domain.LifecycleJob{
		OrgID:            orgID,
		UnitID:           &unitID,
		RootResourceType: unit.RootResourceType,
		RootResourceID:   unit.RootResourceID,
		RootFolderID:     unit.RootResourceID,
		RootFolderPath:   unit.RootFolderPath,
		InitiatedByType:  domain.LifecycleJobInitiatorSystem,
		Operation:        domain.LifecycleJobPurge,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       []byte(`{}`),
		NextRunAt:        &now,
		QueuedAt:         &now,
	}
	if err := tx.Create(&job).Error; err != nil {
		t.Fatalf("seed purge job: %v", err)
	}

	worker := repository.NewLifecycleJobWorkerRepository(tx)
	if claimed, err := worker.ClaimNextLifecycleJob(context.Background(), "purge-retry-worker"); err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim purge job: job=%#v err=%v", claimed, err)
	}
	if err := worker.FailLifecycleJob(context.Background(), job.ID, "purge-retry-worker"); err != nil {
		t.Fatalf("requeue failed purge job: %v", err)
	}

	var persisted domain.LifecycleJob
	if err := tx.Where("id = ?", job.ID).First(&persisted).Error; err != nil {
		t.Fatalf("read requeued purge job: %v", err)
	}
	if persisted.Status != domain.LifecycleJobQueued || persisted.NextRunAt == nil || persisted.LeaseOwner != nil {
		t.Errorf("purge job = %#v, want queued retry without lease", persisted)
	}
	assertLifecycleUnitState(t, tx, unit.ID, domain.LifecyclePurgeQueued)
}

// This is the storage-facing proof: the real MinIO client removes every
// manifest object before the repository deletes the metadata/media rows.
func TestLifecyclePurger_PostgresAndMinIOIntegration_DeletesObjectsBeforeAssetRows(t *testing.T) {
	fixture := newTeardownFixture(t)
	ctx := context.Background()
	var folder struct {
		ID   string
		Path string
	}
	if err := fixture.db.Raw(`SELECT folder.id, folder.path
		FROM folders AS folder JOIN metadata_items AS metadata ON metadata.folder_id = folder.id
		WHERE metadata.id = ?`, fixture.assetID).Scan(&folder).Error; err != nil {
		t.Fatalf("read metadata parent folder: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	unitID := uuid.NewString()
	unit := domain.LifecycleUnit{
		ID:                unitID,
		OrgID:             fixture.orgID,
		RootResourceType:  domain.LifecycleResourceMetadata,
		RootResourceID:    fixture.assetID,
		RootFolderPath:    folder.Path,
		OriginalFolderID:  &folder.ID,
		State:             domain.LifecyclePurgeQueued,
		RequestedBy:       fixture.userID,
		DeleteCompletedAt: &now,
	}
	if err := fixture.db.Create(&unit).Error; err != nil {
		t.Fatalf("seed lifecycle unit: %v", err)
	}
	if err := fixture.db.Exec(`UPDATE metadata_items SET deleted_at = ?, lifecycle_unit_id = ? WHERE id = ?`, now, unitID, fixture.assetID).Error; err != nil {
		t.Fatalf("tombstone asset: %v", err)
	}
	jobID := uuid.NewString()
	job := domain.LifecycleJob{
		ID:               jobID,
		OrgID:            fixture.orgID,
		UnitID:           &unitID,
		RootResourceType: domain.LifecycleResourceMetadata,
		RootResourceID:   fixture.assetID,
		RootFolderID:     folder.ID,
		RootFolderPath:   folder.Path,
		InitiatedByType:  domain.LifecycleJobInitiatorSystem,
		Operation:        domain.LifecycleJobPurge,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       []byte(`{}`),
		NextRunAt:        &now,
		QueuedAt:         &now,
	}
	if err := fixture.db.Create(&job).Error; err != nil {
		t.Fatalf("seed lifecycle job: %v", err)
	}
	workerID := "minio-purge-worker"
	worker := repository.NewLifecycleJobWorkerRepository(fixture.db)
	if claimed, err := worker.ClaimNextLifecycleJob(ctx, workerID); err != nil || claimed == nil || claimed.ID != jobID {
		t.Fatalf("claim purge lifecycle job: job=%#v err=%v", claimed, err)
	}

	purgeStore := repository.NewLifecyclePurgeRepository(fixture.db)
	prepared, err := purgeStore.NextLifecyclePurgeAsset(ctx, jobID, workerID)
	if err != nil || prepared == nil || len(prepared.ObjectKeys) == 0 {
		t.Fatalf("prepare object manifest: work=%#v err=%v", prepared, err)
	}
	objects, err := storage.NewMinIOStorage(ctx, storage.MinIOConfig{
		Bucket: "seta-media", Region: "us-east-1", InternalEndpoint: "http://minio:9000", PublicEndpoint: "http://minio:9000",
		AccessKeyID: "seta-media-local", SecretAccessKey: "seta-media-local-secret", ChecksumSupported: true,
	})
	if err != nil {
		t.Fatalf("connect disposable MinIO: %v", err)
	}
	payload := []byte("purge integration object")
	digest := sha256.Sum256(payload)
	for _, key := range prepared.ObjectKeys {
		if err := objects.Put(ctx, domain.ObjectKey(key), bytes.NewReader(payload), domain.PutAttributes{
			ContentType: domain.MediaContentTypePNG, ChecksumSHA256: digest[:],
		}); err != nil {
			t.Fatalf("seed MinIO object %s: %v", key, err)
		}
	}

	if err := usecase.NewLifecyclePurger(purgeStore, objects).Process(ctx, jobID, workerID); err != nil {
		t.Fatalf("run lifecycle purger: %v", err)
	}
	for _, key := range prepared.ObjectKeys {
		if _, err := objects.Head(ctx, domain.ObjectKey(key)); !errors.Is(err, domain.ErrObjectNotFound) {
			t.Errorf("object %s still exists or cannot be checked after purge: %v", key, err)
		}
	}
	var assetRows int64
	if err := fixture.db.Raw("SELECT count(*) FROM metadata_items WHERE id = ?", fixture.assetID).Scan(&assetRows).Error; err != nil {
		t.Fatalf("count purged asset row: %v", err)
	}
	if assetRows != 0 {
		t.Errorf("metadata row count = %d, want zero after storage teardown", assetRows)
	}
}
