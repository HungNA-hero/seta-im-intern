package repository_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
)

func TestLifecycleUnitSchema_PostgresIntegration(t *testing.T) {
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
	unit := domain.LifecycleUnit{
		OrgID:             orgID,
		RootResourceType:  domain.LifecycleResourceFolder,
		RootResourceID:    uuid.NewString(),
		RootFolderPath:    "root",
		State:             domain.LifecycleDeleted,
		RequestedBy:       userID,
		DeleteCompletedAt: &now,
		RetentionUntil:    pointerToTime(now.Add(30 * 24 * time.Hour)),
	}
	if err := tx.WithContext(ctx).Create(&unit).Error; err != nil {
		t.Fatalf("create lifecycle unit: %v", err)
	}

	duplicate := unit
	duplicate.ID = ""
	duplicateErr := tx.WithContext(ctx).Transaction(func(duplicateTx *gorm.DB) error {
		return duplicateTx.Create(&duplicate).Error
	})
	if duplicateErr == nil {
		t.Fatal("expected a second live unit for the same root to be rejected")
	}

	if err := tx.WithContext(ctx).Model(&domain.LifecycleUnit{}).
		Where("id = ? AND org_id = ?", unit.ID, orgID).
		Update("state", domain.LifecycleRestored).Error; err != nil {
		t.Fatalf("mark first unit restored: %v", err)
	}
	replacement := unit
	replacement.ID = ""
	if err := tx.WithContext(ctx).Create(&replacement).Error; err != nil {
		t.Fatalf("allow a new unit after the old unit is restored: %v", err)
	}

	if err := tx.WithContext(ctx).Model(&domain.LifecycleUnit{}).
		Where("id = ? AND org_id = ?", replacement.ID, orgID).
		Update("state", domain.LifecyclePurged).Error; err != nil {
		t.Fatalf("mark replacement unit purged: %v", err)
	}
	thirdUnit := unit
	thirdUnit.ID = ""
	if err := tx.WithContext(ctx).Create(&thirdUnit).Error; err != nil {
		t.Fatalf("allow a new unit after the old unit is purged: %v", err)
	}

	var crossOrgCount int64
	if err := tx.WithContext(ctx).Model(&domain.LifecycleUnit{}).
		Where("org_id = ?", otherOrgID).
		Count(&crossOrgCount).Error; err != nil {
		t.Fatalf("count other-org units: %v", err)
	}
	if crossOrgCount != 0 {
		t.Fatalf("expected no lifecycle units in another organization, got %d", crossOrgCount)
	}
}

func pointerToTime(value time.Time) *time.Time {
	return &value
}
