package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestLifecycleCleanupWorker_QueuesEligibleSystemPurgeJobsAndDefersNestedRoot_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	orgID := uuid.NewString()
	userID := uuid.NewString()
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization ref: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user ref: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	due := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	standalone := createRetentionCleanupUnit(t, tx, orgID, userID, "standalone", due)
	futureUnit := createRetentionCleanupUnit(t, tx, orgID, userID, "future", future)
	parent := createRetentionCleanupUnit(t, tx, orgID, userID, "parent", due)
	child := createRetentionCleanupUnit(t, tx, orgID, userID, "parent.child", due)

	scheduler := repository.NewLifecycleCleanupSchedulerRepository(tx)
	acquired, err := scheduler.AcquireDailyCleanupRun(context.Background(), "daily-retention-cleanup-worker-test", "scheduler-a", now, "Asia/Bangkok")
	if err != nil {
		t.Fatalf("create daily cleanup run: %v", err)
	}
	if !acquired.LeaseAcquired || !acquired.Created || acquired.Run == nil {
		t.Fatalf("expected cleanup run creation, got %+v", acquired)
	}

	worker := repository.NewLifecycleCleanupWorkerRepository(tx)
	batch, err := worker.ProcessNextLifecycleCleanupBatch(context.Background())
	if err != nil {
		t.Fatalf("queue eligible lifecycle purges: %v", err)
	}
	if batch == nil || batch.RunID != acquired.Run.ID || batch.QueuedJobs != 2 || batch.Completed {
		t.Fatalf("expected two queued system purge jobs, got %#v", batch)
	}

	var jobs []domain.LifecycleJob
	if err := tx.Where("operation = ?", domain.LifecycleJobPurge).Order("root_folder_path ASC").Find(&jobs).Error; err != nil {
		t.Fatalf("list queued purge jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected two purge jobs, got %d", len(jobs))
	}
	for _, job := range jobs {
		if job.InitiatedByType != domain.LifecycleJobInitiatorSystem || job.RequestedBy != nil {
			t.Fatalf("expected system-owned purge job without user identity, got %#v", job)
		}
		if job.Status != domain.LifecycleJobQueued {
			t.Fatalf("expected queued purge job, got %q", job.Status)
		}
	}

	assertLifecycleUnitState(t, tx, standalone.ID, domain.LifecyclePurgeQueued)
	assertLifecycleUnitState(t, tx, child.ID, domain.LifecyclePurgeQueued)
	assertLifecycleUnitState(t, tx, futureUnit.ID, domain.LifecycleDeleted)
	assertLifecycleUnitState(t, tx, parent.ID, domain.LifecycleDeleted)

	completed, err := worker.ProcessNextLifecycleCleanupBatch(context.Background())
	if err != nil {
		t.Fatalf("complete cleanup run after eligible batch: %v", err)
	}
	if completed == nil || !completed.Completed || completed.QueuedJobs != 0 {
		t.Fatalf("expected cleanup run to complete with nested parent deferred, got %#v", completed)
	}
	var run domain.LifecycleCleanupRun
	if err := tx.Where("id = ?", acquired.Run.ID).First(&run).Error; err != nil {
		t.Fatalf("read completed cleanup run: %v", err)
	}
	if run.Status != domain.LifecycleCleanupRunSucceeded || run.CompletedAt == nil {
		t.Fatalf("expected completed cleanup run, got %#v", run)
	}
	third, err := worker.ProcessNextLifecycleCleanupBatch(context.Background())
	if err != nil {
		t.Fatalf("process completed cleanup run: %v", err)
	}
	if third != nil {
		t.Fatalf("expected no duplicate dispatch after completion, got %#v", third)
	}
}

