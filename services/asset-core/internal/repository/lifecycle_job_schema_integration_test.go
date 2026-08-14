package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"seta-im-intern/go-asset-core/internal/domain"
)

func TestLifecycleJobSchema_PostgresIntegration(t *testing.T) {
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
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && !errors.Is(err, gorm.ErrInvalidTransaction) {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})

	ctx := context.Background()
	orgID := uuid.NewString()
	otherOrgID := uuid.NewString()
	userID := uuid.NewString()
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?), (?)", orgID, otherOrgID).Error; err != nil {
		t.Fatalf("seed organization refs: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user ref: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	unit := createLifecycleUnit(t, tx, orgID, userID, "root")
	unitID := unit.ID
	queued := domain.LifecycleJob{
		OrgID:            orgID,
		UnitID:           &unitID,
		RootResourceType: domain.LifecycleResourceFolder,
		RootResourceID:   unit.RootResourceID,
		RootFolderID:     unit.RootResourceID,
		RootFolderPath:   unit.RootFolderPath,
		RequestedBy:      userID,
		Operation:        domain.LifecycleJobDelete,
		Status:           domain.LifecycleJobQueued,
		Checkpoint:       json.RawMessage(`{"last_folder_path":"root"}`),
		QueuedAt:         &now,
	}
	if err := tx.WithContext(ctx).Create(&queued).Error; err != nil {
		t.Fatalf("create executable lifecycle job: %v", err)
	}

	duplicate := queued
	duplicate.ID = ""
	duplicateErr := tx.WithContext(ctx).Transaction(func(duplicateTx *gorm.DB) error {
		return duplicateTx.Create(&duplicate).Error
	})
	if duplicateErr == nil {
		t.Fatal("expected a second active job for one lifecycle unit to be rejected")
	}

	previewExpiresAt := now.Add(15 * time.Minute)
	preview := domain.LifecycleJob{
		OrgID:            orgID,
		RootResourceType: domain.LifecycleResourceFolder,
		RootResourceID:   uuid.NewString(),
		RootFolderID:     uuid.NewString(),
		RootFolderPath:   "preview_root",
		RequestedBy:      userID,
		Operation:        domain.LifecycleJobDelete,
		Status:           domain.LifecycleJobPreviewed,
		Checkpoint:       json.RawMessage(`{}`),
		PreviewTokenHash: []byte("token-hash"),
		PreviewExpiresAt: &previewExpiresAt,
	}
	if err := tx.WithContext(ctx).Create(&preview).Error; err != nil {
		t.Fatalf("create delete preview without a lifecycle unit: %v", err)
	}

	crossOrgUnit := createLifecycleUnit(t, tx, orgID, userID, "cross_org_root")
	crossOrgUnitID := crossOrgUnit.ID
	wrongOrgJob := queued
	wrongOrgJob.ID = ""
	wrongOrgJob.OrgID = otherOrgID
	wrongOrgJob.UnitID = &crossOrgUnitID
	wrongOrgJob.RootResourceID = crossOrgUnit.RootResourceID
	wrongOrgJob.RootFolderID = crossOrgUnit.RootResourceID
	wrongOrgJob.RootFolderPath = crossOrgUnit.RootFolderPath
	wrongOrgErr := tx.WithContext(ctx).Transaction(func(wrongOrgTx *gorm.DB) error {
		return wrongOrgTx.Create(&wrongOrgJob).Error
	})
	if wrongOrgErr == nil {
		t.Fatal("expected a lifecycle job to reject a unit from another organization")
	}

	invalidCheckpoint := preview
	invalidCheckpoint.ID = ""
	invalidCheckpoint.Checkpoint = json.RawMessage(`[]`)
	invalidCheckpointErr := tx.WithContext(ctx).Transaction(func(invalidTx *gorm.DB) error {
		return invalidTx.Create(&invalidCheckpoint).Error
	})
	if invalidCheckpointErr == nil {
		t.Fatal("expected a lifecycle checkpoint JSON array to be rejected")
	}

	queuedWithoutUnit := queued
	queuedWithoutUnit.ID = ""
	queuedWithoutUnit.UnitID = nil
	queuedWithoutUnitErr := tx.WithContext(ctx).Transaction(func(invalidTx *gorm.DB) error {
		return invalidTx.Create(&queuedWithoutUnit).Error
	})
	if queuedWithoutUnitErr == nil {
		t.Fatal("expected an executable lifecycle job without a unit to be rejected")
	}
}

func createLifecycleUnit(t *testing.T, tx *gorm.DB, orgID, userID, rootPath string) domain.LifecycleUnit {
	t.Helper()
	unit := domain.LifecycleUnit{
		OrgID:            orgID,
		RootResourceType: domain.LifecycleResourceFolder,
		RootResourceID:   uuid.NewString(),
		RootFolderPath:   rootPath,
		State:            domain.LifecycleDeleteQueued,
		RequestedBy:      userID,
	}
	if err := tx.Create(&unit).Error; err != nil {
		t.Fatalf("create lifecycle unit: %v", err)
	}
	return unit
}
