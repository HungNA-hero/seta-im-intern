package repository_test

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestRecycleBinRepository_PostgresIntegration(t *testing.T) {
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
		t.Fatalf("seed organizations: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	deletedAt := time.Date(2026, time.August, 12, 3, 0, 0, 0, time.UTC)
	rootIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	sort.Strings(rootIDs)
	for index, rootID := range rootIDs {
		path := strings.ReplaceAll(rootID, "-", "")
		if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, ?)",
			rootID, orgID, path, "Deleted folder "+string(rune('A'+index)), userID, deletedAt).Error; err != nil {
			t.Fatalf("seed deleted folder %d: %v", index, err)
		}
		unit := domain.LifecycleUnit{
			OrgID:             orgID,
			RootResourceType:  domain.LifecycleResourceFolder,
			RootResourceID:    rootID,
			RootFolderPath:    path,
			State:             domain.LifecycleDeleted,
			RequestedBy:       userID,
			DeleteCompletedAt: &deletedAt,
		}
		if err := tx.Create(&unit).Error; err != nil {
			t.Fatalf("seed deleted lifecycle unit %d: %v", index, err)
		}
	}
	metadataFolderID := uuid.NewString()
	metadataFolderPath := strings.ReplaceAll(metadataFolderID, "-", "")
	metadataRootID := uuid.NewString()
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		metadataFolderID, orgID, metadataFolderPath, "Active metadata parent", userID).Error; err != nil {
		t.Fatalf("seed metadata parent: %v", err)
	}
	if err := tx.Exec("INSERT INTO metadata_items (id, folder_id, title, created_by, deleted_at) VALUES (?, ?, ?, ?, ?)",
		metadataRootID, metadataFolderID, "Deleted metadata root", userID, deletedAt).Error; err != nil {
		t.Fatalf("seed deleted metadata root: %v", err)
	}
	metadataUnit := domain.LifecycleUnit{
		OrgID:             orgID,
		RootResourceType:  domain.LifecycleResourceMetadata,
		RootResourceID:    metadataRootID,
		RootFolderPath:    metadataFolderPath,
		OriginalFolderID:  &metadataFolderID,
		State:             domain.LifecycleDeleted,
		RequestedBy:       userID,
		DeleteCompletedAt: &deletedAt,
	}
	if err := tx.Create(&metadataUnit).Error; err != nil {
		t.Fatalf("seed metadata lifecycle unit: %v", err)
	}

	restoredRootID := uuid.NewString()
	restoredRootPath := strings.ReplaceAll(restoredRootID, "-", "")
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, ?)",
		restoredRootID, orgID, restoredRootPath, "Restored history only", userID, deletedAt).Error; err != nil {
		t.Fatalf("seed restored-history root: %v", err)
	}
	restoredUnit := domain.LifecycleUnit{
		OrgID:             orgID,
		RootResourceType:  domain.LifecycleResourceFolder,
		RootResourceID:    restoredRootID,
		RootFolderPath:    restoredRootPath,
		State:             domain.LifecycleRestored,
		RequestedBy:       userID,
		DeleteCompletedAt: &deletedAt,
	}
	if err := tx.Create(&restoredUnit).Error; err != nil {
		t.Fatalf("seed restored-history lifecycle unit: %v", err)
	}

	otherRootID := uuid.NewString()
	otherPath := strings.ReplaceAll(otherRootID, "-", "")
	if err := tx.Exec("INSERT INTO folders (id, org_id, path, name, created_by, deleted_at) VALUES (?, ?, ?::ltree, ?, ?, ?)",
		otherRootID, otherOrgID, otherPath, "Other organization", userID, deletedAt).Error; err != nil {
		t.Fatalf("seed other-org folder: %v", err)
	}
	otherUnit := domain.LifecycleUnit{
		OrgID:             otherOrgID,
		RootResourceType:  domain.LifecycleResourceFolder,
		RootResourceID:    otherRootID,
		RootFolderPath:    otherPath,
		State:             domain.LifecycleDeleted,
		RequestedBy:       userID,
		DeleteCompletedAt: &deletedAt,
	}
	if err := tx.Create(&otherUnit).Error; err != nil {
		t.Fatalf("seed other-org lifecycle unit: %v", err)
	}

	repo := repository.NewAssetRepository(tx)
	firstPage, err := repo.ListRecycleBinEntries(ctx, orgID, domain.RecycleBinFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list first Recycle Bin page: %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("expected two first-page entries, got %#v", firstPage)
	}
	if firstPage[0].LifecycleUnitID >= firstPage[1].LifecycleUnitID {
		t.Fatalf("expected id ASC tie-breaker for same timestamp, got %#v", firstPage)
	}
	for _, entry := range firstPage {
		if (entry.ResourceType != domain.LifecycleResourceFolder && entry.ResourceType != domain.LifecycleResourceMetadata) || entry.DisplayName == "" || !entry.DeletedAt.Equal(deletedAt) {
			t.Fatalf("unexpected Recycle Bin entry: %#v", entry)
		}
	}

	afterDeletedAt := firstPage[1].DeletedAt
	afterLifecycleID := firstPage[1].LifecycleUnitID
	secondPage, err := repo.ListRecycleBinEntries(ctx, orgID, domain.RecycleBinFilter{
		Limit:            2,
		AfterDeletedAt:   &afterDeletedAt,
		AfterLifecycleID: &afterLifecycleID,
	})
	if err != nil {
		t.Fatalf("list second Recycle Bin page: %v", err)
	}
	if len(secondPage) != 2 || secondPage[0].LifecycleUnitID <= afterLifecycleID {
		t.Fatalf("expected two non-duplicate continuation entries, got %#v", secondPage)
	}
	allEntries := append(append([]domain.RecycleBinEntry{}, firstPage...), secondPage...)
	for _, entry := range allEntries {
		if entry.ResourceID == otherRootID || entry.ResourceID == restoredRootID {
			t.Fatalf("expected other-org and restored roots to stay out of Trash, got %#v", allEntries)
		}
	}
	metadataFound := false
	for _, entry := range allEntries {
		if entry.ResourceID == metadataRootID {
			metadataFound = entry.ResourceType == domain.LifecycleResourceMetadata && entry.DisplayName == "Deleted metadata root" && entry.RootFolderPath == metadataFolderPath
		}
	}
	if !metadataFound {
		t.Fatalf("expected deleted metadata root with title and containing path, got %#v", allEntries)
	}

	if err := tx.Model(&domain.LifecycleUnit{}).
		Where("id = ? AND org_id = ?", afterLifecycleID, orgID).
		Update("state", domain.LifecycleRestored).Error; err != nil {
		t.Fatalf("mark cursor target restored: %v", err)
	}
	if _, err := repo.ListRecycleBinEntries(ctx, orgID, domain.RecycleBinFilter{
		Limit:            2,
		AfterDeletedAt:   &afterDeletedAt,
		AfterLifecycleID: &afterLifecycleID,
	}); !errors.Is(err, domain.ErrCursorInvalid) {
		t.Fatalf("expected stale Recycle Bin cursor to fail closed, got %v", err)
	}
}