func TestLifecycleJobSchema_SeparatesUserAndSystemInitiators_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	orgID := uuid.NewString()
	userID := uuid.NewString()
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization ref: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user ref: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	systemUnit := createRetentionCleanupUnit(t, tx, orgID, userID, "system", now.Add(-time.Minute))
	systemUnitID := systemUnit.ID
	systemJob := domain.LifecycleJob{
		OrgID:            orgID,
		UnitID:           &systemUnitID,
		RootResourceType: domain.LifecycleResourceFolder,
		RootResourceID:   systemUnit.RootResourceID,
		RootFolderID:     systemUnit.RootResourceID,
		RootFolderPath:   systemUnit.RootFolderPath,
		InitiatedByType:  domain.LifecycleJobInitiatorSystem,
		Operation:        domain.LifecycleJobPurge,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       []byte(`{}`),
		QueuedAt:         &now,
	}
	if err := tx.Create(&systemJob).Error; err != nil {
		t.Fatalf("create system-owned purge job: %v", err)
	}

	invalidSystemUnit := createRetentionCleanupUnit(t, tx, orgID, userID, "invalid_system", now.Add(-time.Minute))
	invalidSystemUnitID := invalidSystemUnit.ID
	invalidSystem := systemJob
	invalidSystem.ID = ""
	invalidSystem.UnitID = &invalidSystemUnitID
	invalidSystem.RootResourceID = invalidSystemUnit.RootResourceID
	invalidSystem.RootFolderID = invalidSystemUnit.RootResourceID
	invalidSystem.RootFolderPath = invalidSystemUnit.RootFolderPath
	invalidSystem.RequestedBy = &userID
	if err := tx.Transaction(func(invalidTx *gorm.DB) error { return invalidTx.Create(&invalidSystem).Error }); err == nil {
		t.Fatal("expected SYSTEM initiator with a user ID to be rejected")
	}

	invalidUserUnit := createRetentionCleanupUnit(t, tx, orgID, userID, "invalid_user", now.Add(-time.Minute))
	invalidUserUnitID := invalidUserUnit.ID
	invalidUser := systemJob
	invalidUser.ID = ""
	invalidUser.UnitID = &invalidUserUnitID
	invalidUser.RootResourceID = invalidUserUnit.RootResourceID
	invalidUser.RootFolderID = invalidUserUnit.RootResourceID
	invalidUser.RootFolderPath = invalidUserUnit.RootFolderPath
	invalidUser.InitiatedByType = domain.LifecycleJobInitiatorUser
	if err := tx.Transaction(func(invalidTx *gorm.DB) error { return invalidTx.Create(&invalidUser).Error }); err == nil {
		t.Fatal("expected USER initiator without a user ID to be rejected")
	}
}

func createRetentionCleanupUnit(t *testing.T, tx *gorm.DB, orgID, userID, rootPath string, retentionUntil time.Time) domain.LifecycleUnit {
	t.Helper()
	deletedAt := retentionUntil.Add(-30 * 24 * time.Hour)
	unit := domain.LifecycleUnit{
		OrgID:             orgID,
		RootResourceType:  domain.LifecycleResourceFolder,
		RootResourceID:    uuid.NewString(),
		RootFolderPath:    rootPath,
		State:             domain.LifecycleDeleted,
		RequestedBy:       userID,
		DeleteCompletedAt: &deletedAt,
		RetentionUntil:    &retentionUntil,
	}
	if err := tx.Create(&unit).Error; err != nil {
		t.Fatalf("create retention cleanup lifecycle unit: %v", err)
	}
	return unit
}

func assertLifecycleUnitState(t *testing.T, tx *gorm.DB, unitID string, expected domain.LifecycleUnitState) {
	t.Helper()
	var unit domain.LifecycleUnit
	if err := tx.Where("id = ?", unitID).First(&unit).Error; err != nil {
		t.Fatalf("read lifecycle unit %s: %v", unitID, err)
	}
	if unit.State != expected {
		t.Fatalf("lifecycle unit %s state = %q, want %q", unitID, unit.State, expected)
	}
}
