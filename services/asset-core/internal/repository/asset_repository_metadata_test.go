package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

// TestMetadataRepository_GetByFolder_EmptyList distinguishes an existing empty folder from a missing folder.
func TestMetadataRepository_GetByFolder_EmptyList(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	// The repository must distinguish an existing empty folder from a missing or cross-org folder.
	mock.ExpectQuery(`SELECT "id" FROM "folders" WHERE \(id = \$1 AND org_id = \$2\) AND "folders"\."deleted_at" IS NULL ORDER BY "folders"\."id" LIMIT \$3`).
		WithArgs("folder-1", "org-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("folder-1"))
	mock.ExpectQuery(`SELECT metadata_items\.\* FROM "metadata_items" JOIN folders ON folders.id = metadata_items.folder_id.*metadata_items\.folder_id = \$1.*folders\.org_id = \$2.*metadata_items\.deleted_at IS NULL.*ORDER BY metadata_items.created_at DESC, metadata_items.id ASC`).
		WithArgs("folder-1", "org-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "title"})) // empty rows

	items, err := repo.GetMetadataItemsByFolder(context.Background(), "org-1", "folder-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty list, got %d items", len(items))
	}
}

// TestMetadataRepository_GetByFolder_MissingFolder verifies that list does not hide a missing folder behind an empty result.
func TestMetadataRepository_GetByFolder_MissingFolder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}
	repo := repository.NewAssetRepository(gormDB)

	mock.ExpectQuery(`SELECT "id" FROM "folders" WHERE \(id = \$1 AND org_id = \$2\) AND "folders"\."deleted_at" IS NULL ORDER BY "folders"\."id" LIMIT \$3`).
		WithArgs("missing-folder", "org-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	_, err = repo.GetMetadataItemsByFolder(context.Background(), "org-1", "missing-folder")
	if !errors.Is(err, domain.ErrFolderNotFound) {
		t.Fatalf("expected ErrFolderNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMetadataRepository_SearchKeysetUsesMixedTimestampPredicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}
	repo := repository.NewAssetRepository(gormDB)

	updatedAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT metadata_items\.id FROM "metadata_items" JOIN folders ON folders\.id = metadata_items\.folder_id.*`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cursor-item"))
	mock.ExpectQuery(`SELECT metadata_items\.\* FROM "metadata_items" JOIN folders ON folders\.id = metadata_items\.folder_id.*metadata_items\.updated_at < .*metadata_items\.updated_at = .*metadata_items\.id > .*ORDER BY metadata_items\.updated_at DESC, metadata_items\.id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("next-item"))

	folderID := "folder-1"
	afterID := "cursor-item"
	items, err := repo.SearchMetadataItems(context.Background(), "org-1", domain.MetadataSearchFilter{
		FolderID:       &folderID,
		Limit:          3,
		Keyset:         true,
		AfterUpdatedAt: &updatedAt,
		AfterID:        &afterID,
	})
	if err != nil {
		t.Fatalf("unexpected keyset search error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "next-item" {
		t.Fatalf("unexpected keyset results: %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMetadataRepository_SearchKeysetRejectsStaleCursor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}
	repo := repository.NewAssetRepository(gormDB)

	updatedAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT metadata_items\.id FROM "metadata_items" JOIN folders ON folders\.id = metadata_items\.folder_id.*`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	folderID := "folder-1"
	afterID := "stale-item"
	_, err = repo.SearchMetadataItems(context.Background(), "org-1", domain.MetadataSearchFilter{
		FolderID:       &folderID,
		Limit:          3,
		Keyset:         true,
		AfterUpdatedAt: &updatedAt,
		AfterID:        &afterID,
	})
	if !errors.Is(err, domain.ErrCursorInvalid) {
		t.Fatalf("expected stale cursor error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMetadataRepository_CreateSuccess verifies refs, folder scope, insert, and commit share one transaction.
func TestMetadataRepository_CreateSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))

	// Mock the folder locking check
	mock.ExpectQuery(`SELECT \* FROM "folders" WHERE \(id = \$1 AND org_id = \$2\) AND "folders"\."deleted_at" IS NULL ORDER BY "folders"."id" LIMIT \$3 FOR SHARE`).
		WithArgs("folder-1", "org-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "org_id"}).AddRow("folder-1", "org-1"))

	// Insert Metadata
	mock.ExpectQuery(`.*INSERT INTO "metadata_items".*`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("meta-1"))

	mock.ExpectCommit()

	source1 := "source-1"
	ext1 := "ext-1"
	input := domain.CreateMetadataInput{
		FolderID:       "folder-1",
		Title:          "Meta",
		Labels:         pq.StringArray{"label1"},
		ExternalSource: &source1,
		ExternalID:     &ext1,
	}

	_, err = repo.CreateMetadataItem(context.Background(), "org-1", "user-1", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMetadataRepository_UpdateRejectsInvalidFinalExternalPair proves pair validation occurs after the row lock.
func TestMetadataRepository_UpdateRejectsInvalidFinalExternalPair(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}
	repo := repository.NewAssetRepository(gormDB)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT metadata_items\.\* FROM "metadata_items" JOIN folders ON folders.id = metadata_items.folder_id.*metadata_items\.id = \$1.*folders\.org_id = \$2.*FOR UPDATE`).
		WithArgs("meta-1", "org-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "folder_id", "title", "labels", "metadata_json", "external_source", "external_id",
		}).AddRow("meta-1", "folder-1", "Title", "{}", []byte(`{}`), "source", "external-id"))
	mock.ExpectRollback()

	_, err = repo.UpdateMetadataItem(context.Background(), "org-1", "user-1", "meta-1", domain.UpdateMetadataInput{
		ExternalSourceSet: true,
		ExternalSource:    nil,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
