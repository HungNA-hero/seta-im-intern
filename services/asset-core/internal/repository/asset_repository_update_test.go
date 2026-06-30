package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestUpdateFolder_Success(t *testing.T) {
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

	orgID := "org-1"
	userID := "user-1"
	folderID := "folder-1"
	newName := "Renamed Folder"
	input := domain.UpdateFolderInput{
		Name:    &newName,
		NameSet: true,
	}

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))

	// Load active folder
	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(id = \\$1 AND org_id = \\$2\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY \"folders\".\"id\" LIMIT \\$3 FOR UPDATE$").
		WithArgs(folderID, orgID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "org_id", "path", "name"}).AddRow(folderID, orgID, "parent.seg", "Old Name"))

	// Child uniqueness check
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\" WHERE \\(org_id = \\$1 AND path <@ \\$2::ltree AND nlevel\\(path\\) = nlevel\\(\\$3::ltree\\) \\+ 1 AND name = \\$4 AND id != \\$5\\) AND \"folders\".\"deleted_at\" IS NULL$").
		WithArgs(orgID, "parent", "parent", newName, folderID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// Save
	mock.ExpectExec("^UPDATE \"folders\" SET").WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectCommit()

	_, err = repo.UpdateFolder(context.Background(), orgID, userID, folderID, input)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestUpdateFolder_NotFound(t *testing.T) {
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
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))

	// Load active folder
	mock.ExpectQuery("^SELECT \\* FROM \"folders\"").WillReturnError(gorm.ErrRecordNotFound)
	mock.ExpectRollback()

	newName := "Renamed"
	_, err = repo.UpdateFolder(context.Background(), "org-1", "user-1", "folder-not-found", domain.UpdateFolderInput{
		Name:    &newName,
		NameSet: true,
	})
	if !errors.Is(err, domain.ErrFolderNotFound) {
		t.Errorf("expected ErrFolderNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestUpdateFolder_ClearDescription(t *testing.T) {
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
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("^SELECT \\* FROM \"folders\"").
		WillReturnRows(sqlmock.NewRows([]string{"id", "org_id", "path", "name", "description"}).
			AddRow("folder-1", "org-1", "root", "Folder", "Old description"))
	mock.ExpectExec("^UPDATE \"folders\" SET").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	folder, err := repo.UpdateFolder(
		context.Background(),
		"org-1",
		"user-1",
		"folder-1",
		domain.UpdateFolderInput{DescriptionSet: true},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if folder.Description != nil {
		t.Fatalf("expected description to be cleared, got %q", *folder.Description)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestUpdateFolder_SiblingCheckFailureRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	expectedErr := errors.New("sibling lookup failed")
	repo := repository.NewAssetRepository(gormDB)
	newName := "Renamed"

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("^SELECT \\* FROM \"folders\"").
		WillReturnRows(sqlmock.NewRows([]string{"id", "org_id", "path", "name"}).
			AddRow("folder-1", "org-1", "parent.child", "Old"))
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\"").
		WillReturnError(expectedErr)
	mock.ExpectRollback()

	_, err = repo.UpdateFolder(
		context.Background(),
		"org-1",
		"user-1",
		"folder-1",
		domain.UpdateFolderInput{Name: &newName, NameSet: true},
	)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected sibling lookup error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestUpdateFolder_DuplicateSiblingRollsBack(t *testing.T) {
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
	newName := "Duplicate"

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("^SELECT \\* FROM \"folders\"").
		WillReturnRows(sqlmock.NewRows([]string{"id", "org_id", "path", "name"}).
			AddRow("folder-1", "org-1", "parent.child", "Old"))
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	_, err = repo.UpdateFolder(
		context.Background(),
		"org-1",
		"user-1",
		"folder-1",
		domain.UpdateFolderInput{Name: &newName, NameSet: true},
	)
	if !errors.Is(err, domain.ErrFolderConflict) {
		t.Fatalf("expected ErrFolderConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}
