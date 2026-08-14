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

func TestLifecycleRestoreRepository_PostgresIntegration_RespectsNestedUnitBoundaryAndVisibilityGate(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}

	ctx := context.Background()
	orgID := uuid.NewString()
	userID := uuid.NewString()
	rootID := uuid.NewString()
	childID := uuid.NewString()
	independentChildID := uuid.NewString()
	rootPath := strings.ReplaceAll(rootID, "-", "")
	childPath := rootPath + "." + strings.ReplaceAll(childID, "-", "")
	independentChildPath := rootPath + "." + strings.ReplaceAll(independentChildID, "-", "")
	unitAID := uuid.NewString()
	unitBID := uuid.NewString()
	metadataAID := uuid.NewString()
	metadataBID := uuid.NewString()
	activeCollisionID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	retention := now.AddDate(0, 0, 30)
	rootParent := rootPath

	t.Cleanup(func() {
		_ = database.Exec("DELETE FROM asset_lifecycle_jobs WHERE org_id = ?", orgID).Error
		_ = database.Unscoped().Exec("DELETE FROM metadata_items USING folders WHERE metadata_items.folder_id = folders.id AND folders.org_id = ?", orgID).Error
		_ = database.Unscoped().Exec("DELETE FROM folders WHERE org_id = ?", orgID).Error
		_ = database.Exec("DELETE FROM asset_lifecycle_units WHERE org_id = ?", orgID).Error
		_ = database.Exec("DELETE FROM user_ref WHERE user_id = ?", userID).Error
		_ = database.Exec("DELETE FROM organization_ref WHERE org_id = ?", orgID).Error
	})

	if err := database.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization reference: %v", err)
	}
	if err := database.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user reference: %v", err)
	}
	for _, unit := range []domain.LifecycleUnit{
		{
			ID:                unitAID,
			OrgID:             orgID,
			RootResourceType:  domain.LifecycleResourceFolder,
			RootResourceID:    rootID,
			RootFolderPath:    rootPath,
			State:             domain.LifecycleDeleted,
			RequestedBy:       userID,
			DeleteCompletedAt: &now,
			RetentionUntil:    &retention,
		},
		{
			ID:                 unitBID,
			OrgID:              orgID,
			RootResourceType:   domain.LifecycleResourceFolder,
			RootResourceID:     independentChildID,
			RootFolderPath:     independentChildPath,
			OriginalParentPath: &rootParent,
			State:              domain.LifecycleDeleted,
			RequestedBy:        userID,
			DeleteCompletedAt:  &now,
			RetentionUntil:     &retention,
		},
	} {
		if err := database.Create(&unit).Error; err != nil {
			t.Fatalf("seed lifecycle unit %s: %v", unit.ID, err)
		}
	}

	duplicateRootID := uuid.NewString()
	duplicateRootPath := strings.ReplaceAll(duplicateRootID, "-", "")
	if err := database.Exec(`
		INSERT INTO folders (id, org_id, path, name, created_by, deleted_at, lifecycle_unit_id)
		VALUES
		  (?, ?, ?::ltree, ?, ?, NULL, NULL),
		  (?, ?, ?::ltree, ?, ?, NULL, NULL),
		  (?, ?, ?::ltree, ?, ?, NULL, NULL)
	`,
		rootID, orgID, rootPath, "Project", userID,
		childID, orgID, childPath, "Design", userID,
		independentChildID, orgID, independentChildPath, "Archive", userID,
	).Error; err != nil {
		t.Fatalf("seed folder lifecycle roots and members: %v", err)
	}
	if err := database.Exec(`
		INSERT INTO metadata_items (id, folder_id, title, created_by, deleted_at, lifecycle_unit_id)
		VALUES
		  (?, ?, ?, ?, NULL, NULL),
		  (?, ?, ?, ?, NULL, NULL),
		  (?, ?, ?, ?, NULL, NULL)
	`,
		metadataAID, childID, "Asset", userID,
		metadataBID, independentChildID, "Independent asset", userID,
		activeCollisionID, childID, "Asset", userID,
	).Error; err != nil {
		t.Fatalf("seed metadata lifecycle members and collision: %v", err)
	}
	// Worker-compatible ordering: the source tree and its metadata exist while
	// active, then the DELETE job tombstones and assigns members. This honors
	// the database trigger that prevents inserting a new active item below a
	// tombstoned ancestor.
	if err := database.Exec("UPDATE metadata_items SET deleted_at = ?, lifecycle_unit_id = ? WHERE id = ?", now, unitAID, metadataAID).Error; err != nil {
		t.Fatalf("assign A metadata member: %v", err)
	}
	if err := database.Exec("UPDATE metadata_items SET deleted_at = ?, lifecycle_unit_id = ? WHERE id = ?", now, unitBID, metadataBID).Error; err != nil {
		t.Fatalf("assign B metadata member: %v", err)
	}
	if err := database.Exec("UPDATE folders SET deleted_at = ?, lifecycle_unit_id = ? WHERE id IN (?, ?)", now, unitAID, rootID, childID).Error; err != nil {
		t.Fatalf("assign A folder members: %v", err)
	}
	if err := database.Exec("UPDATE folders SET deleted_at = ?, lifecycle_unit_id = ? WHERE id = ?", now, unitBID, independentChildID).Error; err != nil {
		t.Fatalf("tombstone and assign lifecycle members: %v", err)
	}
	if err := database.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)", duplicateRootID, orgID, duplicateRootPath, "Project", userID).Error; err != nil {
		t.Fatalf("seed active root collision after source root is tombstoned: %v", err)
	}

	assets := repository.NewAssetRepository(database)
	factA, err := assets.GetLifecycleRestoreAuthorizationFact(ctx, orgID, unitAID)
	if err != nil {
		t.Fatalf("load trusted lifecycle restore fact for A: %v", err)
	}
	if factA.RootResourceType != domain.LifecycleResourceFolder || factA.RootResourceID != rootID || factA.RootFolderID != rootID || factA.RootFolderPath != rootPath {
		t.Fatalf("unexpected lifecycle root fact: %#v", factA)
	}
	if _, err := assets.QueueLifecycleRestore(ctx, orgID, userID, unitBID); !errors.Is(err, domain.ErrRestoreParentDeleted) {
		t.Fatalf("expected standalone B restore to be blocked while A is deleted, got %v", err)
	}
	var blockedBJobs int64
	if err := database.Model(&domain.LifecycleJob{}).Where("unit_id = ?", unitBID).Count(&blockedBJobs).Error; err != nil {
		t.Fatalf("count B restore jobs after parent guard: %v", err)
	}
	if blockedBJobs != 0 {
		t.Fatalf("parent guard must not create a B job, got %d", blockedBJobs)
	}

	queued, err := assets.QueueLifecycleRestore(ctx, orgID, userID, unitAID)
	if err != nil {
		t.Fatalf("queue A restore: %v", err)
	}
	if queued.Status != domain.LifecycleJobQueued || queued.Operation != domain.LifecycleJobRestore {
		t.Fatalf("expected queued native restore job, got %#v", queued)
	}
	if queued.RootFolderID != rootID {
		t.Fatalf("expected queued job to retain its authorization root folder, got %#v", queued)
	}
	loadedQueuedJob, err := assets.GetLifecycleJob(ctx, orgID, queued.ID)
	if err != nil {
		t.Fatalf("load lifecycle job by same-org id: %v", err)
	}
	if loadedQueuedJob.ID != queued.ID || loadedQueuedJob.RootFolderID != rootID {
		t.Fatalf("unexpected trusted lifecycle job: %#v", loadedQueuedJob)
	}
	var queuedUnit domain.LifecycleUnit
	if err := database.Where("id = ?", unitAID).First(&queuedUnit).Error; err != nil {
		t.Fatalf("reload queued unit A: %v", err)
	}
	if queuedUnit.State != domain.LifecycleRestoreQueued {
		t.Fatalf("expected U-A to enter RESTORE_QUEUED before worker processing, got %s", queuedUnit.State)
	}
	if _, err := assets.GetFolderByID(ctx, orgID, rootID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("root must remain invisible while restore is queued, got %v", err)
	}

	worker := repository.NewLifecycleJobWorkerRepository(database)
	claimed, err := worker.ClaimNextLifecycleJob(ctx, "restore-integration-worker")
	if err != nil || claimed == nil || claimed.ID != queued.ID || claimed.Operation != domain.LifecycleJobRestore {
		t.Fatalf("claim queued restore: job=%#v err=%v", claimed, err)
	}
	secondClaim, err := worker.ClaimNextLifecycleJob(ctx, "second-restore-integration-worker")
	if err != nil {
		t.Fatalf("claim while the first worker owns the restore lease: %v", err)
	}
	if secondClaim != nil {
		t.Fatalf("expected exactly one worker to claim the restore job, got %#v", secondClaim)
	}
	if err := worker.ProcessLifecycleJob(ctx, claimed.ID, "restore-integration-worker"); err != nil {
		t.Fatalf("process restore: %v", err)
	}

	var restoredJob domain.LifecycleJob
	if err := database.Where("id = ?", queued.ID).First(&restoredJob).Error; err != nil {
		t.Fatalf("reload restore job: %v", err)
	}
	if restoredJob.Status != domain.LifecycleJobSucceeded || restoredJob.CompletedAt == nil {
		t.Fatalf("expected successful durable restore job, got %#v", restoredJob)
	}
	var checkpoint struct {
		FolderBatches   int64 `json:"folder_batches"`
		FolderRows      int64 `json:"folder_rows"`
		MetadataBatches int64 `json:"metadata_batches"`
		MetadataRows    int64 `json:"metadata_rows"`
	}
	if err := json.Unmarshal(restoredJob.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode restore checkpoint: %v", err)
	}
	if checkpoint.FolderBatches != 1 || checkpoint.FolderRows != 1 || checkpoint.MetadataBatches != 1 || checkpoint.MetadataRows != 1 {
		t.Fatalf("unexpected bounded restore checkpoint: %#v", checkpoint)
	}

	for _, id := range []string{rootID, childID} {
		var count int64
		if err := database.Raw("SELECT COUNT(*) FROM folders WHERE id = ? AND deleted_at IS NULL AND lifecycle_unit_id IS NULL", id).Scan(&count).Error; err != nil {
			t.Fatalf("count restored folder %s: %v", id, err)
		}
		if count != 1 {
			t.Fatalf("expected A member %s to be active and released from U-A, got %d", id, count)
		}
	}
	var restoredMetadata domain.MetadataItem
	if err := database.Where("id = ?", metadataAID).First(&restoredMetadata).Error; err != nil {
		t.Fatalf("load restored metadata A member: %v", err)
	}
	if restoredMetadata.Title != "Asset (1)" || restoredMetadata.DeletedAt.Valid {
		t.Fatalf("expected metadata collision suffix and active A member, got %#v", restoredMetadata)
	}
	var restoredRoot domain.Folder
	if err := database.Where("id = ?", rootID).First(&restoredRoot).Error; err != nil {
		t.Fatalf("load restored root: %v", err)
	}
	if restoredRoot.Name != "Project (1)" {
		t.Fatalf("expected root display-name collision suffix, got %q", restoredRoot.Name)
	}
	if _, err := assets.GetFolderByID(ctx, orgID, rootID); err != nil {
		t.Fatalf("root must become visible only after complete restore: %v", err)
	}

	var restoredUnit domain.LifecycleUnit
	if err := database.Where("id = ?", unitAID).First(&restoredUnit).Error; err != nil {
		t.Fatalf("reload restored unit A: %v", err)
	}
	if restoredUnit.State != domain.LifecycleRestored {
		t.Fatalf("expected U-A to close as RESTORED, got %s", restoredUnit.State)
	}
	var remainingBFolders, remainingBMetadata int64
	if err := database.Raw("SELECT COUNT(*) FROM folders WHERE id = ? AND deleted_at IS NOT NULL AND lifecycle_unit_id = ?", independentChildID, unitBID).Scan(&remainingBFolders).Error; err != nil {
		t.Fatalf("count independent B folder: %v", err)
	}
	if err := database.Raw("SELECT COUNT(*) FROM metadata_items WHERE id = ? AND deleted_at IS NOT NULL AND lifecycle_unit_id = ?", metadataBID, unitBID).Scan(&remainingBMetadata).Error; err != nil {
		t.Fatalf("count independent B metadata: %v", err)
	}
	if remainingBFolders != 1 || remainingBMetadata != 1 {
		t.Fatalf("restore A must not restore independently deleted B, folders=%d metadata=%d", remainingBFolders, remainingBMetadata)
	}
}
